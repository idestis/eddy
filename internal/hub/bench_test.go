package hub

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/loadgen/synth"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// Benchmarks for the hub's hot paths (ADR-0006). The default sizes are
// small enough for CI; -loadgen.full runs them at the target scale:
//
//	go test ./internal/hub -run '^$' -bench . -benchmem -loadgen.full
var loadgenFull = flag.Bool("loadgen.full", false, "run the hub benchmarks at 100 clusters × 15k resources")

func benchSize() (clusters, resources int) {
	if *loadgenFull {
		return 100, 15000
	}
	return 10, 2000
}

// benchFleet is a fleet service over in-memory sessions filled with
// synthetic clusters. Access checks are answered by synth.Allow without a
// network hop, and counted.
type benchFleet struct {
	f        *fleetService
	agents   *agents
	clusters []*synth.Cluster
	checks   atomic.Int64
	users    map[synth.Role]identity.Principal
}

var (
	benchMu     sync.Mutex
	benchFleets = map[[2]int]*benchFleet{}
)

func principalOf(u synth.User) identity.Principal {
	return identity.Principal{User: "proxy:" + u.Name, Groups: append([]string{"eddy:authenticated"}, u.Groups...)}
}

// getBenchFleet builds (once per size) n clusters of m resources.
func getBenchFleet(tb testing.TB, n, m int) *benchFleet {
	tb.Helper()
	benchMu.Lock()
	defer benchMu.Unlock()
	if bf := benchFleets[[2]int{n, m}]; bf != nil {
		return bf
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	met := newMetrics()
	b := newBus()
	reg := NewRegistry()
	ag := newAgents(b, met)
	bf := &benchFleet{agents: ag, users: map[synth.Role]identity.Principal{}}
	entries := map[string]clusterEntry{}
	for i := range n {
		name := fmt.Sprintf("lg-%03d", i)
		entries[name] = clusterEntry{spec: ClusterSpec{Name: name, DisplayName: name, Order: i}}
		bf.clusters = append(bf.clusters, synth.NewCluster(name, i, synth.Spec{Resources: m}))
	}
	reg.replace(entries)
	bf.f = &fleetService{reg: reg, agents: ag, log: log}
	bf.f.authz = newAuthorizer(func(_ context.Context, _ string, id protocol.Identity, checks []protocol.AccessCheck) ([]bool, error) {
		bf.checks.Add(int64(len(checks)))
		out := make([]bool, len(checks))
		for i, c := range checks {
			out[i] = synth.Allow(id, c)
		}
		return out, nil
	}, met)
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for _, c := range bf.clusters {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			s := newSession(c.Name, protocol.Hello{Protocol: protocol.Version, Cluster: c.Name, Kinds: benchKinds()}, [32]byte{}, nil,
				sessionDeps{bus: b, emit: ag.emit, metrics: met, rvSeq: &ag.rvSeq, log: log})
			s.instance, s.seq = "bench", 1
			s.replace(c.Snapshot())
			fs, _ := c.Findings()
			s.replaceFindings(&protocol.FindingSet{Items: fs})
			if err := ag.add(s); err != nil {
				panic(err)
			}
		})
	}
	wg.Wait()
	for _, u := range synth.Users(10) {
		if _, ok := bf.users[u.Role]; !ok {
			bf.users[u.Role] = principalOf(u)
		}
	}
	benchFleets[[2]int{n, m}] = bf
	return bf
}

func benchKinds() []string {
	out := []string{}
	for _, k := range flux.All() {
		if k.Surfaced && k.Preset == "" {
			out = append(out, k.Group+"/"+k.Kind)
		}
	}
	return out
}

var roles = []synth.Role{synth.RoleAdmin, synth.RoleTeam, synth.RoleMixed}

// BenchmarkSnapshotIngest decodes one cluster's chunked snapshot frames
// and applies them to a fresh session, as the agent connection does.
func BenchmarkSnapshotIngest(b *testing.B) {
	_, m := benchSize()
	c := synth.NewCluster("ingest", 0, synth.Spec{Resources: m})
	chunks := splitResources(c.Snapshot(), protocol.MaxFrameBytes-4096)
	var frames [][]byte
	for i, ch := range chunks {
		typ, payload := protocol.TypeDelta, any(protocol.Delta{Upserts: ch, Part: i + 1})
		if i == 0 {
			typ, payload = protocol.TypeSnapshot, protocol.Snapshot{Resources: ch, Parts: len(chunks)}
		}
		p, _ := json.Marshal(payload)
		f, _ := json.Marshal(protocol.Frame{Type: typ, Payload: p})
		frames = append(frames, f)
	}
	var total int
	for _, f := range frames {
		total += len(f)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var rv atomic.Uint64
	b.SetBytes(int64(total))
	b.ReportAllocs()
	for b.Loop() {
		s := newSession("ingest", protocol.Hello{}, [32]byte{}, nil, sessionDeps{emit: func(clusterSession, event) {}, metrics: newMetrics(), rvSeq: &rv, log: log})
		for _, raw := range frames {
			var f protocol.Frame
			if err := json.Unmarshal(raw, &f); err != nil {
				b.Fatal(err)
			}
			if err := s.handle(f); err != nil {
				b.Fatal(err)
			}
		}
		if s.size() != m {
			b.Fatalf("view has %d rows, want %d", s.size(), m)
		}
	}
	b.ReportMetric(float64(m), "rows/op")
}

// BenchmarkViewBytesPerRow reports the heap one view row costs: rows are
// decoded from JSON (as from an agent frame) so none of their strings are
// shared with the generator.
func BenchmarkViewBytesPerRow(b *testing.B) {
	_, m := benchSize()
	raw, err := json.Marshal(synth.NewCluster("bytes", 0, synth.Spec{Resources: m}).Snapshot())
	if err != nil {
		b.Fatal(err)
	}
	var rv atomic.Uint64
	var perRow float64
	for b.Loop() {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var snap []model.Resource
		if err := json.Unmarshal(raw, &snap); err != nil {
			b.Fatal(err)
		}
		v := newClusterView(&rv)
		v.replace(snap)
		snap = nil
		runtime.GC()
		runtime.ReadMemStats(&after)
		perRow = float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / float64(v.size())
		runtime.KeepAlive(v)
		runtime.KeepAlive(snap)
	}
	b.ReportMetric(perRow, "B/row")
}

// BenchmarkClusters is GET /api/v1/clusters (counts and findings of every
// cluster) for one user with a warm SAR cache.
func BenchmarkClusters(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	for _, role := range roles {
		b.Run(string(role), func(b *testing.B) {
			p := bf.users[role]
			if _, err := bf.f.Clusters(context.Background(), p); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := bf.f.Clusters(context.Background(), p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkClustersColdSAR is GET /api/v1/clusters with an empty SAR
// cache: it reports the access checks one user's fleet page needs.
func BenchmarkClustersColdSAR(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	for _, role := range roles {
		b.Run(string(role), func(b *testing.B) {
			p := bf.users[role]
			var checks int64
			for b.Loop() {
				b.StopTimer()
				bf.f.authz.mu.Lock()
				bf.f.authz.cache = map[accessKey]accessEntry{}
				bf.f.authz.mu.Unlock()
				before := bf.checks.Load()
				b.StartTimer()
				if _, err := bf.f.Clusters(context.Background(), p); err != nil {
					b.Fatal(err)
				}
				checks = bf.checks.Load() - before
			}
			b.ReportMetric(float64(checks), "checks/op")
		})
	}
}

// BenchmarkList is GET …/resources of one cluster (filter, SAR, sort).
func BenchmarkList(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	for _, role := range roles {
		b.Run(string(role), func(b *testing.B) {
			p := bf.users[role]
			cluster := bf.clusters[0].Name
			b.ReportAllocs()
			var rows int
			for b.Loop() {
				items, _, err := bf.f.list(context.Background(), p, cluster, fleet.Filter{})
				if err != nil {
					b.Fatal(err)
				}
				rows = len(items)
			}
			b.ReportMetric(float64(rows), "rows/op")
		})
	}
}

// BenchmarkListEncode is the JSON encoding of one admin list response.
func BenchmarkListEncode(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	items, rv, err := bf.f.list(context.Background(), bf.users[synth.RoleAdmin], bf.clusters[0].Name, fleet.Filter{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	var size int
	for b.Loop() {
		raw, err := json.Marshal(map[string]any{"items": items, "resourceVersion": rv})
		if err != nil {
			b.Fatal(err)
		}
		size = len(raw)
	}
	b.ReportMetric(float64(size), "bytes/op")
	b.ReportMetric(float64(size)/float64(len(items)), "B/row")
}

// BenchmarkKinds is GET …/kinds of one cluster.
func BenchmarkKinds(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	for _, role := range roles {
		b.Run(string(role), func(b *testing.B) {
			p := bf.users[role]
			b.ReportAllocs()
			for b.Loop() {
				if _, err := bf.f.Kinds(context.Background(), p, bf.clusters[0].Name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFilterChange is the SSE filter of one 20-row delta for one user.
func BenchmarkFilterChange(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	c := bf.clusters[0]
	ups := make([]model.Resource, 20)
	for i := range ups {
		ups[i] = c.Row(i * (m / 20))
	}
	e := event{kind: evChange, cluster: c.Name, upserts: ups}
	for _, role := range roles {
		b.Run(string(role), func(b *testing.B) {
			p := bf.users[role]
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := bf.f.filterChange(context.Background(), p, e); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSearchFleet is GET /api/v1/search?scope=fleet for one user
// (ADR-0006 budget: p95 ≤ 40 ms at 1.5M rows on the hub).
func BenchmarkSearchFleet(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	for _, q := range []string{"api", "billing worker", "nightly", "zzz", "gw"} {
		for _, role := range roles {
			b.Run(q+"/"+string(role), func(b *testing.B) {
				p := bf.users[role]
				o := searchOptions{Query: q, Fleet: true, Cluster: bf.clusters[0].Name, Limit: searchDefaultLimit}
				if _, err := bf.f.Search(context.Background(), p, o); err != nil {
					b.Fatal(err)
				}
				var lat []time.Duration
				var items int
				for b.Loop() {
					start := time.Now()
					res, err := bf.f.Search(context.Background(), p, o)
					if err != nil {
						b.Fatal(err)
					}
					lat = append(lat, time.Since(start))
					items = len(res.Items)
				}
				slices.Sort(lat)
				b.ReportMetric(float64(lat[min(len(lat)-1, len(lat)*95/100)].Microseconds())/1000, "p95-ms")
				b.ReportMetric(float64(items), "items")
			})
		}
	}
}

// BenchmarkAttention is GET /api/v1/attention across the fleet.
func BenchmarkAttention(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	for _, role := range roles {
		b.Run(string(role), func(b *testing.B) {
			p := bf.users[role]
			if _, err := bf.f.Attention(context.Background(), p, "", 0); err != nil {
				b.Fatal(err)
			}
			var total int
			for b.Loop() {
				res, err := bf.f.Attention(context.Background(), p, "", 0)
				if err != nil {
					b.Fatal(err)
				}
				total = res.Total
			}
			b.ReportMetric(float64(total), "rows")
		})
	}
}

// BenchmarkListGzip is the level-5 gzip of one admin list response.
func BenchmarkListGzip(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	items, rv, err := bf.f.list(context.Background(), bf.users[synth.RoleAdmin], bf.clusters[0].Name, fleet.Filter{})
	if err != nil {
		b.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"items": items, "resourceVersion": rv})
	b.SetBytes(int64(len(raw)))
	var out int
	for b.Loop() {
		var buf countWriter
		zw := gzipPools[gzipLevelBody].Get().(*gzip.Writer)
		zw.Reset(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		gzipPools[gzipLevelBody].Put(zw)
		out = int(buf)
	}
	b.ReportMetric(float64(out), "gz-bytes")
	b.ReportMetric(float64(len(raw))/float64(out), "ratio")
}

type countWriter int

func (c *countWriter) Write(p []byte) (int, error) { *c += countWriter(len(p)); return len(p), nil }
