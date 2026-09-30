package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eddy-gitops/eddy/internal/ai"
	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/protocol"
	"github.com/eddy-gitops/eddy/internal/store"
	"github.com/eddy-gitops/eddy/internal/threads"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("x: %w", fleet.ErrNotFound), 404, "not_found"},
		{store.ErrNotFound, 404, "not_found"},
		{fleet.ErrForbidden, 403, "forbidden"},
		{fleet.ErrDisconnected, 503, "disconnected"},
		{fleet.ErrDisabled, 503, "disabled"},
		{fleet.ErrConfirmRequired, 428, "confirm_required"},
		{badRequest("nope"), 400, "bad_request"},
		{fmt.Errorf("%w: empty", threads.ErrInvalid), 400, "bad_request"},
		{store.ErrInvalid, 400, "bad_request"},
		{fmt.Errorf("%w: q", ai.ErrInvalid), 400, "bad_request"},
		{ai.ErrRateLimited, 429, "rate_limited"},
		{store.ErrLimit, 409, "conflict"},
		{store.ErrConflict, 409, "conflict"},
		{&AgentError{Code: 403, Message: "rbac"}, 403, "forbidden"},
		{&AgentError{Code: 404}, 404, "not_found"},
		{&AgentError{Code: 400}, 400, "bad_request"},
		{&AgentError{Code: 409}, 409, "conflict"},
		{&AgentError{Code: 503}, 503, "unavailable"},
		{&AgentError{Code: 500, Message: "secret internals"}, 500, "internal"},
		{&http.MaxBytesError{Limit: 1}, 413, "bad_request"},
		{errors.New("boom"), 500, "internal"},
	}
	for _, tc := range tests {
		got := classify(tc.err)
		if got.status != tc.status || got.code != tc.code {
			t.Errorf("classify(%v) = %d %s, want %d %s", tc.err, got.status, got.code, tc.status, tc.code)
		}
		if got.status == 500 && got.message != "internal error" {
			t.Errorf("500 leaks %q", got.message)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t, "")
	for _, path := range []string{"/", "/api/v1/me", "/auth/providers", "/healthz"} {
		resp, err := http.Get(e.baseURL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		h := resp.Header
		want := map[string]string{
			"Content-Security-Policy":      contentSecurityPolicy,
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Permissions-Policy":           permissionsPolicy,
		}
		for k, v := range want {
			if h.Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", path, k, h.Get(k), v)
			}
		}
		if h.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS on a plain-http publicURL", path)
		}
		if h.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS header present", path)
		}
		if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/auth") {
			if h.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: Cache-Control %q", path, h.Get("Cache-Control"))
			}
		}
		if h.Get("X-Request-Id") == "" {
			t.Errorf("%s: no request id", path)
		}
	}
	if !strings.Contains(contentSecurityPolicy, "frame-ancestors 'none'") || strings.Contains(contentSecurityPolicy, "unsafe-inline") {
		t.Fatal("CSP drifted from ADR-0003")
	}
	rec := &statusRecorder{ResponseWriter: newRecorder()}
	securityHeaders(true, http.NotFoundHandler()).ServeHTTP(rec, mustReq("GET", "/"))
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS for an https publicURL")
	}
}

func TestCSRFEnforcedOnAPI(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)})
	c := e.login("alice")
	path := "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile"

	saved := c.csrf
	c.csrf = "" // no header and no Origin
	if st, code := c.errorCode("POST", path, map[string]any{}); st != 403 || code != "forbidden" {
		t.Fatalf("POST without CSRF: %d %s", st, code)
	}
	c.csrf = "wrong"
	if st, _ := c.errorCode("POST", path, map[string]any{}); st != 403 {
		t.Fatalf("POST with a wrong CSRF token: %d", st)
	}
	c.csrf = saved
	c.do("POST", path, map[string]any{}, nil, http.StatusAccepted)

	// A foreign Origin fails even with the right token.
	req, _ := http.NewRequest("POST", e.baseURL+path, strings.NewReader("{}"))
	req.Header.Set("X-Eddy-CSRF", c.csrf)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
}

func TestUnauthenticatedAndPATOnAPI(t *testing.T) {
	e := newEnv(t, "")
	c := e.newClient()
	if st, code := c.errorCode("GET", "/api/v1/clusters", nil); st != 401 || code != "unauthorized" {
		t.Fatalf("anonymous: %d %s", st, code)
	}
	tok := e.issuePAT(e.login("alice"), []string{"read"})
	req, _ := http.NewRequest("GET", e.baseURL+"/api/v1/clusters", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("PAT on /api: %d, want 401", resp.StatusCode)
	}
}

func (e *testEnv) issuePAT(c *client, scopes []string) string {
	e.t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	c.do("POST", "/api/v1/tokens", map[string]any{"name": "test", "scopes": scopes, "ttl": "1d"}, &out, http.StatusCreated)
	return out.Token
}

func mcpCall(t *testing.T, e *testEnv, bearer, cookieFrom string, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.baseURL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookieFrom != "" {
		req.Header.Set("Cookie", cookieFrom)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMCPNotBehindSessionAuth(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)})
	c := e.login("alice")
	var cookie string
	for _, ck := range c.http.Jar.Cookies(mustURL(e.baseURL)) {
		cookie += ck.Name + "=" + ck.Value + "; "
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_clusters","arguments":{}}}`
	// A session cookie is ignored: no CSRF 403, no session, just the bearer 401.
	if st, _ := mcpCall(t, e, "", cookie, list); st != http.StatusUnauthorized {
		t.Fatalf("cookie-only /mcp: %d, want 401", st)
	}
	tok := e.issuePAT(c, []string{"read"})
	st, body := mcpCall(t, e, tok, "", list)
	if st != http.StatusOK || !strings.Contains(body, `\"name\":\"dev\"`) {
		t.Fatalf("PAT /mcp: %d %s", st, body)
	}
	// GET is refused by the MCP handler itself, not by the SPA or the session layer.
	resp, err := http.Get(e.baseURL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp: %d", resp.StatusCode)
	}
}

func TestProtectedClusterNeedsConfirm(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("prod", testToken+"-prod", []model.Resource{res("HelmRelease", "team-a", "podinfo", model.StatusReady)})
	c := e.login("alice")
	path := "/api/v1/clusters/prod/objects/HelmRelease/team-a/podinfo/suspend"
	if st, code := c.errorCode("POST", path, map[string]any{}); st != 428 || code != "confirm_required" {
		t.Fatalf("suspend without confirm: %d %s", st, code)
	}
	if st, code := c.errorCode("POST", path, map[string]any{"confirm": "dev"}); st != 428 || code != "confirm_required" {
		t.Fatalf("suspend with a wrong confirm: %d %s", st, code)
	}
	if n := len(a.recorded(protocol.OpSuspend)); n != 0 {
		t.Fatalf("agent received %d suspend requests before confirmation", n)
	}
	c.do("POST", path, map[string]any{"confirm": "prod"}, nil, http.StatusAccepted)
	reqs := a.recorded(protocol.OpSuspend)
	if len(reqs) != 1 || reqs[0].Identity.User != "local:alice" || reqs[0].Target.Group != "helm.toolkit.fluxcd.io" {
		t.Fatalf("suspend requests %+v", reqs)
	}
	events, _, _ := e.store.Audit().Query(context.Background(), store.AuditFilter{Subject: "local:alice"})
	var results []store.AuditResult
	for _, ev := range events {
		if ev.Action == "suspend" {
			results = append(results, ev.Result)
		}
	}
	if len(results) != 3 || results[0] != store.AuditOK || results[1] != store.AuditDenied {
		t.Fatalf("suspend audit results (newest first) %v", results)
	}
}

func TestWriteDeniedByRBACIsAudited(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-b", "apps", model.StatusReady)})
	c := e.login("alice") // team-a only; writes are not pre-checked
	if st, code := c.errorCode("POST", "/api/v1/clusters/dev/objects/Kustomization/team-b/apps/reconcile", nil); st != 403 || code != "forbidden" {
		t.Fatalf("reconcile outside RBAC: %d %s", st, code)
	}
	events, _, _ := e.store.Audit().Query(context.Background(), store.AuditFilter{Subject: "local:alice"})
	if len(events) == 0 || events[0].Action != "reconcile" || events[0].Result != store.AuditDenied {
		t.Fatalf("audit %+v", events)
	}
}

func TestReadsAreFiltered(t *testing.T) {
	e := newEnv(t, "")
	pod := res("Pod", "team-a", "web-1", model.StatusReady)
	dep := res("Deployment", "team-a", "web", model.StatusReady)
	pod.Owner = &dep.Ref
	e.connectAgent("dev", testToken, []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		res("Kustomization", "team-b", "infra", model.StatusFailed),
		dep, pod,
	})
	alice, bob := e.login("alice"), e.login("bob")

	var list struct {
		Items           []model.Resource `json:"items"`
		ResourceVersion string           `json:"resourceVersion"`
	}
	alice.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	if len(list.Items) != 3 || list.ResourceVersion == "" {
		t.Fatalf("alice sees %d items (rv %q)", len(list.Items), list.ResourceVersion)
	}
	alice.do("GET", "/api/v1/clusters/dev/resources?kind=kustomization&status=ready", nil, &list, 200)
	if len(list.Items) != 1 || list.Items[0].Name != "apps" {
		t.Fatalf("filtered list %+v", list.Items)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/clusters/dev/resources?kind=Secret", nil); st != 400 {
		t.Fatalf("unknown kind filter: %d", st)
	}
	bob.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	if len(list.Items) != 1 || list.Items[0].Name != "infra" {
		t.Fatalf("bob sees %+v", list.Items)
	}

	var clusters struct{ Items []model.ClusterInfo }
	alice.do("GET", "/api/v1/clusters", nil, &clusters, 200)
	if len(clusters.Items) != 2 || !clusters.Items[0].Connected || clusters.Items[0].Counts[model.StatusReady] != 3 || clusters.Items[0].Counts[model.StatusFailed] != 0 {
		t.Fatalf("alice clusters %+v", clusters.Items)
	}

	if st, code := bob.errorCode("GET", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps", nil); st != 404 || code != "not_found" {
		t.Fatalf("bob reads alice's object: %d %s", st, code)
	}
	var obj model.Resource
	alice.do("GET", "/api/v1/clusters/dev/objects/kustomization/team-a/apps", nil, &obj, 200)
	if obj.Kind != "Kustomization" {
		t.Fatalf("object %+v", obj)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/clusters/dev/objects/Secret/team-a/x", nil); st != 404 {
		t.Fatalf("unknown kind: %d", st)
	}
	if st, code := alice.errorCode("GET", "/api/v1/clusters/prod/resources", nil); st != 503 || code != "disconnected" {
		t.Fatalf("disconnected cluster: %d %s", st, code)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/clusters/ghost/resources", nil); st != 404 {
		t.Fatalf("unknown cluster: %d", st)
	}

	var kids struct{ Items []model.Resource }
	alice.do("GET", "/api/v1/clusters/dev/objects/Deployment/team-a/web/children", nil, &kids, 200)
	if len(kids.Items) != 1 || kids.Items[0].Name != "web-1" {
		t.Fatalf("children %+v", kids.Items)
	}

	var y struct{ YAML string }
	alice.do("GET", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/yaml", nil, &y, 200)
	if strings.Contains(y.YAML, "ghp_") || !strings.Contains(y.YAML, "REDACTED") {
		t.Fatalf("yaml not redacted: %q", y.YAML)
	}
	var ev struct{ Items []model.Event }
	alice.do("GET", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/events", nil, &ev, 200)
	if len(ev.Items) != 1 {
		t.Fatalf("events %+v", ev.Items)
	}
	if st, _ := bob.errorCode("GET", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/events", nil); st != 404 {
		t.Fatalf("bob reads alice's events: %d", st)
	}
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func mustReq(method, path string) *http.Request { return httptest.NewRequest(method, path, nil) }

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func httptestGet(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Body.String()
}

// sseStream reads events from an SSE response.
type sseEvent struct {
	name string
	data string
}

func (c *client) openStream(path string) (<-chan sseEvent, func()) {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", c.e.baseURL+path, nil)
	hc := *c.http
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		c.t.Fatalf("stream: %d %v", resp.StatusCode, resp.Header)
	}
	ch := make(chan sseEvent, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "" && cur.name != "":
				ch <- cur
				cur = sseEvent{}
			}
		}
	}()
	return ch, cancel
}

// next returns the next event named name, skipping others.
func next(t *testing.T, ch <-chan sseEvent, name string) sseEvent {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %s", name)
			}
			if ev.name == name {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %s event", name)
		}
	}
}

func TestStreamFiltersChangesPerUser(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, nil)
	alice, bob := e.login("alice"), e.login("bob")
	as, stopA := alice.openStream("/api/v1/stream")
	defer stopA()
	bs, stopB := bob.openStream("/api/v1/stream")
	defer stopB()
	next(t, as, "hello")
	next(t, bs, "hello")
	next(t, as, "clusters")

	ka, kb := res("Kustomization", "team-a", "apps", model.StatusReady), res("Kustomization", "team-b", "infra", model.StatusReady)
	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{ka, kb}})

	var ch changeEvent
	_ = json.Unmarshal([]byte(next(t, as, "change").data), &ch)
	if ch.Cluster != "dev" || len(ch.Upserts) != 1 || ch.Upserts[0].Name != "apps" {
		t.Fatalf("alice change %+v", ch)
	}
	_ = json.Unmarshal([]byte(next(t, bs, "change").data), &ch)
	if len(ch.Upserts) != 1 || ch.Upserts[0].Name != "infra" {
		t.Fatalf("bob change %+v", ch)
	}

	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Deletes: []string{kb.ID}})
	_ = json.Unmarshal([]byte(next(t, bs, "change").data), &ch)
	if len(ch.Deletes) != 1 || ch.Deletes[0] != kb.ID {
		t.Fatalf("bob delete %+v", ch)
	}
	// Alice must not learn about bob's delete; her next event is the clusters update.
	for {
		ev := <-as
		if ev.name == "change" {
			t.Fatalf("alice saw another team's change: %s", ev.data)
		}
		if ev.name == "clusters" {
			break
		}
	}

	// Threads: a thread on team-a reaches alice, not bob.
	var created struct{ Thread store.Thread }
	alice.do("POST", "/api/v1/threads", map[string]any{
		"ref": map[string]string{"cluster": "dev", "kind": "Kustomization", "namespace": "team-a", "name": "apps"}, "title": "t", "body": "b",
	}, &created, http.StatusCreated)
	var te threadEvent
	_ = json.Unmarshal([]byte(next(t, as, "thread").data), &te)
	if te.ThreadID != created.Thread.ID || te.Ref.Group != "kustomize.toolkit.fluxcd.io" {
		t.Fatalf("thread event %+v", te)
	}
	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{kb}})
	for ev := range bs {
		if ev.name == "thread" {
			t.Fatal("bob notified about a thread he cannot see")
		}
		if ev.name == "change" {
			break
		}
	}
}

func TestLogsOverSSE(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{res("Pod", "team-a", "web-1", model.StatusReady)})
	c := e.login("alice")
	ch, stop := c.openStream("/api/v1/clusters/dev/pods/team-a/web-1/logs?tail=10")
	defer stop()
	var lines []string
	for {
		ev := <-ch
		if ev.name == "end" {
			if ev.data != "{}" {
				t.Fatalf("end %s", ev.data)
			}
			break
		}
		var l struct{ Lines []string }
		_ = json.Unmarshal([]byte(ev.data), &l)
		lines = append(lines, l.Lines...)
	}
	if len(lines) != 3 {
		t.Fatalf("lines %v", lines)
	}
	if st, _ := e.login("bob").errorCode("GET", "/api/v1/clusters/dev/pods/team-a/web-1/logs", nil); st != 404 {
		t.Fatalf("bob reads alice's pod logs: %d", st)
	}
	if st, _ := c.errorCode("GET", "/api/v1/clusters/dev/pods/team-a/web-1/logs?tail=0", nil); st != 400 {
		t.Fatalf("tail=0: %d", st)
	}
}

func TestStreamCapPerUser(t *testing.T) {
	e := newEnv(t, "")
	c := e.login("alice")
	var stops []func()
	for range maxStreamsPerUser {
		_, stop := c.openStream("/api/v1/stream")
		stops = append(stops, stop)
	}
	defer func() {
		for _, s := range stops {
			s()
		}
	}()
	if st, code := c.errorCode("GET", "/api/v1/stream", nil); st != 429 || code != "rate_limited" {
		t.Fatalf("stream over the cap: %d %s", st, code)
	}
}

func TestAIDisabled(t *testing.T) {
	e := newEnv(t, "")
	c := e.login("alice")
	if st, code := c.errorCode("POST", "/api/v1/ai/ask", map[string]string{"cluster": "dev", "question": "why?"}); st != 503 || code != "disabled" {
		t.Fatalf("ask: %d %s", st, code)
	}
	var me meResponse
	c.do("GET", "/api/v1/me", nil, &me, 200)
	if me.Features.AI || !me.Features.MCP || !me.Features.MCPWrites || !me.Features.EphemeralStore || me.Features.DevMode {
		t.Fatalf("features %+v", me.Features)
	}
	if me.User != "local:alice" || me.CSRF == "" || len(me.Groups) == 0 {
		t.Fatalf("me %+v", me)
	}
}

func TestAuditVisibility(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)})
	alice, ops := e.login("alice"), e.login("ops")
	alice.do("POST", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile", map[string]bool{"withSource": true}, nil, 202)

	var page struct {
		Items []store.AuditEvent `json:"items"`
		Next  string             `json:"next"`
	}
	alice.do("GET", "/api/v1/audit", nil, &page, 200)
	for _, ev := range page.Items {
		if ev.Subject != "local:alice" {
			t.Fatalf("alice sees %s's events", ev.Subject)
		}
	}
	if st, _ := alice.errorCode("GET", "/api/v1/audit?subject=local:ops", nil); st != 403 {
		t.Fatalf("alice queries ops: %d", st)
	}
	ops.do("GET", "/api/v1/audit?subject=local:alice&cluster=dev", nil, &page, 200)
	if len(page.Items) != 1 || page.Items[0].Action != "reconcile" || page.Items[0].Target.Kind != "Kustomization" {
		t.Fatalf("ops view of alice %+v", page.Items)
	}
}

// TestEndToEnd is the smoke test: a hub with a memory store, a static
// cluster, a fake agent and a local user.
func TestEndToEnd(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		res("GitRepository", "team-a", "repo", model.StatusReady),
	})
	c := e.newClient()
	var prov map[string]bool
	c.do("GET", "/auth/providers", nil, &prov, 200)
	if !prov["local"] || prov["dev"] {
		t.Fatalf("providers %v", prov)
	}
	// Wrong password: generic 401.
	var pre struct{ CSRF string }
	c.do("GET", "/auth/csrf", nil, &pre, 200)
	c.csrf = pre.CSRF
	if st, _ := c.errorCode("POST", "/auth/local/login", map[string]string{"username": "alice", "password": "nope"}); st != 401 {
		t.Fatalf("bad password: %d", st)
	}
	c = e.login("alice")

	var clusters struct{ Items []model.ClusterInfo }
	c.do("GET", "/api/v1/clusters", nil, &clusters, 200)
	if clusters.Items[0].Name != "dev" || clusters.Items[0].AgentVersion != "v0.1.0-test" || clusters.Items[1].Connected {
		t.Fatalf("clusters %+v", clusters.Items)
	}
	var list struct{ Items []model.Resource }
	c.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	if len(list.Items) != 2 {
		t.Fatalf("resources %+v", list.Items)
	}
	c.do("POST", "/api/v1/clusters/dev/objects/Kustomization/team-a/apps/reconcile", map[string]bool{"withSource": true}, nil, 202)
	reqs := a.recorded(protocol.OpReconcile)
	if len(reqs) != 1 || reqs[0].Identity.User != "local:alice" || string(reqs[0].Args) != `{"withSource":true}` {
		t.Fatalf("agent got %+v", reqs)
	}
	if st, _ := c.errorCode("POST", "/api/v1/clusters/dev/objects/GitRepository/team-a/repo/explode", nil); st != 404 {
		t.Fatalf("unknown action: %d", st)
	}
	var page struct{ Items []store.AuditEvent }
	c.do("GET", "/api/v1/audit", nil, &page, 200)
	found := false
	for _, ev := range page.Items {
		if ev.Action == "reconcile" && ev.Result == store.AuditOK && ev.Via == "web" && ev.Target.Name == "apps" && ev.RequestID != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no reconcile audit event in %+v", page.Items)
	}

	// Threads round trip.
	var created struct {
		Thread  store.Thread
		Message store.Message
	}
	c.do("POST", "/api/v1/threads", map[string]any{"ref": map[string]string{"cluster": "dev"}, "title": "Cluster note", "body": "hello"}, &created, 201)
	var reply store.Message
	c.do("POST", "/api/v1/threads/"+created.Thread.ID+"/messages", map[string]string{"body": "again"}, &reply, 201)
	var detail struct {
		Thread   store.Thread
		Messages []store.Message
	}
	c.do("GET", "/api/v1/threads/"+created.Thread.ID, nil, &detail, 200)
	if len(detail.Messages) != 2 || detail.Thread.MessageCount != 2 {
		t.Fatalf("thread %+v", detail)
	}
	c.do("POST", "/api/v1/threads/"+created.Thread.ID+"/resolve", nil, nil, 200)
	var threadsPage struct {
		Items []store.Thread `json:"items"`
		Next  string         `json:"next"`
	}
	c.do("GET", "/api/v1/threads?cluster=dev&status=resolved", nil, &threadsPage, 200)
	if len(threadsPage.Items) != 1 {
		t.Fatalf("threads %+v", threadsPage.Items)
	}
	if st, _ := e.login("bob").errorCode("DELETE", "/api/v1/threads/"+created.Thread.ID, nil); st != 403 {
		t.Fatalf("bob deletes alice's thread: %d", st)
	}
	c.do("DELETE", "/api/v1/threads/"+created.Thread.ID, nil, nil, 204)
	if st, _ := c.errorCode("POST", "/api/v1/threads", map[string]any{"ref": map[string]string{"cluster": "dev"}, "title": "x", "body": "y", "type": "ask"}); st != 400 {
		t.Fatalf("ask thread via API: %d", st)
	}

	c.do("POST", "/auth/logout", nil, nil, 204)
	if st, _ := c.errorCode("GET", "/api/v1/me", nil); st != 401 {
		t.Fatalf("after logout: %d", st)
	}

	resp, err := http.Get(e.baseURL + "/api/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unknown api route unauthenticated: %d", resp.StatusCode)
	}
	mresp := httptestGet(t, e.hub.MetricsHandler(), "/metrics")
	if !strings.Contains(mresp, "eddy_hub_agents_connected 1") || !strings.Contains(mresp, `eddy_hub_http_requests_total{code="200"}`) {
		t.Fatalf("metrics:\n%s", mresp)
	}
	if got := httptestGet(t, e.hub.MetricsHandler(), "/readyz"); got != "ok\n" {
		t.Fatalf("readyz %q", got)
	}
}

// TestStreamOutlivesServerTimeouts runs the stream on a server with short
// read and write timeouts, as Run configures them (much longer) in production.
func TestStreamOutlivesServerTimeouts(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, nil)
	c := e.login("alice")
	srv := httptest.NewUnstartedServer(e.hub.UIHandler())
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()
	e2 := *e
	e2.baseURL = srv.URL
	c2 := *c
	c2.e = &e2
	ch, stop := c2.openStream("/api/v1/stream")
	defer stop()
	next(t, ch, "hello")
	time.Sleep(600 * time.Millisecond)
	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "late", model.StatusReady)}})
	var ce changeEvent
	_ = json.Unmarshal([]byte(next(t, ch, "change").data), &ce)
	if len(ce.Upserts) != 1 || ce.Upserts[0].Name != "late" {
		t.Fatalf("change after the server timeouts: %+v", ce)
	}
}
