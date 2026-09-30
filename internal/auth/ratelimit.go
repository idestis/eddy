package auth

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/eddy-gitops/eddy/internal/config"
)

const (
	// maxLockout caps the exponential per-username lockout.
	maxLockout = 4 * time.Hour
	// maxLimiterEntries bounds each limiter map; the oldest entry is evicted.
	maxLimiterEntries = 10000
)

// loginLimiter tracks failed logins per username and requests per IP.
// Both maps are bounded so a spray of usernames or addresses cannot grow
// memory without limit.
type loginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	failures int
	window   time.Duration
	lockout  time.Duration
	perIP    int
	max      int
	users    map[string]*userLimit
	ips      map[string]*ipLimit
}

type userLimit struct {
	fails       []time.Time
	lockedUntil time.Time
	level       int // number of lockouts so far; doubles the next one
	last        time.Time
}

type ipLimit struct {
	lim  *rate.Limiter
	last time.Time
}

func newLoginLimiter(c config.RateLimit, now func() time.Time) *loginLimiter {
	l := &loginLimiter{
		now:      now,
		failures: c.PerUsernameFailures,
		window:   c.Window.Duration,
		lockout:  c.Lockout.Duration,
		perIP:    c.PerIPPerMinute,
		max:      maxLimiterEntries,
		users:    map[string]*userLimit{},
		ips:      map[string]*ipLimit{},
	}
	if l.failures <= 0 {
		l.failures = 5
	}
	if l.window <= 0 {
		l.window = 15 * time.Minute
	}
	if l.lockout <= 0 {
		l.lockout = 15 * time.Minute
	}
	if l.perIP <= 0 {
		l.perIP = 20
	}
	return l
}

// userKey normalises a username for limiting; unknown usernames are
// limited exactly like known ones.
func userKey(u string) string { return strings.ToLower(truncate(strings.TrimSpace(u), 64)) }

// ipKey groups IPv6 clients by /64 since one host usually owns the prefix.
func ipKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// allowIP consumes one request of the per-IP budget.
func (l *loginLimiter) allowIP(ip string) bool {
	now := l.now()
	k := ipKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.ips[k]
	if e == nil {
		if len(l.ips) >= l.max {
			l.gcLocked(now)
		}
		if len(l.ips) >= l.max {
			evictOldest(l.ips, func(e *ipLimit) (time.Time, bool) { return e.last, false })
		}
		e = &ipLimit{lim: rate.NewLimiter(rate.Limit(float64(l.perIP)/60), l.perIP)}
		l.ips[k] = e
	}
	e.last = now
	return e.lim.AllowN(now, 1)
}

// locked reports whether username is currently locked out.
func (l *loginLimiter) locked(username string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.users[userKey(username)]
	return e != nil && now.Before(e.lockedUntil)
}

// fail records a failed login and returns whether it triggered a lockout.
func (l *loginLimiter) fail(username string) bool {
	now := l.now()
	k := userKey(username)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.users[k]
	if e == nil {
		if len(l.users) >= l.max {
			l.gcLocked(now)
		}
		if len(l.users) >= l.max {
			// Prefer evicting entries that are not locked out, so a spray of
			// usernames cannot lift an active lockout.
			evictOldest(l.users, func(e *userLimit) (time.Time, bool) { return e.last, now.Before(e.lockedUntil) })
		}
		e = &userLimit{}
		l.users[k] = e
	}
	// Forget earlier lockouts after a quiet period of maxLockout since the
	// last lockout ended.
	if e.level > 0 && now.Sub(e.lockedUntil) > maxLockout {
		e.level = 0
	}
	e.last = now
	kept := e.fails[:0]
	for _, t := range e.fails {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	e.fails = append(kept, now)
	if len(e.fails) < l.failures {
		return false
	}
	d := l.lockout << e.level
	if d <= 0 || d > maxLockout {
		d = maxLockout
	}
	e.level++
	e.lockedUntil = now.Add(d)
	e.fails = nil
	return true
}

// success clears the username's failures.
func (l *loginLimiter) success(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.users, userKey(username))
}

func (l *loginLimiter) gc() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(now)
}

func (l *loginLimiter) gcLocked(now time.Time) {
	for k, e := range l.users {
		if now.Sub(e.last) > l.window && !now.Before(e.lockedUntil) && (e.level == 0 || now.Sub(e.lockedUntil) > maxLockout) {
			delete(l.users, k)
		}
	}
	for k, e := range l.ips {
		if now.Sub(e.last) > time.Minute {
			delete(l.ips, k)
		}
	}
}

// evictOldest deletes the least recently used entry, preferring entries
// that are not protected.
func evictOldest[V any](m map[string]V, info func(V) (last time.Time, protected bool)) {
	var (
		oldK      string
		oldT      time.Time
		oldProt   bool
		haveOldst bool
	)
	for k, v := range m {
		t, prot := info(v)
		better := !haveOldst || (oldProt && !prot) || (prot == oldProt && t.Before(oldT))
		if better {
			oldK, oldT, oldProt, haveOldst = k, t, prot, true
		}
	}
	if haveOldst {
		delete(m, oldK)
	}
}
