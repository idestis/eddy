package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	_ "github.com/jackc/pgx/v5/stdlib" // "pgx" driver for the PostgreSQL variant

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
	"github.com/idestis/eddy/internal/store/postgres"
)

// These tests run several hub replicas in one process. They share one
// memory store (its rate limits, agent sessions and events stand in for
// PostgreSQL's), or, with EDDY_TEST_POSTGRES_DSN set, each replica opens
// its own postgres.Store on one throwaway schema, as real replicas do.

// sharedStores returns a constructor of one replica's store.
func sharedStores(t *testing.T) func() store.Store {
	t.Helper()
	base := os.Getenv("EDDY_TEST_POSTGRES_DSN")
	if base == "" {
		st := memory.New()
		t.Cleanup(func() { _ = st.Close() })
		return func() store.Store { return st }
	}
	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("eddy_ha_test_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn := u.String()
	var (
		mu     sync.Mutex
		stores []store.Store
	)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range stores {
			_ = s.Close()
		}
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = db.Close()
	})
	return func() store.Store {
		s, err := postgres.Open(context.Background(), postgres.Options{DSN: dsn, MaxOpenConns: 8, Logger: quietLog()})
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		stores = append(stores, s)
		mu.Unlock()
		return s
	}
}

// newReplica is newEnv for one of several replicas: the same users, key
// and static clusters, a peer listener and a shared store. The hub's
// background work runs (Start), as in production.
func newReplica(t *testing.T, st store.Store, pod, extraYAML string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("0123456789abcdef0123456789abcdef-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	users := filepath.Join(dir, "users.yaml")
	h := passwordHash(t)
	usersYAML := fmt.Sprintf("users:\n  - {username: alice, passwordHash: %q, groups: [team-a]}\n  - {username: bob, passwordHash: %q, groups: [team-b]}\n  - {username: ops, passwordHash: %q, groups: [platform]}\n", h, h, h)
	if err := os.WriteFile(users, []byte(usersYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testTokenEnv, testToken)
	t.Setenv(testProtected, testToken+"-prod")

	mcpLine := "mcp: {enabled: true, writes: true}"
	if strings.HasPrefix(extraYAML, "mcp:") {
		mcpLine, extraYAML = extraYAML, ""
	}
	ui := httptest.NewUnstartedServer(nil)
	peer := httptest.NewUnstartedServer(nil)
	base := "http://" + ui.Listener.Addr().String()
	cfg, err := config.ParseHub([]byte(fmt.Sprintf(`
publicURL: %s
listen: {ui: "127.0.0.1:0", agents: "127.0.0.1:0", metrics: "127.0.0.1:0"}
auth:
  local: {enabled: true, usersFile: %s}
  keyFile: %s
store: {driver: memory}
%s
peer: {listen: "127.0.0.1:0"}
staticClusters:
  - {name: dev, displayName: Dev, tokenEnv: %s, order: 1}
  - {name: prod, displayName: Prod, protected: true, tokenEnv: %s, order: 2}
%s`, base, users, key, mcpLine, testTokenEnv, testProtected, extraYAML)))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := New(context.Background(), cfg, Options{
		Log: quietLog().With("replica", pod), Store: st, SPA: http.NotFoundHandler(),
		Flags:          runtimeflags.Static{MCPEnabled: true, MCPWrites: true},
		RequestTimeout: 3 * time.Second, PodName: pod, PeerAddr: peer.Listener.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ui.Config.Handler = hb.UIHandler()
	ui.Start()
	peer.Config.Handler = hb.PeerHandler()
	peer.Start()
	ag := httptest.NewServer(hb.AgentHandler())
	ctx, cancel := context.WithCancel(context.Background())
	hb.Start(ctx)
	e := &testEnv{t: t, hub: hb, cfg: cfg, store: st, ui: ui, agents: ag, baseURL: base}
	t.Cleanup(func() {
		cancel()
		_ = hb.Close()
		for _, s := range []*httptest.Server{ag, peer, ui} {
			s.CloseClientConnections()
			s.Close()
		}
	})
	return e
}

// replicas starts n replicas named hub-0 … hub-(n-1) on one store.
func replicas(t *testing.T, n int) []*testEnv {
	t.Helper()
	open := sharedStores(t)
	out := make([]*testEnv, n)
	for i := range out {
		out[i] = newReplica(t, open(), "hub-"+strconv.Itoa(i), "")
	}
	return out
}

// dialInstance connects a fake agent that identifies as instance/seq.
func (e *testEnv) dialInstance(cluster, instance string, seq int64, resources []model.Resource) *fakeAgent {
	e.t.Helper()
	a, err := dialAgentHello(context.Background(), e.agentURL(cluster), testToken, protocol.Hello{
		Protocol: protocol.Version, Cluster: cluster, AgentVersion: "v0.1.0-test", KubernetesVersion: "v1.33.0",
		Instance: instance, Seq: seq,
	})
	if err != nil {
		e.t.Fatalf("dial agent: %v", err)
	}
	e.t.Cleanup(a.close)
	a.sendFrame(protocol.TypeSnapshot, "", protocol.Snapshot{Resources: resources, Parts: 1})
	go a.serve()
	return a
}

// waitRemote waits until e serves cluster through a synced relay.
func waitRemote(t *testing.T, e *testEnv, cluster string, size int) *remoteSession {
	t.Helper()
	var rs *remoteSession
	waitForLong(t, 10*time.Second, func() bool {
		r, ok := e.hub.agents.session(cluster).(*remoteSession)
		if !ok || r.size() != size {
			return false
		}
		rs = r
		return true
	})
	return rs
}

// waitForLong polls cond for up to d. On a timeout it logs every
// goroutine's stack first: a relay that normally converges in milliseconds
// and did not is a stuck state worth a dump.
func waitForLong(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			buf := make([]byte, 8<<20)
			t.Logf("goroutines at timeout:\n%s", buf[:runtime.Stack(buf, true)])
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRelayRequests: the agent of dev is connected to replica A, and every
// kind of request on replica B is relayed to it with the caller's identity.
func TestRelayRequests(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	agent := a.connectAgent("dev", testToken, []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		res("Kustomization", "team-b", "infra", model.StatusReady),
		res("Pod", "team-a", "web-1", model.StatusReady),
	})
	waitRemote(t, b, "dev", 3)

	alice := b.login("alice")
	var list struct {
		Items []model.Resource `json:"items"`
	}
	alice.do("GET", "/api/v1/clusters/dev/resources", nil, &list, http.StatusOK)
	if len(list.Items) != 2 {
		t.Fatalf("alice sees %d resources through the relay, want 2 (team-a only)", len(list.Items))
	}
	var y struct{ YAML string }
	alice.do("GET", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/yaml", nil, &y, http.StatusOK)
	if !strings.Contains(y.YAML, "kind: Kustomization") || strings.Contains(y.YAML, "ghp_") {
		t.Fatalf("relayed yaml %q (must be redacted)", y.YAML)
	}
	alice.do("POST", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile", map[string]any{}, nil, http.StatusAccepted)
	recs := agent.recorded(protocol.OpReconcile)
	if len(recs) != 1 || recs[0].Identity.User != "local:alice" || recs[0].Target.Name != "apps" {
		t.Fatalf("reconcile at the agent %+v", recs)
	}
	if st, code := alice.errorCode("POST", "/api/v1/clusters/dev/objects/Kustomization/team-b/infra/suspend", map[string]any{}); st != 403 || code != "forbidden" {
		t.Fatalf("relayed forbidden write: %d %s", st, code)
	}

	// A followed log stream relays chunks and, when the browser leaves,
	// the cancel reaches the agent.
	ch, stop := alice.openStream("/api/v1/clusters/dev/pods/team-a/web-1/logs?follow=true")
	var lines []string
	for len(lines) < 3 {
		ev := next(t, ch, "log")
		var l struct{ Lines []string }
		_ = json.Unmarshal([]byte(ev.data), &l)
		lines = append(lines, l.Lines...)
	}
	stop()
	select {
	case <-agent.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never received the relayed cancel")
	}
	if n := b.hub.metrics.peerRelayed.Load() + a.hub.metrics.peerRelayed.Load(); n == 0 {
		t.Fatal("no request was relayed")
	}
}

// TestRelaySSE: SSE clients on replica B see changes that the agent sends
// to replica A, filtered for the user, and thread changes made on A.
func TestRelaySSE(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	agent := a.connectAgent("dev", testToken, nil)
	waitRemote(t, b, "dev", 0)

	alice := b.login("alice")
	ch, stop := alice.openStream("/api/v1/stream")
	defer stop()
	next(t, ch, "hello")
	next(t, ch, "clusters")

	ka, kb := res("Kustomization", "team-a", "apps", model.StatusReady), res("Kustomization", "team-b", "infra", model.StatusReady)
	agent.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{ka, kb}})
	var chg changeEvent
	_ = json.Unmarshal([]byte(next(t, ch, "change").data), &chg)
	if chg.Cluster != "dev" || len(chg.Upserts) != 1 || chg.Upserts[0].Name != "apps" {
		t.Fatalf("change on B %+v", chg)
	}

	// A thread created on A reaches alice's stream on B.
	opsA := a.login("ops")
	var created struct {
		Thread store.Thread `json:"thread"`
	}
	opsA.do("POST", "/api/v1/threads", map[string]any{
		"ref":   map[string]string{"cluster": "dev", "kind": "Kustomization", "namespace": "team-a", "name": "apps"},
		"title": "Investigating", "body": "looking into it",
	}, &created, http.StatusCreated)
	var te threadEvent
	_ = json.Unmarshal([]byte(next(t, ch, "thread").data), &te)
	if te.ThreadID == "" || te.ThreadID != created.Thread.ID {
		t.Fatalf("thread event on B %+v, want %s", te, created.Thread.ID)
	}
}

// TestAgentMovesReplica: the agent reconnects from A to B (same instance,
// higher seq). Both replicas keep serving the cluster: B locally, A
// through the relay.
func TestAgentMovesReplica(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	one := []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)}
	first := a.dialInstance("dev", "agent-x", 1, one)
	waitForLong(t, 5*time.Second, func() bool { s := a.hub.agents.get("dev"); return s != nil && s.size() == 1 })
	waitRemote(t, b, "dev", 1)

	first.close()
	b.dialInstance("dev", "agent-x", 2, one)
	waitForLong(t, 10*time.Second, func() bool { s := b.hub.agents.get("dev"); return s != nil && s.size() == 1 })
	waitRemote(t, a, "dev", 1)
	if a.hub.agents.hasLocal("dev") {
		t.Fatal("A still holds a local session after the agent moved")
	}
	alice := a.login("alice")
	var list struct {
		Items []model.Resource `json:"items"`
	}
	alice.do("GET", "/api/v1/clusters/dev/resources", nil, &list, http.StatusOK)
	if len(list.Items) != 1 {
		t.Fatalf("A lists %d resources after the move", len(list.Items))
	}
}

// TestStaleDialRefused: a connection of the same instance with a lower seq
// than one on another replica is refused.
func TestStaleDialRefused(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	a.dialInstance("dev", "agent-x", 5, nil)
	waitForLong(t, 5*time.Second, func() bool { return a.hub.agents.get("dev") != nil })
	stale, err := dialAgentHello(context.Background(), b.agentURL("dev"), testToken, protocol.Hello{
		Protocol: protocol.Version, Cluster: "dev", Instance: "agent-x", Seq: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := stale.conn.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("stale dial ended with %v, want a policy violation", err)
			}
			break
		}
	}
	if b.hub.agents.hasLocal("dev") {
		t.Fatal("the stale session was kept")
	}
}

// watchConnected samples whether every replica serves cluster until stop
// is closed, and reports the first gap.
func watchConnected(envs []*testEnv, cluster string, stop <-chan struct{}) <-chan string {
	out := make(chan string, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, e := range envs {
				if e.hub.agents.session(cluster) == nil {
					out <- e.hub.peers.pod + " had no session for " + cluster
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return out
}

// TestAgentReplicasFailover: two agent instances serve one cluster. When
// the primary instance dies, the standby takes over and the cluster never
// becomes Disconnected on any replica, whether the standby is on the same
// replica or another one.
func TestAgentReplicasFailover(t *testing.T) {
	one := []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)}
	t.Run("standby on the same replica", func(t *testing.T) {
		rs := replicas(t, 2)
		a, b := rs[0], rs[1]
		primary := a.dialInstance("dev", "agent-1", 1, one)
		waitForLong(t, 5*time.Second, func() bool { s := a.hub.agents.get("dev"); return s != nil && s.size() == 1 })
		standby := a.dialInstance("dev", "agent-2", 1, one)
		waitForLong(t, 5*time.Second, func() bool { return len(a.hub.agents.all()) == 2 })
		waitRemote(t, b, "dev", 1)
		first := a.hub.agents.session("dev")

		stop := make(chan struct{})
		gaps := watchConnected(rs, "dev", stop)
		primary.close()
		waitForLong(t, 5*time.Second, func() bool {
			s, ok := a.hub.agents.session("dev").(*agentSession)
			return ok && s != first && s.instance == "agent-2"
		})
		time.Sleep(200 * time.Millisecond)
		close(stop)
		if gap, ok := <-gaps; ok {
			t.Fatal(gap)
		}
		// The relay on B now reaches the standby.
		alice := b.login("alice")
		alice.do("POST", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile", map[string]any{}, nil, http.StatusAccepted)
		if n := len(standby.recorded(protocol.OpReconcile)); n != 1 {
			t.Fatalf("the standby got %d reconciles", n)
		}
	})
	t.Run("standby on another replica", func(t *testing.T) {
		rs := replicas(t, 2)
		a, b := rs[0], rs[1]
		primary := a.dialInstance("dev", "agent-1", 1, one)
		waitForLong(t, 5*time.Second, func() bool { s := a.hub.agents.get("dev"); return s != nil && s.size() == 1 })
		standby := b.dialInstance("dev", "agent-2", 1, one)
		waitForLong(t, 5*time.Second, func() bool { s := b.hub.agents.get("dev"); return s != nil && s.size() == 1 })

		stop := make(chan struct{})
		gaps := watchConnected(rs, "dev", stop)
		primary.close()
		// A has no local session left: it relays to B within the failover grace.
		waitRemote(t, a, "dev", 1)
		close(stop)
		if gap, ok := <-gaps; ok {
			t.Fatal(gap)
		}
		alice := a.login("alice")
		alice.do("POST", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile", map[string]any{}, nil, http.StatusAccepted)
		if n := len(standby.recorded(protocol.OpReconcile)); n != 1 {
			t.Fatalf("the standby got %d reconciles", n)
		}
	})
}

// TestPeerAuth checks the peer listener's authentication.
func TestPeerAuth(t *testing.T) {
	rs := replicas(t, 1)
	n := rs[0].hub.peers
	srv := httptest.NewServer(n.handler())
	defer srv.Close()
	// The node verifies against its advertised address, which the dialer
	// signs; point it at this test server.
	target := strings.TrimPrefix(srv.URL, "http://")
	n.setAddr(target)
	key := n.key
	sign := func(k []byte, pod string, at time.Time, tgt, from string) http.Header {
		unix := strconv.FormatInt(at.Unix(), 10)
		return http.Header{
			"Authorization": []string{peerAuthScheme + " " + pod + ":" + unix + ":" + peerMAC(k, "dial", pod, unix, tgt, from)},
			peerAddrHeader:  []string{from},
		}
	}
	dial := func(h http.Header) (int, http.Header) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, resp, err := websocket.Dial(ctx, "ws://"+target+peerPath, &websocket.DialOptions{HTTPHeader: h})
		if err == nil {
			defer conn.CloseNow()
			return http.StatusSwitchingProtocols, resp.Header
		}
		if resp == nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header
	}
	now := time.Now()
	from := "127.0.0.1:1"
	for _, tc := range []struct {
		name string
		h    http.Header
	}{
		{"no credentials", http.Header{}},
		{"bearer scheme", http.Header{"Authorization": []string{"Bearer x"}}},
		{"wrong key", sign([]byte("another-key-another-key-another-k"), "hub-9", now, target, from)},
		{"wrong target", sign(key, "hub-9", now, "10.0.0.1:8444", from)},
		{"clock skew", sign(key, "hub-9", now.Add(-2*peerSkew), target, from)},
		{"future clock", sign(key, "hub-9", now.Add(2*peerSkew), target, from)},
		{"own pod name", sign(key, n.pod, now, target, from)},
		{"forged address", func() http.Header {
			h := sign(key, "hub-9", now, target, from)
			h.Set(peerAddrHeader, "127.0.0.1:2")
			return h
		}()},
	} {
		if st, _ := dial(tc.h); st != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", tc.name, st)
		}
	}
	good := sign(key, "hub-9", now, target, from)
	st, hdr := dial(good)
	if st != http.StatusSwitchingProtocols {
		t.Fatalf("valid credentials: status %d", st)
	}
	parts := strings.Split(hdr.Get(peerReplyHeader), ":")
	if len(parts) != 3 || parts[0] != n.pod {
		t.Fatalf("reply header %q", hdr.Get(peerReplyHeader))
	}
	if st, _ := dial(good); st != http.StatusUnauthorized {
		t.Fatalf("replayed credentials: status %d, want 401", st)
	}
	if got := n.metrics.peerAuthFailures.Load(); got < 9 {
		t.Fatalf("peer auth failures = %d", got)
	}
	// Only the peer listener serves the peer endpoint.
	for _, h := range []http.Handler{rs[0].hub.UIHandler(), rs[0].hub.AgentHandler(), rs[0].hub.MetricsHandler()} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", peerPath, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("another listener answered %s with %d", peerPath, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	n.handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/me", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("the peer listener answered /api/v1/me with %d", rr.Code)
	}
}

// TestPeerLinkReadiness: a discovered peer that cannot be linked (here:
// it uses another hub key) makes readiness fail after the grace period.
func TestPeerLinkReadiness(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	if err := a.hub.peers.ready(time.Now()); err != nil {
		t.Fatalf("fresh replica not ready: %v", err)
	}
	_, err := a.hub.peers.dial(context.Background(), b.hub.peers.selfAddr())
	if err != nil {
		t.Fatalf("link A→B: %v", err)
	}
	waitForLong(t, 5*time.Second, func() bool { return len(b.hub.peers.peers()) == 1 })
	b.hub.peers.key = []byte("a-different-hub-key-a-different-")
	for _, l := range b.hub.peers.allLinks() {
		l.close("test")
	}
	if _, err := a.hub.peers.dial(context.Background(), b.hub.peers.selfAddr()); err == nil {
		t.Fatal("a peer with another key accepted the link")
	}
	if err := a.hub.peers.ready(time.Now()); err == nil || !strings.Contains(err.Error(), "hub key") {
		t.Fatalf("readiness after an auth mismatch: %v", err)
	}
}

// TestGlobalLoginLimits: the per-IP and per-user login limits hold across
// replicas.
func TestGlobalLoginLimits(t *testing.T) {
	rs := replicas(t, 2)
	login := func(e *testEnv, user, pw string) int {
		c := e.newClient()
		var pre struct{ CSRF string }
		c.do("GET", "/auth/csrf", nil, &pre, http.StatusOK)
		c.csrf = pre.CSRF
		resp := c.request("POST", "/auth/local/login", map[string]string{"username": user, "password": pw})
		resp.Body.Close()
		return resp.StatusCode
	}
	// Five failures for carol, alternating replicas, lock her out on both.
	for i := range 5 {
		if st := login(rs[i%2], "carol", "wrong"); st != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i, st)
		}
	}
	if v, _, _ := rs[0].store.RateLimits().Get(context.Background(), "login:lock:carol", time.Now()); v != 1 {
		t.Fatalf("no shared lockout for carol (count %d)", v)
	}
	// The per-IP budget (20 a minute, shared) is used up across replicas.
	codes := map[int]int{}
	for i := range 20 {
		codes[login(rs[i%2], "nobody", "x")]++
	}
	if st := login(rs[0], "alice", testPassword); st != http.StatusTooManyRequests {
		t.Fatalf("login after the shared per-IP budget: %d (earlier: %v)", st, codes)
	}
	if st := login(rs[1], "alice", testPassword); st != http.StatusTooManyRequests {
		t.Fatalf("login on the other replica after the shared per-IP budget: %d", st)
	}
}

// TestGlobalTokenLimit: concurrent PAT creation on two replicas never
// exceeds auth.tokens.maxPerUser.
func TestGlobalTokenLimit(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	a.cfg.Auth.Tokens.MaxPerUser, b.cfg.Auth.Tokens.MaxPerUser = 3, 3
	ca, cb := a.login("alice"), b.login("alice")
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		n  = map[int]int{}
	)
	for i := range 12 {
		c := ca
		if i%2 == 1 {
			c = cb
		}
		wg.Go(func() {
			resp := c.request("POST", "/api/v1/tokens", map[string]any{"name": fmt.Sprintf("t%d", i), "scopes": []string{"read"}})
			resp.Body.Close()
			mu.Lock()
			n[resp.StatusCode]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if n[http.StatusCreated] != 3 {
		t.Fatalf("created %d tokens under a cap of 3 (%v)", n[http.StatusCreated], n)
	}
}

// TestGlobalMCPCallLimit: the per-token MCP call rate holds across replicas.
func TestGlobalMCPCallLimit(t *testing.T) {
	open := sharedStores(t)
	a := newReplica(t, open(), "hub-0", "mcp: {enabled: true, writes: true, callsPerMinute: 4}")
	b := newReplica(t, open(), "hub-1", "mcp: {enabled: true, writes: true, callsPerMinute: 4}")
	tok := a.issuePAT(a.login("alice"), []string{"read"})
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_clusters","arguments":{}}}`
	for i := range 4 {
		e := a
		if i%2 == 1 {
			e = b
		}
		if st, body := mcpCall(t, e, tok, "", list); st != http.StatusOK || strings.Contains(body, "rate limit") {
			t.Fatalf("call %d on %s: %d %s", i, e.hub.peers.pod, st, body)
		}
	}
	for _, e := range []*testEnv{a, b} {
		if _, body := mcpCall(t, e, tok, "", list); !strings.Contains(body, "rate limit exceeded") {
			t.Fatalf("call over the shared limit on %s: %s", e.hub.peers.pod, body)
		}
	}
}

// TestLogoutRevokesOnOtherReplicas: signing out on A drops the session from
// B's 30 s cache through the revoke event.
func TestLogoutRevokesOnOtherReplicas(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	c := a.login("alice")
	// The cookie jar is per host, not per port, so the same browser session
	// reaches B.
	cb := &client{t: t, e: b, http: c.http, csrf: c.csrf}
	cb.do("GET", "/api/v1/me", nil, nil, http.StatusOK) // cached on B now
	c.do("POST", "/auth/logout", nil, nil, http.StatusNoContent)
	waitForLong(t, 5*time.Second, func() bool {
		resp := cb.request("GET", "/api/v1/me", nil)
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized
	})
}
