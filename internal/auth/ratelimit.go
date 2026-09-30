package auth

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/store"
)

const (
	// maxLockout caps the exponential per-username lockout.
	maxLockout = 4 * time.Hour
	// lockoutLevelWindow is how long earlier lockouts of a username count
	// towards doubling the next one, measured from the first of them.
	lockoutLevelWindow = 24 * time.Hour
)

// loginLimiter tracks failed logins per username and requests per IP in
// store.RateLimits, so the limits hold across every hub replica:
//
//	login:ip:<ip or /64>     requests per minute from one address
//	login:fail:<username>    failures in the current window
//	login:lock:<username>    an open window here is an active lockout
//	login:level:<username>   lockouts so far (doubles the next one)
//
// Counters are throwaway state: losing them (a PostgreSQL crash truncates
// the UNLOGGED table) only resets the limits.
type loginLimiter struct {
	rl       store.RateLimits
	now      func() time.Time
	failures int
	window   time.Duration
	lockout  time.Duration
	perIP    int
}

func newLoginLimiter(rl store.RateLimits, c config.RateLimit, now func() time.Time) *loginLimiter {
	l := &loginLimiter{
		rl:       rl,
		now:      now,
		failures: c.PerUsernameFailures,
		window:   c.Window.Duration,
		lockout:  c.Lockout.Duration,
		perIP:    c.PerIPPerMinute,
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
func (l *loginLimiter) allowIP(ctx context.Context, ip string) (bool, error) {
	_, ok, err := l.rl.Hit(ctx, "login:ip:"+ipKey(ip), time.Minute, l.perIP, l.now())
	if err != nil {
		return false, fmt.Errorf("auth: login rate limit: %w", err)
	}
	return ok, nil
}

// locked reports whether username is currently locked out.
func (l *loginLimiter) locked(ctx context.Context, username string) (bool, error) {
	n, _, err := l.rl.Get(ctx, "login:lock:"+userKey(username), l.now())
	if err != nil {
		return false, fmt.Errorf("auth: login lockout: %w", err)
	}
	return n > 0, nil
}

// fail records a failed login and returns whether it triggered a lockout.
// The lockout doubles with every earlier lockout of the username within
// lockoutLevelWindow, up to maxLockout.
func (l *loginLimiter) fail(ctx context.Context, username string) (bool, error) {
	k := userKey(username)
	now := l.now()
	n, _, err := l.rl.Hit(ctx, "login:fail:"+k, l.window, 0, now)
	if err != nil {
		return false, fmt.Errorf("auth: record login failure: %w", err)
	}
	if n < l.failures {
		return false, nil
	}
	level, _, err := l.rl.Get(ctx, "login:level:"+k, now)
	if err != nil {
		return false, fmt.Errorf("auth: login lockout level: %w", err)
	}
	d := maxLockout
	if level < 16 {
		if v := l.lockout << level; v > 0 && v < maxLockout {
			d = v
		}
	}
	// A lockout window starts with its first hit, so reset first: the
	// lockout then lasts exactly d from now.
	for _, step := range []func() error{
		func() error { return l.rl.Reset(ctx, "login:lock:"+k) },
		func() error { _, _, err := l.rl.Hit(ctx, "login:lock:"+k, d, 0, now); return err },
		func() error { _, _, err := l.rl.Hit(ctx, "login:level:"+k, lockoutLevelWindow, 0, now); return err },
		func() error { return l.rl.Reset(ctx, "login:fail:"+k) },
	} {
		if err := step(); err != nil {
			return false, fmt.Errorf("auth: record login lockout: %w", err)
		}
	}
	return true, nil
}

// success clears the username's failures and lockout history.
func (l *loginLimiter) success(ctx context.Context, username string) error {
	k := userKey(username)
	for _, key := range []string{"login:fail:" + k, "login:level:" + k} {
		if err := l.rl.Reset(ctx, key); err != nil {
			return fmt.Errorf("auth: reset login failures: %w", err)
		}
	}
	return nil
}
