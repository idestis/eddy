package hub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// collect returns every event of ch received within d.
func collect(ch <-chan sseEvent, d time.Duration) []sseEvent {
	var out []sseEvent
	timeout := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			return out
		}
	}
}

// nextFor returns the next event named name for cluster, failing on any
// event that bad rejects first.
func nextFor(t *testing.T, ch <-chan sseEvent, name, cluster string, bad func(sseEvent) bool) sseEvent {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %s %s", name, cluster)
			}
			if bad != nil && bad(ev) {
				t.Fatalf("unexpected %s event: %s", ev.name, ev.data)
			}
			var c clusterEvent
			_ = json.Unmarshal([]byte(ev.data), &c)
			if ev.name == name && c.Cluster == cluster {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %s event for %s", name, cluster)
		}
	}
}

func clusterOf(ev sseEvent) string {
	var c clusterEvent
	_ = json.Unmarshal([]byte(ev.data), &c)
	return c.Cluster
}

func TestParseWatch(t *testing.T) {
	tests := []struct {
		query   string
		scoped  bool
		watched int
		bad     bool
	}{
		{query: "", scoped: false},
		{query: "watch=", scoped: true},
		{query: "watch=dev", scoped: true, watched: 1},
		{query: "watch=a,b,c,d,e", scoped: true, watched: 5},
		{query: "watch=a,b&watch=c,,d", scoped: true, watched: 4},
		{query: "watch=a,a,a,a,a,a,a", scoped: true, watched: 1},
		{query: "watch=a,b,c,d,e,f", bad: true},
		{query: "watch=a&watch=b,c,d,e,f", bad: true},
		{query: "watch=a%2Fb", bad: true},
		{query: "watch=a%0Ab", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			q, _ := url.ParseQuery(tt.query)
			w, scoped, err := parseWatch(q)
			if tt.bad {
				if err == nil {
					t.Fatalf("want an error, got %v", w)
				}
				return
			}
			if err != nil || scoped != tt.scoped || len(w) != tt.watched {
				t.Fatalf("parseWatch = %v, %v, %v", w, scoped, err)
			}
		})
	}
}

func TestStreamWatchLimit(t *testing.T) {
	e := newEnv(t, "")
	alice := e.login("alice")
	if st, code := alice.errorCode("GET", "/api/v1/stream?watch=a,b,c,d,e,f", nil); st != http.StatusBadRequest || code != "bad_request" {
		t.Fatalf("6 watched clusters: %d %s, want 400", st, code)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/stream?watch=dev/x", nil); st != http.StatusBadRequest {
		t.Fatalf("invalid watch name: %d, want 400", st)
	}
	// Five, unknown names included, are fine.
	ch, stop := alice.openStream("/api/v1/stream?watch=dev,prod,gone-1,gone-2,gone-3")
	defer stop()
	next(t, ch, "hello")
}

// TestStreamWatchScoping: a scoped stream gets change events of watched
// clusters only, and counts events for the others.
func TestStreamWatchScoping(t *testing.T) {
	e := newEnv(t, "")
	dev := e.connectAgent("dev", testToken, nil)
	prod := e.connectAgent("prod", testToken+"-prod", nil)
	alice := e.login("alice")
	ch, stop := alice.openStream("/api/v1/stream?watch=dev")
	defer stop()
	next(t, ch, "hello")
	next(t, ch, "clusters")
	// Watched clusters are resynced on connect.
	nextFor(t, ch, "resync", "dev", nil)

	noProdChange := func(ev sseEvent) bool { return ev.name == "change" && clusterOf(ev) == "prod" }
	prod.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)}})
	var ce countsEvent
	_ = json.Unmarshal([]byte(nextFor(t, ch, "counts", "prod", noProdChange).data), &ce)
	if ce.Counts[model.StatusReady] != 1 || ce.Kinds["Kustomization"][model.StatusReady] != 1 {
		t.Fatalf("prod counts %+v", ce)
	}

	dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "web", model.StatusReady)}})
	var chg changeEvent
	_ = json.Unmarshal([]byte(nextFor(t, ch, "change", "dev", noProdChange).data), &chg)
	if len(chg.Upserts) != 1 || chg.Upserts[0].Name != "web" {
		t.Fatalf("dev change %+v", chg)
	}
	nextFor(t, ch, "counts", "dev", noProdChange)

	// A delta that changes no counts sends no counts event.
	r := res("Kustomization", "team-a", "apps", model.StatusReady)
	r.ResourceVersion = "2"
	prod.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{r}})
	for _, ev := range collect(ch, 1500*time.Millisecond) {
		if noProdChange(ev) || ev.name == "counts" {
			t.Fatalf("unexpected %s: %s", ev.name, ev.data)
		}
	}
}

// TestStreamCountsDebounce: a burst of count changes on one cluster sends
// at most one counts event per second, ending with the final counts.
func TestStreamCountsDebounce(t *testing.T) {
	e := newEnv(t, "")
	dev := e.connectAgent("dev", testToken, nil)
	ops := e.login("ops")
	ch, stop := ops.openStream("/api/v1/stream?watch=")
	defer stop()
	next(t, ch, "hello")
	next(t, ch, "clusters")

	start := time.Now()
	const n = 12
	go func() {
		for i := range n {
			dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "k"+string(rune('a'+i)), model.StatusReady)}})
			time.Sleep(100 * time.Millisecond)
		}
	}()
	var got []countsEvent
	var times []time.Duration
	timeout := time.After(3500 * time.Millisecond)
loop:
	for {
		select {
		case ev := <-ch:
			switch ev.name {
			case "change":
				t.Fatalf("change on an unwatched cluster: %s", ev.data)
			case "counts":
				var ce countsEvent
				_ = json.Unmarshal([]byte(ev.data), &ce)
				got = append(got, ce)
				times = append(times, time.Since(start))
			}
		case <-timeout:
			break loop
		}
	}
	// 1.2 s of changes: the first event at once, then one per second.
	if len(got) < 2 || len(got) > 3 {
		t.Fatalf("%d counts events in %v, want 2 or 3", len(got), times)
	}
	for i := 1; i < len(times); i++ {
		if d := times[i] - times[i-1]; d < 900*time.Millisecond {
			t.Fatalf("counts events %v apart, want ≥ 1 s (%v)", d, times)
		}
	}
	if last := got[len(got)-1]; last.Counts[model.StatusReady] != n {
		t.Fatalf("final counts %+v, want %d ready", last, n)
	}
}

func TestDebounce(t *testing.T) {
	d := newDebounce(50 * time.Millisecond)
	defer d.stop()
	if d.due() != nil {
		t.Fatal("due before schedule")
	}
	d.last = time.Now().Add(-time.Second)
	d.schedule()
	select {
	case <-d.due():
	case <-time.After(20 * time.Millisecond):
		t.Fatal("an idle debounce must fire at once")
	}
	d.fired()
	start := time.Now()
	d.schedule()
	d.schedule() // no second timer
	<-d.due()
	if el := time.Since(start); el < 40*time.Millisecond {
		t.Fatalf("fired after %v, want ≥ 50 ms", el)
	}
}

// TestStreamAttentionPerUser: attention events carry only the rows (and
// findings) the user may see, and deletes when rows heal.
func TestStreamAttentionPerUser(t *testing.T) {
	e := newEnv(t, "")
	dev := e.connectAgent("dev", testToken, nil)
	alice, bob := e.login("alice"), e.login("bob")
	as, stopA := alice.openStream("/api/v1/stream?watch=prod")
	defer stopA()
	bs, stopB := bob.openStream("/api/v1/stream?watch=")
	defer stopB()
	next(t, as, "hello")
	next(t, bs, "hello")
	next(t, as, "clusters")
	next(t, bs, "clusters")

	ka, kb := res("Kustomization", "team-a", "apps", model.StatusFailed), res("Kustomization", "team-b", "infra", model.StatusFailed)
	ok := res("Kustomization", "team-a", "fine", model.StatusReady)
	dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{ka, kb, ok}})
	var ae attentionEvent
	_ = json.Unmarshal([]byte(nextFor(t, as, "attention", "dev", nil).data), &ae)
	if len(ae.Upserts) != 1 || ae.Upserts[0].ID != ka.ID || len(ae.Deletes) != 0 {
		t.Fatalf("alice attention %+v", ae)
	}
	_ = json.Unmarshal([]byte(nextFor(t, bs, "attention", "dev", nil).data), &ae)
	if len(ae.Upserts) != 1 || ae.Upserts[0].ID != kb.ID {
		t.Fatalf("bob attention %+v", ae)
	}

	// team-a heals: alice gets the delete, bob nothing.
	ka.Status = model.StatusReady
	dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{ka}})
	_ = json.Unmarshal([]byte(nextFor(t, as, "attention", "dev", nil).data), &ae)
	if len(ae.Upserts) != 0 || len(ae.Deletes) != 1 || ae.Deletes[0] != ka.ID {
		t.Fatalf("alice heal %+v", ae)
	}
	// team-b is deleted: bob gets the delete.
	dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Deletes: []string{kb.ID}})
	_ = json.Unmarshal([]byte(nextFor(t, bs, "attention", "dev", nil).data), &ae)
	if len(ae.Deletes) != 1 || ae.Deletes[0] != kb.ID {
		t.Fatalf("bob delete %+v", ae)
	}

	// A team-a finding reaches alice only.
	f := model.Finding{Kind: model.FindingJobBuildup, Severity: model.SeverityWarning, Namespace: "team-a",
		Message: "Jobs pile up", Jobs: &model.JobBuildup{Hidden: 200, Threshold: 100}}
	dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Findings: &protocol.FindingSet{Items: []model.Finding{f}}})
	_ = json.Unmarshal([]byte(nextFor(t, as, "attention", "dev", nil).data), &ae)
	if len(ae.Findings) != 1 || ae.Findings[0].Namespace != "team-a" || len(ae.Upserts) != 0 {
		t.Fatalf("alice findings %+v", ae)
	}
	for _, ev := range collect(bs, 1500*time.Millisecond) {
		if ev.name == "attention" || ev.name == "change" {
			t.Fatalf("bob saw %s: %s", ev.name, ev.data)
		}
	}
	for _, ev := range collect(as, 300*time.Millisecond) {
		if ev.name == "change" {
			t.Fatalf("alice saw a change of an unwatched cluster: %s", ev.data)
		}
	}
}

// TestStreamWithoutWatch: without ?watch= the stream keeps the old
// behaviour: every cluster's changes, no counts or attention events.
func TestStreamWithoutWatch(t *testing.T) {
	e := newEnv(t, "")
	dev := e.connectAgent("dev", testToken, nil)
	prod := e.connectAgent("prod", testToken+"-prod", nil)
	alice := e.login("alice")
	ch, stop := alice.openStream("/api/v1/stream")
	defer stop()
	next(t, ch, "hello")
	next(t, ch, "clusters")
	dev.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "a", model.StatusFailed)}})
	prod.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "b", model.StatusFailed)}})
	seen := map[string]bool{}
	for _, ev := range collect(ch, 1500*time.Millisecond) {
		switch ev.name {
		case "counts", "attention", "resync":
			t.Fatalf("legacy stream got %s: %s", ev.name, ev.data)
		case "change":
			seen[clusterOf(ev)] = true
		}
	}
	if !seen["dev"] || !seen["prod"] {
		t.Fatalf("changes seen for %v, want dev and prod", seen)
	}
}

// TestRelayScopedSSE: a scoped stream on replica B, for a cluster whose
// agent is on A, gets attention and counts but no changes while the
// cluster is unwatched, and changes once it is watched.
func TestRelayScopedSSE(t *testing.T) {
	rs := replicas(t, 2)
	a, b := rs[0], rs[1]
	agent := a.connectAgent("dev", testToken, nil)
	waitRemote(t, b, "dev", 0)

	alice := b.login("alice")
	un, stopU := alice.openStream("/api/v1/stream?watch=prod")
	defer stopU()
	wa, stopW := alice.openStream("/api/v1/stream?watch=dev")
	defer stopW()
	next(t, un, "hello")
	next(t, wa, "hello")
	next(t, un, "clusters")
	nextFor(t, wa, "resync", "dev", nil)

	failed := res("Kustomization", "team-a", "apps", model.StatusFailed)
	agent.sendFrame(protocol.TypeDelta, "", protocol.Delta{Upserts: []model.Resource{failed, res("Kustomization", "team-b", "infra", model.StatusFailed)}})
	devChange := func(ev sseEvent) bool { return ev.name == "change" && clusterOf(ev) == "dev" }
	var ae attentionEvent
	_ = json.Unmarshal([]byte(nextFor(t, un, "attention", "dev", devChange).data), &ae)
	if len(ae.Upserts) != 1 || ae.Upserts[0].ID != failed.ID {
		t.Fatalf("relayed attention %+v", ae)
	}
	var ce countsEvent
	_ = json.Unmarshal([]byte(nextFor(t, un, "counts", "dev", devChange).data), &ce)
	if ce.Counts[model.StatusFailed] != 1 {
		t.Fatalf("relayed counts %+v, want alice's 1 failed", ce)
	}
	var chg changeEvent
	_ = json.Unmarshal([]byte(nextFor(t, wa, "change", "dev", nil).data), &chg)
	if len(chg.Upserts) != 1 || chg.Upserts[0].ID != failed.ID {
		t.Fatalf("relayed change %+v", chg)
	}
}

// TestSearchKindFilter: kind= limits search to those kinds, inventory-only
// kinds included, and unknown kinds are a 400.
func TestSearchKindFilter(t *testing.T) {
	e := newEnv(t, "")
	owner := model.Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "team-a", Name: "apps"}
	inv := model.Resource{Ref: model.Ref{Group: "", Kind: "ConfigMap", Namespace: "team-a", Name: "api-config"}, Status: model.StatusUnknown, InventoryOnly: true, Owner: &owner}
	inv.ID = inv.Ref.ID()
	e.connectAgent("dev", testToken, []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		res("Kustomization", "team-a", "api", model.StatusReady),
		res("Deployment", "team-a", "api", model.StatusReady),
		inv,
	})
	alice := e.login("alice")
	var out searchResult
	alice.do("GET", "/api/v1/search?q=api", nil, &out, http.StatusOK)
	if len(out.Items) != 3 {
		t.Fatalf("q=api: %d items, want 3 (with the inventory-only ConfigMap): %+v", len(out.Items), out.Items)
	}
	alice.do("GET", "/api/v1/search?q=api&kind=deployment", nil, &out, http.StatusOK)
	if len(out.Items) != 1 || out.Items[0].Resource.Kind != "Deployment" || out.Items[0].Cluster != "dev" {
		t.Fatalf("kind=deployment: %+v", out.Items)
	}
	alice.do("GET", "/api/v1/search?q=api&kind=ConfigMap,Kustomization", nil, &out, http.StatusOK)
	if len(out.Items) != 2 {
		t.Fatalf("kind=ConfigMap,Kustomization: %+v", out.Items)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/search?q=api&kind=no/such", nil); st != http.StatusBadRequest {
		t.Fatalf("bad kind: %d", st)
	}
}
