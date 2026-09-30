// Package threads is the authorization-aware thread service. HTTP handlers,
// the MCP server and Ask AI all go through Service rather than store.Threads,
// so every thread read and write is checked against the caller's Kubernetes
// RBAC on the thread's target, exactly like a read of the target itself.
//
// Rules (docs/adr/0002-hub-storage.md §1):
//   - Reading and replying require `get` on the target (fleet.CanGet). A
//     cluster-level thread (Ref.Kind == "") needs only that the cluster is
//     visible to the caller (it appears in fleet.Clusters).
//   - Private threads are additionally visible only to their creator.
//   - Resolving and reopening require being the author or `patch` on the
//     target (fleet.CanPatch).
//   - Deleting requires being the author.
//
// A thread the caller may not see is reported as fleet.ErrNotFound, never
// fleet.ErrForbidden, so thread ids do not leak the existence of threads.
// fleet.ErrForbidden is returned only for visible threads the caller may not
// resolve, reopen or delete.
package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eddy-gitops/eddy/internal/audit"
	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/store"
)

// ErrInvalid is returned (wrapped, with a reason) for malformed input such
// as an empty title or body. Oversized input wraps store.ErrLimit instead.
var ErrInvalid = errors.New("threads: invalid input")

// Limits applied on top of the store's.
const (
	// MaxBodyBytesMCP caps message bodies written through MCP.
	MaxBodyBytesMCP = 8 << 10
	// DefaultListLimit and MaxListLimit bound List's page size.
	DefaultListLimit = 50
	MaxListLimit     = 200
	// maxListPages bounds how many store pages one List call reads while
	// looking for rows the caller may see.
	maxListPages = 5
)

// Service enforces thread authorization on top of a store.Threads.
type Service struct {
	st     store.Threads
	fl     fleet.Service
	rec    *audit.Recorder
	notify func(store.Thread)
	now    func() time.Time
}

// New returns a Service. rec and notify may be nil. notify is called after
// every successful write with the thread's new state (its state before
// deletion for Delete), so the hub can emit a `thread` SSE event; it must not
// block.
func New(st store.Threads, fl fleet.Service, rec *audit.Recorder, notify func(store.Thread)) *Service {
	return &Service{st: st, fl: fl, rec: rec, notify: notify, now: time.Now}
}

// CreateInput is the input of Create.
type CreateInput struct {
	Ref   store.ResourceRef
	Title string
	Body  string
	// Type defaults to store.ThreadDiscussion.
	Type store.ThreadType
	// Visibility defaults to private for ask threads and resource otherwise.
	Visibility store.Visibility
	// Author defaults to AuthorFor(p, ""). Subject and Via are always taken
	// from the principal.
	Author store.Author
	Meta   json.RawMessage
}

// AuthorFor builds the Author for a principal. t selects the author type;
// when empty it is store.AuthorAI for Ask AI principals and
// store.AuthorHuman otherwise.
func AuthorFor(p identity.Principal, t store.AuthorType) store.Author {
	if t == "" {
		t = store.AuthorHuman
		if p.Via == identity.ViaAskAI {
			t = store.AuthorAI
		}
	}
	return store.Author{Type: t, Subject: p.User, Display: p.Display, Via: string(p.Via), Client: p.Client}
}

// List returns the threads matching f that p may see, newest activity first,
// and a cursor for the next page ("" when there is none). f.Viewer is
// ignored and replaced by p.User.
//
// Visibility is decided after the query, so List reads store pages (twice
// the requested size) until it has f.Limit visible threads or has read
// maxListPages pages. A page may therefore hold fewer than f.Limit threads
// even when more exist; callers continue while the cursor is non-empty.
func (s *Service) List(ctx context.Context, p identity.Principal, f store.ThreadFilter) ([]store.Thread, string, error) {
	if p.User == "" {
		return nil, "", fleet.ErrForbidden
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	f.Viewer = p.User
	f.Limit = min(2*limit, MaxListLimit)

	c := s.checker(p)
	out := make([]store.Thread, 0, limit)
	for range maxListPages {
		items, next, err := s.st.List(ctx, f)
		if err != nil {
			return nil, "", fmt.Errorf("threads: list: %w", err)
		}
		for i, t := range items {
			// Fail closed: a thread whose visibility cannot be decided
			// right now (for example, its cluster is disconnected) is skipped.
			if ok, err := c.canSee(ctx, t); err != nil || !ok {
				continue
			}
			out = append(out, t)
			if len(out) < limit {
				continue
			}
			if i == len(items)-1 {
				return out, next, nil
			}
			// The page was only partly consumed. Cursors are opaque, so ask
			// the store for the position right after items[i] by re-reading
			// the page prefix. A thread bumped by a concurrent write between
			// the two reads can shift that position by one row, which is the
			// usual keyset caveat for activity-ordered lists.
			pf := f
			pf.Limit = i + 1
			_, cur, err := s.st.List(ctx, pf)
			if err != nil {
				return nil, "", fmt.Errorf("threads: list: %w", err)
			}
			return out, cur, nil
		}
		if next == "" {
			return out, "", nil
		}
		f.Cursor = next
	}
	return out, f.Cursor, nil
}

// Get returns a thread p may see and one page of its messages, oldest first.
func (s *Service) Get(ctx context.Context, p identity.Principal, id, cursor string, limit int) (store.Thread, []store.Message, string, error) {
	t, err := s.visible(ctx, p, id)
	if err != nil {
		return store.Thread{}, nil, "", err
	}
	msgs, next, err := s.st.Messages(ctx, id, cursor, limit)
	if errors.Is(err, store.ErrNotFound) {
		return store.Thread{}, nil, "", fleet.ErrNotFound
	}
	if err != nil {
		return store.Thread{}, nil, "", fmt.Errorf("threads: messages of %s: %w", id, err)
	}
	return t, msgs, next, nil
}

// Create starts a thread on a target p may see, with its first message.
func (s *Service) Create(ctx context.Context, p identity.Principal, in CreateInput) (store.Thread, store.Message, error) {
	if p.User == "" {
		return store.Thread{}, store.Message{}, fleet.ErrForbidden
	}
	title := strings.TrimSpace(in.Title)
	switch {
	case in.Ref.Cluster == "":
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: ref.cluster is required", ErrInvalid)
	case in.Ref.Kind == "" && (in.Ref.Group != "" || in.Ref.Namespace != "" || in.Ref.Name != ""):
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: a cluster-level ref must not set group, namespace or name", ErrInvalid)
	case in.Ref.Kind != "" && in.Ref.Name == "":
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: ref.name is required when ref.kind is set", ErrInvalid)
	case title == "":
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: title is required", ErrInvalid)
	case utf8.RuneCountInString(title) > store.MaxTitleLen:
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: title is longer than %d characters", store.ErrLimit, store.MaxTitleLen)
	}
	if err := checkBody(p, in.Body, in.Meta); err != nil {
		return store.Thread{}, store.Message{}, err
	}
	if err := checkAuthorType(in.Author.Type); err != nil {
		return store.Thread{}, store.Message{}, err
	}
	typ := in.Type
	if typ == "" {
		typ = store.ThreadDiscussion
	}
	if typ != store.ThreadDiscussion && typ != store.ThreadAsk {
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: unknown thread type %q", ErrInvalid, typ)
	}
	vis := in.Visibility
	if vis == "" {
		vis = store.VisibilityResource
		if typ == store.ThreadAsk {
			vis = store.VisibilityPrivate
		}
	}
	if vis != store.VisibilityResource && vis != store.VisibilityPrivate {
		return store.Thread{}, store.Message{}, fmt.Errorf("%w: unknown visibility %q", ErrInvalid, vis)
	}

	ok, err := s.checker(p).canGetRef(ctx, in.Ref)
	if err != nil {
		return store.Thread{}, store.Message{}, fmt.Errorf("threads: check access to target: %w", err)
	}
	if !ok {
		return store.Thread{}, store.Message{}, fleet.ErrNotFound
	}

	author := s.author(p, in.Author)
	now := s.now()
	t, m, err := s.st.Create(ctx,
		store.Thread{Ref: in.Ref, Type: typ, Visibility: vis, Title: title, CreatedBy: author, CreatedAt: now},
		store.Message{Author: author, Body: in.Body, Meta: in.Meta, CreatedAt: now})
	if err != nil {
		return store.Thread{}, store.Message{}, fmt.Errorf("threads: create: %w", err)
	}
	s.record(ctx, p, "thread.create", t.Ref, store.AuditOK, map[string]any{
		"threadId": t.ID, "type": t.Type, "visibility": t.Visibility, "authorType": author.Type,
	})
	s.changed(t)
	return t, m, nil
}

// Reply appends a message to a thread p may see. author is normalised as in
// CreateInput.Author.
func (s *Service) Reply(ctx context.Context, p identity.Principal, id, body string, author store.Author, meta json.RawMessage) (store.Message, error) {
	t, err := s.visible(ctx, p, id)
	if err != nil {
		return store.Message{}, err
	}
	if err := checkBody(p, body, meta); err != nil {
		return store.Message{}, err
	}
	if err := checkAuthorType(author.Type); err != nil {
		return store.Message{}, err
	}
	a := s.author(p, author)
	m, err := s.st.AddMessage(ctx, id, store.Message{Author: a, Body: body, Meta: meta, CreatedAt: s.now()})
	if errors.Is(err, store.ErrNotFound) {
		return store.Message{}, fleet.ErrNotFound
	}
	if err != nil {
		return store.Message{}, fmt.Errorf("threads: reply to %s: %w", id, err)
	}
	s.record(ctx, p, "thread.reply", t.Ref, store.AuditOK, map[string]any{
		"threadId": id, "messageId": m.ID, "authorType": a.Type,
	})
	if fresh, err := s.st.Get(ctx, id); err == nil {
		t = fresh
	} else {
		t.MessageCount++
		t.UpdatedAt = m.CreatedAt
	}
	s.changed(t)
	return m, nil
}

// Resolve marks a thread resolved. p must be its author or may patch its target.
func (s *Service) Resolve(ctx context.Context, p identity.Principal, id string) (store.Thread, error) {
	return s.setStatus(ctx, p, id, store.ThreadResolved, "thread.resolve")
}

// Reopen marks a resolved thread open again, under the same rule as Resolve.
func (s *Service) Reopen(ctx context.Context, p identity.Principal, id string) (store.Thread, error) {
	return s.setStatus(ctx, p, id, store.ThreadOpen, "thread.reopen")
}

func (s *Service) setStatus(ctx context.Context, p identity.Principal, id string, st store.ThreadStatus, action string) (store.Thread, error) {
	t, err := s.visible(ctx, p, id)
	if err != nil {
		return store.Thread{}, err
	}
	if t.Status == st {
		return t, nil
	}
	allowed := t.CreatedBy.Subject == p.User
	if !allowed && t.Ref.Kind != "" {
		ok, err := s.fl.CanPatch(ctx, p, t.Ref.Cluster, toModelRef(t.Ref))
		if err != nil && !isDenied(err) {
			return store.Thread{}, fmt.Errorf("threads: check patch access: %w", err)
		}
		allowed = ok && err == nil
	}
	if !allowed {
		s.record(ctx, p, action, t.Ref, store.AuditDenied, map[string]any{"threadId": id})
		return store.Thread{}, fleet.ErrForbidden
	}
	if err := s.st.SetStatus(ctx, id, st, p.User, s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Thread{}, fleet.ErrNotFound
		}
		return store.Thread{}, fmt.Errorf("threads: set status of %s: %w", id, err)
	}
	t, err = s.st.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Thread{}, fleet.ErrNotFound
	}
	if err != nil {
		return store.Thread{}, fmt.Errorf("threads: get %s: %w", id, err)
	}
	s.record(ctx, p, action, t.Ref, store.AuditOK, map[string]any{"threadId": id})
	s.changed(t)
	return t, nil
}

// Delete removes a thread and its messages. Only the author may delete.
func (s *Service) Delete(ctx context.Context, p identity.Principal, id string) error {
	t, err := s.visible(ctx, p, id)
	if err != nil {
		return err
	}
	if t.CreatedBy.Subject != p.User {
		s.record(ctx, p, "thread.delete", t.Ref, store.AuditDenied, map[string]any{"threadId": id})
		return fleet.ErrForbidden
	}
	if err := s.st.Delete(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fleet.ErrNotFound
		}
		return fmt.Errorf("threads: delete %s: %w", id, err)
	}
	s.record(ctx, p, "thread.delete", t.Ref, store.AuditOK, map[string]any{"threadId": id, "messages": t.MessageCount})
	s.changed(t)
	return nil
}

// visible loads a thread and checks that p may see it.
func (s *Service) visible(ctx context.Context, p identity.Principal, id string) (store.Thread, error) {
	if p.User == "" {
		return store.Thread{}, fleet.ErrNotFound
	}
	t, err := s.st.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Thread{}, fleet.ErrNotFound
	}
	if err != nil {
		return store.Thread{}, fmt.Errorf("threads: get %s: %w", id, err)
	}
	ok, err := s.checker(p).canSee(ctx, t)
	if err != nil {
		return store.Thread{}, fmt.Errorf("threads: check access to %s: %w", id, err)
	}
	if !ok {
		return store.Thread{}, fleet.ErrNotFound
	}
	return t, nil
}

// author normalises a caller-supplied author: the accountable subject and the
// channel always come from the principal.
func (s *Service) author(p identity.Principal, a store.Author) store.Author {
	def := AuthorFor(p, a.Type)
	if a.Display != "" {
		def.Display = a.Display
	}
	if a.Client != "" {
		def.Client = a.Client
	}
	return def
}

func checkAuthorType(t store.AuthorType) error {
	switch t {
	case "", store.AuthorHuman, store.AuthorAI, store.AuthorSystem:
		return nil
	default:
		return fmt.Errorf("%w: unknown author type %q", ErrInvalid, t)
	}
}

func checkBody(p identity.Principal, body string, meta json.RawMessage) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: body is required", ErrInvalid)
	}
	maxBytes := store.MaxMessageBytes
	if p.Via == identity.ViaMCP {
		maxBytes = MaxBodyBytesMCP
	}
	if len(body) > maxBytes {
		return fmt.Errorf("%w: body is larger than %d bytes", store.ErrLimit, maxBytes)
	}
	if len(meta) > 0 && !json.Valid(meta) {
		return fmt.Errorf("%w: meta is not valid JSON", ErrInvalid)
	}
	return nil
}

func (s *Service) record(ctx context.Context, p identity.Principal, action string, ref store.ResourceRef, res store.AuditResult, detail any) {
	if s.rec != nil {
		s.rec.Record(ctx, p, action, ref, res, detail)
	}
}

func (s *Service) changed(t store.Thread) {
	if s.notify != nil {
		s.notify(t)
	}
}

// checker decides visibility for one principal, caching fleet answers for
// the duration of one Service call.
type checker struct {
	fl       fleet.Service
	p        identity.Principal
	get      map[store.ResourceRef]bool
	clusters map[string]bool // nil until loaded
}

func (s *Service) checker(p identity.Principal) *checker {
	return &checker{fl: s.fl, p: p, get: map[store.ResourceRef]bool{}}
}

func (c *checker) canSee(ctx context.Context, t store.Thread) (bool, error) {
	if t.Visibility != store.VisibilityResource && t.CreatedBy.Subject != c.p.User {
		return false, nil
	}
	return c.canGetRef(ctx, t.Ref)
}

func (c *checker) canGetRef(ctx context.Context, ref store.ResourceRef) (bool, error) {
	if ok, cached := c.get[ref]; cached {
		return ok, nil
	}
	var ok bool
	if ref.Kind == "" {
		if c.clusters == nil {
			list, err := c.fl.Clusters(ctx, c.p)
			if err != nil {
				return false, fmt.Errorf("list clusters: %w", err)
			}
			c.clusters = make(map[string]bool, len(list))
			for _, ci := range list {
				c.clusters[ci.Name] = true
			}
		}
		ok = c.clusters[ref.Cluster]
	} else {
		var err error
		ok, err = c.fl.CanGet(ctx, c.p, ref.Cluster, toModelRef(ref))
		if err != nil {
			if !isDenied(err) {
				return false, err
			}
			ok = false
		}
	}
	c.get[ref] = ok
	return ok, nil
}

// isDenied reports fleet errors that simply mean "no".
func isDenied(err error) bool {
	return errors.Is(err, fleet.ErrNotFound) || errors.Is(err, fleet.ErrForbidden)
}

func toModelRef(r store.ResourceRef) model.Ref {
	return model.Ref{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}
