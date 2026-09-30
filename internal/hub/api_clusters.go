package hub

import (
	"net/http"
	"strings"

	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/version"
)

type features struct {
	AI             bool   `json:"ai"`
	AIProvider     string `json:"aiProvider,omitempty"`
	MCP            bool   `json:"mcp"`
	MCPWrites      bool   `json:"mcpWrites"`
	Logs           bool   `json:"logs"`
	EphemeralStore bool   `json:"ephemeralStore"`
	DevMode        bool   `json:"devMode"`
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
		EphemeralStore: a.ephemeral,
		DevMode:        a.auth.DevMode(),
	}
	f.MCPWrites = f.MCP && a.cfg.MCP.Writes && fl.MCPWrites
	if a.ai != nil && a.ai.Enabled() {
		f.AI, f.AIProvider = true, a.ai.ProviderName()
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
	items, err := a.fleet.Clusters(r.Context(), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

var validStatuses = []model.Status{model.StatusReady, model.StatusFailed, model.StatusReconciling, model.StatusSuspended, model.StatusUnknown}

func (a *api) handleResources(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
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
	items, rv, err := a.fleet.list(r.Context(), p, r.PathValue("cluster"), fl)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []model.Resource{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "resourceVersion": rv})
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
// objects. The group comes from the kind table (fleet canonicalises it).
func objectRef(r *http.Request) model.Ref {
	ns := r.PathValue("ns")
	if ns == "_" {
		ns = ""
	}
	return model.Ref{Kind: r.PathValue("kind"), Namespace: ns, Name: r.PathValue("name")}
}

func (a *api) handleObject(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	res, err := a.fleet.Get(r.Context(), p, r.PathValue("cluster"), objectRef(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *api) handleChildren(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	items, err := a.fleet.Children(r.Context(), p, r.PathValue("cluster"), objectRef(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
