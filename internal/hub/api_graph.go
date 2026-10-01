package hub

import (
	"net/http"
	"strconv"

	"github.com/idestis/eddy/internal/identity"
)

// handleGraph serves GET /api/v1/clusters/{cluster}/graph?kinds=flux|all&focus=<id>&hops=N.
func (a *api) handleGraph(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	cluster := r.PathValue("cluster")
	o, err := parseGraphOptions(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if version, stale, ok := a.fleet.viewVersion(cluster); ok {
		a.markStale(w, stale)
		if a.listNotModified(w, r, p, version) {
			return
		}
	}
	res, err := a.fleet.Graph(r.Context(), p, cluster, o)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res.Stale = a.fleet.agents.isStale(cluster)
	writeJSON(w, http.StatusOK, res)
}

func parseGraphOptions(r *http.Request) (graphOptions, error) {
	q := r.URL.Query()
	o := graphOptions{Hops: graphDefaultHops}
	switch q.Get("kinds") {
	case "", "flux":
	case "all":
		o.All = true
	default:
		return o, badRequest("kinds must be flux or all")
	}
	if s := q.Get("hops"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return o, badRequest("hops must be a positive integer")
		}
		o.Hops = min(n, graphMaxHops)
	}
	if s := q.Get("focus"); s != "" {
		id, err := canonicalFocus(s)
		if err != nil {
			return o, err
		}
		o.Focus = id
	}
	return o, nil
}
