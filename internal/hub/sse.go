package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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

// handleStream serves GET /api/v1/stream.
func (a *api) handleStream(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
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
	if err := sw.event("hello", struct{}{}); err != nil {
		return
	}
	if err := c.sendClusters(ctx); err != nil {
		return
	}

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	var (
		clustersDue  <-chan time.Time
		clustersTmr  *time.Timer
		lastClusters = time.Now()
	)
	defer func() {
		if clustersTmr != nil {
			clustersTmr.Stop()
		}
	}()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-a.shutdown:
			return
		case <-keepalive.C:
			err = sw.comment("keepalive")
		case <-clustersDue:
			clustersDue, clustersTmr = nil, nil
			lastClusters = time.Now()
			err = c.sendClusters(ctx)
		case e := <-sub.ch:
			if sub.overflow.Swap(false) {
				a.metrics.sseDropped.Add(1)
				err = c.resyncAll()
				e = event{kind: evClusters}
			}
			if err == nil {
				if e.kind == evClusters {
					if clustersDue == nil {
						clustersTmr = time.NewTimer(max(0, time.Until(lastClusters.Add(sseClustersEvery))))
						clustersDue = clustersTmr.C
					}
				} else {
					err = c.send(ctx, e)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// streamClient filters bus events for one user.
type streamClient struct {
	a  *api
	p  identity.Principal
	sw *sseWriter
}

func (c *streamClient) sendClusters(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, sseClustersTimeout)
	defer cancel()
	items, err := c.a.fleet.Clusters(ctx, c.p)
	if err != nil {
		return nil // try again on the next change
	}
	return c.sw.event("clusters", map[string]any{"items": items})
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
	case evChange:
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
