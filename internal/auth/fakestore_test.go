package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
)

// fakeStore implements store.Store with in-memory sessions and tokens only.
type fakeStore struct {
	sess   *fakeSessions
	tok    *fakeTokens
	limits store.RateLimits
	events store.Events
}

func newFakeStore() *fakeStore {
	m := memory.New()
	return &fakeStore{sess: &fakeSessions{m: map[string]store.Session{}}, tok: &fakeTokens{m: map[string]store.Token{}},
		limits: m.RateLimits(), events: m.Events()}
}

func (f *fakeStore) Sessions() store.Sessions           { return f.sess }
func (f *fakeStore) Tokens() store.Tokens               { return f.tok }
func (f *fakeStore) Threads() store.Threads             { return nil }
func (f *fakeStore) Audit() store.Audit                 { return nil }
func (f *fakeStore) Prefs() store.Prefs                 { return nil }
func (f *fakeStore) RateLimits() store.RateLimits       { return f.limits }
func (f *fakeStore) AgentSessions() store.AgentSessions { return nil }
func (f *fakeStore) Events() store.Events               { return f.events }
func (f *fakeStore) JoinTokens() store.JoinTokens       { return nil }
func (f *fakeStore) ConnectionAttempts() store.ConnectionAttempts {
	return nil
}
func (f *fakeStore) Prune(context.Context, time.Time, store.Retention) (store.PruneStats, error) {
	return store.PruneStats{}, nil
}
func (f *fakeStore) Ping(context.Context) error { return nil }
func (f *fakeStore) Close() error               { return nil }

type fakeSessions struct {
	mu      sync.Mutex
	m       map[string]store.Session
	touches int
	gets    int
	down    bool // Get fails as if the database were unreachable
}

func (f *fakeSessions) Create(_ context.Context, s store.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := hex.EncodeToString(s.IDHash)
	if _, ok := f.m[k]; ok {
		return store.ErrConflict
	}
	s.Groups = slices.Clone(s.Groups)
	f.m[k] = s
	return nil
}

func (f *fakeSessions) Get(_ context.Context, h []byte, now time.Time) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.down {
		return store.Session{}, errors.New("fake store: connection refused")
	}
	s, ok := f.m[hex.EncodeToString(h)]
	if !ok || !now.Before(s.ExpiresAt) {
		return store.Session{}, store.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessions) Touch(_ context.Context, h []byte, seen, exp time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := hex.EncodeToString(h)
	s, ok := f.m[k]
	if !ok {
		return store.ErrNotFound
	}
	s.LastSeenAt, s.ExpiresAt = seen, exp
	f.m[k] = s
	f.touches++
	return nil
}

func (f *fakeSessions) Delete(_ context.Context, h []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, hex.EncodeToString(h))
	return nil
}

func (f *fakeSessions) DeleteBySubject(_ context.Context, subject string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.m {
		if s.Subject == subject {
			delete(f.m, k)
		}
	}
	return nil
}

func (f *fakeSessions) LatestGroups(_ context.Context, subject string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *store.Session
	for _, s := range f.m {
		if s.Subject == subject && (best == nil || s.LastSeenAt.After(best.LastSeenAt)) {
			s := s
			best = &s
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return slices.Clone(best.Groups), nil
}

func (f *fakeSessions) DeleteOldestBySubject(_ context.Context, subject string, keep int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	type kv struct {
		k string
		t time.Time
	}
	var mine []kv
	for k, s := range f.m {
		if s.Subject == subject {
			mine = append(mine, kv{k, s.CreatedAt})
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].t.After(mine[j].t) })
	for i := keep; i < len(mine); i++ {
		delete(f.m, mine[i].k)
	}
	return nil
}

func (f *fakeSessions) count(subject string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.m {
		if s.Subject == subject {
			n++
		}
	}
	return n
}

type fakeTokens struct {
	mu    sync.Mutex
	m     map[string]store.Token
	marks int
}

func (f *fakeTokens) Create(_ context.Context, t store.Token, maxActive int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[t.ID]; ok {
		return store.ErrConflict
	}
	if maxActive > 0 {
		n := 0
		for _, o := range f.m {
			if o.Subject == t.Subject && o.RevokedAt == nil && t.CreatedAt.Before(o.ExpiresAt) {
				n++
			}
		}
		if n >= maxActive {
			return store.ErrLimit
		}
	}
	f.m[t.ID] = t
	return nil
}

func (f *fakeTokens) Get(_ context.Context, id string, now time.Time) (store.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.m[id]
	if !ok || t.RevokedAt != nil || !now.Before(t.ExpiresAt) {
		return store.Token{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeTokens) List(_ context.Context, subject string) ([]store.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Token
	for _, t := range f.m {
		if t.Subject == subject {
			t.Hash = nil
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeTokens) CountActive(_ context.Context, subject string, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.m {
		if t.Subject == subject && t.RevokedAt == nil && now.Before(t.ExpiresAt) {
			n++
		}
	}
	return n, nil
}

func (f *fakeTokens) Revoke(_ context.Context, subject, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.m[id]
	if !ok || t.Subject != subject || t.RevokedAt != nil {
		return store.ErrNotFound
	}
	t.RevokedAt = &at
	f.m[id] = t
	return nil
}

func (f *fakeTokens) RevokeBySubject(_ context.Context, subject string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, t := range f.m {
		if t.Subject == subject && t.RevokedAt == nil {
			t.RevokedAt = &at
			f.m[id] = t
		}
	}
	return nil
}

func (f *fakeTokens) MarkUsed(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.m[id]
	if !ok {
		return store.ErrNotFound
	}
	t.LastUsedAt = &at
	f.m[id] = t
	f.marks++
	return nil
}
