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
	msgIDs   map[string]string // message ID → thread ID, for uniqueness
	chats    map[string]*chatRec
	chatMsgs map[string]string  // chat message ID → chat ID, for uniqueness
	audit    []store.AuditEvent // ascending by ID
	auditSeq int64
	prefs    map[string]json.RawMessage
	joins    map[string]store.JoinToken           // keyed by ID
	attempts map[string][]store.ConnectionAttempt // keyed by cluster, oldest first
	closed   bool

	limits *inproc.RateLimits
	agents *inproc.AgentSessions
	events inproc.Events
}

type threadRec struct {
	t    store.Thread
	msgs []store.Message // sorted by (CreatedAt, ID)
}

type chatRec struct {
	c    store.Chat
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
		chats:    map[string]*chatRec{},
		chatMsgs: map[string]string{},
		prefs:    map[string]json.RawMessage{},
		joins:    map[string]store.JoinToken{},
		attempts: map[string][]store.ConnectionAttempt{},
		limits:   inproc.NewRateLimits(),
		agents:   inproc.NewAgentSessions(),
		events:   inproc.NewEvents(),
	}
}

func (s *Store) Sessions() store.Sessions { return sessions{s} }
func (s *Store) Tokens() store.Tokens     { return tokens{s} }
func (s *Store) Threads() store.Threads   { return threads{s} }
func (s *Store) Chats() store.Chats       { return chats{s} }
func (s *Store) Audit() store.Audit       { return audit{s} }
func (s *Store) Prefs() store.Prefs       { return prefs{s} }

func (s *Store) RateLimits() store.RateLimits       { return s.limits }
func (s *Store) AgentSessions() store.AgentSessions { return s.agents }
func (s *Store) Events() store.Events               { return s.events }

func (s *Store) JoinTokens() store.JoinTokens                 { return joinTokens{s} }
func (s *Store) ConnectionAttempts() store.ConnectionAttempts { return attempts{s} }

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
	joinCut := storeutil.Ms(now.Add(-store.JoinTokenPruneAfter))
	for id, t := range s.joins {
		end := storeutil.Ms(t.ExpiresAt)
		if t.UsedAt != nil {
			end = min(end, storeutil.Ms(*t.UsedAt))
		}
		if t.RevokedAt != nil {
			end = min(end, storeutil.Ms(*t.RevokedAt))
		}
		if end <= joinCut {
			delete(s.joins, id)
			st.JoinTokens++
		}
	}
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
		}
	}
	if r.ChatDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.ChatDays)))
		for id, rec := range s.chats {
			if storeutil.Ms(rec.c.UpdatedAt) <= cut {
				s.deleteChat(id)
				st.Chats++
			}
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

// deleteChat removes a chat and its messages. The caller holds s.mu.
func (s *Store) deleteChat(id string) {
	if rec, ok := s.chats[id]; ok {
		for _, m := range rec.msgs {
			delete(s.chatMsgs, m.ID)
		}
		delete(s.chats, id)
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
	return pageMessages(rec.msgs, cur, hasCur, limit)
}

// pageMessages returns the page of msgs (sorted by (CreatedAt, ID)) after
// cur, copied, and the cursor of the next page. The caller holds s.mu.
func pageMessages(msgs []store.Message, cur storeutil.Cursor, hasCur bool, limit int) ([]store.Message, string, error) {
	start := 0
	if hasCur {
		start = len(msgs)
		for i, m := range msgs {
			ms := storeutil.Ms(m.CreatedAt)
			if ms > cur.Ms || (ms == cur.Ms && m.ID > cur.ID) {
				start = i
				break
			}
		}
	}
	rest := msgs[start:]
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

// ---- chats ----

type chats struct{ s *Store }

func copyChat(c store.Chat) store.Chat {
	c.Context = storeutil.ChatContext(c.Context)
	return c
}

// chatCmp orders chats by (UpdatedAt desc, ID desc), the List order.
func chatCmp(a, b store.Chat) int {
	return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(b.ID, a.ID))
}

// owned returns the owner's chat, or nil. The caller holds s.mu.
func (x chats) owned(owner, id string) *chatRec {
	rec, ok := x.s.chats[id]
	if !ok || owner == "" || rec.c.Owner != owner {
		return nil
	}
	return rec
}

func (x chats) Create(_ context.Context, c store.Chat) (store.Chat, error) {
	c, err := storeutil.PrepareChat(c)
	if err != nil {
		return store.Chat{}, err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if _, ok := x.s.chats[c.ID]; ok {
		return store.Chat{}, store.ErrConflict
	}
	var mine []store.Chat
	for _, rec := range x.s.chats {
		if rec.c.Owner == c.Owner {
			mine = append(mine, rec.c)
		}
	}
	if len(mine) >= store.MaxChatsPerOwner {
		// Drop the least recently updated chats: the end of the List order.
		slices.SortFunc(mine, chatCmp)
		for _, old := range mine[store.MaxChatsPerOwner-1:] {
			x.s.deleteChat(old.ID)
		}
	}
	x.s.chats[c.ID] = &chatRec{c: c}
	return copyChat(c), nil
}

func (x chats) Get(_ context.Context, owner, id string) (store.Chat, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	rec := x.owned(owner, id)
	if rec == nil {
		return store.Chat{}, store.ErrNotFound
	}
	return copyChat(rec.c), nil
}

func (x chats) List(_ context.Context, owner, cursor string, limit int) ([]store.Chat, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = storeutil.ClampLimit(limit, storeutil.DefaultThreadLimit, storeutil.MaxThreadLimit)

	x.s.mu.RLock()
	var all []store.Chat
	for _, rec := range x.s.chats {
		c := rec.c
		if owner == "" || c.Owner != owner {
			continue
		}
		if hasCur {
			ms := storeutil.Ms(c.UpdatedAt)
			if ms > cur.Ms || (ms == cur.Ms && c.ID >= cur.ID) {
				continue
			}
		}
		all = append(all, copyChat(c))
	}
	x.s.mu.RUnlock()

	slices.SortFunc(all, chatCmp)
	if len(all) <= limit {
		return all, "", nil
	}
	page := all[:limit]
	last := page[len(page)-1]
	return page, storeutil.EncodeCursor(storeutil.Ms(last.UpdatedAt), last.ID), nil
}

func (x chats) Update(_ context.Context, owner, id string, u store.ChatUpdate) (store.Chat, error) {
	if u.Title != nil {
		if err := storeutil.CheckChatTitle(*u.Title); err != nil {
			return store.Chat{}, err
		}
	}
	if u.Context != nil {
		if err := storeutil.CheckChatContext(*u.Context); err != nil {
			return store.Chat{}, err
		}
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	rec := x.owned(owner, id)
	if rec == nil {
		return store.Chat{}, store.ErrNotFound
	}
	if u.Title != nil {
		rec.c.Title = *u.Title
	}
	if u.Context != nil {
		rec.c.Context = storeutil.ChatContext(*u.Context)
	}
	return copyChat(rec.c), nil
}

func (x chats) AddMessage(_ context.Context, owner, chatID string, m store.Message) (store.Message, error) {
	m, err := storeutil.PrepareMessage(chatID, m)
	if err != nil {
		return store.Message{}, err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	rec := x.owned(owner, chatID)
	if rec == nil {
		return store.Message{}, store.ErrNotFound
	}
	if rec.c.MessageCount >= store.MaxMessagesPerChat {
		return store.Message{}, fmt.Errorf("%w: chat has %d messages", store.ErrConflict, store.MaxMessagesPerChat)
	}
	if _, ok := x.s.chatMsgs[m.ID]; ok {
		return store.Message{}, store.ErrConflict
	}
	i, _ := slices.BinarySearchFunc(rec.msgs, m, msgCmp)
	rec.msgs = slices.Insert(rec.msgs, i, m)
	x.s.chatMsgs[m.ID] = chatID
	rec.c.MessageCount++
	rec.c.UpdatedAt = m.CreatedAt
	return copyMessage(m), nil
}

func (x chats) Messages(_ context.Context, owner, chatID, cursor string, limit int) ([]store.Message, string, error) {
	cur, hasCur, err := storeutil.DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = storeutil.ClampLimit(limit, storeutil.DefaultMessageLimit, storeutil.MaxMessageLimit)

	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	rec := x.owned(owner, chatID)
	if rec == nil {
		return nil, "", store.ErrNotFound
	}
	return pageMessages(rec.msgs, cur, hasCur, limit)
}

func (x chats) Delete(_ context.Context, owner, id string) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if x.owned(owner, id) == nil {
		return store.ErrNotFound
	}
	x.s.deleteChat(id)
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

// ---- join tokens ----

type joinTokens struct{ s *Store }

func copyJoin(t store.JoinToken) store.JoinToken {
	t.Hash = storeutil.Bytes(t.Hash)
	t.UsedAt = storeutil.NormPtr(t.UsedAt)
	t.RevokedAt = storeutil.NormPtr(t.RevokedAt)
	return t
}

func (x joinTokens) Create(_ context.Context, t store.JoinToken) error {
	t, err := storeutil.PrepareJoinToken(t)
	if err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if x.s.closed {
		return errClosed
	}
	if _, ok := x.s.joins[t.ID]; ok {
		return store.ErrConflict
	}
	for _, o := range x.s.joins {
		if string(o.Hash) == string(t.Hash) {
			return store.ErrConflict
		}
	}
	for id, o := range x.s.joins {
		if o.Cluster == t.Cluster && o.UsedAt == nil && o.RevokedAt == nil {
			at := t.CreatedAt
			o.RevokedAt = &at
			x.s.joins[id] = o
		}
	}
	x.s.joins[t.ID] = copyJoin(t)
	return nil
}

func (x joinTokens) Get(_ context.Context, id string) (store.JoinToken, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	t, ok := x.s.joins[id]
	if !ok {
		return store.JoinToken{}, store.ErrNotFound
	}
	return copyJoin(t), nil
}

func (x joinTokens) Consume(_ context.Context, id string, now time.Time) (store.JoinToken, error) {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	t, ok := x.s.joins[id]
	if !ok || t.UsedAt != nil || t.RevokedAt != nil || storeutil.Ms(t.ExpiresAt) <= storeutil.Ms(now) {
		return store.JoinToken{}, store.ErrNotFound
	}
	at := storeutil.Norm(now)
	t.UsedAt = &at
	x.s.joins[id] = t
	return copyJoin(t), nil
}

func (x joinTokens) List(_ context.Context, cluster string) ([]store.JoinToken, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	var out []store.JoinToken
	for _, t := range x.s.joins {
		if t.Cluster == cluster {
			t = copyJoin(t)
			t.Hash = nil
			out = append(out, t)
		}
	}
	// Newest first. Create revokes every live predecessor, so of two tokens
	// created in the same millisecond the unrevoked one is the newer.
	revoked := func(t store.JoinToken) int {
		if t.RevokedAt != nil {
			return 1
		}
		return 0
	}
	slices.SortFunc(out, func(a, b store.JoinToken) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(revoked(a), revoked(b)), cmp.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (x joinTokens) RevokeByCluster(_ context.Context, cluster string, at time.Time) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	at = storeutil.Norm(at)
	for id, t := range x.s.joins {
		if t.Cluster == cluster && t.UsedAt == nil && t.RevokedAt == nil {
			a := at
			t.RevokedAt = &a
			x.s.joins[id] = t
		}
	}
	return nil
}

// ---- connection attempts ----

type attempts struct{ s *Store }

func (x attempts) Record(_ context.Context, a store.ConnectionAttempt) error {
	a, err := storeutil.PrepareAttempt(a)
	if err != nil {
		return err
	}
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	if x.s.closed {
		return errClosed
	}
	list := append(x.s.attempts[a.Cluster], a)
	slices.SortStableFunc(list, func(p, q store.ConnectionAttempt) int { return p.At.Compare(q.At) })
	if n := len(list) - store.MaxAttemptsPerCluster; n > 0 {
		list = slices.Clone(list[n:])
	}
	x.s.attempts[a.Cluster] = list
	return nil
}

func (x attempts) List(_ context.Context, cluster string) ([]store.ConnectionAttempt, error) {
	x.s.mu.RLock()
	defer x.s.mu.RUnlock()
	list := x.s.attempts[cluster]
	out := make([]store.ConnectionAttempt, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out, nil
}

func (x attempts) DeleteByCluster(_ context.Context, cluster string) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	delete(x.s.attempts, cluster)
	return nil
}
