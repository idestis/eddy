package hub

import (
	"net/http"
	"slices"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/store"
)

// Audit paging.
const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

// handleAudit serves GET /api/v1/audit. Callers see their own events;
// members of auth.auditViewerGroups may query anyone's.
func (a *api) handleAudit(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), defaultAuditLimit, maxAuditLimit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	subject := q.Get("subject")
	if !a.auditViewer(p) {
		if subject != "" && subject != p.User {
			writeError(w, http.StatusForbidden, "forbidden", "you may only read your own audit events")
			return
		}
		subject = p.User
	}
	items, next, err := a.store.Audit().Query(r.Context(), store.AuditFilter{
		Subject: subject,
		Target:  store.ResourceRef{Cluster: q.Get("cluster")},
		Cursor:  q.Get("cursor"),
		Limit:   limit,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []store.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

func (a *api) auditViewer(p identity.Principal) bool {
	for _, g := range a.cfg.Auth.AuditViewerGroups {
		if slices.Contains(p.Groups, g) {
			return true
		}
	}
	return false
}
