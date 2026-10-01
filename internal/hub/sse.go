package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
)

// SSE settings.
const (
	sseKeepalive       = 20 * time.Second
	sseWriteTimeout    = 30 * time.Second
	sseClustersEvery   = time.Second
	maxStreamsPerUser  = 8
	maxLogStreamsUser  = 4
	sseFilterTimeout   = 15 * time.Second
	sseClustersTimeout = 15 * time.Second
)

// sseWriter writes server-sent events. It is safe for concurrent use.
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

// startSSE commits the response as an event stream. The server's read and
// write timeouts are lifted (an expired read deadline would cancel the
// request); each write gets its own deadline instead, so a stuck client is
// dropped without limiting how long a healthy stream lives.
func startSSE(w http.ResponseWriter) (*sseWriter, error) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseWriter{w: w, rc: rc}
	if err := s.write(": stream open\n\n"); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *sseWriter) write(chunk string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if _, err := fmt.Fprint(s.w, chunk); err != nil {
		return fmt.Errorf("hub: sse write: %w", err)
	}
	if err := s.rc.Flush(); err != nil {
		return fmt.Errorf("hub: sse flush: %w", err)
	}
	return nil
}

// event sends one event. encoding/json never emits a raw newline, so the
// data fits on one line.
func (s *sseWriter) event(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("hub: encode %s event: %w", name, err)
	}
	return s.write("event: " + name + "\ndata: " + string(b) + "\n\n")
}

func (s *sseWriter) comment(text string) error { return s.write(": " + text + "\n\n") }

type changeEvent struct {
	Cluster string           `json:"cluster"`
	Upserts []model.Resource `json:"upserts"`
	Deletes []string         `json:"deletes"`
}

type clusterEvent struct {
	Cluster string `json:"cluster"`
}

type threadEvent struct {
	ThreadID string            `json:"threadId"`
	Ref      store.ResourceRef `json:"ref"`
}

// countsEvent is one cluster's counts for the user, like ClusterInfo's.
type countsEvent struct {
	Cluster string                          `json:"cluster"`
	Counts  map[model.Status]int            `json:"counts"`
	Kinds   map[string]map[model.Status]int `json:"kinds"`
}

// attentionEvent moves rows into (upserts) and out of (deletes) a
// cluster's needs-attention set, filtered per user. Findings, when set,
// replace the cluster's visible findings.
type attentionEvent struct {
	Cluster  string           `json:"cluster"`
	Upserts  []model.Resource `json:"upserts"`
	Deletes  []string         `json:"deletes"`
	Findings []model.Finding  `json:"findings,omitempty"`
}

// Scoped streams (ADR-0006 P2).
const (
	// maxWatch is how many clusters one stream may watch.
	maxWatch = 5
	// sseCountsEvery is the debounce of counts and findings: at most one
	// event of each per cluster per interval.
	sseCountsEvery = time.Second
)

// parseWatch reads ?watch=a,b (repeatable). scoped is false when the
// parameter is absent: the stream then carries every cluster's changes
// (deprecated, kept for one release). Unknown names are kept: a cluster
// may be registered later, and a client must not fail on a removed one.
func parseWatch(q url.Values) (watch map[string]bool, scoped bool, err error) {
	vals, ok := q["watch"]
	if !ok {
		return nil, false, nil
	}
	watch = map[string]bool{}
	for _, v := range vals {
		for name := range strings.SplitSeq(v, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if !validWatchName(name) {
				return nil, false, badRequest("invalid cluster name in watch")
			}
			watch[name] = true
			if len(watch) > maxWatch {
				return nil, false, badRequest("watch names at most %d clusters", maxWatch)
			}
		}
	}
	return watch, true, nil
}

func validWatchName(s string) bool {
	if len(s) > 253 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

// handleStream serves GET /api/v1/stream[?watch=a,b].
func (a *api) handleStream(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	watch, scoped, err := parseWatch(r.URL.Query())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	release, ok := a.streams.acquire(p.User)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many open streams")
		return
	}
	defer release()
	sub := a.bus.subscribe()
	defer a.bus.unsubscribe(sub)

	sw, err := startSSE(w)
	if err != nil {
		return
	}
	a.metrics.sseClients.Add(1)
	defer a.metrics.sseClients.Add(-1)

	ctx := r.Context()
	c := &streamClient{a: a, p: p, sw: sw}
	if scoped {
		c.scoped, c.watch = true, watch
		c.dirtyCounts, c.dirtyFindings = map[string]struct{}{}, map[string]struct{}{}
		c.sentCounts, c.sentFindings = map[string]sentCounts{}, map[string]string{}
		a.metrics.sseScoped.Add(1)
		defer a.metrics.sseScoped.Add(-1)
	}
	if err := sw.event("hello", struct{}{}); err != nil {
		return
	}
	if err := c.sendClusters(ctx); err != nil {
		return
	}
	// Changes between the client's fetch and this point are lost: it
	// refetches the watched clusters.
	for name := range c.watch {
		if _, ok := a.reg.Get(name); ok {
			if err := sw.event("resync", clusterEvent{Cluster: name}); err != nil {
				return
			}
		}
	}

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	clusters := newDebounce(sseClustersEvery)
	defer clusters.stop()
	counts := newDebounce(sseCountsEvery)
	defer counts.stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-a.shutdown:
			return
		case <-keepalive.C:
			err = sw.comment("keepalive")
		case <-clusters.due():
			clusters.fired()
			err = c.sendClusters(ctx)
		case <-counts.due():
			counts.fired()
			err = c.flushCounts(ctx)
		case e := <-sub.ch:
			if sub.overflow.Swap(false) {
				a.metrics.sseDropped.Add(1)
				err = c.resyncAll()
				e = event{kind: evClusters}
			}
			if err != nil {
				break
			}
			if e.kind != evClusters {
				err = c.send(ctx, e)
				break
			}
			// Scoped streams take a cluster's counts and findings from
			// counts and attention events, and resend clusters only when
			// something else changed.
			if !c.scoped || e.why == 0 {
				clusters.schedule()
				break
			}
			if e.why&whyCounts != 0 {
				c.dirtyCounts[e.cluster] = struct{}{}
			}
			if e.why&whyFindings != 0 {
				c.dirtyFindings[e.cluster] = struct{}{}
			}
			counts.schedule()
		}
		if err != nil {
			return
		}
	}
}

// debounce fires at most once per interval: schedule arms it, at once
// when the last firing is an interval ago, else at the end of the
// interval. It is not safe for concurrent use.
type debounce struct {
	every time.Duration
	last  time.Time
	tmr   *time.Timer
	ch    <-chan time.Time
}

func newDebounce(every time.Duration) *debounce { return &debounce{every: every, last: time.Now()} }

func (d *debounce) schedule() {
	if d.ch == nil {
		d.tmr = time.NewTimer(max(0, time.Until(d.last.Add(d.every))))
		d.ch = d.tmr.C
	}
}

// due is nil (never ready) while nothing is scheduled.
func (d *debounce) due() <-chan time.Time { return d.ch }

func (d *debounce) fired() { d.tmr, d.ch, d.last = nil, nil, time.Now() }

func (d *debounce) stop() {
	if d.tmr != nil {
		d.tmr.Stop()
	}
}

// streamClient filters bus events for one user.
type streamClient struct {
	a  *api
	p  identity.Principal
	sw *sseWriter

	// A scoped stream (?watch=) sends change events of the watched
	// clusters only, and counts and attention events for every cluster.
	scoped bool
	watch  map[string]bool
	// dirtyCounts and dirtyFindings are the clusters whose counts or
	// findings changed since the last flush; sentCounts and sentFindings
	// what was last sent per cluster (findings as JSON), so unchanged
	// values are not resent.
	dirtyCounts, dirtyFindings map[string]struct{}
	sentCounts                 map[string]sentCounts
	sentFindings               map[string]string
}

// sentCounts are one cluster's counts as last sent. The maps come fresh
// from fleetService.counts and are never modified.
type sentCounts struct {
	counts map[model.Status]int
	kinds  map[string]map[model.Status]int
}

func (s sentCounts) equal(counts map[model.Status]int, kinds map[string]map[model.Status]int) bool {
	return maps.Equal(s.counts, counts) && maps.EqualFunc(s.kinds, kinds, maps.Equal)
}

func (c *streamClient) sendClusters(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, sseClustersTimeout)
	defer cancel()
	items, err := c.a.fleet.Clusters(ctx, c.p)
	if err != nil {
		return nil // try again on the next change
	}
	if c.scoped {
		// What the client now holds: counts and findings equal to these
		// are not sent again.
		for _, ci := range items {
			c.sentCounts[ci.Name] = sentCounts{counts: ci.Counts, kinds: ci.Kinds}
			c.sentFindings[ci.Name] = findingsKey(ci.Findings)
		}
	}
	return c.sw.event("clusters", map[string]any{"items": items})
}

func findingsKey(fs []model.Finding) string {
	if len(fs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(fs)
	if err != nil {
		return ""
	}
	return string(b)
}

// flushCounts sends the counts and findings of the dirty clusters that
// changed for the user since they were last sent. Counts come from the
// view's incremental per-kind counts (O(kinds) for a cluster-wide
// reader), never from a scan of the rows.
func (c *streamClient) flushCounts(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, sseFilterTimeout)
	defer cancel()
	for cluster := range c.dirtyCounts {
		counts, kinds, ok := c.a.fleet.clusterCounts(ctx, c.p, cluster)
		if !ok {
			continue
		}
		if last, ok := c.sentCounts[cluster]; ok && last.equal(counts, kinds) {
			continue
		}
		c.sentCounts[cluster] = sentCounts{counts: counts, kinds: kinds}
		if err := c.sw.event("counts", countsEvent{Cluster: cluster, Counts: counts, Kinds: kinds}); err != nil {
			return err
		}
	}
	clear(c.dirtyCounts)
	for cluster := range c.dirtyFindings {
		fs, ok := c.a.fleet.clusterFindings(ctx, c.p, cluster)
		if !ok {
			continue
		}
		key := findingsKey(fs)
		if c.sentFindings[cluster] == key {
			continue
		}
		c.sentFindings[cluster] = key
		if fs == nil {
			fs = []model.Finding{}
		}
		if err := c.sw.event("attention", attentionEvent{Cluster: cluster, Upserts: []model.Resource{}, Deletes: []string{}, Findings: fs}); err != nil {
			return err
		}
	}
	clear(c.dirtyFindings)
	return nil
}

func (c *streamClient) resyncAll() error {
	for _, spec := range c.a.reg.List() {
		if err := c.sw.event("resync", clusterEvent{Cluster: spec.Name}); err != nil {
			return err
		}
	}
	return nil
}

func (c *streamClient) send(ctx context.Context, e event) error {
	switch e.kind {
	case evResync:
		return c.sw.event("resync", clusterEvent{Cluster: e.cluster})
	case evConnection:
		// Only the name travels; GET …/connection checks access.
		return c.sw.event("connection", clusterEvent{Cluster: e.cluster})
	case evChange:
		if c.scoped {
			if err := c.sendAttention(ctx, e); err != nil {
				return err
			}
			if !c.watch[e.cluster] {
				return nil
			}
		}
		fctx, cancel := context.WithTimeout(ctx, sseFilterTimeout)
		ups, dels, err := c.a.fleet.filterChange(fctx, c.p, e)
		cancel()
		if err != nil {
			// The change cannot be filtered right now: let the client refetch
			// (the snapshot endpoint filters again) rather than guess.
			return c.sw.event("resync", clusterEvent{Cluster: e.cluster})
		}
		if len(ups) == 0 && len(dels) == 0 {
			return nil
		}
		if ups == nil {
			ups = []model.Resource{}
		}
		if dels == nil {
			dels = []string{}
		}
		return c.sw.event("change", changeEvent{Cluster: e.cluster, Upserts: ups, Deletes: dels})
	case evThread:
		fctx, cancel := context.WithTimeout(ctx, sseFilterTimeout)
		ok := c.a.threadVisible(fctx, c.p, e.thread)
		cancel()
		if !ok {
			return nil
		}
		return c.sw.event("thread", threadEvent{ThreadID: e.thread.ID, Ref: e.thread.Ref})
	}
	return nil
}

// sendAttention sends a change's moves into and out of the attention set
// that the user may see (the list rule, as for change events).
func (c *streamClient) sendAttention(ctx context.Context, e event) error {
	if len(e.attnUpserts) == 0 && len(e.attnDeletes) == 0 {
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, sseFilterTimeout)
	ups, dels, err := c.a.fleet.filterChange(fctx, c.p, event{cluster: e.cluster, upserts: e.attnUpserts, deletes: e.attnDeletes})
	cancel()
	if err != nil {
		return c.sw.event("resync", clusterEvent{Cluster: e.cluster})
	}
	if len(ups) == 0 && len(dels) == 0 {
		return nil
	}
	if ups == nil {
		ups = []model.Resource{}
	}
	if dels == nil {
		dels = []string{}
	}
	return c.sw.event("attention", attentionEvent{Cluster: e.cluster, Upserts: ups, Deletes: dels})
}

// threadVisible applies the thread read rule for SSE notifications: private
// threads only to their creator; cluster-level threads to anyone who sees
// the cluster; otherwise `get` on the target. Errors mean "no".
func (a *api) threadVisible(ctx context.Context, p identity.Principal, t store.Thread) bool {
	if t.Visibility != store.VisibilityResource && t.CreatedBy.Subject != p.User {
		return false
	}
	if _, ok := a.reg.Get(t.Ref.Cluster); !ok {
		return false
	}
	if t.Ref.Kind == "" {
		return true
	}
	ok, err := a.fleet.CanGet(ctx, p, t.Ref.Cluster, model.Ref{Group: t.Ref.Group, Kind: t.Ref.Kind, Namespace: t.Ref.Namespace, Name: t.Ref.Name})
	return err == nil && ok
}

// handleLogs serves GET /api/v1/clusters/{c}/pods/{ns}/{name}/logs as SSE:
// `log` events with {lines}, then one `end` event, {} or {error}.
func (a *api) handleLogs(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	cluster, ns, name := r.PathValue("cluster"), r.PathValue("ns"), r.PathValue("name")
	q := r.URL.Query()
	var tail int64
	if v := q.Get("tail"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > MaxTailLines {
			a.fail(w, r, badRequest("tail must be between 1 and %d", MaxTailLines))
			return
		}
		tail = n
	}
	follow, err := parseBool(q.Get("follow"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	container := q.Get("container")
	if len(container) > 253 {
		a.fail(w, r, badRequest("invalid container name"))
		return
	}
	pod := model.Ref{Kind: "Pod", Namespace: ns, Name: name}
	// Check visibility before committing to a stream, so the common errors
	// (unknown pod, disconnected cluster) are plain JSON responses.
	if _, err := a.fleet.Get(r.Context(), p, cluster, pod); err != nil {
		a.fail(w, r, err)
		return
	}
	release, ok := a.logStreams.acquire(p.User)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many open log streams")
		return
	}
	defer release()
	sw, err := startSSE(w)
	if err != nil {
		return
	}
	a.metrics.logStreams.Add(1)
	defer a.metrics.logStreams.Add(-1)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		t := time.NewTicker(sseKeepalive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.shutdown:
				cancel()
				return
			case <-t.C:
				if sw.comment("keepalive") != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = a.fleet.Logs(ctx, p, cluster, pod, fleet.LogOptions{Container: container, TailLines: tail, Follow: follow},
		fleet.LineWriterFunc(func(lines []string) error {
			if lines == nil {
				lines = []string{}
			}
			return sw.event("log", map[string]any{"lines": lines})
		}))
	if ctx.Err() != nil && r.Context().Err() != nil {
		return
	}
	end := map[string]any{}
	if err != nil {
		he := classify(err)
		end["error"] = errorDetail{Code: he.code, Message: he.message}
	}
	_ = sw.event("end", end)
}

func parseBool(s string) (bool, error) {
	if s == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, badRequest("invalid boolean %q", truncate(s, 16))
	}
	return b, nil
}
