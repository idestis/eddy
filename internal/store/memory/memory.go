// Package memory is an in-process store.Store backed by maps and guarded by
// a single mutex. Everything is lost when the process exits, so it is meant
// for tests and `task dev:hub`, never for production.
//
// It implements exactly the semantics of the postgres backend; both are
// checked by the storetest conformance suite. Rate limits, agent sessions
// and events stay inside the process, which is correct for one replica only.
package memory

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/inproc"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// Store is the in-memory backend. The zero value is not usable; use New.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]store.Session // keyed by string(IDHash)
	tokens   map[string]store.Token   // keyed by ID
	threads  map[string]*threadRec
	msgIDs   map[string]string  // message ID → thread ID, for uniqueness
	audit    []store.AuditEvent // ascending by ID
	auditSeq int64
	prefs    map[string]json.RawMessage
	closed   bool

	limits *inproc.RateLimits
	agents *inproc.AgentSessions
	events inproc.Events
}

type threadRec struct {
	t    store.Thread
	msgs []store.Message // sorted by (CreatedAt, ID)
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{
		sessions: map[string]store.Session{},
		tokens:   map[string]store.Token{},
		threads:  map[string]*threadRec{},
		msgIDs:   map[string]string{},
		prefs:    map[string]json.RawMessage{},
		limits:   inproc.NewRateLimits(),
		agents:   inproc.NewAgentSessions(),
		events:   inproc.NewEvents(),
	}
}

func (s *Store) Sessions() store.Sessions { return sessions{s} }
func (s *Store) Tokens() store.Tokens     { return tokens{s} }
func (s *Store) Threads() store.Threads   { return threads{s} }
func (s *Store) Audit() store.Audit       { return audit{s} }
func (s *Store) Prefs() store.Prefs       { return prefs{s} }

func (s *Store) RateLimits() store.RateLimits       { return s.limits }
func (s *Store) AgentSessions() store.AgentSessions { return s.agents }
func (s *Store) Events() store.Events               { return s.events }

// Ping reports whether the store is open.
func (s *Store) Ping(context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errClosed
	}
	return nil
}

// Close marks the store closed and closes every event subscription. Data is
// kept so tests can still inspect it.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.events.Close()
	return nil
}

var errClosed = fmt.Errorf("memory store: closed")

// Prune applies the retention rules. A zero duration or day count disables
// the corresponding rule, except for sessions, which are always removed
// once expired.
func (s *Store) Prune(_ context.Context, now time.Time, r store.Retention) (store.PruneStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st store.PruneStats
	nowMs := storeutil.Ms(now)

	st.RateLimits = s.limits.Prune(now)
	st.AgentSessions = s.agents.Prune(now.Add(-store.AgentSessionPruneAfter))
	for k, v := range s.sessions {
		if storeutil.Ms(v.ExpiresAt) <= nowMs {
			delete(s.sessions, k)
			st.Sessions++
		}
	}
	if r.TokenPurgeAfter > 0 {
		cut := storeutil.Ms(now.Add(-r.TokenPurgeAfter))
		for k, v := range s.tokens {
			if storeutil.Ms(v.ExpiresAt) <= cut || (v.RevokedAt != nil && storeutil.Ms(*v.RevokedAt) <= cut) {
				delete(s.tokens, k)
				st.Tokens++
			}
		}
	}
	if r.AuditDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.AuditDays)))
		kept := s.audit[:0]
		for _, e := range s.audit {
			if storeutil.Ms(e.Time) < cut {
				st.Audit++
				continue
			}
			kept = append(kept, e)
		}
		clear(s.audit[len(kept):])
		s.audit = kept
	}
	for id, rec := range s.threads {
		t := rec.t
		if r.ResolvedThreadsDays > 0 && t.Status == store.ThreadResolved && t.ResolvedAt != nil &&
			storeutil.Ms(*t.ResolvedAt) <= storeutil.Ms(now.Add(-storeutil.Days(r.ResolvedThreadsDays))) {
			s.deleteThread(id)
			st.Threads++
			continue
		}
		if r.AskThreadsDays > 0 && t.Type == store.ThreadAsk &&
			storeutil.Ms(t.UpdatedAt) <= storeutil.Ms(now.Add(-storeutil.Days(r.AskThreadsDays))) {
			s.deleteThread(id)
			st.Threads++
		}
	}
	return st, nil
}

// deleteThread removes a thread and its messages. The caller holds s.mu.
func (s *Store) deleteThread(id string) {
	if rec, ok := s.threads[id]; ok {
		for _, m := range rec.msgs {
			delete(s.msgIDs, m.ID)
		}
		delete(s.threads, id)
	}
}

// ---- sessions ----

type sessions struct{ s *Store }

func copySession(v store.Session) store.Session {
	v.IDHash = storeutil.Bytes(v.IDHash)
	v.Groups = storeutil.Strings(v.Groups)
	return v
}

func (x sessions) Create(_ context.Context, v store.Session) error {
	v, err := storeutil.PrepareSession(v)
	if err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	k := string(v.IDHash)
	if _, ok := x.s.sessions[k]; ok {
		return store.ErrConflict
	}
	x.s.sessions[k] = v
	return nil
}

func (x sessions) Get(_ context.Context, idHash []byte, now time.Time) (store.Session, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	v, ok := x.s.sessions[string(idHash)]
	if !ok || storeutil.Ms(v.ExpiresAt) <= storeutil.Ms(now) {
		return store.Session{}, store.ErrNotFound
	}
	return copySession(v), nil
}

func (x sessions) Touch(_ context.Context, idHash []byte, seen, expires time.Time) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	k := string(idHash)
	v, ok := x.s.sessions[k]
	if !ok {
		return store.ErrNotFound
	}
	v.LastSeenAt = storeutil.Norm(seen)
	v.ExpiresAt = storeutil.Norm(expires)
	x.s.sessions[k] = v
	return nil
}

func (x sessions) Delete(_ context.Context, idHash []byte) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	delete(x.s.sessions, string(idHash))
	return nil
}

func (x sessions) DeleteBySubject(_ context.Context, subject string) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	for k, v := range x.s.sessions {
		if v.Subject == subject {
			delete(x.s.sessions, k)
		}
	}
	return nil
}

func (x sessions) DeleteOldestBySubject(_ context.Context, subject string, keep int) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	var mine []store.Session
	for _, v := range x.s.sessions {
		if v.Subject == subject {
			mine = append(mine, v)
		}
	}
	if len(mine) <= keep {
		return nil
	}
	slices.SortFunc(mine, func(a, b store.Session) int { return latestCmp(b, a) })
	for _, v := range mine[max(keep, 0):] {
		delete(x.s.sessions, string(v.IDHash))
	}
	return nil
}

func (x sessions) LatestGroups(_ context.Context, subject string) ([]string, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	var best *store.Session
	for _, v := range x.s.sessions {
		if v.Subject != subject {
			continue
		}
		if best == nil || latestCmp(v, *best) > 0 {
			best = &v
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return storeutil.Strings(best.Groups), nil
}

// latestCmp orders sessions by (LastSeenAt, CreatedAt, IDHash), matching the
// postgres ORDER BY.
func latestCmp(a, b store.Session) int {
	return cmp.Or(
		a.LastSeenAt.Compare(b.LastSeenAt),
		a.CreatedAt.Compare(b.CreatedAt),
		cmp.Compare(string(a.IDHash), string(b.IDHash)),
	)
}

// ---- tokens ----

type tokens struct{ s *Store }

func copyToken(t store.Token) store.Token {
	t.Hash = storeutil.Bytes(t.Hash)
	t.Groups = storeutil.Strings(t.Groups)
	t.Scopes = storeutil.Strings(t.Scopes)
	t.LastUsedAt = storeutil.NormPtr(t.LastUsedAt)
	t.RevokedAt = storeutil.NormPtr(t.RevokedAt)
	return t
}

func active(t store.Token, now time.Time) bool {
	return t.RevokedAt == nil && storeutil.Ms(t.ExpiresAt) > storeutil.Ms(now)
}

func (x tokens) Create(_ context.Context, t store.Token, maxActive int) error {
	t, err := storeutil.PrepareToken(t)
	if err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if maxActive > 0 {
		n := 0
		for _, o := range x.s.tokens {
			if o.Subject == t.Subject && active(o, t.CreatedAt) {
				n++
			}
		}
		if n >= maxActive {
			return fmt.Errorf("memory: create token: %w: %d active tokens", store.ErrLimit, n)
		}
	}
	if _, ok := x.s.tokens[t.ID]; ok {
		return store.ErrConflict
	}
	for _, o := range x.s.tokens {
		if string(o.Hash) == string(t.Hash) {
			return store.ErrConflict
		}
	}
	x.s.tokens[t.ID] = t
	return nil
}

func (x tokens) Get(_ context.Context, id string, now time.Time) (store.Token, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	t, ok := x.s.tokens[id]
	if !ok || !active(t, now) {
		return store.Token{}, store.ErrNotFound
	}
	return copyToken(t), nil
}

func (x tokens) List(_ context.Context, subject string) ([]store.Token, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	var out []store.Token
	for _, t := range x.s.tokens {
		if t.Subject != subject {
			continue
		}
		t = copyToken(t)
		t.Hash = nil
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b store.Token) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (x tokens) CountActive(_ context.Context, subject string, now time.Time) (int, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	n := 0
	for _, t := range x.s.tokens {
		if t.Subject == subject && active(t, now) {
			n++
		}
	}
	return n, nil
}

func (x tokens) Revoke(_ context.Context, subject, id string, at time.Time) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	t, ok := x.s.tokens[id]
	if !ok || t.Subject != subject {
		return store.ErrNotFound
	}
	if t.RevokedAt == nil {
		at = storeutil.Norm(at)
		t.RevokedAt = &at
		x.s.tokens[id] = t
	}
	return nil
}

func (x tokens) RevokeBySubject(_ context.Context, subject string, at time.Time) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	at = storeutil.Norm(at)
	for id, t := range x.s.tokens {
		if t.Subject == subject && t.RevokedAt == nil {
			t.RevokedAt = &at
			x.s.tokens[id] = t
		}
	}
	return nil
}

func (x tokens) MarkUsed(_ context.Context, id string, at time.Time) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	t, ok := x.s.tokens[id]
	if !ok {
		return store.ErrNotFound
	}
	at = storeutil.Norm(at)
	t.LastUsedAt = &at
	x.s.tokens[id] = t
	return nil
}

// ---- threads ----

type threads struct{ s *Store }

func copyThread(t store.Thread) store.Thread {
	t.ResolvedAt = storeutil.NormPtr(t.ResolvedAt)
	return t
}

func copyMessage(m store.Message) store.Message {
	m.Meta = storeutil.Raw(m.Meta)
	return m
}

func msgCmp(a, b store.Message) int {
	return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
}

func (x threads) Create(_ context.Context, t store.Thread, first store.Message) (store.Thread, store.Message, error) {
	t, first, err := storeutil.PrepareThread(t, first)
	if err != nil {
		return store.Thread{}, store.Message{}, err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if _, ok := x.s.threads[t.ID]; ok {
		return store.Thread{}, store.Message{}, store.ErrConflict
	}
	if _, ok := x.s.msgIDs[first.ID]; ok {
		return store.Thread{}, store.Message{}, store.ErrConflict
	}
	x.s.threads[t.ID] = &threadRec{t: t, msgs: []store.Message{first}}
	x.s.msgIDs[first.ID] = t.ID
	return copyThread(t), copyMessage(first), nil
}

func (x threads) Get(_ context.Context, id string) (store.Thread, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	rec, ok := x.s.threads[id]
	if !ok {
		return store.Thread{}, store.ErrNotFound
	}
	return copyThread(rec.t), nil
}

func (x threads) List(_ context.Context, f store.ThreadFilter) ([]store.Thread, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := storeutil.ClampLimit(f.Limit, storeutil.DefaultThreadLimit, storeutil.MaxThreadLimit)

	x.s.mu.RLock()
	var all []store.Thread
	for _, rec := range x.s.threads {
		t := rec.t
		if !storeutil.RefMatches(f.Ref, t.Ref) ||
			(f.Status != "" && t.Status != f.Status) ||
			(f.Type != "" && t.Type != f.Type) {
			continue
		}
		if t.Visibility == store.VisibilityPrivate && (f.Viewer == "" || t.CreatedBy.Subject != f.Viewer) {
			continue
		}
		if hasCur {
			ms := storeutil.Ms(t.UpdatedAt)
			if ms > cur.Ms || (ms == cur.Ms && t.ID >= cur.ID) {
				continue
			}
		}
		all = append(all, copyThread(t))
	}
	x.s.mu.RUnlock()

	slices.SortFunc(all, func(a, b store.Thread) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(b.ID, a.ID))
	})
	if len(all) <= limit {
		return all, "", nil
	}
	page := all[:limit]
	last := page[len(page)-1]
	return page, storeutil.EncodeCursor(storeutil.Ms(last.UpdatedAt), last.ID), nil
}

func (x threads) AddMessage(_ context.Context, threadID string, m store.Message) (store.Message, error) {
	m, err := storeutil.PrepareMessage(threadID, m)
	if err != nil {
		return store.Message{}, err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	rec, ok := x.s.threads[threadID]
	if !ok {
		return store.Message{}, store.ErrNotFound
	}
	if rec.t.MessageCount >= store.MaxMessagesPerThread {
		return store.Message{}, fmt.Errorf("%w: thread has %d messages", store.ErrLimit, store.MaxMessagesPerThread)
	}
	if _, ok := x.s.msgIDs[m.ID]; ok {
		return store.Message{}, store.ErrConflict
	}
	i, _ := slices.BinarySearchFunc(rec.msgs, m, msgCmp)
	rec.msgs = slices.Insert(rec.msgs, i, m)
	x.s.msgIDs[m.ID] = threadID
	rec.t.MessageCount++
	rec.t.UpdatedAt = m.CreatedAt
	return copyMessage(m), nil
}

func (x threads) Messages(_ context.Context, threadID, cursor string, limit int) ([]store.Message, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = storeutil.ClampLimit(limit, storeutil.DefaultMessageLimit, storeutil.MaxMessageLimit)

	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	rec, ok := x.s.threads[threadID]
	if !ok {
		return nil, "", store.ErrNotFound
	}
	start := 0
	if hasCur {
		start = len(rec.msgs)
		for i, m := range rec.msgs {
			ms := storeutil.Ms(m.CreatedAt)
			if ms > cur.Ms || (ms == cur.Ms && m.ID > cur.ID) {
				start = i
				break
			}
		}
	}
	rest := rec.msgs[start:]
	n := min(limit, len(rest))
	out := make([]store.Message, n)
	for i := range n {
		out[i] = copyMessage(rest[i])
	}
	if len(rest) <= limit {
		return out, "", nil
	}
	last := out[n-1]
	return out, storeutil.EncodeCursor(storeutil.Ms(last.CreatedAt), last.ID), nil
}

func (x threads) SetStatus(_ context.Context, id string, st store.ThreadStatus, by string, at time.Time) error {
	if err := storeutil.CheckStatus(st); err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	rec, ok := x.s.threads[id]
	if !ok {
		return store.ErrNotFound
	}
	at = storeutil.NowIfZero(at)
	rec.t.Status = st
	rec.t.UpdatedAt = at
	if st == store.ThreadResolved {
		rec.t.ResolvedBy = by
		rec.t.ResolvedAt = &at
	} else {
		rec.t.ResolvedBy = ""
		rec.t.ResolvedAt = nil
	}
	return nil
}

func (x threads) Delete(_ context.Context, id string) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if _, ok := x.s.threads[id]; !ok {
		return store.ErrNotFound
	}
	x.s.deleteThread(id)
	return nil
}

// ---- audit ----

type audit struct{ s *Store }

func copyEvent(e store.AuditEvent) store.AuditEvent {
	e.Groups = storeutil.Strings(e.Groups)
	e.Detail = storeutil.Raw(e.Detail)
	return e
}

func (x audit) Append(_ context.Context, e store.AuditEvent) error {
	e, err := storeutil.PrepareAudit(e)
	if err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	x.s.auditSeq++
	e.ID = x.s.auditSeq
	x.s.audit = append(x.s.audit, e)
	return nil
}

func (x audit) Query(_ context.Context, f store.AuditFilter) ([]store.AuditEvent, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	var curID int64
	if hasCur {
		if curID, err = strconv.ParseInt(cur.ID, 10, 64); err != nil {
			return nil, "", fmt.Errorf("%w: %v", storeutil.ErrInvalidCursor, err)
		}
	}
	limit := storeutil.ClampLimit(f.Limit, storeutil.DefaultAuditLimit, storeutil.MaxAuditLimit)
	since := int64(0)
	if !f.Since.IsZero() {
		since = storeutil.Ms(f.Since)
	}

	x.s.mu.RLock()
	var all []store.AuditEvent
	for _, e := range x.s.audit {
		ms := storeutil.Ms(e.Time)
		if (f.Subject != "" && e.Subject != f.Subject) || !storeutil.RefMatches(f.Target, e.Target) ||
			(!f.Since.IsZero() && ms < since) {
			continue
		}
		if hasCur && (ms > cur.Ms || (ms == cur.Ms && e.ID >= curID)) {
			continue
		}
		all = append(all, copyEvent(e))
	}
	x.s.mu.RUnlock()

	slices.SortFunc(all, func(a, b store.AuditEvent) int {
		return cmp.Or(b.Time.Compare(a.Time), cmp.Compare(b.ID, a.ID))
	})
	if len(all) <= limit {
		return all, "", nil
	}
	page := all[:limit]
	last := page[len(page)-1]
	return page, storeutil.EncodeCursor(storeutil.Ms(last.Time), strconv.FormatInt(last.ID, 10)), nil
}

// ---- prefs ----

type prefs struct{ s *Store }

func (x prefs) Get(_ context.Context, subject string) (json.RawMessage, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	v, ok := x.s.prefs[subject]
	if !ok {
		return nil, store.ErrNotFound
	}
	return storeutil.Raw(v), nil
}

func (x prefs) Put(_ context.Context, subject string, data json.RawMessage) error {
	if err := storeutil.CheckPrefs(data); err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	x.s.prefs[subject] = storeutil.Raw(data)
	return nil
}
