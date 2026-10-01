package hub

import (
	"net/http"
	"strings"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/version"
)

type features struct {
	AI         bool   `json:"ai"`
	AIProvider string `json:"aiProvider,omitempty"`
	// AILogs: Ask AI may read pod logs (ai.allowLogs). The UI offers log questions only then.
	AILogs    bool `json:"aiLogs"`
	MCP       bool `json:"mcp"`
	MCPWrites bool `json:"mcpWrites"`
	Logs      bool `json:"logs"`
	// WorkloadLogs: GET …/workloads/{kind}/{ns}/{name}/logs exists (the
	// cluster's agent must be new enough; an older one answers 400).
	WorkloadLogs   bool `json:"workloadLogs"`
	EphemeralStore bool `json:"ephemeralStore"`
	DevMode        bool `json:"devMode"`
	// Onboarding: clusters can be added from the UI (ADR-0005). Whether
	// this user may is GET /api/v1/clusters/permissions.
	Onboarding bool `json:"onboarding"`
}

type meResponse struct {
	User     string   `json:"user"`
	Display  string   `json:"display"`
	Groups   []string `json:"groups"`
	Provider string   `json:"provider"`
	CSRF     string   `json:"csrf"`
	Features features `json:"features"`
	Version  string   `json:"version"`
}

func (a *api) features() features {
	fl := a.flags.Current()
	f := features{
		MCP:            a.cfg.MCP.Enabled && fl.MCPEnabled,
		Logs:           true,
		WorkloadLogs:   true,
		EphemeralStore: a.ephemeral,
		DevMode:        a.auth.DevMode(),
		Onboarding:     a.onboard.enabled(),
	}
	f.MCPWrites = f.MCP && a.cfg.MCP.Writes && fl.MCPWrites
	if a.ai != nil && a.ai.Enabled() {
		f.AI, f.AIProvider = true, a.ai.ProviderName()
		f.AILogs = a.cfg.AI.AllowLogs
	}
	return f
}

func (a *api) handleMe(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	groups := p.Groups
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, http.StatusOK, meResponse{
		User: p.User, Display: p.Display, Groups: groups, Provider: p.Provider,
		CSRF: a.auth.CSRFToken(r), Features: a.features(), Version: version.Version,
	})
}

func (a *api) handleClusters(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	if a.listNotModified(w, r, p, a.fleet.fleetVersion()) {
		return
	}
	items, err := a.fleet.Clusters(r.Context(), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

var validStatuses = model.Statuses

func (a *api) handleResources(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	switch q.Get("view") {
	case "", "full":
	case "index":
		a.handleIndex(w, r, p)
		return
	default:
		a.fail(w, r, badRequest("view must be full or index"))
		return
	}
	var fl fleet.Filter
	for _, v := range q["kind"] {
		fl.Kinds = append(fl.Kinds, strings.Split(v, ",")...)
	}
	fl.Namespace = q.Get("namespace")
	if st := q.Get("status"); st != "" {
		fl.Status = model.Status(st)
		if !containsStatus(fl.Status) {
			a.fail(w, r, badRequest("unknown status %q", truncate(st, 32)))
			return
		}
	}
	fl.Query = q.Get("q")
	if len(fl.Query) > 256 {
		a.fail(w, r, badRequest("q is too long"))
		return
	}
	if offset, limit, ok, err := hiddenJobsQuery(r, fl.Kinds); err != nil || ok {
		if err != nil {
			a.fail(w, r, err)
			return
		}
		a.handleResourcesWithHidden(w, r, p, r.PathValue("cluster"), fl, offset, limit)
		return
	}
	cluster := r.PathValue("cluster")
	if version, stale, ok := a.fleet.viewVersion(cluster); ok {
		a.markStale(w, stale)
		if a.listNotModified(w, r, p, version) {
			return
		}
	}
	items, rv, err := a.fleet.list(r.Context(), p, cluster, fl)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []model.Resource{}
	}
	body := map[string]any{"items": items, "resourceVersion": rv}
	if a.fleet.agents.isStale(cluster) {
		body["stale"] = true
	}
	writeJSON(w, http.StatusOK, body)
}

// markStale flags a read served from a stale view (ADR-0006) with the
// X-Eddy-Stale header; list bodies also say "stale": true.
func (a *api) markStale(w http.ResponseWriter, stale bool) {
	if stale {
		w.Header().Set("X-Eddy-Stale", "true")
	}
}

func containsStatus(s model.Status) bool {
	for _, v := range validStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// objectRef reads {kind}/{ns}/{name}; "_" is the namespace of cluster-scoped
// objects. The optional ?group= picks the API group when a Kind name exists
// in several groups ("core" is the core group); without it the group comes
// from the kind table or the one inventory-only row that matches (fleet
// canonicalises it).
func objectRef(r *http.Request) model.Ref {
	ns := r.PathValue("ns")
	if ns == "_" {
		ns = ""
	}
	return model.Ref{Group: truncate(r.URL.Query().Get("group"), 253), Kind: r.PathValue("kind"), Namespace: ns, Name: r.PathValue("name")}
}

func (a *api) handleObject(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	res, err := a.fleet.Get(r.Context(), p, r.PathValue("cluster"), objectRef(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.markStale(w, a.fleet.agents.isStale(r.PathValue("cluster")))
	writeJSON(w, http.StatusOK, res)
}

func (a *api) handleChildren(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	items, err := a.fleet.Children(r.Context(), p, r.PathValue("cluster"), objectRef(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := map[string]any{"items": items}
	if a.fleet.agents.isStale(r.PathValue("cluster")) {
		a.markStale(w, true)
		body["stale"] = true
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *api) handleYAML(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	y, err := a.fleet.YAML(r.Context(), p, r.PathValue("cluster"), objectRef(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"yaml": y})
}

func (a *api) handleEvents(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	items, err := a.fleet.Events(r.Context(), p, r.PathValue("cluster"), objectRef(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type actionBody struct {
	WithSource bool   `json:"withSource"`
	Confirm    string `json:"confirm"`
}

func (a *api) handleAction(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var body actionBody
	if err := decodeJSON(w, r, maxJSONBodyBytes, &body, true); err != nil {
		a.fail(w, r, err)
		return
	}
	cluster, ref := r.PathValue("cluster"), objectRef(r)
	o := fleet.ActionOptions{WithSource: body.WithSource, Confirm: body.Confirm}
	var err error
	switch r.PathValue("action") {
	case "reconcile":
		err = a.fleet.Reconcile(r.Context(), p, cluster, ref, o)
	case "suspend":
		err = a.fleet.Suspend(r.Context(), p, cluster, ref, o)
	case "resume":
		err = a.fleet.Resume(r.Context(), p, cluster, ref, o)
	default:
		notFoundAPI(w, r)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *api) handleKinds(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	cluster := r.PathValue("cluster")
	if version, stale, ok := a.fleet.viewVersion(cluster); ok {
		a.markStale(w, stale)
		if a.listNotModified(w, r, p, version) {
			return
		}
	}
	res, err := a.fleet.Kinds(r.Context(), p, cluster)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res.Stale = a.fleet.agents.isStale(cluster)
	writeJSON(w, http.StatusOK, res)
}

// handleSearch serves GET /api/v1/search?q=&scope=cluster|fleet&cluster=&kind=&limit=.
func (a *api) handleSearch(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	o := searchOptions{Query: q.Get("q"), Cluster: q.Get("cluster")}
	for _, v := range q["kind"] {
		o.Kinds = append(o.Kinds, strings.Split(v, ",")...)
	}
	if len(o.Kinds) > searchMaxKinds {
		a.fail(w, r, badRequest("kind names at most %d kinds", searchMaxKinds))
		return
	}
	switch q.Get("scope") {
	case "", "fleet":
		o.Fleet = true
	case "cluster":
		if o.Cluster == "" {
			a.fail(w, r, badRequest("scope=cluster needs cluster="))
			return
		}
	default:
		a.fail(w, r, badRequest("scope must be cluster or fleet"))
		return
	}
	var err error
	if o.Limit, err = parseLimit(q.Get("limit"), searchDefaultLimit, searchMaxLimit); err != nil {
		a.fail(w, r, err)
		return
	}
	release, ok := a.searches.acquire(p.User)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many searches in flight")
		return
	}
	defer release()
	res, err := a.fleet.Search(r.Context(), p, o)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	writeJSON(w, http.StatusOK, res)
}

// handleAttention serves GET /api/v1/attention?cluster=&limit=.
func (a *api) handleAttention(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), attentionDefaultLimit, attentionMaxLimit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cluster := q.Get("cluster")
	version := a.fleet.fleetVersion()
	if cluster != "" {
		v, _, ok := a.fleet.viewVersion(cluster)
		if !ok {
			v = "none"
		}
		version = v
	}
	if a.listNotModified(w, r, p, version) {
		return
	}
	res, err := a.fleet.Attention(r.Context(), p, cluster, limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleIndex serves GET …/resources?view=index (ADR-0006 P2): one page of
// index rows with the total and facets (index_list.go).
func (a *api) handleIndex(w http.ResponseWriter, r *http.Request, p identity.Principal) {
	q := r.URL.Query()
	if q.Get("includeHidden") != "" {
		a.fail(w, r, badRequest("includeHidden does not apply to view=index"))
		return
	}
	iq, err := parseIndexQuery(q)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cluster := r.PathValue("cluster")
	if version, stale, ok := a.fleet.viewVersion(cluster); ok {
		a.markStale(w, stale)
		if a.listNotModified(w, r, p, version) {
			return
		}
	}
	page, err := a.fleet.indexList(r.Context(), p, cluster, iq)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.metrics.indexLists.Add(1)
	writeJSON(w, http.StatusOK, page)
}
