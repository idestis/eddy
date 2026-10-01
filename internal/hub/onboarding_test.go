package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/idestis/eddy/internal/agent"
	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
)

// fakeManagement is a fake management cluster: Cluster CRs, Secrets and
// SubjectAccessReviews (members of eddy:platform may do anything with
// clusters.gitops.eddy.dev). No real API server is ever contacted.
func fakeManagement(t *testing.T) *KubeClients {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{ClusterGVR: "ClusterList"})
	kube := k8sfake.NewClientset()
	kube.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview).DeepCopy()
		ra := sar.Spec.ResourceAttributes
		sar.Status.Allowed = ra != nil && ra.Group == clusterGroup && ra.Resource == clusterResource &&
			slices.Contains(sar.Spec.Groups, "eddy:platform")
		return true, sar, nil
	})
	return &KubeClients{Dynamic: dyn, Kube: kube}
}

// newKubeReplica is a hub replica that watches Cluster CRs in km instead
// of using static clusters, with onboarding on.
func newKubeReplica(t *testing.T, st store.Store, pod string, km *KubeClients) *testEnv {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("0123456789abcdef0123456789abcdef-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	users := filepath.Join(dir, "users.yaml")
	h := passwordHash(t)
	if err := os.WriteFile(users, fmt.Appendf(nil, "users:\n  - {username: alice, passwordHash: %q, groups: [team-a]}\n  - {username: ops, passwordHash: %q, groups: [platform]}\n", h, h), 0o600); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(nil)
	peer := httptest.NewUnstartedServer(nil)
	base := "http://" + ui.Listener.Addr().String()
	cfg, err := config.ParseHub(fmt.Appendf(nil, `
publicURL: %s
namespace: eddy
agentsPublicURL: https://agents.example.com
onboarding: {enabled: true}
listen: {ui: "127.0.0.1:0", agents: "127.0.0.1:0", metrics: "127.0.0.1:0"}
auth:
  local: {enabled: true, usersFile: %s}
  keyFile: %s
store: {driver: memory}
peer: {listen: "127.0.0.1:0"}
`, base, users, key))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := New(context.Background(), cfg, Options{
		Log: quietLog().With("replica", pod), Store: st, SPA: http.NotFoundHandler(),
		Flags: runtimeflags.Static{}, RequestTimeout: 3 * time.Second, PodName: pod,
		PeerAddr: peer.Listener.Addr().String(), Kube: km,
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
	waitFor(t, hb.reg.Synced)
	return e
}

// memSecret is the agent's token Secret, in memory.
type memSecret struct {
	mu    sync.Mutex
	token string
	fail  error
}

func (m *memSecret) Load(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token, nil
}

func (m *memSecret) Save(_ context.Context, tok string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.token = tok
	return nil
}

type staticSource struct{ rs []model.Resource }

func (s staticSource) Snapshot() []model.Resource          { return s.rs }
func (s staticSource) Drain() ([]model.Resource, []string) { return nil, nil }

type handlerFunc struct{}

func (handlerFunc) Handle(context.Context, protocol.Request, func(protocol.LogChunk) error) (json.RawMessage, *protocol.Error) {
	return nil, &protocol.Error{Code: 400, Message: "not supported in this test"}
}

type connection struct {
	Cluster   onboardedCluster
	Checks    []connectionCheck
	Agents    int
	JoinToken *joinTokenInfo
	Attempts  []store.ConnectionAttempt
}

func (c connection) check(id string) connectionCheck {
	for _, ch := range c.Checks {
		if ch.ID == id {
			return ch
		}
	}
	return connectionCheck{}
}

func (c connection) hasAttempt(reason string) bool {
	return slices.ContainsFunc(c.Attempts, func(a store.ConnectionAttempt) bool { return a.Reason == reason })
}

// TestOnboardingEndToEnd: two replicas share a store and a fake management
// cluster. A platform user adds a cluster on replica A; a real agent
// session joins with the join token through replica B, stores the
// permanent token and reconnects with it; the checklist turns green on
// both replicas. Bad, reused and cross-cluster join tokens are refused and
// listed as rejected attempts.
func TestOnboardingEndToEnd(t *testing.T) {
	km := fakeManagement(t)
	open := sharedStores(t)
	a := newKubeReplica(t, open(), "hub-0", km)
	b := newKubeReplica(t, open(), "hub-1", km)
	st := a.store
	ops, alice := a.login("ops"), a.login("alice")

	var perms map[string]bool
	ops.do("GET", "/api/v1/clusters/permissions", nil, &perms, 200)
	if !perms["onboarding"] || !perms["create"] {
		t.Fatalf("ops permissions %v", perms)
	}
	alice.do("GET", "/api/v1/clusters/permissions", nil, &perms, 200)
	if perms["create"] {
		t.Fatal("alice may not create clusters")
	}
	var me meResponse
	alice.do("GET", "/api/v1/me", nil, &me, 200)
	if !me.Features.Onboarding {
		t.Fatal("features.onboarding")
	}

	body := map[string]any{"name": "edge-1", "displayName": "Edge 1", "environment": "Staging", "color": "#0F766E", "order": 3}
	if st, code := alice.errorCode("POST", "/api/v1/clusters", body); st != 403 || code != "forbidden" {
		t.Fatalf("alice create: %d %s", st, code)
	}
	for _, bad := range []map[string]any{{"name": "Edge_1"}, {"name": "edge", "color": "red"}, {"name": "edge", "ttl": "48h"}} {
		if st, _ := ops.errorCode("POST", "/api/v1/clusters", bad); st != 400 {
			t.Fatalf("invalid %v: %d", bad, st)
		}
	}
	var created createdCluster
	ops.do("POST", "/api/v1/clusters", body, &created, 201)
	if !auth.IsJoinToken(created.JoinToken.Token) || created.Cluster.Phase != phasePending {
		t.Fatalf("created %+v", created)
	}
	g := created.Guide
	for _, part := range []string{g.Helm, g.Values, g.Manifests} {
		if !strings.Contains(part, created.JoinToken.Token) || !strings.Contains(part, "wss://agents.example.com/agent/v1/connect") {
			t.Fatalf("guide part lacks token or hub url:\n%s", part)
		}
	}
	if !strings.Contains(g.ClusterResource, "name: edge-1") {
		t.Fatalf("cluster resource %s", g.ClusterResource)
	}
	if st, code := ops.errorCode("POST", "/api/v1/clusters", body); st != 409 || code != "conflict" {
		t.Fatalf("duplicate create: %d %s", st, code)
	}
	for _, e := range []*testEnv{a, b} {
		waitFor(t, func() bool { m, ok := e.hub.reg.meta("edge-1"); return ok && m.phase == phasePending })
	}

	// Rejected join attempts, recorded on one replica and seen on the other.
	bogus, _, _, _ := auth.NewJoinToken()
	if _, err := dialAgent(context.Background(), b.agentURL("edge-1"), "edge-1", bogus); err == nil {
		t.Fatal("unknown join token accepted")
	}
	if _, err := dialAgent(context.Background(), b.agentURL("edge-1"), "edge-1", "not-a-token"); err == nil {
		t.Fatal("bad agent token accepted")
	}
	var conn connection
	waitFor(t, func() bool {
		ops.do("GET", "/api/v1/clusters/edge-1/connection", nil, &conn, 200)
		return conn.hasAttempt(store.AttemptBadToken)
	})
	if conn.check("connected").State != checkFail || conn.JoinToken == nil || conn.JoinToken.State != "active" {
		t.Fatalf("checklist before join %+v", conn)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/clusters/edge-1/connection", nil); st != 403 {
		t.Fatalf("alice reads the checklist: %d", st)
	}

	// The agent joins through replica B.
	secret := &memSecret{}
	pinned := true
	sess := &agent.Session{
		URL: strings.Replace(b.agents.URL, "http", "ws", 1) + "/agent/v1/connect", Cluster: "edge-1",
		Creds:   agent.NewCredentials("", created.JoinToken.Token, secret),
		Source:  staticSource{rs: []model.Resource{res("Kustomization", "flux-system", "apps", model.StatusReady)}},
		Handler: handlerFunc{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hello: protocol.Hello{AgentVersion: "v1.0.0", KubernetesVersion: "v1.33.0", FluxVersion: "v2.7.0",
			Diagnostics: &protocol.Diagnostics{ServedKinds: []string{"Kustomization"}, SAROK: true, ImpersonationPinned: &pinned, InformersSynced: 3, InformersTotal: 3}},
		Instance: "agent-a",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = sess.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitForLong(t, 15*time.Second, sess.Connected)
	secret.mu.Lock()
	perm := secret.token
	secret.mu.Unlock()
	if perm == "" || auth.IsJoinToken(perm) {
		t.Fatalf("agent stored %q", perm)
	}
	sec, err := km.Kube.CoreV1().Secrets("eddy").Get(context.Background(), "eddy-agent-edge-1", metav1.GetOptions{})
	if err != nil || string(sec.Data["token"]) != perm {
		t.Fatalf("hub token secret %+v %v", sec, err)
	}
	for _, e := range []*testEnv{a, b} {
		var c connection
		waitForLong(t, 10*time.Second, func() bool {
			c = connection{}
			ops.e = e
			ops.do("GET", "/api/v1/clusters/edge-1/connection", nil, &c, 200)
			return c.check("connected").State == checkOK && c.check("informers").State == checkOK
		})
		for _, id := range []string{"protocol", "flux", "sar", "impersonation"} {
			if c.check(id).State != checkOK {
				t.Fatalf("%s: check %s = %+v", e.hub.onboard.pod, id, c.check(id))
			}
		}
		if c.JoinToken == nil || c.JoinToken.State != "used" || c.Cluster.Phase != "Connected" {
			t.Fatalf("after join %+v", c)
		}
	}
	ops.e = a

	// The used join token and a join token for another cluster are refused.
	if _, err := dialAgent(context.Background(), a.agentURL("edge-1"), "edge-1", created.JoinToken.Token); err == nil {
		t.Fatal("reused join token accepted")
	}
	waitFor(t, func() bool {
		conn = connection{}
		ops.do("GET", "/api/v1/clusters/edge-1/connection", nil, &conn, 200)
		return conn.hasAttempt(store.AttemptJoinUsed)
	})
	var other createdCluster
	ops.do("POST", "/api/v1/clusters", map[string]any{"name": "edge-2", "protected": true}, &other, 201)
	waitFor(t, func() bool { _, ok := a.hub.reg.Get("edge-2"); return ok })
	if _, err := dialAgent(context.Background(), a.agentURL("edge-1"), "edge-1", other.JoinToken.Token); err == nil {
		t.Fatal("cross-cluster join token accepted")
	}
	waitFor(t, func() bool {
		conn = connection{}
		ops.do("GET", "/api/v1/clusters/edge-2/connection", nil, &conn, 200)
		return conn.hasAttempt(store.AttemptWrongCluster)
	})

	// An expired join token (issued with a short TTL, then aged).
	var re struct{ JoinToken issuedToken }
	ops.do("POST", "/api/v1/clusters/edge-2/join-token", map[string]string{"ttl": "5m"}, &re, 201)
	id, _, _ := auth.ParseJoinToken(re.JoinToken.Token)
	b.hub.onboard.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := dialAgent(context.Background(), b.agentURL("edge-2"), "edge-2", re.JoinToken.Token); err == nil {
		t.Fatal("expired join token accepted")
	}
	b.hub.onboard.now = time.Now
	waitFor(t, func() bool {
		conn = connection{}
		ops.do("GET", "/api/v1/clusters/edge-2/connection", nil, &conn, 200)
		return conn.hasAttempt(store.AttemptJoinExpired)
	})
	if jt, _ := st.JoinTokens().Get(context.Background(), id); jt.UsedAt != nil {
		t.Fatal("an expired join token must not be consumed")
	}
	// The previous token for edge-2 was revoked by the new one.
	if toks, _ := st.JoinTokens().List(context.Background(), "edge-2"); len(toks) != 2 || toks[0].ID != id || toks[0].RevokedAt != nil || toks[1].RevokedAt == nil {
		t.Fatalf("edge-2 tokens %+v", toks)
	}

	// Update and delete; protected clusters need a typed confirmation.
	var upd struct{ Cluster onboardedCluster }
	ops.do("PATCH", "/api/v1/clusters/edge-1", map[string]any{"displayName": "Edge One", "protected": true}, &upd, 200)
	if upd.Cluster.DisplayName != "Edge One" || !upd.Cluster.Protected {
		t.Fatalf("updated %+v", upd.Cluster)
	}
	if st, code := ops.errorCode("DELETE", "/api/v1/clusters/edge-2", map[string]string{}); st != 428 || code != "confirm_required" {
		t.Fatalf("delete protected without confirm: %d %s", st, code)
	}
	ops.do("DELETE", "/api/v1/clusters/edge-2", map[string]string{"confirm": "edge-2"}, nil, 204)
	waitFor(t, func() bool { _, ok := b.hub.reg.Get("edge-2"); return !ok })

	events, _, _ := st.Audit().Query(context.Background(), store.AuditFilter{Limit: 200})
	seen := map[string]bool{}
	for _, e := range events {
		if e.Result == store.AuditOK {
			seen[e.Action] = true
		}
	}
	for _, act := range []string{"cluster.create", "cluster.join_token", "cluster.joined", "cluster.update", "cluster.delete"} {
		if !seen[act] {
			t.Errorf("no audit event %s", act)
		}
	}
}

func TestOnboardingDisabledWithStaticClusters(t *testing.T) {
	e := newEnv(t, "")
	ops := e.login("ops")
	var me meResponse
	ops.do("GET", "/api/v1/me", nil, &me, 200)
	if me.Features.Onboarding {
		t.Fatal("onboarding must be off with static clusters")
	}
	if st, code := ops.errorCode("POST", "/api/v1/clusters", map[string]any{"name": "x"}); st != 409 || code != "conflict" {
		t.Fatalf("create: %d %s", st, code)
	}
	var perms map[string]bool
	ops.do("GET", "/api/v1/clusters/permissions", nil, &perms, 200)
	if perms["create"] || perms["onboarding"] {
		t.Fatalf("permissions %v", perms)
	}
}

func TestRenderGuide(t *testing.T) {
	cfg := &config.Hub{Onboarding: config.Onboarding{AgentChart: "oci://x/eddy-agent", AgentChartVersion: "1.2.3", AgentNamespace: "eddy-system"}}
	g, err := renderGuide(cfg, ClusterSpec{Name: "prod-eu", DisplayName: `Prod "EU"`, Protected: true}, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.Helm, "--version 1.2.3") || !strings.Contains(g.Helm, guideTokenHolder) || len(g.Warnings) != 1 {
		t.Fatalf("guide %+v", g)
	}
	if !strings.Contains(g.ClusterResource, `displayName: "Prod \"EU\""`) {
		t.Fatalf("quoting: %s", g.ClusterResource)
	}
	if n := strings.Count(g.Manifests, "\n---\n"); n != 9 {
		t.Fatalf("%d manifest separators", n)
	}
	for in, want := range map[string]string{
		"https://a.example.com":                "wss://a.example.com/agent/v1/connect",
		"http://localhost:8443/":               "ws://localhost:8443/agent/v1/connect",
		"wss://a.example.com/agent/v1/connect": "wss://a.example.com/agent/v1/connect",
		"":                                     "",
	} {
		if got := agentsURL(in); got != want {
			t.Errorf("agentsURL(%q) = %q, want %q", in, got, want)
		}
	}
}
