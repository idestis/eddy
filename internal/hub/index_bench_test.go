package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/loadgen/synth"
)

// BenchmarkIndexPage is the first page of GET …/resources?view=index of
// one cluster: filter, SAR, facets, sort and page, plus the JSON encoding
// (ADR-0006 gate: ≤ 5 ms for 15k rows).
func BenchmarkIndexPage(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	cluster := bf.clusters[0].Name
	for _, query := range []string{"", "sort=status", "sort=name&order=desc", "q=api", "kind=Job&status=failed"} {
		for _, role := range roles {
			b.Run(fmt.Sprintf("%s/%s", queryName(query), role), func(b *testing.B) {
				vals, _ := url.ParseQuery(query)
				q, err := parseIndexQuery(vals)
				if err != nil {
					b.Fatal(err)
				}
				p := bf.users[role]
				var page indexPage
				var size int
				for b.Loop() {
					if page, err = bf.f.indexList(context.Background(), p, cluster, q); err != nil {
						b.Fatal(err)
					}
					raw, _ := json.Marshal(page)
					size = len(raw)
				}
				b.ReportMetric(float64(page.Total), "total")
				b.ReportMetric(float64(size), "bytes")
			})
		}
	}
}

func queryName(q string) string {
	if q == "" {
		return "default"
	}
	return q
}

// BenchmarkSearchFleetConcurrent runs fleet searches from 16 concurrent
// users, with the hub-wide scan slots and without them (every search
// scanning up to GOMAXPROCS views at once, as before), and reports the
// latency percentiles of one search.
func BenchmarkSearchFleetConcurrent(b *testing.B) {
	n, m := benchSize()
	bf := getBenchFleet(b, n, m)
	bf.f.lazyInit()
	saved := bf.f.scans
	defer func() { bf.f.scans = saved }()
	terms := []string{"api", "billing worker", "nightly", "redis", "gw", "pay sync"}
	for _, slots := range []int{searchScanSlots(), 16 * runtime.GOMAXPROCS(0)} {
		name := fmt.Sprintf("slots=%d", slots)
		if slots > runtime.GOMAXPROCS(0) {
			name = "slots=unbounded"
		}
		b.Run(name, func(b *testing.B) {
			bf.f.scans = newScanSlots(slots)
			p := bf.users[synth.RoleAdmin]
			var (
				mu      sync.Mutex
				lat     []time.Duration
				partial int
			)
			for b.Loop() {
				var wg sync.WaitGroup
				for u := range 16 {
					wg.Go(func() {
						o := searchOptions{Query: terms[u%len(terms)], Fleet: true, Cluster: bf.clusters[u%n].Name, Limit: searchDefaultLimit}
						start := time.Now()
						res, err := bf.f.Search(context.Background(), p, o)
						if err != nil {
							b.Error(err)
							return
						}
						mu.Lock()
						lat = append(lat, time.Since(start))
						if len(res.Partial) > 0 {
							partial++
						}
						mu.Unlock()
					})
				}
				wg.Wait()
			}
			slices.Sort(lat)
			b.ReportMetric(float64(lat[len(lat)/2].Microseconds())/1000, "p50-ms")
			b.ReportMetric(float64(lat[min(len(lat)-1, len(lat)*95/100)].Microseconds())/1000, "p95-ms")
			b.ReportMetric(float64(partial)/float64(len(lat)), "partial/op")
		})
	}
}
