package hub

import (
	"net/http"

	"github.com/idestis/eddy/internal/identity"
)

// Onboarding endpoints (ADR-0005). Every one checks the caller's RBAC in
// the management cluster with a SubjectAccessReview on
// clusters.gitops.eddy.dev, and every change is audited.

func (a *api) handleClusterPermissions(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	out := map[string]bool{"onboarding": a.onboard.enabled(), "create": false}
	if a.onboard.enabled() {
		ok, err := a.onboard.can(r.Context(), p, "create", "")
		if err != nil {
			a.fail(w, r, err)
			return
		}
		out["create"] = ok
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) handleCreateCluster(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var in clusterInput
	if err := decodeJSON(w, r, 64<<10, &in, false); err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := a.onboard.create(r.Context(), p, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *api) handleUpdateCluster(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var in clusterInput
	if err := decodeJSON(w, r, 64<<10, &in, false); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Name != "" && in.Name != r.PathValue("cluster") {
		a.fail(w, r, badRequest("a cluster cannot be renamed"))
		return
	}
	out, err := a.onboard.update(r.Context(), p, r.PathValue("cluster"), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster": out})
}

func (a *api) handleJoinToken(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var in struct {
		TTL string `json:"ttl"`
	}
	if err := decodeJSON(w, r, 4<<10, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	tok, guide, err := a.onboard.reissue(r.Context(), p, r.PathValue("cluster"), in.TTL)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"joinToken": tok, "guide": guide})
}

func (a *api) handleDeleteCluster(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, 4<<10, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.onboard.remove(r.Context(), p, r.PathValue("cluster"), in.Confirm); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleConnection(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	out, err := a.onboard.connection(r.Context(), p, r.PathValue("cluster"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
