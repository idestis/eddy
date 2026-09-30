package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
)

const (
	tokRead    = "eddy_pat_read000000000000000000000000000000000000000000"
	tokOperate = "eddy_pat_oper000000000000000000000000000000000000000000"
	tokBob     = "eddy_pat_bob0000000000000000000000000000000000000000000"
)

var principals = map[string]identity.Principal{
	tokRead:    {User: "local:alice", Groups: []string{"eddy:dev"}, Display: "Alice", TokenID: "readtok00001", Scopes: []identity.Scope{identity.ScopeRead}},
	tokOperate: {User: "local:alice", Groups: []string{"eddy:dev"}, Display: "Alice", TokenID: "opertok00001", Scopes: []identity.Scope{identity.ScopeRead, identity.ScopeOperate}},
	tokBob:     {User: "local:bob", Groups: []string{"eddy:dev"}, Display: "Bob", TokenID: "bobtok000001", Scopes: []identity.Scope{identity.ScopeRead}},
}

type flagBox struct {
	mu sync.Mutex
	f  runtimeflags.Flags
}

func (b *flagBox) Current() runtimeflags.Flags {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.f
}

func (b *flagBox) set(fn func(*runtimeflags.Flags)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(&b.f)
}

type env struct {
	t       *testing.T
	url     string
	fleet   *fakeFleet
	threads *fakeThreads
	audit   *fakeAudit
	flags   *flagBox
}

func defaultConfig() config.MCP {
	return config.MCP{Enabled: true, Writes: true, AllowLogs: true, ProtectedClusters: "confirm", CallsPerMinute: 1000, WritesPerMinute: 100, MaxResultBytes: 64 << 10}
}

func newEnv(t *testing.T, cfg config.MCP, mod ...func(*Options)) *env {
	t.Helper()
	e := &env{t: t, fleet: newFakeFleet(), threads: newFakeThreads(), audit: &fakeAudit{},
		flags: &flagBox{f: runtimeflags.Flags{MCPEnabled: true, MCPWrites: true, MCPAllowLogs: true}}}
	var h http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	e.url = srv.URL
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	o := Options{
		Config:    cfg,
		PublicURL: srv.URL,
		Fleet:     e.fleet,
		Threads:   e.threads,
		Audit:     audit.New(e.audit, log),
		Flags:     e.flags,
		Verify: func(_ context.Context, token string) (identity.Principal, time.Time, error) {
			p, ok := principals[token]
			if !ok {
				return identity.Principal{}, time.Time{}, errors.New("store: token lookup failed for eddy_pat_secret")
			}
			return p, time.Now().Add(time.Hour), nil
		},
		GroupForKind: groupForKind,
		Version:      "test",
		Log:          log,
	}
	for _, m := range mod {
		m(&o)
	}
	var err error
	h, err = NewHandler(o)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return e
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func (e *env) connect(token string) *sdk.ClientSession {
	e.t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "1.0"}, nil)
	tr := &sdk.StreamableClientTransport{
		Endpoint:             e.url,
		HTTPClient:           &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	cs, err := c.Connect(context.Background(), tr, nil)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes a tool and returns the result text and error flag.
func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// data decodes the untrusted_data envelope of a successful call.
func data[T any](t *testing.T, text string) (T, bool) {
	t.Helper()
	var r Result[T]
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return r.UntrustedData, r.Truncated
}

func TestToolListAndAnnotations(t *testing.T) {
	e := newEnv(t, defaultConfig())
	cs := e.connect(tokRead)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*sdk.Tool{}
	for _, tl := range res.Tools {
		got[tl.Name] = tl
	}
	want := []string{"list_clusters", "list_resources", "list_unhealthy", "get_resource", "get_events", "get_logs",
		"list_threads", "get_thread", "create_thread", "reply_thread", "resolve_thread", "reconcile", "suspend", "resume"}
	if len(got) != len(want) {
		t.Errorf("tools = %d, want %d", len(got), len(want))
	}
	for _, n := range want {
		tl := got[n]
		if tl == nil {
			t.Errorf("missing tool %s", n)
			continue
		}
		a := tl.Annotations
		if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: openWorldHint must be false", n)
		}
	}
	for _, n := range []string{"list_resources", "get_resource", "get_logs", "get_thread"} {
		if !got[n].Annotations.ReadOnlyHint || !strings.Contains(got[n].Description, "untrusted") {
			t.Errorf("%s: want readOnlyHint and an untrusted-data note", n)
		}
	}
	if a := got["suspend"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || !a.IdempotentHint {
		t.Errorf("suspend annotations = %+v", a)
	}
	if a := got["reconcile"].Annotations; a.ReadOnlyHint || *a.DestructiveHint {
		t.Errorf("reconcile annotations = %+v", a)
	}
	if a := got["create_thread"].Annotations; a.ReadOnlyHint || *a.DestructiveHint || a.IdempotentHint {
		t.Errorf("create_thread annotations = %+v", a)
	}

	cfg := defaultConfig()
	cfg.AllowLogs, cfg.Writes = false, false
	e2 := newEnv(t, cfg)
	res, err = e2.connect(tokRead).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		switch tl.Name {
		case "get_logs", "reconcile", "suspend", "resume":
			t.Errorf("%s registered while disabled in config", tl.Name)
		}
	}
}

func TestFleetWideReads(t *testing.T) {
	e := newEnv(t, defaultConfig())
	cs := e.connect(tokRead)

	text, isErr := callTool(t, cs, "list_unhealthy", nil)
	if isErr {
		t.Fatal(text)
	}
	u, _ := data[UnhealthyList](t, text)
	if u.Total != 2 || len(u.Items) != 2 || len(u.Disconnected) != 1 || u.Disconnected[0] != "edge" {
		t.Errorf("unhealthy = %+v", u)
	}
	clusters := map[string]string{}
	for _, it := range u.Items {
		clusters[it.Cluster] = string(it.Status)
	}
	if clusters["prod"] != "failed" || clusters["dev"] != "suspended" {
		t.Errorf("unhealthy clusters = %v", clusters)
	}

	// Pagination across clusters.
	text, _ = callTool(t, cs, "list_resources", map[string]any{"limit": 3})
	page1, _ := data[ResourceList](t, text)
	if len(page1.Items) != 3 || page1.Next == "" || page1.Items[0].Cluster != "prod" {
		t.Fatalf("page1 = %+v", page1)
	}
	text, _ = callTool(t, cs, "list_resources", map[string]any{"limit": 3, "cursor": page1.Next})
	page2, _ := data[ResourceList](t, text)
	if len(page2.Items) != 2 || page2.Next != "" {
		t.Errorf("page2 = %+v", page2)
	}
	// A cursor is bound to its user.
	if text, isErr := callTool(t, e.connect(tokBob), "list_resources", map[string]any{"cursor": page1.Next}); !isErr || !strings.Contains(text, "invalid cursor") {
		t.Errorf("foreign cursor accepted: %s", text)
	}
	if text, isErr := callTool(t, cs, "list_resources", map[string]any{"status": "broken"}); !isErr {
		t.Errorf("bad status accepted: %s", text)
	}

	// Every fleet call carries the token owner, via mcp, with the client name.
	for _, p := range e.fleet.users {
		if p.User != "local:alice" || p.Via != identity.ViaMCP || p.TokenID != "readtok00001" || p.Client != "test-client" {
			t.Fatalf("fleet called as %+v", p)
		}
	}
}

func TestGetResourceRedactsAndWraps(t *testing.T) {
	e := newEnv(t, defaultConfig())
	cs := e.connect(tokRead)
	text, isErr := callTool(t, cs, "get_resource", map[string]any{"cluster": "prod", "kind": "Kustomization", "namespace": "flux-system", "name": "apps", "include_yaml": true})
	if isErr {
		t.Fatal(text)
	}
	if !strings.HasPrefix(text, `{"untrusted_data":`) {
		t.Errorf("result not wrapped: %s", text)
	}
	d, _ := data[ResourceDetail](t, text)
	if d.Resource.Name != "apps" || strings.Contains(d.YAML, "hunter2") || !strings.Contains(d.YAML, "[REDACTED:userinfo]") {
		t.Errorf("detail = %+v", d)
	}

	text, _ = callTool(t, cs, "get_events", map[string]any{"cluster": "prod", "kind": "Kustomization", "namespace": "flux-system", "name": "apps"})
	if strings.Contains(text, "abc123") {
		t.Errorf("events not redacted: %s", text)
	}

	for _, kind := range []string{"Secret", "configmap"} {
		text, isErr := callTool(t, cs, "get_resource", map[string]any{"cluster": "prod", "kind": kind, "namespace": "x", "name": "y"})
		if !isErr || !strings.Contains(text, "never available") {
			t.Errorf("%s: %s", kind, text)
		}
	}
	if text, isErr := callTool(t, cs, "get_resource", map[string]any{"cluster": "prod", "kind": "Widget", "name": "y"}); !isErr || !strings.Contains(text, "unsupported kind") {
		t.Errorf("unknown kind: %s", text)
	}
}

func TestResultCap(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxResultBytes = 400
	e := newEnv(t, cfg)
	text, isErr := callTool(t, e.connect(tokRead), "list_resources", nil)
	if isErr {
		t.Fatal(text)
	}
	l, truncated := data[ResourceList](t, text)
	if !truncated || len(l.Items) == 0 || len(l.Items) >= 5 {
		t.Errorf("truncated=%v items=%d", truncated, len(l.Items))
	}
}

func TestWritesScopeAndProtection(t *testing.T) {
	args := func(cluster string, extra map[string]any) map[string]any {
		m := map[string]any{"cluster": cluster, "kind": "Kustomization", "namespace": "flux-system", "name": "apps"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	e := newEnv(t, defaultConfig())
	read, op := e.connect(tokRead), e.connect(tokOperate)

	if text, isErr := callTool(t, read, "reconcile", args("dev", nil)); !isErr || !strings.Contains(text, "operate scope") {
		t.Errorf("read token reconcile: %s", text)
	}
	if text, isErr := callTool(t, op, "reconcile", args("dev", map[string]any{"with_source": true})); isErr {
		t.Errorf("reconcile dev: %s", text)
	}
	if text, isErr := callTool(t, op, "suspend", args("prod", nil)); !isErr || !strings.Contains(text, "confirm_cluster") || !strings.Contains(text, "ask the human") {
		t.Errorf("protected without confirm: %s", text)
	}
	if text, isErr := callTool(t, op, "suspend", args("prod", map[string]any{"confirm_cluster": "dev"})); !isErr {
		t.Errorf("protected with wrong confirm: %s", text)
	}
	if text, isErr := callTool(t, op, "suspend", args("prod", map[string]any{"confirm_cluster": "prod"})); isErr {
		t.Errorf("protected with confirm: %s", text)
	}
	if text, isErr := callTool(t, op, "resume", args("nowhere", nil)); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("unknown cluster: %s", text)
	}

	acts := e.fleet.actionsCopy()
	if len(acts) != 2 || acts[0].Verb != "reconcile" || !acts[0].Opts.WithSource || acts[1].Verb != "suspend" || acts[1].Opts.Confirm != "prod" {
		t.Errorf("actions = %+v", acts)
	}

	e.flags.set(func(f *runtimeflags.Flags) { f.MCPWrites = false })
	if text, isErr := callTool(t, op, "reconcile", args("dev", nil)); !isErr || !strings.Contains(text, "disabled") {
		t.Errorf("writes flag off: %s", text)
	}

	cfg := defaultConfig()
	cfg.ProtectedClusters = "deny"
	e2 := newEnv(t, cfg)
	if text, isErr := callTool(t, e2.connect(tokOperate), "reconcile", args("prod", map[string]any{"confirm_cluster": "prod"})); !isErr || !strings.Contains(text, "protected") {
		t.Errorf("deny mode: %s", text)
	}
	if n := len(e2.fleet.actionsCopy()); n != 0 {
		t.Errorf("deny mode reached fleet %d times", n)
	}

	// Denials are audited as denied.
	var denied int
	for _, ev := range e.audit.all() {
		if ev.Action == "mcp.suspend" && ev.Result == store.AuditDenied {
			denied++
		}
	}
	if denied != 2 {
		t.Errorf("denied suspend audits = %d, want 2", denied)
	}
}

func TestWriteRateLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.WritesPerMinute = 1
	e := newEnv(t, cfg)
	op := e.connect(tokOperate)
	a := map[string]any{"cluster": "dev", "kind": "Kustomization", "namespace": "flux-system", "name": "apps"}
	if text, isErr := callTool(t, op, "reconcile", a); isErr {
		t.Fatal(text)
	}
	if text, isErr := callTool(t, op, "reconcile", a); !isErr || !strings.Contains(text, "per minute") {
		t.Errorf("second write: %s", text)
	}
}

func TestCallRateLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.CallsPerMinute = 2
	e := newEnv(t, cfg)
	cs := e.connect(tokRead)
	for range 2 {
		if text, isErr := callTool(t, cs, "list_clusters", nil); isErr {
			t.Fatal(text)
		}
	}
	if text, isErr := callTool(t, cs, "list_clusters", nil); !isErr || !strings.Contains(text, "rate limit") {
		t.Errorf("third call: %s", text)
	}
	// Another token has its own budget.
	if text, isErr := callTool(t, e.connect(tokBob), "list_clusters", nil); isErr {
		t.Errorf("other token limited: %s", text)
	}
}

func TestThreads(t *testing.T) {
	e := newEnv(t, defaultConfig())
	cs := e.connect(tokRead)
	text, isErr := callTool(t, cs, "create_thread", map[string]any{"cluster": "prod", "kind": "Kustomization", "namespace": "flux-system", "name": "apps", "title": "Broken build", "body": "It fails since main@sha1:abc"})
	if isErr {
		t.Fatal(text)
	}
	w, _ := data[ThreadWrite](t, text)
	th := e.threads.threads[w.Thread.ID]
	wantAuthor := store.Author{Type: store.AuthorHuman, Subject: "local:alice", Display: "Alice", Via: "mcp", Client: "test-client"}
	if th.CreatedBy != wantAuthor || th.Visibility != store.VisibilityResource || th.Type != store.ThreadDiscussion || th.Ref.Group != "kustomize.toolkit.fluxcd.io" {
		t.Errorf("thread = %+v", th)
	}

	if text, isErr := callTool(t, cs, "reply_thread", map[string]any{"id": w.Thread.ID, "body": "password=hunter2 fixed"}); isErr {
		t.Fatal(text)
	}
	text, _ = callTool(t, cs, "get_thread", map[string]any{"id": w.Thread.ID})
	d, _ := data[ThreadDetail](t, text)
	if len(d.Messages) != 2 || strings.Contains(text, "hunter2") {
		t.Errorf("thread detail = %s", text)
	}
	text, _ = callTool(t, cs, "list_threads", map[string]any{"cluster": "prod"})
	if l, _ := data[ThreadList](t, text); len(l.Items) != 1 {
		t.Errorf("list = %s", text)
	}
	if text, isErr := callTool(t, cs, "resolve_thread", map[string]any{"id": w.Thread.ID}); isErr {
		t.Error(text)
	}
	if text, isErr := callTool(t, cs, "reply_thread", map[string]any{"id": w.Thread.ID, "body": strings.Repeat("x", MaxThreadBody+1)}); !isErr || !strings.Contains(text, "exceeds") {
		t.Errorf("long body: %s", text)
	}
	if text, isErr := callTool(t, cs, "get_thread", map[string]any{"id": "missing"}); !isErr || text != "not found" {
		t.Errorf("missing thread: %s", text)
	}

	// With threadWriteScope: operate, a read token cannot write threads.
	e2 := newEnv(t, defaultConfig(), func(o *Options) { o.ThreadWriteScope = identity.ScopeOperate })
	if text, isErr := callTool(t, e2.connect(tokRead), "create_thread", map[string]any{"cluster": "prod", "title": "t", "body": "b"}); !isErr || !strings.Contains(text, "operate") {
		t.Errorf("thread write scope: %s", text)
	}
	if text, isErr := callTool(t, e2.connect(tokOperate), "create_thread", map[string]any{"cluster": "prod", "title": "t", "body": "b"}); isErr {
		t.Errorf("operate token thread write: %s", text)
	}
}

func TestLogs(t *testing.T) {
	e := newEnv(t, defaultConfig())
	e.fleet.logs = []string{"a", "Authorization: Bearer abcdefgh12345", "c"}
	cs := e.connect(tokRead)
	text, isErr := callTool(t, cs, "get_logs", map[string]any{"cluster": "dev", "namespace": "flux-system", "pod": "p", "tail": 100000})
	if isErr {
		t.Fatal(text)
	}
	l, _ := data[LogLines](t, text)
	if len(l.Lines) != 3 || strings.Contains(text, "abcdefgh12345") {
		t.Errorf("logs = %s", text)
	}
	if o := e.fleet.logOpts; o.Follow || o.TailLines != maxLogTail {
		t.Errorf("log options = %+v", o)
	}
	e.flags.set(func(f *runtimeflags.Flags) { f.MCPAllowLogs = false })
	if text, isErr := callTool(t, cs, "get_logs", map[string]any{"cluster": "dev", "namespace": "flux-system", "pod": "p"}); !isErr || !strings.Contains(text, "disabled") {
		t.Errorf("logs flag off: %s", text)
	}
}

func TestAuditEveryCall(t *testing.T) {
	e := newEnv(t, defaultConfig())
	cs := e.connect(tokRead)
	callTool(t, cs, "get_resource", map[string]any{"cluster": "prod", "kind": "Kustomization", "namespace": "flux-system", "name": "apps"})
	callTool(t, cs, "create_thread", map[string]any{"cluster": "prod", "title": "t", "body": "token: ghp_0123456789abcdefghijABCDEFGHIJ012345"})
	evs := e.audit.all()
	if len(evs) != 2 {
		t.Fatalf("audit events = %d", len(evs))
	}
	ev := evs[0]
	if ev.Action != "mcp.get_resource" || ev.Via != "mcp" || ev.TokenID != "readtok00001" || ev.Subject != "local:alice" || ev.Result != store.AuditOK {
		t.Errorf("audit = %+v", ev)
	}
	if ev.Target != (store.ResourceRef{Cluster: "prod", Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}) {
		t.Errorf("target = %+v", ev.Target)
	}
	var d map[string]any
	_ = json.Unmarshal(ev.Detail, &d)
	if d["bytes"].(float64) == 0 || d["client"] != "test-client" || d["args"] == nil {
		t.Errorf("detail = %v", d)
	}
	if _, ok := d["durationMs"]; !ok {
		t.Error("no durationMs")
	}
	if strings.Contains(string(evs[1].Detail), "ghp_") {
		t.Errorf("audit args not redacted: %s", evs[1].Detail)
	}
}

// rawPost sends a raw JSON-RPC request.
func rawPost(t *testing.T, e *env, method string, mod func(*http.Request)) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, _ := http.NewRequest(method, e.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+tokRead)
	if mod != nil {
		mod(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestTransportGuards(t *testing.T) {
	cfg := defaultConfig()
	cfg.AllowedOrigins = []string{"https://claude.ai"}
	e := newEnv(t, cfg)
	tests := []struct {
		name   string
		method string
		mod    func(*http.Request)
		want   int
	}{
		{"get", http.MethodGet, nil, http.StatusMethodNotAllowed},
		{"delete", http.MethodDelete, nil, http.StatusMethodNotAllowed},
		{"preflight", http.MethodOptions, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusMethodNotAllowed},
		{"foreign origin", http.MethodPost, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"allowed origin", http.MethodPost, func(r *http.Request) { r.Header.Set("Origin", "https://claude.ai") }, http.StatusOK},
		{"foreign host", http.MethodPost, func(r *http.Request) { r.Host = "evil.example" }, http.StatusForbidden},
		{"no token", http.MethodPost, func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		{"bad token", http.MethodPost, func(r *http.Request) { r.Header.Set("Authorization", "Bearer eddy_pat_nope") }, http.StatusUnauthorized},
		{"cookie only", http.MethodPost, func(r *http.Request) {
			r.Header.Del("Authorization")
			r.Header.Set("Cookie", "__Host-eddy_session=abc")
		}, http.StatusUnauthorized},
		{"basic auth", http.MethodPost, func(r *http.Request) { r.SetBasicAuth("alice", "pw") }, http.StatusUnauthorized},
		{"ok", http.MethodPost, nil, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := rawPost(t, e, tt.method, tt.mod)
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
				t.Errorf("CORS header sent: %q", v)
			}
		})
	}

	t.Run("body too large", func(t *testing.T) {
		big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"x":"` + strings.Repeat("a", MaxBodyBytes) + `"}}`
		req, _ := http.NewRequest(http.MethodPost, e.url, strings.NewReader(big))
		req.Header.Set("Authorization", "Bearer "+tokRead)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d", resp.StatusCode)
		}
	})

	t.Run("401 hides verifier errors", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, e.url, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer eddy_pat_nope")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), "eddy_pat") || strings.Contains(string(b), "store") {
			t.Errorf("body leaks detail: %q", b)
		}
	})

	t.Run("kill switch", func(t *testing.T) {
		e.flags.set(func(f *runtimeflags.Flags) { f.MCPEnabled = false })
		defer e.flags.set(func(f *runtimeflags.Flags) { f.MCPEnabled = true })
		if resp := rawPost(t, e, http.MethodPost, nil); resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status = %d", resp.StatusCode)
		}
	})
}

func TestConcurrencyPerToken(t *testing.T) {
	s := &server{inflight: newLimiter(0, ConcurrentPerToken, time.Minute, time.Now)}
	var releases []func()
	for range ConcurrentPerToken {
		rel, ok := s.inflight.enter("t:x")
		if !ok {
			t.Fatal("slot refused under the cap")
		}
		releases = append(releases, rel)
	}
	if _, ok := s.inflight.enter("t:x"); ok {
		t.Error("fifth concurrent request allowed")
	}
	if _, ok := s.inflight.enter("t:y"); !ok {
		t.Error("other token refused")
	}
	releases[0]()
	if _, ok := s.inflight.enter("t:x"); !ok {
		t.Error("slot not released")
	}
}

func TestNewHandlerValidation(t *testing.T) {
	if _, err := NewHandler(Options{}); err == nil {
		t.Error("empty options accepted")
	}
	o := Options{Fleet: newFakeFleet(), Threads: newFakeThreads(), Flags: runtimeflags.Static{}, GroupForKind: groupForKind,
		Verify: func(context.Context, string) (identity.Principal, time.Time, error) {
			return identity.Principal{}, time.Time{}, nil
		}, PublicURL: "::bad"}
	if _, err := NewHandler(o); err == nil {
		t.Error("bad publicURL accepted")
	}
}

func TestCanonicalHost(t *testing.T) {
	for _, tt := range []struct{ scheme, in, want string }{
		{"https", "Eddy.Example.com:443", "eddy.example.com"},
		{"https", "eddy.example.com:8443", "eddy.example.com:8443"},
		{"http", "localhost:80", "localhost"},
		{"http", "localhost:8080", "localhost:8080"},
	} {
		if got := canonicalHost(tt.scheme, tt.in); got != tt.want {
			t.Errorf("canonicalHost(%q, %q) = %q, want %q", tt.scheme, tt.in, got, tt.want)
		}
	}
}
