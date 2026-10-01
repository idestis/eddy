//go:build dev

// Command eddy-loadgen is a synthetic agent fleet for load tests (ADR-0006).
// It starts a hub in-process with the memory store, connects N fake agents
// (the real agent session code with a synthetic source) holding M
// resources each, applies steady churn and drives K simulated users with
// different RBAC through the HTTP API and SSE. It prints a Markdown report:
// hub heap, snapshot ingest, agent→hub bytes, browser payloads and
// latencies, SAR checks per user and SSE fan-out cost.
//
// It binds only 127.0.0.1 on ephemeral ports and touches no kube context.
//
//	go run -tags dev ./cmd/eddy-loadgen -clusters 100 -resources 15000 -users 50
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/hub"
	"github.com/idestis/eddy/internal/loadgen/synth"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store/memory"
)

type options struct {
	clusters, resources, users int
	agentReplicas              int
	churn                      float64
	connectConcurrency         int
	steady, sseFor, userFor    time.Duration
	think                      time.Duration
	deflate                    bool
	fullFetch                  bool
	label                      string
	role                       string
	sseProfile                 string
}

func main() {
	var o options
	flag.IntVar(&o.clusters, "clusters", 10, "fake clusters")
	flag.IntVar(&o.resources, "resources", 2000, "resources per cluster")
	flag.IntVar(&o.users, "users", 10, "simulated users (20% admin, 50% team, 30% mixed)")
	flag.IntVar(&o.agentReplicas, "agent-replicas", 1, "agent sessions per cluster (2 = one standby view per cluster)")
	flag.Float64Var(&o.churn, "churn", 5, "changes per second per cluster")
	flag.IntVar(&o.connectConcurrency, "connect-concurrency", 16, "agents sending their snapshot at once")
	flag.DurationVar(&o.steady, "steady", 15*time.Second, "steady-churn phase without users")
	flag.DurationVar(&o.sseFor, "sse", 15*time.Second, "phase with every user's SSE stream open and no requests")
	flag.DurationVar(&o.userFor, "duration", 60*time.Second, "phase with every user browsing")
	flag.DurationVar(&o.think, "think", 2*time.Second, "think time between a user's page loads")
	flag.BoolVar(&o.deflate, "deflate", true, "permessage-deflate on agent connections (when the build supports it)")
	flag.BoolVar(&o.fullFetch, "full-fetch", true, "also measure the old UI path that fetches every cluster's snapshot (sidebar, palette)")
	flag.StringVar(&o.label, "label", "", "label printed in the report")
	flag.StringVar(&o.sseProfile, "sse-cpuprofile", "", "write a CPU profile of the SSE fan-out phase to this file")
	flag.StringVar(&o.role, "role", "", "make every user admin, team or mixed (default: 20% admin, 50% team, 30% mixed)")
	flag.Parse()
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "eddy-loadgen:", err)
		os.Exit(1)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type env struct {
	o        options
	h        *hub.Hub
	uiURL    string
	agentURL string
	uiLn     *countingListener
	agentLn  *countingListener
	secret   string
	tokens   []string
	clusters []*synth.Cluster
	sar      *sarCounter
	tr       *http.Transport
}

func startHub(ctx context.Context, o options) (*env, func(), error) {
	dir, err := os.MkdirTemp("", "eddy-loadgen-")
	if err != nil {
		return nil, nil, err
	}
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(randomHex(32)), 0o600); err != nil {
		return nil, nil, err
	}
	e := &env{o: o, secret: randomHex(24), sar: &sarCounter{}}
	_ = os.Setenv("EDDY_LOADGEN_PROXY_SECRET", e.secret)

	uiRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	agRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	e.uiLn, e.agentLn = &countingListener{Listener: uiRaw}, &countingListener{Listener: agRaw}
	e.uiURL = "http://" + uiRaw.Addr().String()
	e.agentURL = "ws://" + agRaw.Addr().String() + "/agent/v1/connect"

	var sc strings.Builder
	for i := range o.clusters {
		name := fmt.Sprintf("lg-%03d", i)
		tok := randomHex(24)
		env := fmt.Sprintf("EDDY_LOADGEN_TOKEN_%d", i)
		_ = os.Setenv(env, tok)
		e.tokens = append(e.tokens, tok)
		fmt.Fprintf(&sc, "  - {name: %s, displayName: %s, environment: %s, tokenEnv: %s, order: %d}\n",
			name, name, []string{"prod", "stage", "dev"}[i%3], env, i)
		e.clusters = append(e.clusters, synth.NewCluster(name, i, synth.Spec{Resources: o.resources}))
	}
	cfg, err := config.ParseHub([]byte(fmt.Sprintf(`
publicURL: %s
listen: {ui: "127.0.0.1:0", agents: "127.0.0.1:0", metrics: "127.0.0.1:0"}
auth:
  keyFile: %s
  proxy: {enabled: true, trustedCIDRs: ["127.0.0.1/32"], sharedSecretEnv: EDDY_LOADGEN_PROXY_SECRET}
store: {driver: memory}
staticClusters:
%s`, e.uiURL, key, sc.String())))
	if err != nil {
		return nil, nil, fmt.Errorf("hub config: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if os.Getenv("EDDY_LOADGEN_LOG") != "" {
		log = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	st := memory.New()
	e.h, err = hub.New(ctx, cfg, hub.Options{
		Log: log, Store: st, Flags: runtimeflags.Static{}, SPA: http.NotFoundHandler(), PodName: "loadgen-0",
	})
	if err != nil {
		return nil, nil, err
	}
	hctx, cancel := context.WithCancel(ctx)
	e.h.Start(hctx)
	uiSrv := &http.Server{Handler: e.h.UIHandler(), ReadHeaderTimeout: 10 * time.Second}
	agSrv := &http.Server{Handler: e.h.AgentHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = uiSrv.Serve(e.uiLn) }()
	go func() { _ = agSrv.Serve(e.agentLn) }()
	e.tr = &http.Transport{MaxIdleConns: 1000, MaxIdleConnsPerHost: 1000, MaxConnsPerHost: 0, DisableCompression: true}
	stop := func() {
		cancel()
		_ = uiSrv.Close()
		_ = agSrv.Close()
		_ = e.h.Close()
		_ = st.Close()
		_ = os.RemoveAll(dir)
	}
	return e, stop, nil
}

// report is printed as Markdown at the end.
type report struct {
	lines []string
}

func (r *report) add(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *report) print() {
	fmt.Println(strings.Join(r.lines, "\n"))
}

func run(o options) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := &report{}
	label := o.label
	if label == "" {
		label = "run"
	}
	rep.add("## eddy-loadgen: %s", label)
	rep.add("")
	mix := o.role
	if mix == "" {
		mix = "20% admin, 50% team, 30% mixed"
	}
	rep.add("- Scale: %d clusters × %d resources (%d rows), %d agent session(s) per cluster, %d users (%s), churn %.1f changes/s/cluster", o.clusters, o.resources, o.clusters*o.resources, o.agentReplicas, o.users, mix, o.churn)
	rep.add("- Machine: %s/%s, %d CPUs, GOMAXPROCS %d, %s", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), runtime.Version())
	rep.add("- Command: `go run -tags dev ./cmd/eddy-loadgen %s`", strings.Join(os.Args[1:], " "))
	rep.add("")

	heap0 := liveHeap()
	e, stop, err := startHub(ctx, o)
	if err != nil {
		return err
	}
	defer stop()
	heapHub := liveHeap()

	users := synth.UsersOf(o.users, synth.Role(o.role))
	browsers := make([]*browser, len(users))
	for i, u := range users {
		browsers[i] = newBrowser(u, e.uiURL, e.secret, e.tr)
	}
	// The ingest probe is an extra admin, outside the measured users.
	admin := newBrowser(synth.UsersOf(1, synth.RoleAdmin)[0], e.uiURL, e.secret, e.tr)
	admin.user.Name = "probe@example.com"
	expected := map[string]int{}
	for _, c := range e.clusters {
		expected[c.Name] = c.Counted()
	}

	// ---- ingest ----
	fmt.Fprintln(os.Stderr, "ingest: connecting agents…")
	agentsStart := time.Now()
	cpu0 := cpuTime()
	read0 := e.agentLn.read.Load()
	var agents []*fakeAgent
	complete := map[string]time.Duration{}
	started := 0
	for len(complete) < len(e.clusters) {
		for started < len(e.clusters) && started-len(complete) < o.connectConcurrency {
			c := e.clusters[started]
			for r := range o.agentReplicas {
				agents = append(agents, startAgent(ctx, e.agentURL, e.tokens[started], fmt.Sprintf("%s-%d", c.Name, r), c, &fakeHandler{sar: e.sar}, agentOptions{deflate: o.deflate}))
			}
			started++
		}
		time.Sleep(100 * time.Millisecond)
		res, err := admin.get(ctx, "/api/v1/clusters", true, false)
		if err != nil {
			return err
		}
		items, err := decodeClusters(res.body)
		if err != nil {
			return err
		}
		for _, ci := range items {
			if _, done := complete[ci.Name]; done || !ci.Connected {
				continue
			}
			n := 0
			for _, v := range ci.Counts {
				n += v
			}
			if n == expected[ci.Name] {
				complete[ci.Name] = time.Since(agentsStart)
			}
		}
		if time.Since(agentsStart) > 30*time.Minute {
			return fmt.Errorf("ingest did not finish: %d of %d clusters complete", len(complete), len(e.clusters))
		}
	}
	ingest := time.Since(agentsStart)
	ingestCPU := cpuTime() - cpu0
	ingestBytes := e.agentLn.read.Load() - read0
	defer func() {
		for _, a := range agents {
			a.stop()
		}
	}()
	// Snapshots may still be settling on standby sessions.
	time.Sleep(2 * time.Second)
	heapViews := liveHeap()
	rows := int64(o.clusters * o.resources)

	rep.add("### Hub memory and ingest")
	rep.add("")
	rep.add("| Metric | Value |")
	rep.add("|---|---|")
	rep.add("| Live heap before the hub | %s |", bytesStr(int64(heap0)))
	rep.add("| Live heap, hub started, no agents | %s |", bytesStr(int64(heapHub)))
	rep.add("| Live heap with every view loaded | %s |", bytesStr(int64(heapViews)))
	rep.add("| Heap per row (views − empty hub) | %d B |", (int64(heapViews)-int64(heapHub))/max(1, rows))
	rep.add("| Snapshot ingest, all clusters (connect concurrency %d) | %s |", o.connectConcurrency, ms(ingest))
	rep.add("| Ingest per cluster (wall, %d at a time) | %s |", o.connectConcurrency, ms(ingest*time.Duration(min(o.connectConcurrency, o.clusters))/time.Duration(o.clusters)))
	rep.add("| Ingest CPU (process) | %s |", ms(ingestCPU))
	rep.add("| Agent→hub bytes during ingest | %s (%d B/row) |", bytesStr(ingestBytes), ingestBytes/max(1, rows*int64(o.agentReplicas)))
	rep.add("| permessage-deflate requested | %v |", o.deflate)
	rep.add("")

	health := func(phase string) {
		rep.add("- Health after %s: %d of %d agent sessions connected; %d agent connections accepted so far (%d expected without reconnects)",
			phase, connectedAgents(agents), len(agents), e.agentLn.accepted.Load(), len(agents))
	}
	health("ingest")
	rep.add("")

	// ---- steady churn ----
	stopChurn := startChurn(ctx, e.clusters, o.churn)
	defer stopChurn()
	measurePhase := func(d time.Duration) (cpu time.Duration, agentBytes, uiBytes int64) {
		c0, a0, u0 := cpuTime(), e.agentLn.read.Load(), e.uiLn.written.Load()
		time.Sleep(d)
		return cpuTime() - c0, e.agentLn.read.Load() - a0, e.uiLn.written.Load() - u0
	}
	fmt.Fprintln(os.Stderr, "steady churn…")
	steadyCPU, steadyAgent, _ := measurePhase(o.steady)
	sec := o.steady.Seconds()
	rep.add("### Steady state (churn %.1f/s/cluster, no users)", o.churn)
	rep.add("")
	rep.add("| Metric | Value |")
	rep.add("|---|---|")
	rep.add("| Agent→hub bytes/s | %s/s |", bytesStr(int64(float64(steadyAgent)/sec)))
	rep.add("| Hub process CPU | %.1f %% of one core |", 100*steadyCPU.Seconds()/sec)
	rep.add("")
	health("steady churn")
	rep.add("")

	// ---- SSE fan-out ----
	fmt.Fprintln(os.Stderr, "sse fan-out…")
	var streams []*stream
	for _, b := range browsers {
		s, err := b.openStream(ctx)
		if err != nil {
			return fmt.Errorf("open stream for %s: %w", b.user.Name, err)
		}
		streams = append(streams, s)
	}
	time.Sleep(2 * time.Second)
	var w0, ev0, ch0 int64
	for _, s := range streams {
		w0, ev0, ch0 = w0+s.wire.Load(), ev0+s.events.Load(), ch0+s.changes.Load()
	}
	if o.sseProfile != "" {
		f, err := os.Create(o.sseProfile)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
	}
	sseCPU, _, _ := measurePhase(o.sseFor)
	if o.sseProfile != "" {
		pprof.StopCPUProfile()
	}
	var w1, ev1, ch1 int64
	for _, s := range streams {
		w1, ev1, ch1 = w1+s.wire.Load(), ev1+s.events.Load(), ch1+s.changes.Load()
	}
	ssec := o.sseFor.Seconds()
	nu := float64(max(1, len(streams)))
	rep.add("### SSE fan-out (%d streams open, churn on, no requests)", len(streams))
	rep.add("")
	rep.add("| Metric | Value |")
	rep.add("|---|---|")
	rep.add("| Wire bytes/s per user | %s/s |", bytesStr(int64(float64(w1-w0)/ssec/nu)))
	rep.add("| Events/s per user (change events) | %.1f (%.1f) |", float64(ev1-ev0)/ssec/nu, float64(ch1-ch0)/ssec/nu)
	rep.add("| Hub process CPU with streams | %.1f %% of one core (%.1f %% without) |", 100*sseCPU.Seconds()/ssec, 100*steadyCPU.Seconds()/sec)
	rep.add("| SSE cost per user | %.2f %% of one core |", 100*(sseCPU.Seconds()/ssec-steadyCPU.Seconds()/sec)/nu)
	rep.add("| Streams that received their first `clusters` event | %d of %d |", streamsWithClusters(streams), len(streams))
	rep.add("")
	health("SSE phase")
	rep.add("")

	// ---- single-user page loads, per role ----
	fmt.Fprintln(os.Stderr, "single-user page loads…")
	p1 := hasEndpoint(ctx, admin, "/api/v1/attention")
	rep.add("### Page loads, one user at a time (cold SAR, then warm)")
	rep.add("")
	rep.add("P1 endpoints present: %v", p1)
	rep.add("")
	rep.add("| Role | Request | Cold | Warm p50 | Warm p95 | Wire | Decoded | Revalidate (If-None-Match) |")
	rep.add("|---|---|---|---|---|---|---|---|")
	seen := map[synth.Role]bool{}
	for _, b := range browsers {
		if seen[b.user.Role] {
			continue
		}
		seen[b.user.Role] = true
		for _, rq := range pageRequests(e, p1, 0) {
			cold, err := b.get(ctx, rq.path, false, false)
			if err != nil {
				return err
			}
			var warm samples
			for range 5 {
				r, err := b.get(ctx, rq.path, false, false)
				if err != nil {
					return err
				}
				warm.add(r.dur, r.wire, r.decoded, r.status)
			}
			reval := "n/a"
			if cold.etag != "" {
				r, err := b.get(ctx, rq.path, false, true)
				if err == nil {
					reval = fmt.Sprintf("%d in %s", r.status, ms(r.dur))
				}
			}
			ws := warm.summary()
			rep.add("| %s | %s | %s | %s | %s | %s | %s | %s |", b.user.Role, rq.name, ms(cold.dur), ms(ws.p50), ms(ws.p95), bytesStr(ws.wireAvg), bytesStr(ws.decodeAvg), reval)
		}
		if o.fullFetch {
			start := time.Now()
			wire, decoded, err := fetchAllSnapshots(ctx, b, e.clusters)
			if err != nil {
				return err
			}
			rep.add("| %s | every cluster's /resources (old sidebar, palette and needs-attention path, 6 at a time) | %s | | | %s | %s | |", b.user.Role, ms(time.Since(start)), bytesStr(wire), bytesStr(decoded))
		}
	}
	rep.add("")
	health("single-user page loads")
	rep.add("")

	// ---- concurrent users ----
	fmt.Fprintf(os.Stderr, "concurrent users for %s…\n", o.userFor)
	sar0 := e.sar.snapshot()
	byEndpoint := map[string]*samples{}
	var mu sync.Mutex
	sampleFor := func(name string) *samples {
		mu.Lock()
		defer mu.Unlock()
		s := byEndpoint[name]
		if s == nil {
			s = &samples{}
			byEndpoint[name] = s
		}
		return s
	}
	var errs atomic.Int64
	uctx, ucancel := context.WithTimeout(ctx, o.userFor)
	cpuU0 := cpuTime()
	uiW0 := e.uiLn.written.Load()
	var wg sync.WaitGroup
	for i, b := range browsers {
		wg.Go(func() {
			iter := i
			for uctx.Err() == nil {
				for _, rq := range pageRequests(e, p1, iter) {
					r, err := b.get(uctx, rq.path, false, true)
					if err != nil {
						if uctx.Err() == nil {
							errs.Add(1)
						}
						continue
					}
					sampleFor(rq.name).add(r.dur, r.wire, r.decoded, r.status)
				}
				iter++
				select {
				case <-uctx.Done():
				case <-time.After(o.think):
				}
			}
		})
	}
	wg.Wait()
	ucancel()
	userCPU := cpuTime() - cpuU0
	uiBytes := e.uiLn.written.Load() - uiW0
	sar1 := e.sar.snapshot()

	rep.add("### %d concurrent users for %s (think time %s, SSE open, churn on)", len(browsers), o.userFor, o.think)
	rep.add("")
	rep.add("| Request | n | p50 | p95 | max | Wire avg | Decoded avg | Statuses |")
	rep.add("|---|---|---|---|---|---|---|---|")
	names := make([]string, 0, len(byEndpoint))
	for n := range byEndpoint {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		s := byEndpoint[n].summary()
		rep.add("| %s | %d | %s | %s | %s | %s | %s | %v |", n, s.n, ms(s.p50), ms(s.p95), ms(s.max), bytesStr(s.wireAvg), bytesStr(s.decodeAvg), s.statuses)
	}
	rep.add("")
	rep.add("- Hub process CPU: %.1f %% of one core; hub→browser bytes %s/s; request errors %d", 100*userCPU.Seconds()/o.userFor.Seconds(), bytesStr(int64(float64(uiBytes)/o.userFor.Seconds())), errs.Load())
	// SAR checks per user per minute, by role.
	byRole := map[synth.Role][]float64{}
	for _, b := range browsers {
		var n int64
		for user, v := range sar1 {
			if strings.HasSuffix(user, b.user.Name) {
				n += v - sar0[user]
			}
		}
		byRole[b.user.Role] = append(byRole[b.user.Role], float64(n)/o.userFor.Minutes())
	}
	for _, role := range []synth.Role{synth.RoleAdmin, synth.RoleTeam, synth.RoleMixed} {
		v := byRole[role]
		if len(v) == 0 {
			continue
		}
		var sum float64
		for _, x := range v {
			sum += x
		}
		rep.add("- SAR checks per user per minute, %s: %.0f", role, sum/float64(len(v)))
	}
	var total int64
	for _, v := range sar1 {
		total += v
	}
	rep.add("- SAR checks answered by agents in total: %d (in %d access requests)", total, e.sar.requests.Load())
	rep.add("- Live heap at the end: %s", bytesStr(int64(liveHeap())))
	health("concurrent users")
	rep.add("")
	rep.print()
	return nil
}

type pageRequest struct{ name, path string }

var searchTerms = []string{"api", "billing worker", "nightly", "crashloop", "redis", "pay sync", "team-03", "gw"}

// pageRequests is what one page view loads. Iteration i picks the cluster
// and search term.
func pageRequests(e *env, p1 bool, i int) []pageRequest {
	c := e.clusters[i%len(e.clusters)].Name
	out := []pageRequest{
		{"fleet: GET /clusters", "/api/v1/clusters"},
		{"sidebar: GET /clusters/{c}/kinds", "/api/v1/clusters/" + c + "/kinds"},
		{"list: GET /clusters/{c}/resources", "/api/v1/clusters/" + c + "/resources"},
	}
	if p1 {
		q := strings.ReplaceAll(searchTerms[i%len(searchTerms)], " ", "+")
		out = append(out,
			pageRequest{"palette: GET /search?scope=fleet", "/api/v1/search?scope=fleet&limit=30&q=" + q},
			pageRequest{"palette: GET /search?scope=cluster", "/api/v1/search?scope=cluster&limit=30&cluster=" + c + "&q=" + q},
			pageRequest{"attention: GET /attention", "/api/v1/attention?limit=200"},
		)
	}
	return out
}

func hasEndpoint(ctx context.Context, b *browser, path string) bool {
	r, err := b.get(ctx, path, false, false)
	return err == nil && r.status == http.StatusOK
}

// fetchAllSnapshots is the old UI path: every connected cluster's full
// snapshot, 6 requests at a time like a browser on HTTP/1.1.
func fetchAllSnapshots(ctx context.Context, b *browser, clusters []*synth.Cluster) (wire, decoded int64, err error) {
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	var w, d atomic.Int64
	var firstErr atomic.Pointer[error]
	for _, c := range clusters {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			r, err := b.get(ctx, "/api/v1/clusters/"+c.Name+"/resources", false, false)
			if err != nil {
				firstErr.CompareAndSwap(nil, &err)
				return
			}
			w.Add(r.wire)
			d.Add(r.decoded)
		})
	}
	wg.Wait()
	if p := firstErr.Load(); p != nil {
		return 0, 0, *p
	}
	return w.Load(), d.Load(), nil
}

// startChurn changes rate rows per second in every cluster.
func startChurn(ctx context.Context, clusters []*synth.Cluster, rate float64) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		acc := 0.0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			acc += rate / 10
			n := int(math.Floor(acc))
			acc -= float64(n)
			if n == 0 {
				continue
			}
			for _, c := range clusters {
				c.Churn(n)
			}
		}
	}()
	return cancel
}
