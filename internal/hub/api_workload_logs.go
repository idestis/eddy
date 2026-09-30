package hub

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// Workload log request limits. The agent applies its own caps as well
// (EDDY_MAX_LOG_PODS, EDDY_LOG_LINE_RATE).
const (
	DefaultWorkloadTail = 100
	MaxWorkloadTail     = 1000
	maxWorkloadPodNames = 20
	maxSinceSeconds     = 30 * 24 * 3600
)

// handleWorkloadLogs serves
// GET /api/v1/clusters/{c}/workloads/{kind}/{ns}/{name}/logs as SSE: a
// `pods` event with the streamed pod set (again whenever it changes), `log`
// events with {entries}, then one `end` event, {} or {error}.
func (a *api) handleWorkloadLogs(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	cluster := r.PathValue("cluster")
	ref := model.Ref{Kind: r.PathValue("kind"), Namespace: r.PathValue("ns"), Name: r.PathValue("name")}
	q := r.URL.Query()
	o := workloadLogOptions{TailLines: DefaultWorkloadTail}
	if v := q.Get("tail"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > MaxWorkloadTail {
			a.fail(w, r, badRequest("tail must be between 1 and %d", MaxWorkloadTail))
			return
		}
		o.TailLines = n
	}
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > maxSinceSeconds {
			a.fail(w, r, badRequest("since must be between 1 and %d seconds", maxSinceSeconds))
			return
		}
		o.SinceSeconds = n
	}
	var err error
	if o.Follow, err = parseBool(q.Get("follow")); err != nil {
		a.fail(w, r, err)
		return
	}
	if v := q.Get("allContainers"); v != "" {
		all, err := parseBool(v)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		o.AllContainers = &all
	}
	o.Container = q.Get("container")
	if len(o.Container) > 253 {
		a.fail(w, r, badRequest("invalid container name"))
		return
	}
	for _, v := range q["pods"] {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if len(name) > 253 || len(o.Pods) == maxWorkloadPodNames {
				a.fail(w, r, badRequest("pods takes at most %d names", maxWorkloadPodNames))
				return
			}
			o.Pods = append(o.Pods, name)
		}
	}
	// Check the target before committing to a stream, so the common errors
	// (unknown workload, disconnected cluster, wrong kind) are plain JSON.
	if _, _, err := a.fleet.checkWorkload(r.Context(), p, cluster, ref); err != nil {
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
	go a.sseKeepalive(ctx, cancel, sw)
	err = a.fleet.WorkloadLogs(ctx, p, cluster, ref, o, func(c protocol.LogChunk) error {
		if c.Pods != nil {
			if err := sw.event("pods", c.Pods); err != nil {
				return err
			}
		}
		if len(c.Entries) > 0 {
			return sw.event("log", map[string]any{"entries": c.Entries})
		}
		return nil
	})
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

// sseKeepalive writes a comment every sseKeepalive until ctx ends, and
// cancels the stream on hub shutdown or a failed write.
func (a *api) sseKeepalive(ctx context.Context, cancel context.CancelFunc, sw *sseWriter) {
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
}

// handleFindings serves GET /api/v1/clusters/{c}/findings.
func (a *api) handleFindings(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	items, err := a.fleet.Findings(r.Context(), p, r.PathValue("cluster"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// Hidden Job pages of GET …/resources?kind=Job&includeHidden=1.
const (
	defaultHiddenPage = 500
	maxHiddenPage     = 1000
)

// hiddenJobsQuery reads includeHidden, cursor and limit. It reports false
// when includeHidden is not set.
func hiddenJobsQuery(r *http.Request, kinds []string) (offset, limit int, ok bool, err error) {
	q := r.URL.Query()
	include, err := parseBool(q.Get("includeHidden"))
	if err != nil || !include {
		return 0, 0, false, err
	}
	if len(kinds) != 1 || !strings.EqualFold(strings.TrimSpace(kinds[0]), flux.KindJob) {
		return 0, 0, false, badRequest("includeHidden needs kind=Job")
	}
	limit = defaultHiddenPage
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxHiddenPage {
			return 0, 0, false, badRequest("limit must be between 1 and %d", maxHiddenPage)
		}
	}
	if v := q.Get("cursor"); v != "" {
		if offset, err = strconv.Atoi(v); err != nil || offset < 0 {
			return 0, 0, false, badRequest("invalid cursor")
		}
	}
	return offset, limit, true, nil
}

// handleResourcesWithHidden answers GET …/resources?kind=Job&includeHidden=1:
// the first page (no cursor) is the listed Jobs plus the first page of
// hidden ones; later pages (cursor) hold hidden Jobs only. `hidden` says how
// many there are and where the next page starts.
func (a *api) handleResourcesWithHidden(w http.ResponseWriter, r *http.Request, p identity.Principal, cluster string, fl fleet.Filter, offset, limit int) {
	var items []model.Resource
	rv := ""
	if r.URL.Query().Get("cursor") == "" {
		var err error
		if items, rv, err = a.fleet.list(r.Context(), p, cluster, fl); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	page, err := a.fleet.HiddenJobs(r.Context(), p, cluster, fl.Namespace, offset, limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, j := range page.Items {
		if matches(j, []string{flux.KindJob}, fl) {
			items = append(items, j)
		}
	}
	if items == nil {
		items = []model.Resource{}
	}
	hidden := map[string]any{"total": page.Total}
	if page.Next > 0 {
		hidden["next"] = strconv.Itoa(page.Next)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "resourceVersion": rv, "hidden": hidden})
}
