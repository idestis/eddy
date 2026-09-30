package mcp

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/store"
)

// limiter enforces a sliding-window rate and an optional concurrency cap
// per key (a token id or a user).
type limiter struct {
	mu         sync.Mutex
	window     time.Duration
	max        int
	concurrent int
	now        func() time.Time
	keys       map[string]*slot
	calls      int
}

type slot struct {
	hits     []time.Time
	inFlight int
}

func newLimiter(max, concurrent int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{window: window, max: max, concurrent: concurrent, now: now, keys: map[string]*slot{}}
}

// allow records one event for key if the rate allows it. It does not touch
// the concurrency count.
func (l *limiter) allow(key string) bool {
	rel, ok := l.take(key, false)
	if ok {
		rel()
	}
	return ok
}

// enter reserves a concurrency slot for key without counting toward the
// rate. The returned func releases it.
func (l *limiter) enter(key string) (func(), bool) { return l.take(key, true) }

func (l *limiter) take(key string, concurrency bool) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.calls++
	if l.calls%1024 == 0 {
		l.sweepLocked(now)
	}
	s := l.keys[key]
	if s == nil {
		s = &slot{}
		l.keys[key] = s
	}
	if concurrency {
		if l.concurrent > 0 && s.inFlight >= l.concurrent {
			return nil, false
		}
		s.inFlight++
		var once sync.Once
		return func() {
			once.Do(func() {
				l.mu.Lock()
				defer l.mu.Unlock()
				s.inFlight--
			})
		}, true
	}
	cutoff := now.Add(-l.window)
	kept := s.hits[:0]
	for _, t := range s.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.hits = kept
	if l.max > 0 && len(s.hits) >= l.max {
		return nil, false
	}
	s.hits = append(s.hits, now)
	return func() {}, true
}

func (l *limiter) sweepLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	for k, s := range l.keys {
		if s.inFlight == 0 && (len(s.hits) == 0 || !s.hits[len(s.hits)-1].After(cutoff)) {
			delete(l.keys, k)
		}
	}
}

// errLimitUnavailable means a fail-closed limit could not be checked because
// the store is unreachable.
var errLimitUnavailable = errors.New("mcp: rate limit unavailable")

// rate is a per-key rate limit shared by every hub replica through
// store.RateLimits (fixed windows keyed "<prefix><key>"). Without a store
// (tests) it uses the local limiter. When the store fails, a fail-open
// limit falls back to the local limiter, so reads keep working, and a
// fail-closed one (writes) refuses with errLimitUnavailable.
type rate struct {
	prefix     string
	max        int
	window     time.Duration
	rl         store.RateLimits
	local      *limiter
	failClosed bool
	now        func() time.Time
	log        *slog.Logger
}

func newRate(prefix string, max int, window time.Duration, rl store.RateLimits, failClosed bool, now func() time.Time, log *slog.Logger) *rate {
	return &rate{prefix: prefix, max: max, window: window, rl: rl, local: newLimiter(max, 0, window, now),
		failClosed: failClosed, now: now, log: log}
}

// allow counts one event for key. It returns false when the limit is used
// up, and errLimitUnavailable when a fail-closed limit cannot be checked.
func (r *rate) allow(ctx context.Context, key string) (bool, error) {
	if r.rl == nil {
		return r.local.allow(key), nil
	}
	_, ok, err := r.rl.Hit(ctx, r.prefix+key, r.window, r.max, r.now())
	if err == nil {
		return ok, nil
	}
	if r.failClosed {
		r.log.Error("rate limit unavailable; refusing", "limit", r.prefix, "err", err)
		return false, errLimitUnavailable
	}
	r.log.Warn("rate limit unavailable; using this replica's limiter", "limit", r.prefix, "err", err)
	return r.local.allow(key), nil
}
