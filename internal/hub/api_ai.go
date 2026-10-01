package hub

import (
	"net/http"
	"time"

	"github.com/idestis/eddy/internal/ai"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// maxAskBody caps POST /api/v1/ai/ask: an 8 KiB question, ten context
// references and 32 KiB of attachments, with room for JSON escaping.
const maxAskBody = 256 << 10

// askBody is the body of POST /api/v1/ai/ask (docs/api.md).
type askBody struct {
	ChatID      string               `json:"chatId"`
	Context     *[]store.ResourceRef `json:"context"`
	Question    string               `json:"question"`
	Attachments []ai.Attachment      `json:"attachments"`
}

func (a *api) handleAsk(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	// The kill switch blocks asks only; the chat routes keep working.
	if a.ai == nil || !a.ai.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "disabled", "Ask AI is disabled")
		return
	}
	var body askBody
	if err := decodeJSON(w, r, maxAskBody, &body, false); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(body.Question) > ai.MaxQuestionBytes {
		a.fail(w, r, badRequest("question exceeds %d bytes", ai.MaxQuestionBytes))
		return
	}
	req := ai.AskRequest{ChatID: body.ChatID, Question: body.Question, Attachments: body.Attachments}
	if body.Context != nil {
		refs, err := a.canonicalChatContext(r, p, *body.Context)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		req.Context = &refs
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
	if resp.ContextStatus == nil {
		resp.ContextStatus = []ai.ContextStatus{}
	}
	resp.Chat = chatView(resp.Chat)
	writeJSON(w, http.StatusOK, resp)
}
