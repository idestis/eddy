package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
)

const (
	testToken     = "agent-token-0123456789abcdef0123456789"
	testPassword  = "correct horse battery staple"
	testTokenEnv  = "EDDY_TEST_AGENT_TOKEN"
	testProtected = "EDDY_TEST_PROD_TOKEN"
)

func quietLog() *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// hashes are computed once: argon2id is deliberately slow.
var (
	hashOnce sync.Once
	pwHash   string
)

func passwordHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		h, err := auth.HashPassword(testPassword)
		if err != nil {
			panic(err)
		}
		pwHash = h
	})
	return pwHash
}

// testEnv is a hub with a memory store, static clusters "dev" and
// "prod" (protected), and local users alice, bob and ops.
type testEnv struct {
	t       *testing.T
	hub     *Hub
	cfg     *config.Hub
	store   store.Store
	ui      *httptest.Server
	agents  *httptest.Server
	baseURL string
}

func newEnv(t *testing.T, extraYAML string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("0123456789abcdef0123456789abcdef-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	users := filepath.Join(dir, "users.yaml")
	h := passwordHash(t)
	usersYAML := fmt.Sprintf(`users:
  - username: alice
    passwordHash: %q
    groups: [team-a]
  - username: bob
    passwordHash: %q
    groups: [team-b]
  - username: ops
    passwordHash: %q
    groups: [platform]
`, h, h, h)
	if err := os.WriteFile(users, []byte(usersYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testTokenEnv, testToken)
	t.Setenv(testProtected, testToken+"-prod")

	ui := httptest.NewUnstartedServer(nil)
	base := "http://" + ui.Listener.Addr().String()
	cfg, err := config.ParseHub([]byte(fmt.Sprintf(`
publicURL: %s
listen: {ui: "127.0.0.1:0", agents: "127.0.0.1:0", metrics: "127.0.0.1:0"}
auth:
  local: {enabled: true, usersFile: %s}
  keyFile: %s
  auditViewerGroups: ["eddy:platform"]
store: {driver: memory}
mcp: {enabled: true, writes: true}
staticClusters:
  - {name: dev, displayName: Dev, tokenEnv: %s, order: 1}
  - {name: prod, displayName: Prod, protected: true, tokenEnv: %s, order: 2}
%s`, base, users, key, testTokenEnv, testProtected, extraYAML)))
	if err != nil {
		t.Fatal(err)
	}
	st := memory.New()
	hb, err := New(context.Background(), cfg, Options{
		Log:            quietLog(),
		Store:          st,
		Flags:          runtimeflags.Static{MCPEnabled: true, MCPWrites: true},
		SPA:            http.NotFoundHandler(),
		RequestTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ui.Config.Handler = hb.UIHandler()
	ui.Start()
	ag := httptest.NewServer(hb.AgentHandler())
	e := &testEnv{t: t, hub: hb, cfg: cfg, store: st, ui: ui, agents: ag, baseURL: base}
	t.Cleanup(func() {
		_ = hb.Close()
		ag.CloseClientConnections()
		ag.Close()
		ui.CloseClientConnections()
		ui.Close()
	})
	return e
}

func (e *testEnv) agentURL(cluster string) string {
	return "ws" + strings.TrimPrefix(e.agents.URL, "http") + "/agent/v1/connect?cluster=" + cluster
}

// connectAgent starts a fake agent for cluster and waits until the hub has
// its snapshot.
func (e *testEnv) connectAgent(cluster, token string, resources []model.Resource) *fakeAgent {
	e.t.Helper()
	a, err := dialAgent(context.Background(), e.agentURL(cluster), cluster, token)
	if err != nil {
		e.t.Fatalf("dial agent: %v", err)
	}
	e.t.Cleanup(a.close)
	a.sendFrame(protocol.TypeSnapshot, "", protocol.Snapshot{Resources: resources})
	go a.serve()
	waitFor(e.t, func() bool {
		s := e.hub.agents.get(cluster)
		return s != nil && s.size() == len(resources)
	})
	return a
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func res(kind, ns, name string, st model.Status) model.Resource {
	group := map[string]string{
		"Kustomization": "kustomize.toolkit.fluxcd.io", "HelmRelease": "helm.toolkit.fluxcd.io",
		"GitRepository": "source.toolkit.fluxcd.io", "Deployment": "apps", "Pod": "",
	}[kind]
	r := model.Resource{Ref: model.Ref{Group: group, Kind: kind, Namespace: ns, Name: name}, Status: st, Version: "v1", ResourceVersion: "1"}
	r.ID = r.Ref.ID()
	return r
}

// --- fake agent -----------------------------------------------------------

// fakeAgent speaks the agent side of the protocol over a real WebSocket.
// Access checks are answered by allow; by default a user may do anything
// in a namespace named like one of their groups without the "eddy:" prefix,
// and ops (eddy:platform) may do anything.
type fakeAgent struct {
	conn *websocket.Conn

	mu          sync.Mutex
	requests    []protocol.Request
	accessCalls int
	cancels     []string
	allow       func(id protocol.Identity, c protocol.AccessCheck) bool
	respond     func(f protocol.Frame, req protocol.Request) bool // true if handled
	cancelled   chan string
}

func dialAgent(ctx context.Context, url, cluster, token string) (*fakeAgent, error) {
	return dialAgentHello(ctx, url, token, protocol.Hello{Protocol: protocol.Version, Cluster: cluster, AgentVersion: "v0.1.0-test", KubernetesVersion: "v1.33.0", FluxVersion: "v2.7.0"})
}

// dialAgentHello connects a fake agent that sends hello as its first frame.
func dialAgentHello(ctx context.Context, url, token string, hello protocol.Hello) (*fakeAgent, error) {
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("http %d: %w", resp.StatusCode, err)
		}
		return nil, err
	}
	conn.SetReadLimit(protocol.MaxFrameBytes)
	a := &fakeAgent{conn: conn, cancelled: make(chan string, 16), allow: defaultAllow}
	a.sendFrame(protocol.TypeHello, "", hello)
	return a, nil
}

func defaultAllow(id protocol.Identity, c protocol.AccessCheck) bool {
	if slices.Contains(id.Groups, "eddy:platform") {
		return true
	}
	return slices.Contains(id.Groups, "eddy:"+c.Namespace)
}

func (a *fakeAgent) close() { a.conn.CloseNow() }

// onRequest installs a custom responder; it returns true for requests it handled.
func (a *fakeAgent) onRequest(fn func(f protocol.Frame, req protocol.Request) bool) {
	a.mu.Lock()
	a.respond = fn
	a.mu.Unlock()
}

func (a *fakeAgent) sendFrame(typ protocol.FrameType, id string, payload any) {
	f := protocol.Frame{Type: typ, ID: id}
	if payload != nil {
		b, _ := json.Marshal(payload)
		f.Payload = b
	}
	b, _ := json.Marshal(f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.conn.Write(ctx, websocket.MessageText, b)
}

func (a *fakeAgent) reply(id string, result any, perr *protocol.Error) {
	var raw json.RawMessage
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	a.sendFrame(protocol.TypeResponse, id, protocol.Response{Result: raw, Error: perr})
}

func (a *fakeAgent) recorded(op protocol.Op) []protocol.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []protocol.Request
	for _, r := range a.requests {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

func (a *fakeAgent) serve() {
	for {
		_, data, err := a.conn.Read(context.Background())
		if err != nil {
			return
		}
		var f protocol.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.Type {
		case protocol.TypeCancel:
			a.mu.Lock()
			a.cancels = append(a.cancels, f.ID)
			a.mu.Unlock()
			a.cancelled <- f.ID
		case protocol.TypeRequest:
			var req protocol.Request
			_ = json.Unmarshal(f.Payload, &req)
			a.mu.Lock()
			a.requests = append(a.requests, req)
			respond := a.respond
			a.mu.Unlock()
			if respond != nil && respond(f, req) {
				continue
			}
			go a.handle(f, req)
		}
	}
}

func (a *fakeAgent) handle(f protocol.Frame, req protocol.Request) {
	switch req.Op {
	case protocol.OpAccess:
		var args protocol.AccessArgs
		_ = json.Unmarshal(req.Args, &args)
		a.mu.Lock()
		a.accessCalls++
		allow := a.allow
		a.mu.Unlock()
		out := make([]bool, len(args.Checks))
		for i, c := range args.Checks {
			out[i] = allow(req.Identity, c)
		}
		a.reply(f.ID, protocol.AccessResult{Allowed: out}, nil)
	case protocol.OpReconcile, protocol.OpSuspend, protocol.OpResume:
		if !defaultAllow(req.Identity, protocol.AccessCheck{Namespace: req.Target.Namespace}) {
			a.reply(f.ID, nil, &protocol.Error{Code: 403, Message: "forbidden: User \"" + req.Identity.User + "\" cannot patch"})
			return
		}
		a.reply(f.ID, nil, nil)
	case protocol.OpYAML:
		a.reply(f.ID, protocol.YAMLResult{YAML: "kind: Kustomization\nmetadata:\n  name: x\nspec:\n  token: ghp_0123456789abcdefghijklmnopqrstuvwxyzAB\n"}, nil)
	case protocol.OpEvents:
		a.reply(f.ID, protocol.EventsResult{Events: []model.Event{{Type: "Normal", Reason: "ReconciliationSucceeded", Message: "ok", Count: 1}}}, nil)
	case protocol.OpLogs:
		var args protocol.LogsArgs
		_ = json.Unmarshal(req.Args, &args)
		a.sendFrame(protocol.TypeStream, f.ID, protocol.LogChunk{Lines: []string{"line 1", "line 2"}})
		a.sendFrame(protocol.TypeStream, f.ID, protocol.LogChunk{Lines: []string{"line 3"}})
		if !args.Follow {
			a.sendFrame(protocol.TypeStreamEnd, f.ID, protocol.Response{})
		}
	default:
		a.reply(f.ID, nil, &protocol.Error{Code: 400, Message: "unknown op"})
	}
}

// --- browser client ---------------------------------------------------------

type client struct {
	t    *testing.T
	e    *testEnv
	http *http.Client
	csrf string
}

func (e *testEnv) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, e: e, http: &http.Client{Jar: jar, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// login signs in as a local user through /auth/csrf and /auth/local/login.
func (e *testEnv) login(user string) *client {
	e.t.Helper()
	c := e.newClient()
	var pre struct{ CSRF string }
	c.do("GET", "/auth/csrf", nil, &pre, http.StatusOK)
	c.csrf = pre.CSRF
	c.do("POST", "/auth/local/login", map[string]string{"username": user, "password": testPassword}, nil, http.StatusNoContent)
	var me meResponse
	c.do("GET", "/api/v1/me", nil, &me, http.StatusOK)
	c.csrf = me.CSRF
	return c
}

func (c *client) request(method, path string, body any) *http.Response {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.e.baseURL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && c.csrf != "" {
		req.Header.Set("X-Eddy-CSRF", c.csrf)
		req.Header.Set("Origin", c.e.baseURL)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

// do sends a request, checks the status and decodes the JSON body into out.
func (c *client) do(method, path string, body, out any, want int) {
	c.t.Helper()
	resp := c.request(method, path, body)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// errorCode performs a request and returns the status and error code.
func (c *client) errorCode(method, path string, body any) (int, string) {
	c.t.Helper()
	resp := c.request(method, path, body)
	defer resp.Body.Close()
	var eb errorBody
	_ = json.NewDecoder(resp.Body).Decode(&eb)
	return resp.StatusCode, eb.Error.Code
}

// rawRequest sends a raw string body with the session's CSRF headers.
func (c *client) rawRequest(method, path, body string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.e.baseURL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Eddy-CSRF", c.csrf)
	req.Header.Set("Origin", c.e.baseURL)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}
