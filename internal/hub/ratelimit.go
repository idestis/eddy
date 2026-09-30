package hub

import (
	"sync"
	"time"
)

// windowLimiter counts events per key in fixed windows. It backs the
// failed-agent-auth limit (10 a minute per IP).
type windowLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	keys   map[string]*windowCount
}

type windowCount struct {
	start time.Time
	n     int
}

// maxLimiterKeys bounds memory under a flood of distinct keys.
const maxLimiterKeys = 10000

func newWindowLimiter(max int, window time.Duration) *windowLimiter {
	return &windowLimiter{max: max, window: window, now: time.Now, keys: map[string]*windowCount{}}
}

// blocked reports whether key already used up its window.
func (l *windowLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.keys[key]
	return ok && l.now().Sub(c.start) < l.window && c.n >= l.max
}

// add records one event for key.
func (l *windowLimiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	c, ok := l.keys[key]
	if !ok || now.Sub(c.start) >= l.window {
		if len(l.keys) >= maxLimiterKeys {
			for k, v := range l.keys {
				if now.Sub(v.start) >= l.window {
					delete(l.keys, k)
				}
			}
			if len(l.keys) >= maxLimiterKeys {
				clear(l.keys)
			}
		}
		l.keys[key] = &windowCount{start: now, n: 1}
		return
	}
	c.n++
}

// concurrencyLimiter caps concurrent operations per key (per-user SSE
// connections and log streams).
type concurrencyLimiter struct {
	mu  sync.Mutex
	max int
	n   map[string]int
}

func newConcurrencyLimiter(max int) *concurrencyLimiter {
	return &concurrencyLimiter{max: max, n: map[string]int{}}
}

// acquire returns a release function, or false when key is at the cap.
func (l *concurrencyLimiter) acquire(key string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[key] >= l.max {
		return nil, false
	}
	l.n[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.n[key]--; l.n[key] <= 0 {
				delete(l.n, key)
			}
		})
	}, true
}
