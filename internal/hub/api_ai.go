package hub

import (
	"net/http"
	"time"

	"github.com/eddy-gitops/eddy/internal/ai"
	"github.com/eddy-gitops/eddy/internal/identity"
)

func (a *api) handleAsk(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	if a.ai == nil || !a.ai.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "disabled", "Ask AI is disabled")
		return
	}
	var req ai.AskRequest
	if err := decodeJSON(w, r, maxJSONBodyBytes, &req, false); err != nil {
		a.fail(w, r, err)
		return
	}
	// An answer can take up to ai.limits.timeout; extend the server's write
	// deadline for this response only.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(a.cfg.AI.Limits.Timeout.Duration + 30*time.Second))
	resp, err := a.ai.Ask(r.Context(), p, req)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if resp.Steps == nil {
		resp.Steps = []ai.Step{}
	}
	writeJSON(w, http.StatusOK, resp)
}
