package hub

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// Ask AI chats (ADR-0007). Every chat is private to its owner: the store
// scopes each call by p.User, so another user's chat id is 404. These routes
// keep working when the aiEnabled kill switch is off, so users can still
// read, rename and delete their history; only asks are blocked.

// Chat list paging (docs/api.md): 50 by default, at most 200.
const (
	defaultChatLimit = 50
	maxChatLimit     = 200
)

// chatView is a Chat as the API returns it: context is always an array.
func chatView(c store.Chat) store.Chat {
	if c.Context == nil {
		c.Context = []store.ResourceRef{}
	}
	return c
}

// canonicalChatContext validates and canonicalises the references of a chat
// context, like thread targets: the cluster must exist, the kind must be
// known or a well-formed inventory kind (its group found from the rows p
// may see when not given), and kind "" means the whole cluster. Duplicates
// are dropped, keeping the first. Whether p may still see each reference is
// decided on every ask, not here.
func (a *api) canonicalChatContext(r *http.Request, p identity.Principal, refs []store.ResourceRef) ([]store.ResourceRef, error) {
	if len(refs) > store.MaxChatContext {
		return nil, badRequest("context has more than %d references", store.MaxChatContext)
	}
	out := make([]store.ResourceRef, 0, len(refs))
	seen := make(map[store.ResourceRef]bool, len(refs))
	for i, ref := range refs {
		switch {
		case ref.Cluster == "":
			return nil, badRequest("context[%d]: cluster is required", i)
		case ref.Kind == "" && (ref.Group != "" || ref.Namespace != "" || ref.Name != ""):
			return nil, badRequest("context[%d]: a cluster reference must not set group, namespace or name", i)
		case ref.Kind != "" && ref.Name == "":
			return nil, badRequest("context[%d]: name is required when kind is set", i)
		}
		if _, ok := a.reg.Get(ref.Cluster); !ok {
			return nil, badRequest("context[%d]: unknown cluster %q", i, truncate(ref.Cluster, 64))
		}
		c, err := canonicalThreadRef(ref)
		if err == nil {
			c, err = a.resolveThreadRef(r, p, c)
		}
		if errors.Is(err, fleet.ErrNotFound) {
			return nil, badRequest("context[%d]: no %s named %q that you can see", i, truncate(ref.Kind, 64), truncate(ref.Name, 253))
		}
		if err != nil {
			return nil, fmt.Errorf("context[%d]: %w", i, err)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

func (a *api) handleListChats(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), defaultChatLimit, maxChatLimit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items, next, err := a.store.Chats().List(r.Context(), p.User, q.Get("cursor"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]store.Chat, len(items))
	for i, c := range items {
		out[i] = chatView(c)
	}
	a.writeJSONWithETag(w, r, p, map[string]any{"items": out, "next": next})
}

func (a *api) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var body struct {
		Context []store.ResourceRef `json:"context"`
	}
	if err := decodeJSON(w, r, maxJSONBodyBytes, &body, true); err != nil {
		a.fail(w, r, err)
		return
	}
	refs, err := a.canonicalChatContext(r, p, body.Context)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.store.Chats().Create(r.Context(), store.Chat{Owner: p.User, Context: refs})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, chatView(c))
}

func (a *api) handleGetChat(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), defaultMessageLimit, maxMessageLimit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	c, err := a.store.Chats().Get(r.Context(), p.User, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	msgs, next, err := a.store.Chats().Messages(r.Context(), p.User, id, q.Get("cursor"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	a.writeJSONWithETag(w, r, p, map[string]any{"chat": chatView(c), "messages": msgs, "next": next})
}

func (a *api) handlePatchChat(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var body struct {
		Title   *string              `json:"title"`
		Context *[]store.ResourceRef `json:"context"`
	}
	if err := decodeJSON(w, r, maxJSONBodyBytes, &body, false); err != nil {
		a.fail(w, r, err)
		return
	}
	var u store.ChatUpdate
	if body.Title != nil {
		title := strings.Join(strings.Fields(*body.Title), " ")
		switch {
		case title == "":
			a.fail(w, r, badRequest("title must not be empty"))
			return
		case utf8.RuneCountInString(title) > store.MaxTitleLen:
			a.fail(w, r, badRequest("title is longer than %d characters", store.MaxTitleLen))
			return
		}
		u.Title = &title
	}
	if body.Context != nil {
		refs, err := a.canonicalChatContext(r, p, *body.Context)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		u.Context = &refs
	}
	c, err := a.store.Chats().Update(r.Context(), p.User, r.PathValue("id"), u)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, chatView(c))
}

func (a *api) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	if err := a.store.Chats().Delete(r.Context(), p.User, r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
