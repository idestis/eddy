package hub

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/threads"
)

// Message paging (docs/api.md): 100 by default, at most 500.
const (
	defaultMessageLimit = 100
	maxMessageLimit     = 500
)

func parseLimit(s string, def, max int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, badRequest("limit must be a positive integer")
	}
	return min(n, max), nil
}

// canonicalThreadRef fills in the API group of a thread target. A kind of
// the kind table, with no group or its own, gets the table's group. Any
// other well-formed kind names an inventory-only row (a ConfigMap, a CRD
// of another group…): kind and group are kept as given, "core" naming the
// core group. Malformed kinds are refused.
func canonicalThreadRef(ref store.ResourceRef) (store.ResourceRef, error) {
	if ref.Kind == "" {
		return ref, nil
	}
	if k, ok := flux.KindByName(ref.Kind); ok && (ref.Group == "" || ref.Group == k.Group || (ref.Group == coreGroupAlias && k.Group == "")) {
		ref.Kind, ref.Group = k.Kind, k.Group
		return ref, nil
	}
	if !flux.ValidKindName(ref.Kind) || len(ref.Group) > 253 || strings.ContainsAny(ref.Group, "\x00/ ") {
		return ref, badRequest("unknown kind %q", truncate(ref.Kind, 64))
	}
	if ref.Group == coreGroupAlias {
		ref.Group = ""
		return ref, nil
	}
	if ref.Group == "" {
		ref.Group = groupUnresolved
	}
	return ref, nil
}

// groupUnresolved marks a ref outside the kind table whose group the
// caller did not give; resolveThreadRef finds it from the visible rows.
const groupUnresolved = "\x00"

// resolveThreadRef completes a canonical ref outside the kind table whose
// group was not given: the group of the one visible inventory row of that
// kind, namespace and name (400 when several groups have one, ErrNotFound
// when none is visible).
func (a *api) resolveThreadRef(r *http.Request, p identity.Principal, ref store.ResourceRef) (store.ResourceRef, error) {
	if ref.Group != groupUnresolved {
		return ref, nil
	}
	got, _, _, err := a.fleet.locate(r.Context(), p, ref.Cluster, model.Ref{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name})
	if err != nil {
		return ref, err
	}
	ref.Group = got.Group
	return ref, nil
}

func (a *api) handleListThreads(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	ref, err := canonicalThreadRef(store.ResourceRef{
		Cluster: q.Get("cluster"), Group: q.Get("group"), Kind: q.Get("kind"), Namespace: q.Get("namespace"), Name: q.Get("name"),
	})
	if err == nil {
		ref, err = a.resolveThreadRef(r, p, ref)
	}
	if errors.Is(err, fleet.ErrNotFound) {
		// Threads on an object p cannot see are not listed.
		a.writeJSONWithETag(w, r, p, map[string]any{"items": []store.Thread{}, "next": ""})
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f := store.ThreadFilter{Ref: ref, Cursor: q.Get("cursor")}
	switch st := store.ThreadStatus(q.Get("status")); st {
	case "", store.ThreadOpen, store.ThreadResolved:
		f.Status = st
	default:
		a.fail(w, r, badRequest("status must be open or resolved"))
		return
	}
	switch t := store.ThreadType(q.Get("type")); t {
	case "", store.ThreadDiscussion, store.ThreadAsk:
		f.Type = t
	default:
		a.fail(w, r, badRequest("type must be discussion or ask"))
		return
	}
	if f.Limit, err = parseLimit(q.Get("limit"), threads.DefaultListLimit, threads.MaxListLimit); err != nil {
		a.fail(w, r, err)
		return
	}
	items, next, err := a.threads.List(r.Context(), p, f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []store.Thread{}
	}
	a.writeJSONWithETag(w, r, p, map[string]any{"items": items, "next": next})
}

type createThreadBody struct {
	Ref   store.ResourceRef `json:"ref"`
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Type  store.ThreadType  `json:"type"`
}

func (a *api) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var body createThreadBody
	if err := decodeJSON(w, r, maxThreadBody, &body, false); err != nil {
		a.fail(w, r, err)
		return
	}
	// Ask threads are created by Ask AI only.
	if body.Type != "" && body.Type != store.ThreadDiscussion {
		a.fail(w, r, badRequest("type must be discussion"))
		return
	}
	ref, err := canonicalThreadRef(body.Ref)
	if err == nil {
		ref, err = a.resolveThreadRef(r, p, ref)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	t, m, err := a.threads.Create(r.Context(), p, threads.CreateInput{
		Ref: ref, Title: body.Title, Body: body.Body, Type: store.ThreadDiscussion,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"thread": t, "message": m})
}

func (a *api) handleGetThread(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), defaultMessageLimit, maxMessageLimit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	t, msgs, next, err := a.threads.Get(r.Context(), p, r.PathValue("id"), q.Get("cursor"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": t, "messages": msgs, "next": next})
}

func (a *api) handleReply(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(w, r, maxThreadBody, &body, false); err != nil {
		a.fail(w, r, err)
		return
	}
	m, err := a.threads.Reply(r.Context(), p, r.PathValue("id"), body.Body, store.Author{}, nil)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (a *api) handleResolve(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	t, err := a.threads.Resolve(r.Context(), p, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *api) handleReopen(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	t, err := a.threads.Reopen(r.Context(), p, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *api) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.From(r.Context())
	if err := a.threads.Delete(r.Context(), p, r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
