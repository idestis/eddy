package ai

import (
	"sync"
	"time"
)

// userLimiter enforces a sliding-window rate and a concurrency cap per key.
type userLimiter struct {
	mu         sync.Mutex
	window     time.Duration
	max        int
	concurrent int
	now        func() time.Time
	users      map[string]*userSlot
}

type userSlot struct {
	hits     []time.Time
	inFlight int
}

func newUserLimiter(max, concurrent int, window time.Duration, now func() time.Time) *userLimiter {
	return &userLimiter{window: window, max: max, concurrent: concurrent, now: now, users: map[string]*userSlot{}}
}

// acquire reserves one call for key. It returns a release func, or false if
// the rate or concurrency cap is reached.
func (l *userLimiter) acquire(key string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	u := l.users[key]
	if u == nil {
		u = &userSlot{}
		l.users[key] = u
	}
	cutoff := now.Add(-l.window)
	kept := u.hits[:0]
	for _, t := range u.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	u.hits = kept
	if l.concurrent > 0 && u.inFlight >= l.concurrent {
		return nil, false
	}
	if l.max > 0 && len(u.hits) >= l.max {
		return nil, false
	}
	u.hits = append(u.hits, now)
	u.inFlight++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			u.inFlight--
			if u.inFlight == 0 && len(u.hits) == 0 {
				delete(l.users, key)
			}
		})
	}, true
}

// sweep drops idle keys whose window has passed. Callers run it now and then.
func (l *userLimiter) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-l.window)
	for k, u := range l.users {
		if u.inFlight == 0 && (len(u.hits) == 0 || !u.hits[len(u.hits)-1].After(cutoff)) {
			delete(l.users, k)
		}
	}
}
