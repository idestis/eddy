package hub

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// indexRows is a small mixed cluster: two namespaces, several kinds and
// statuses, distinct change times.
func indexRows() []model.Resource {
	var out []model.Resource
	add := func(kind, ns, name string, st model.Status, changed int64) {
		r := res(kind, ns, name, st)
		r.LastChanged = time.Unix(changed, 0).UTC()
		r.Message = "msg " + name
		out = append(out, r)
	}
	add("Kustomization", "team-a", "apps", model.StatusReady, 100)
	add("Kustomization", "team-b", "infra", model.StatusFailed, 200)
	add("HelmRelease", "team-a", "podinfo", model.StatusReconciling, 300)
	add("HelmRelease", "team-b", "redis", model.StatusReady, 400)
	add("Deployment", "team-a", "web", model.StatusReady, 500)
	add("Deployment", "team-a", "api", model.StatusFailed, 600)
	add("Deployment", "team-b", "worker", model.StatusSuspended, 700)
	add("Pod", "team-b", "worker-0", model.StatusUnknown, 800)
	return out
}

type indexBody struct {
	Items []struct {
		ID          string       `json:"id"`
		Kind        string       `json:"kind"`
		Namespace   string       `json:"namespace"`
		Name        string       `json:"name"`
		Status      model.Status `json:"status"`
		Message     string       `json:"message"`
		Revision    string       `json:"revision"`
		LastChanged time.Time    `json:"lastChanged"`
	} `json:"items"`
	Total  int    `json:"total"`
	Offset *int   `json:"offset"`
	Next   string `json:"next"`
	Facets struct {
		Kinds      map[string]int       `json:"kinds"`
		Statuses   map[model.Status]int `json:"statuses"`
		Namespaces []namespaceCount     `json:"namespaces"`
	} `json:"facets"`
	ResourceVersion string `json:"resourceVersion"`
	Stale           bool   `json:"stale"`
}

// index fetches an index page into a fresh body.
func (c *client) index(path string) indexBody {
	c.t.Helper()
	var b indexBody
	c.do("GET", path, nil, &b, 200)
	return b
}

func (b indexBody) names() []string {
	out := make([]string, len(b.Items))
	for i, it := range b.Items {
		out[i] = it.Name
	}
	return out
}

func TestIndexBackwardCompatible(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, indexRows())
	ops := e.login("ops")
	for _, path := range []string{"/api/v1/clusters/dev/resources", "/api/v1/clusters/dev/resources?view=full"} {
		var full struct {
			Items           []model.Resource `json:"items"`
			ResourceVersion string           `json:"resourceVersion"`
		}
		ops.do("GET", path, nil, &full, 200)
		if len(full.Items) != len(indexRows()) || full.Items[0].Version != "v1" {
			t.Fatalf("%s: %d items %+v", path, len(full.Items), full.Items)
		}
	}
	for _, bad := range []string{"view=tree", "view=index&sort=size", "view=index&order=up", "view=index&status=sad",
		"view=index&cursor=x&offset=1", "view=index&offset=-1", "view=index&limit=0", "view=index&cursor=%21%21",
		"view=index&kind=Job&includeHidden=1", "view=index&kind=not+a+kind"} {
		if code, _ := ops.errorCode("GET", "/api/v1/clusters/dev/resources?"+bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	if code, _ := ops.errorCode("GET", "/api/v1/clusters/nope/resources?view=index", nil); code != http.StatusNotFound {
		t.Errorf("unknown cluster: %d, want 404", code)
	}
}

func TestIndexSortOrders(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, indexRows())
	ops := e.login("ops")
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"api", "web", "worker", "podinfo", "redis", "apps", "infra", "worker-0"}},
		{"sort=kind&order=desc", []string{"worker-0", "infra", "apps", "redis", "podinfo", "worker", "web", "api"}},
		{"sort=name", []string{"api", "apps", "infra", "podinfo", "redis", "web", "worker", "worker-0"}},
		{"sort=name&order=desc", []string{"worker-0", "worker", "web", "redis", "podinfo", "infra", "apps", "api"}},
		// failed, reconciling, suspended, unknown, ready; then kind, namespace, name.
		{"sort=status", []string{"api", "infra", "podinfo", "worker", "worker-0", "web", "redis", "apps"}},
		{"sort=age", []string{"worker-0", "worker", "api", "web", "redis", "podinfo", "infra", "apps"}},
		{"sort=age&order=desc", []string{"apps", "infra", "podinfo", "redis", "web", "api", "worker", "worker-0"}},
	}
	for _, c := range cases {
		got := ops.index("/api/v1/clusters/dev/resources?view=index&" + c.query)
		if !slices.Equal(got.names(), c.want) {
			t.Errorf("%q: %v\nwant %v", c.query, got.names(), c.want)
		}
		if got.Total != 8 || got.Offset == nil || *got.Offset != 0 || got.Next != "" || got.ResourceVersion == "" {
			t.Errorf("%q: total %d offset %v next %q rv %q", c.query, got.Total, got.Offset, got.Next, got.ResourceVersion)
		}
	}
}

func TestIndexFiltersAndFacets(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, indexRows())
	ops := e.login("ops")
	var got indexBody
	got = ops.index("/api/v1/clusters/dev/resources?view=index&kind=deployment,Pod&status=failed,suspended&namespace=team-b")
	if !slices.Equal(got.names(), []string{"worker"}) || got.Total != 1 {
		t.Fatalf("filtered: %v total %d", got.names(), got.Total)
	}
	// Each facet ignores its own filter.
	wantKinds := map[string]int{"Kustomization": 1, "Deployment": 1} // team-b, failed|suspended
	wantStatus := map[model.Status]int{model.StatusReady: 0, model.StatusSuspended: 1, model.StatusUnknown: 1}
	delete(wantStatus, model.StatusReady)
	if !mapsEqual(got.Facets.Kinds, wantKinds) || !mapsEqual(got.Facets.Statuses, wantStatus) {
		t.Fatalf("facets kinds %v statuses %v", got.Facets.Kinds, got.Facets.Statuses)
	}
	// Deployment|Pod, failed|suspended in any namespace: api (team-a), worker (team-b).
	if !slices.Equal(got.Facets.Namespaces, []namespaceCount{{"team-a", 1}, {"team-b", 1}}) {
		t.Fatalf("namespace facet %v", got.Facets.Namespaces)
	}
	// q matches names, namespaces, kinds and messages, ignoring case.
	for q, want := range map[string][]string{
		"WORK":        {"worker", "worker-0"},
		"helmrelease": {"podinfo", "redis"},
		"msg red":     {"redis"},
		"team-a":      {"api", "web", "podinfo", "apps"},
		"nothing":     {},
	} {
		got = ops.index("/api/v1/clusters/dev/resources?view=index&q=" + url.QueryEscape(q))
		if !slices.Equal(got.names(), want) {
			t.Errorf("q=%q: %v, want %v", q, got.names(), want)
		}
	}
	// No filter: the facets cover everything.
	got = ops.index("/api/v1/clusters/dev/resources?view=index&limit=1")
	if got.Facets.Kinds["Deployment"] != 3 || got.Facets.Statuses[model.StatusFailed] != 2 ||
		!slices.Equal(got.Facets.Namespaces, []namespaceCount{{"team-a", 4}, {"team-b", 4}}) {
		t.Fatalf("unfiltered facets %+v", got.Facets)
	}
}

func mapsEqual[K comparable](a, b map[K]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestIndexSARFiltering(t *testing.T) {
	e := newEnv(t, "")
	rows := indexRows()
	// An inventory-only row of team-a's Kustomization, in team-b: alice may
	// list the parent but not ConfigMaps in team-b.
	inv := model.Resource{Ref: model.Ref{Kind: "ConfigMap", Namespace: "team-b", Name: "cfg"}, Status: model.StatusUnknown, InventoryOnly: true,
		Owner: &model.Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "team-a", Name: "apps"}, Version: "v1"}
	inv.ID = inv.Ref.ID()
	own := inv
	own.Namespace, own.Name = "team-a", "cfg-a"
	own.ID = own.Ref.ID()
	e.connectAgent("dev", testToken, append(rows, inv, own))
	alice := e.login("alice")
	var got indexBody
	got = alice.index("/api/v1/clusters/dev/resources?view=index")
	for _, it := range got.Items {
		if it.Namespace != "team-a" {
			t.Fatalf("alice sees %s", it.ID)
		}
	}
	if got.Total != 5 || len(got.Items) != 5 {
		t.Fatalf("alice: total %d items %v", got.Total, got.names())
	}
	if !slices.Equal(got.Facets.Namespaces, []namespaceCount{{"team-a", 5}}) || got.Facets.Kinds["Pod"] != 0 ||
		got.Facets.Statuses[model.StatusSuspended] != 0 || got.Facets.Kinds["ConfigMap"] != 1 {
		t.Fatalf("alice's facets count hidden rows: %+v", got.Facets)
	}
	// Filtering on a namespace she cannot list shows nothing, and no facet
	// reveals its rows.
	got = alice.index("/api/v1/clusters/dev/resources?view=index&namespace=team-b")
	if got.Total != 0 || len(got.Facets.Kinds) != 0 || len(got.Facets.Statuses) != 0 ||
		!slices.Equal(got.Facets.Namespaces, []namespaceCount{{"team-a", 5}}) {
		t.Fatalf("alice in team-b: %+v", got)
	}
}

func TestIndexPagingOffsetAndCursor(t *testing.T) {
	e := newEnv(t, "")
	rows := manyRows(250)
	e.connectAgent("dev", testToken, rows)
	ops := e.login("ops")
	var all []string
	for _, r := range rows {
		all = append(all, r.Name)
	}
	slices.Sort(all) // one kind; namespaces alternate, so sort by (ns, name)
	var byKey []string
	for _, ns := range []string{"team-a", "team-b"} {
		for _, r := range rows {
			if r.Namespace == ns {
				byKey = append(byKey, r.Name)
			}
		}
	}
	slices.Sort(byKey[:125])
	slices.Sort(byKey[125:])

	var got indexBody
	got = ops.index("/api/v1/clusters/dev/resources?view=index&offset=120&limit=10")
	if got.Total != 250 || *got.Offset != 120 || !slices.Equal(got.names(), byKey[120:130]) || got.Next == "" {
		t.Fatalf("offset page: total %d offset %d %v next %q", got.Total, *got.Offset, got.names(), got.Next)
	}
	got = ops.index("/api/v1/clusters/dev/resources?view=index&offset=300")
	if len(got.Items) != 0 || got.Total != 250 || got.Next != "" {
		t.Fatalf("past the end: %+v", got)
	}
	// Default limit 200, then the cursor continues where it stopped.
	got = ops.index("/api/v1/clusters/dev/resources?view=index")
	if len(got.Items) != indexDefaultLimit || got.Next == "" {
		t.Fatalf("first page: %d items, next %q", len(got.Items), got.Next)
	}
	walked := got.names()
	got = ops.index("/api/v1/clusters/dev/resources?view=index&cursor=" + got.Next)
	if got.Offset != nil || got.Next != "" {
		t.Fatalf("cursor page: offset %v next %q", got.Offset, got.Next)
	}
	walked = append(walked, got.names()...)
	if !slices.Equal(walked, byKey) {
		t.Fatalf("cursor walk differs from the order")
	}
	// A cursor is bound to its sort and order.
	got = ops.index("/api/v1/clusters/dev/resources?view=index&limit=5&sort=name")
	if code, _ := ops.errorCode("GET", "/api/v1/clusters/dev/resources?view=index&cursor="+got.Next, nil); code != http.StatusBadRequest {
		t.Fatalf("cursor of another sort: %d, want 400", code)
	}
	// limit is capped.
	got = ops.index("/api/v1/clusters/dev/resources?view=index&limit=5000")
	if len(got.Items) != 250 {
		t.Fatalf("limit cap: %d", len(got.Items))
	}
}

// TestIndexCursorUnderChanges walks every sort order with a cursor while
// rows are added, deleted and changed. The documented guarantee: a row that
// exists for the whole walk and whose sort key does not change is returned
// exactly once, and the pages follow the order.
func TestIndexCursorUnderChanges(t *testing.T) {
	e := newEnv(t, "")
	var stable []model.Resource
	for i := range 300 {
		r := res("Deployment", []string{"team-a", "team-b", "team-c"}[i%3], fmt.Sprintf("stable-%03d", i), model.StatusReady)
		r.LastChanged = time.Unix(int64(1000+i), 0)
		stable = append(stable, r)
	}
	a := e.connectAgent("dev", testToken, stable)
	ops := e.login("ops")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			churn := res("Deployment", "team-b", fmt.Sprintf("churn-%03d", i%50), []model.Status{model.StatusFailed, model.StatusReady}[i%2])
			churn.LastChanged = time.Unix(int64(500+i), 0)
			d := protocol.Delta{Upserts: []model.Resource{churn}}
			if i%3 == 0 {
				d.Deletes = []string{res("Deployment", "team-b", fmt.Sprintf("churn-%03d", (i+25)%50), "").ID}
			}
			a.sendFrame(protocol.TypeDelta, "", d)
			time.Sleep(time.Millisecond)
		}
	})
	defer func() { close(stop); wg.Wait() }()

	for _, sort := range []string{"kind", "name", "status", "age"} {
		for _, order := range []string{"asc", "desc"} {
			seen := map[string]int{}
			var prev string
			path := "/api/v1/clusters/dev/resources?view=index&limit=17&sort=" + sort + "&order=" + order
			cursor := ""
			for pages := 0; ; pages++ {
				if pages > 100 {
					t.Fatalf("%s %s: the walk does not end", sort, order)
				}
				p := path
				if cursor != "" {
					p += "&cursor=" + cursor
				}
				got := ops.index(p)
				for _, it := range got.Items {
					seen[it.Name]++
				}
				if len(got.Items) > 0 && sort == "name" {
					first := got.Items[0].Name
					if prev != "" && (order == "asc") != (first > prev) {
						t.Fatalf("%s %s: page starts at %s after %s", sort, order, first, prev)
					}
					prev = got.Items[len(got.Items)-1].Name
				}
				if got.Next == "" {
					break
				}
				cursor = got.Next
			}
			for _, r := range stable {
				if seen[r.Name] != 1 {
					t.Fatalf("%s %s: %s seen %d times", sort, order, r.Name, seen[r.Name])
				}
			}
			for name, n := range seen {
				if n > 1 && !strings.HasPrefix(name, "churn-") {
					t.Fatalf("%s %s: %s seen %d times", sort, order, name, n)
				}
			}
		}
	}
}

func TestIndexETag(t *testing.T) {
	e := newEnv(t, "")
	a := e.connectAgent("dev", testToken, indexRows())
	ops, alice := e.login("ops"), e.login("alice")
	path := "/api/v1/clusters/dev/resources?view=index&limit=3"
	resp, _ := ops.get(path, nil)
	etag := resp.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) || resp.Header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("etag %q cache-control %q", etag, resp.Header.Get("Cache-Control"))
	}
	if resp, body := ops.get(path, map[string]string{"If-None-Match": etag}); resp.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("revalidation: %d", resp.StatusCode)
	}
	if resp, _ := alice.get(path, map[string]string{"If-None-Match": etag}); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice with ops' etag: %d", resp.StatusCode)
	}
	// Another page or filter is another validator.
	if resp, _ := ops.get(path+"&offset=3", map[string]string{"If-None-Match": etag}); resp.StatusCode != http.StatusOK {
		t.Fatalf("other page: %d", resp.StatusCode)
	}
	a.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Deployment", "team-a", "new", model.StatusFailed)}})
	waitFor(t, func() bool { return e.hub.agents.get("dev").size() == 9 })
	resp, body := ops.get(path, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"total":9`) {
		t.Fatalf("after a change: %d %s", resp.StatusCode, body)
	}
}

func TestIndexRowShape(t *testing.T) {
	e := newEnv(t, "")
	r := res("Kustomization", "team-a", "apps", model.StatusReady)
	r.Message = strings.Repeat("é", 200)
	r.Revision = "main@sha1:0123456789abcdef0123456789abcdef01234567"
	r.Conditions = []model.Condition{{Type: "Ready", Status: "True"}}
	r.Labels = map[string]string{"app.kubernetes.io/name": "apps"}
	e.connectAgent("dev", testToken, []model.Resource{r})
	ops := e.login("ops")
	_, body := ops.get("/api/v1/clusters/dev/resources?view=index", nil)
	for _, absent := range []string{"conditions", "labels", "resourceVersion\":\"1", "\"version\""} {
		if strings.Contains(string(body), absent) {
			t.Errorf("index row carries %s: %s", absent, body)
		}
	}
	got := ops.index("/api/v1/clusters/dev/resources?view=index")
	it := got.Items[0]
	if it.ID != r.ID || len([]rune(it.Message)) != indexMessageRunes || it.Revision != "main@sha1:0123456789ab" {
		t.Fatalf("row %+v", it)
	}
}

// TestIndexIncremental checks that the patched index always equals a
// fresh build, through random upserts, deletes and status changes.
func TestIndexIncremental(t *testing.T) {
	var seq atomic.Uint64
	v := newClusterView(&seq)
	rnd := rand.New(rand.NewPCG(1, 2))
	name := func() string { return fmt.Sprintf("r-%02d", rnd.IntN(60)) }
	var initial []model.Resource
	for range 40 {
		initial = append(initial, res("Deployment", "team-a", name(), model.StatusReady))
	}
	v.replace(initial)
	v.index()
	for step := range 300 {
		var d protocol.Delta
		for range rnd.IntN(5) {
			kind := []string{"Deployment", "HelmRelease", "Kustomization"}[rnd.IntN(3)]
			r := res(kind, []string{"team-a", "team-b"}[rnd.IntN(2)], name(), model.Statuses[rnd.IntN(len(model.Statuses))])
			d.Upserts = append(d.Upserts, r)
		}
		for range rnd.IntN(3) {
			d.Deletes = append(d.Deletes, res("Deployment", []string{"team-a", "team-b"}[rnd.IntN(2)], name(), "").ID)
		}
		v.apply(d)
		v.mu.RLock()
		ix, want := v.idx, buildIndex(v.resources)
		v.mu.RUnlock()
		if ix == nil {
			t.Fatalf("step %d: the index was dropped", step)
		}
		if len(ix.entries) != len(want.entries) {
			t.Fatalf("step %d: %d entries, want %d", step, len(ix.entries), len(want.entries))
		}
		for i := range ix.entries {
			g, w := ix.entries[i], want.entries[i]
			if g.id != w.id || g.status != w.status || ix.tuples[g.t1] != want.tuples[w.t1] {
				t.Fatalf("step %d: entry %d is %s/%s, want %s/%s", step, i, g.id, g.status, w.id, w.status)
			}
		}
	}
	// A bulk delta drops the index; the next request rebuilds it.
	var bulk protocol.Delta
	for i := range indexBulk + 1 {
		bulk.Upserts = append(bulk.Upserts, res("Deployment", "team-c", fmt.Sprintf("bulk-%d", i), model.StatusReady))
	}
	v.apply(bulk)
	if v.idx != nil {
		t.Fatal("a bulk delta kept the index")
	}
	if ix := v.index(); len(ix.entries) != v.size() {
		t.Fatalf("rebuilt index has %d entries, view %d", len(ix.entries), v.size())
	}
	// A new snapshot drops it too.
	v.replace(initial)
	if v.idx != nil {
		t.Fatal("a snapshot kept the index")
	}
}

func TestVisibleTuplesCache(t *testing.T) {
	var calls atomic.Int64
	f := &fleetService{authz: newAuthorizer(func(_ context.Context, _ string, id protocol.Identity, checks []protocol.AccessCheck) ([]bool, error) {
		calls.Add(1)
		out := make([]bool, len(checks))
		for i, c := range checks {
			out[i] = defaultAllow(id, c)
		}
		return out, nil
	}, newMetrics())}
	f.authz.ttl = time.Nanosecond // every call reaches the sender unless the set is cached
	now := time.Unix(1000, 0)
	f.visCache().now = func() time.Time { return now }
	ix := newViewIndex(0)
	ix.intern(accessTuple{Group: "apps", Resource: "deployments", Namespace: "team-a"})
	ix.intern(accessTuple{Group: "apps", Resource: "deployments", Namespace: "team-b"})
	p := identity.Principal{User: "alice", Groups: []string{"eddy:team-a"}}
	got, err := f.visibleTuples(context.Background(), p, "dev", ix, ix.tuples)
	if err != nil || !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("allowed %v err %v", got, err)
	}
	n := calls.Load()
	if got, _ := f.visibleTuples(context.Background(), p, "dev", ix, ix.tuples); !slices.Equal(got, []bool{true, false}) || calls.Load() != n {
		t.Fatalf("cached set: %v, %d new calls", got, calls.Load()-n)
	}
	// A new tuple is answered on its own and appended.
	ix.intern(accessTuple{Group: "apps", Resource: "deployments", Namespace: "team-c"})
	if got, _ := f.visibleTuples(context.Background(), p, "dev", ix, ix.tuples); !slices.Equal(got, []bool{true, false, false}) {
		t.Fatalf("appended set: %v", got)
	}
	// Other groups are another subject; another index is another set.
	bob := identity.Principal{User: "alice", Groups: []string{"eddy:team-b"}}
	if got, _ := f.visibleTuples(context.Background(), bob, "dev", ix, ix.tuples); !slices.Equal(got, []bool{false, true, false}) {
		t.Fatalf("other groups: %v", got)
	}
	// The set expires.
	n = calls.Load()
	now = now.Add(visibleTTL)
	f.visibleTuples(context.Background(), p, "dev", ix, ix.tuples)
	if calls.Load() == n {
		t.Fatal("an expired set was reused")
	}
}

func TestScanSlots(t *testing.T) {
	s := newScanSlots(2)
	far := time.Now().Add(time.Minute)
	first := s.acquire(context.Background(), far)
	second := s.acquire(context.Background(), far)
	if !first || !second {
		t.Fatal("could not take free slots")
	}
	if s.acquire(context.Background(), time.Now().Add(20*time.Millisecond)) {
		t.Fatal("took a third slot")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.acquire(ctx, far) {
		t.Fatal("took a slot with a cancelled context")
	}
	// Waiters are served in arrival order.
	var (
		mu    sync.Mutex
		order []int
		wg    sync.WaitGroup
	)
	for i := range 5 {
		wg.Go(func() {
			if s.acquire(context.Background(), far) {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
			}
		})
		time.Sleep(20 * time.Millisecond) // let it queue
	}
	for range 5 {
		s.release()
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	if !slices.Equal(order, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("served %v, want arrival order", order)
	}
}

// TestSearchScanSlotsShared checks that searches share the replica's scan
// slots: concurrent searches never run more scans at once than there are
// slots, and a search that cannot get one in time reports its clusters as
// partial instead of waiting.
func TestSearchScanSlotsShared(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, manyRows(500))
	e.connectAgent("prod", testToken+"-prod", manyRows(500))
	f := e.hub.fleet
	f.lazyInit()
	ops := identity.Principal{User: "local:ops", Groups: []string{"eddy:platform"}}
	o := searchOptions{Query: "web", Fleet: true, Limit: 10}

	// Every slot taken: the search gives up after searchQueueDeadline.
	n := cap(f.scans.ch)
	for range n {
		f.scans.acquire(context.Background(), time.Now().Add(time.Second))
	}
	start := time.Now()
	res, err := f.Search(context.Background(), ops, o)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < searchQueueDeadline || took > searchQueueDeadline+2*time.Second {
		t.Fatalf("search returned after %s", took)
	}
	if !slices.Equal(res.Partial, []string{"dev", "prod"}) || len(res.Items) != 0 {
		t.Fatalf("partial %v items %d", res.Partial, len(res.Items))
	}
	for range n {
		f.scans.release()
	}
	// Free again: concurrent searches all complete.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			res, err := f.Search(context.Background(), ops, o)
			if err != nil || len(res.Partial) != 0 || len(res.Items) == 0 {
				t.Errorf("concurrent search: %d items, partial %v, err %v", len(res.Items), res.Partial, err)
			}
		})
	}
	wg.Wait()
	if len(f.scans.ch) != 0 {
		t.Fatalf("%d slots leaked", len(f.scans.ch))
	}
}

func TestTopHits(t *testing.T) {
	rnd := rand.New(rand.NewPCG(3, 4))
	for range 200 {
		n, k := rnd.IntN(300), 1+rnd.IntN(40)
		hs := make([]searchHit, n)
		for i := range hs {
			hs[i] = searchHit{cluster: fmt.Sprint(rnd.IntN(3)), id: fmt.Sprint(rnd.IntN(50)), score: float64(100 + rnd.IntN(800)), order: rnd.IntN(12)}
		}
		want := slices.Clone(hs)
		slices.SortFunc(want, compareHits)
		want = want[:min(k, n)]
		if got := topHits(hs, k); !slices.Equal(got, want) {
			t.Fatalf("n %d k %d: %v\nwant %v", n, k, got, want)
		}
	}
}

// status=attention selects exactly the rows needsAttention picks (failed, or
// a reconciling or suspended Flux object), the encoding the web list uses for
// its "Needs attention" chip on windowed clusters.
func TestIndexStatusAttention(t *testing.T) {
	e := newEnv(t, "")
	rows := indexRows()
	e.connectAgent("dev", testToken, rows)
	ops := e.login("ops")
	var want []string
	for _, r := range rows {
		if needsAttention(r) {
			want = append(want, r.Name)
		}
	}
	got := ops.index("/api/v1/clusters/dev/resources?view=index&status=attention&sort=name")
	gotNames := got.names()
	slices.Sort(want)
	slices.Sort(gotNames)
	if len(want) == 0 || !slices.Equal(gotNames, want) {
		t.Fatalf("status=attention: %v, want %v", gotNames, want)
	}
	// Combined with a real status it is a union.
	both := ops.index("/api/v1/clusters/dev/resources?view=index&status=attention,ready")
	if both.Total <= got.Total {
		t.Fatalf("attention,ready total %d not above attention total %d", both.Total, got.Total)
	}
}
