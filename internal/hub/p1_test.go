package hub

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// manyRows returns n Deployments spread over team-a and team-b, so a list
// response is well over gzipMinBytes.
func manyRows(n int) []model.Resource {
	var out []model.Resource
	for i := range n {
		ns := []string{"team-a", "team-b"}[i%2]
		r := res("Deployment", ns, fmt.Sprintf("web-%03d", i), model.StatusReady)
		r.Message = "Deployment has minimum availability."
		out = append(out, r)
	}
	return out
}

// get sends a GET with extra headers and returns the response with its
// body read (and decompressed when gzipped) into body.
func (c *client) get(path string, header map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.e.baseURL+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var r io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			c.t.Fatalf("GET %s: bad gzip: %v", path, err)
		}
		r = zr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	return resp, b
}

func TestGzipResponses(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, manyRows(200))
	c := e.login("ops")
	gz := map[string]string{"Accept-Encoding": "gzip"}

	resp, body := c.get("/api/v1/clusters/dev/resources", gz)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("resources: status %d, encoding %q, want gzip", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	if !slices.Contains(resp.Header.Values("Vary"), "Accept-Encoding") {
		t.Fatalf("resources: Vary = %v", resp.Header.Values("Vary"))
	}
	var list struct{ Items []model.Resource }
	if err := json.Unmarshal(body, &list); err != nil || len(list.Items) != 200 {
		t.Fatalf("resources: %d items, err %v", len(list.Items), err)
	}

	// Without Accept-Encoding: identity, still Vary.
	resp, _ = c.get("/api/v1/clusters/dev/resources", nil)
	if resp.Header.Get("Content-Encoding") != "" || !slices.Contains(resp.Header.Values("Vary"), "Accept-Encoding") {
		t.Fatalf("no Accept-Encoding: encoding %q vary %v", resp.Header.Get("Content-Encoding"), resp.Header.Values("Vary"))
	}
	resp, _ = c.get("/api/v1/clusters/dev/resources", map[string]string{"Accept-Encoding": "gzip;q=0, br"})
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("gzip;q=0 was compressed")
	}

	// BREACH: routes that carry secrets are never compressed.
	for _, path := range []string{"/api/v1/me", "/api/v1/tokens", "/auth/providers", "/auth/csrf"} {
		resp, _ := c.get(path, gz)
		if resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s was compressed", path)
		}
	}
	// Small bodies stay plain.
	resp, _ = c.get("/api/v1/clusters/dev/findings", gz)
	if resp.Header.Get("Content-Encoding") != "" {
		t.Error("a body under gzipMinBytes was compressed")
	}
}

func TestCompressible(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"/api/v1/clusters", true},
		{"/api/v1/clusters/dev/resources", true},
		{"/api/v1/search", true},
		{"/api/v1/stream", true},
		{"/api/v1/me", false},
		{"/api/v1/tokens", false},
		{"/api/v1/tokens/abc", false},
		{"/api/v1/clusters/dev/pods/ns/p/logs", false},
		{"/api/v1/clusters/dev/workloads/Job/ns/j/logs", false},
		{"/api/v1/clusters/dev/connection", false},
		{"/api/v1/clusters/dev/join-token", false},
		{"/api/v1/ai/ask", false},
		{"/auth/csrf", false},
		{"/mcp", false},
		{"/api/v1/clusters/dev/objects/Kustomization/a/b/x", true},
	} {
		r, _ := http.NewRequest(http.MethodGet, "http://hub"+tt.path, nil)
		if got := compressible(r); got != tt.want {
			t.Errorf("compressible(GET %s) = %v, want %v", tt.path, got, tt.want)
		}
	}
	r, _ := http.NewRequest(http.MethodPost, "http://hub/api/v1/threads", nil)
	if compressible(r) {
		t.Error("a POST is compressible")
	}
}

func TestGzipEventStream(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, manyRows(4))
	c := e.login("ops")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/api/v1/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("stream encoding %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Every event is flushed through the compressor: hello and clusters
	// arrive without the stream ending.
	sc := bufio.NewScanner(zr)
	var events []string
	for sc.Scan() && len(events) < 2 {
		if ev, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			events = append(events, ev)
		}
	}
	if !slices.Equal(events, []string{"hello", "clusters"}) {
		t.Fatalf("events %v, want hello and clusters", events)
	}
}

func TestListETags(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, manyRows(10))
	ops, alice := e.login("ops"), e.login("alice")
	for _, path := range []string{"/api/v1/clusters", "/api/v1/clusters/dev/resources", "/api/v1/clusters/dev/kinds", "/api/v1/threads", "/api/v1/attention"} {
		resp, _ := ops.get(path, nil)
		etag := resp.Header.Get("ETag")
		if resp.StatusCode != 200 || !strings.HasPrefix(etag, `W/"`) || resp.Header.Get("Cache-Control") != "private, no-cache" {
			t.Fatalf("%s: status %d etag %q cache-control %q", path, resp.StatusCode, etag, resp.Header.Get("Cache-Control"))
		}
		resp, body := ops.get(path, map[string]string{"If-None-Match": etag})
		if resp.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("%s: revalidation status %d, want 304", path, resp.StatusCode)
		}
		// Another user never matches ops' validator.
		resp, _ = alice.get(path, map[string]string{"If-None-Match": etag})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: alice got %d with ops' ETag, want 200", path, resp.StatusCode)
		}
	}
	// A change of the view changes the validator.
	resp, _ := ops.get("/api/v1/clusters/dev/resources", nil)
	etag := resp.Header.Get("ETag")
	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Deployment", "team-a", "new", model.StatusFailed)}})
	waitFor(t, func() bool { return e.hub.agents.get("dev").size() == 11 })
	resp, _ = ops.get("/api/v1/clusters/dev/resources", map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after a change: status %d, want 200", resp.StatusCode)
	}
}

func TestListETagBucket(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "http://hub/api/v1/clusters?b=2&a=1", nil)
	r2, _ := http.NewRequest(http.MethodGet, "http://hub/api/v1/clusters?a=1&b=2", nil)
	t0 := time.Unix(45*1000, 0)
	if listETag(alice, r, "7", t0) != listETag(alice, r2, "7", t0.Add(44*time.Second)) {
		t.Fatal("the ETag changed inside one bucket or with query order")
	}
	if listETag(alice, r, "7", t0) == listETag(alice, r, "7", t0.Add(etagBucket)) {
		t.Fatal("the ETag outlived its bucket")
	}
	bob := alice
	bob.Groups = []string{"eddy:team-a"}
	if listETag(alice, r, "7", t0) == listETag(bob, r, "7", t0) {
		t.Fatal("different groups share an ETag")
	}
	if !etagMatches(`"x", W/"abc"`, `W/"abc"`) || !etagMatches(`*`, `W/"abc"`) || etagMatches(`W/"abd"`, `W/"abc"`) {
		t.Fatal("etagMatches")
	}
}

type searchBody struct {
	Items []struct {
		Cluster  string         `json:"cluster"`
		Resource model.Resource `json:"resource"`
		Match    fuzzyMatch     `json:"match"`
		Stale    bool           `json:"stale"`
	} `json:"items"`
	Partial []string `json:"partial"`
}

func TestSearchEndpoint(t *testing.T) {
	e := newEnv(t, "")
	rows := []model.Resource{
		res("HelmRelease", "team-a", "podinfo", model.StatusReady),
		res("HelmRelease", "team-b", "podinfo", model.StatusFailed),
		res("Deployment", "team-a", "podinfo-frontend", model.StatusReady),
		res("Kustomization", "team-a", "apps", model.StatusReady),
		res("Pod", "team-b", "redis-0", model.StatusReady),
	}
	e.connectAgent("dev", testToken, rows)
	e.connectAgent("prod", testToken+"-prod", []model.Resource{res("HelmRelease", "team-a", "podinfo", model.StatusReady)})
	ops, alice := e.login("ops"), e.login("alice")

	var got searchBody
	ops.do("GET", "/api/v1/search?q=podinfo&cluster=prod", nil, &got, 200)
	var ids []string
	for _, it := range got.Items {
		ids = append(ids, it.Cluster+"|"+it.Resource.ID)
	}
	// Exact name matches first; failing first among equals, then the
	// current cluster (prod); prefix matches after.
	want := []string{
		"dev|helm.toolkit.fluxcd.io/HelmRelease/team-b/podinfo",
		"prod|helm.toolkit.fluxcd.io/HelmRelease/team-a/podinfo",
		"dev|helm.toolkit.fluxcd.io/HelmRelease/team-a/podinfo",
		"dev|apps/Deployment/team-a/podinfo-frontend",
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("fleet search = %v\nwant %v", ids, want)
	}
	if m := got.Items[0].Match; len(m.Primary) != 1 || m.Primary[0] != (fuzzyRange{0, 7}) || len(m.Secondary) != 4 {
		t.Fatalf("match ranges %+v", m)
	}

	// SAR-filtered: alice sees team-a only.
	alice.do("GET", "/api/v1/search?q=podinfo", nil, &got, 200)
	for _, it := range got.Items {
		if it.Resource.Namespace != "team-a" {
			t.Fatalf("alice sees %s", it.Resource.ID)
		}
	}
	if len(got.Items) != 3 {
		t.Fatalf("alice: %d results, want 3", len(got.Items))
	}
	// Scope cluster, a secondary-field match (kind abbreviation), limit.
	ops.do("GET", "/api/v1/search?q=hr&scope=cluster&cluster=dev&limit=1", nil, &got, 200)
	if len(got.Items) != 1 || got.Items[0].Cluster != "dev" || got.Items[0].Resource.Kind != "HelmRelease" {
		t.Fatalf("scoped search: %+v", got.Items)
	}
	// Subsequence on the name.
	ops.do("GET", "/api/v1/search?q=pdf&scope=cluster&cluster=dev", nil, &got, 200)
	if len(got.Items) != 1 || got.Items[0].Resource.Name != "podinfo-frontend" {
		t.Fatalf("subsequence search: %+v", got.Items)
	}
	ops.do("GET", "/api/v1/search?q=", nil, &got, 200)
	if len(got.Items) != 0 {
		t.Fatal("an empty query returned items")
	}
	for _, bad := range []string{"/api/v1/search?q=a&scope=cluster", "/api/v1/search?q=a&scope=world", "/api/v1/search?q=a&limit=0",
		"/api/v1/search?q=" + strings.Repeat("a", searchMaxQuery+1)} {
		if code, _ := ops.errorCode("GET", bad, nil); code != http.StatusBadRequest {
			t.Errorf("GET %s: %d, want 400", bad, code)
		}
	}
	if code, _ := ops.errorCode("GET", "/api/v1/search?q=a&scope=cluster&cluster=nope", nil); code != http.StatusNotFound {
		t.Errorf("unknown cluster: %d, want 404", code)
	}
}

func TestSearchLimitsPerUser(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, manyRows(4))
	c := e.login("ops")
	var releases []func()
	for range searchPerUser {
		r, ok := searchLimiterOf(e).acquire("local:ops")
		if !ok {
			t.Fatal("could not take a search slot")
		}
		releases = append(releases, r)
	}
	if code, _ := c.errorCode("GET", "/api/v1/search?q=web", nil); code != http.StatusTooManyRequests {
		t.Fatalf("search beyond the per-user cap: %d, want 429", code)
	}
	for _, r := range releases {
		r()
	}
	if code, _ := c.errorCode("GET", "/api/v1/search?q=web", nil); code != http.StatusOK {
		t.Fatalf("search after release: %d", code)
	}
}

// searchLimiterOf digs the api's search limiter out of the hub for tests.
func searchLimiterOf(e *testEnv) *concurrencyLimiter { return e.hub.httpAPI.searches }

func TestAttentionEndpoint(t *testing.T) {
	e := newEnv(t, "")
	failed := res("HelmRelease", "team-a", "broken", model.StatusFailed)
	failed.LastChanged = time.Unix(2000, 0)
	older := res("Deployment", "team-b", "crashing", model.StatusFailed)
	older.LastChanged = time.Unix(1000, 0)
	rows := []model.Resource{
		failed, older,
		res("Kustomization", "team-a", "apps", model.StatusReconciling),
		res("Kustomization", "team-b", "paused", model.StatusSuspended),
		res("Deployment", "team-a", "rolling", model.StatusReconciling), // not Flux: not attention
		res("Deployment", "team-a", "fine", model.StatusReady),
	}
	a := e.connectAgent("dev", testToken, rows)
	ops, alice := e.login("ops"), e.login("alice")
	var got struct {
		Items []struct {
			Cluster  string         `json:"cluster"`
			Resource model.Resource `json:"resource"`
		} `json:"items"`
		Total int `json:"total"`
	}
	ops.do("GET", "/api/v1/attention", nil, &got, 200)
	var names []string
	for _, it := range got.Items {
		names = append(names, it.Resource.Name)
	}
	if !slices.Equal(names, []string{"broken", "crashing", "apps", "paused"}) || got.Total != 4 {
		t.Fatalf("attention = %v (total %d)", names, got.Total)
	}
	ops.do("GET", "/api/v1/attention?limit=1&cluster=dev", nil, &got, 200)
	if len(got.Items) != 1 || got.Total != 4 {
		t.Fatalf("limit: %d items, total %d", len(got.Items), got.Total)
	}
	alice.do("GET", "/api/v1/attention", nil, &got, 200)
	names = nil
	for _, it := range got.Items {
		names = append(names, it.Resource.Name)
	}
	if !slices.Equal(names, []string{"broken", "apps"}) {
		t.Fatalf("alice attention = %v", names)
	}
	// Kept in step with deltas.
	fixed := failed
	fixed.Status = model.StatusReady
	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{fixed}, Deletes: []string{older.ID}})
	waitFor(t, func() bool { return len(e.hub.agents.get("dev").attentionRows()) == 2 })
	ops.do("GET", "/api/v1/attention", nil, &got, 200)
	if got.Total != 2 {
		t.Fatalf("after the delta: total %d, want 2", got.Total)
	}
}

func TestClusterKindCounts(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		res("Kustomization", "team-b", "infra", model.StatusFailed),
		res("Deployment", "team-a", "web", model.StatusReady),
	})
	var got struct{ Items []model.ClusterInfo }
	e.login("alice").do("GET", "/api/v1/clusters", nil, &got, 200)
	for _, ci := range got.Items {
		if ci.Name != "dev" {
			continue
		}
		want := map[string]map[model.Status]int{"Kustomization": {model.StatusReady: 1}, "Deployment": {model.StatusReady: 1}}
		if fmt.Sprint(ci.Kinds) != fmt.Sprint(want) {
			t.Fatalf("alice kinds = %v, want %v", ci.Kinds, want)
		}
		return
	}
	t.Fatal("dev missing")
}

func TestSnapshotParts(t *testing.T) {
	cases := []struct {
		name  string
		parts int
	}{{"marked parts", 3}, {"legacy agent", 0}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "")
			a, err := dialAgent(context.Background(), e.agentURL("dev"), "dev", testToken)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.close)
			go a.serve()
			rows := manyRows(9)
			a.sendFrame(protocol.TypeSnapshot, "", protocol.Snapshot{Resources: rows[:3], Parts: tc.parts})
			a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: rows[3:6], Part: min(tc.parts, 2)})
			waitFor(t, func() bool { return e.hub.agents.get("dev") != nil })
			time.Sleep(50 * time.Millisecond)
			s := e.hub.agents.get("dev")
			if s.isSynced() || s.size() != 0 {
				t.Fatalf("after 2 of 3 parts: synced %v, %d rows; want an unsynced, empty view", s.isSynced(), s.size())
			}
			last := protocol.Delta{Upserts: rows[6:], Part: tc.parts}
			a.sendFrame(protocol.TypeDelta, "", last)
			waitFor(t, func() bool { return s.isSynced() && s.size() == 9 })
			// Later deltas apply as usual.
			a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Deletes: []string{rows[0].ID}})
			waitFor(t, func() bool { return s.size() == 8 })
		})
	}
}

func TestStaleViewAfterDisconnect(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, manyRows(6))
	ops, alice := e.login("ops"), e.login("alice")
	// Warm the SAR cache of both users, as their pages would.
	var list struct {
		Items []model.Resource `json:"items"`
		Stale bool             `json:"stale"`
	}
	ops.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	alice.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	if list.Stale || len(list.Items) != 3 {
		t.Fatalf("alice before: %d items, stale %v", len(list.Items), list.Stale)
	}
	a.close()
	waitFor(t, func() bool { return e.hub.agents.isStale("dev") })

	var clusters struct{ Items []model.ClusterInfo }
	ops.do("GET", "/api/v1/clusters", nil, &clusters, 200)
	ci := clusters.Items[slices.IndexFunc(clusters.Items, func(c model.ClusterInfo) bool { return c.Name == "dev" })]
	if ci.Connected || !ci.Stale || ci.LastSeen.IsZero() || ci.Counts[model.StatusReady] != 6 {
		t.Fatalf("stale cluster info: %+v", ci)
	}
	resp := alice.request("GET", "/api/v1/clusters/dev/resources", nil)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if resp.StatusCode != 200 || !list.Stale || len(list.Items) != 3 || resp.Header.Get("X-Eddy-Stale") != "true" {
		t.Fatalf("stale read: status %d, stale %v, %d items, header %q", resp.StatusCode, list.Stale, len(list.Items), resp.Header.Get("X-Eddy-Stale"))
	}
	// Writes and live reads are refused.
	if code, ec := ops.errorCode("POST", "/api/v1/clusters/dev/objects/Deployment/team-a/web-000/reconcile", map[string]any{}); code != http.StatusServiceUnavailable && code != http.StatusBadRequest {
		t.Fatalf("write to a stale cluster: %d %s", code, ec)
	}
	if code, ec := ops.errorCode("GET", "/api/v1/clusters/dev/objects/Deployment/team-a/web-000/yaml", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("yaml of a stale cluster: %d %s, want 503", code, ec)
	}
	// A user who never asked is not shown the stale view.
	bob := e.login("bob")
	if code, _ := bob.errorCode("GET", "/api/v1/clusters/dev/resources", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("bob (no cached answers) read a stale view: %d, want 503", code)
	}

	// The agent comes back: the view is refreshed in place.
	e.connectAgent("dev", testToken, manyRows(4))
	waitFor(t, func() bool { return !e.hub.agents.isStale("dev") })
	list.Stale = false
	ops.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	if list.Stale || len(list.Items) != 4 {
		t.Fatalf("after reconnect: %d items, stale %v", len(list.Items), list.Stale)
	}
}

func TestStaleViewBudget(t *testing.T) {
	e := newEnv(t, "")
	e.hub.agents.mu.Lock()
	e.hub.agents.staleMax = 10
	e.hub.agents.mu.Unlock()
	dev := e.connectAgent("dev", testToken, manyRows(6))
	prod := e.connectAgent("prod", testToken+"-prod", manyRows(6))
	dev.close()
	waitFor(t, func() bool { return e.hub.agents.isStale("dev") })
	prod.close()
	waitFor(t, func() bool { return e.hub.agents.isStale("prod") })
	// 12 rows exceed the budget of 10: the older stale view (dev) went.
	if e.hub.agents.isStale("dev") || e.hub.agents.reader("dev") != nil {
		t.Fatal("the oldest stale view was not evicted")
	}
}

// TestDeflateReadLimit: permessage-deflate is negotiated, and the read
// limit counts decompressed bytes, so a small compressed frame that
// inflates past MaxFrameBytes closes the session.
func TestDeflateReadLimit(t *testing.T) {
	e := newEnv(t, "")
	ctx := context.Background()
	conn, resp, err := websocket.Dial(ctx, e.agentURL("dev"), &websocket.DialOptions{
		HTTPHeader:      http.Header{"Authorization": []string{"Bearer " + testToken}},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if ext := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(ext, "permessage-deflate") {
		t.Fatalf("permessage-deflate not negotiated: %q", ext)
	}
	hello, _ := json.Marshal(protocol.Hello{Protocol: protocol.Version, Cluster: "dev", AgentVersion: "test"})
	f, _ := json.Marshal(protocol.Frame{Type: protocol.TypeHello, Payload: hello})
	if err := conn.Write(ctx, websocket.MessageText, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.hub.agents.get("dev") != nil })
	bomb := `{"type":"ping","payload":"` + strings.Repeat("a", protocol.MaxFrameBytes+1024) + `"}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(bomb)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.hub.agents.get("dev") == nil })
}

// TestIncrementalCountsMatchRecount: counts kept up to date by deltas equal
// a full recount after any sequence of upserts and deletes.
func TestIncrementalCountsMatchRecount(t *testing.T) {
	s := testSession(newBus())
	rows := manyRows(40)
	if err := s.handle(frame(t, protocol.TypeSnapshot, protocol.Snapshot{Resources: rows, Parts: 1})); err != nil {
		t.Fatal(err)
	}
	statuses := []model.Status{model.StatusReady, model.StatusFailed, model.StatusReconciling}
	for i := range 200 {
		r := rows[(i*7)%len(rows)]
		r.Status = statuses[i%len(statuses)]
		d := protocol.Delta{Upserts: []model.Resource{r}}
		if i%5 == 0 {
			d = protocol.Delta{Deletes: []string{rows[(i*3)%len(rows)].ID}}
		}
		if err := s.handle(frame(t, protocol.TypeDelta, d)); err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprint(s.counted().tuples)
		s.mu.Lock()
		s.recountLocked()
		s.mu.Unlock()
		s.countSnap = nil
		if want := fmt.Sprint(s.counted().tuples); got != want {
			t.Fatalf("step %d: incremental %s, recount %s", i, got, want)
		}
	}
	var total int
	for _, n := range s.counted().kinds[groupResource{"apps", "deployments"}] {
		total += n
	}
	if total != s.size() {
		t.Fatalf("kind total %d, rows %d", total, s.size())
	}
}
