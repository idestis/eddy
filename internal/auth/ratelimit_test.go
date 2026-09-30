package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
)

func testLimiter(t *testing.T, rl store.RateLimits, c *fakeClock) *loginLimiter {
	t.Helper()
	return newLoginLimiter(rl, config.RateLimit{
		PerUsernameFailures: 5,
		Window:              config.Duration{Duration: 15 * time.Minute},
		Lockout:             config.Duration{Duration: 15 * time.Minute},
		PerIPPerMinute:      20,
	}, c.Now)
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func TestUsernameLockoutBackoff(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	l := testLimiter(t, memory.New().RateLimits(), c)
	b := must[bool](t)
	failN := func(n int) bool {
		var locked bool
		for range n {
			locked = b(l.fail(ctx, "Alice"))
		}
		return locked
	}
	if failN(4) || b(l.locked(ctx, "alice")) {
		t.Fatal("locked before 5 failures")
	}
	if !failN(1) || !b(l.locked(ctx, "ALICE")) {
		t.Fatal("not locked after 5 failures (case-insensitive)")
	}
	if b(l.locked(ctx, "bob")) {
		t.Fatal("lockout leaked to another user")
	}
	// Exponential: 15m, 30m, 1h, 2h, 4h, 4h.
	for i, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 4 * time.Hour} {
		if i > 0 {
			failN(5)
		}
		c.Add(want - time.Second)
		if !b(l.locked(ctx, "alice")) {
			t.Fatalf("lockout %d shorter than %v", i, want)
		}
		c.Add(2 * time.Second)
		if b(l.locked(ctx, "alice")) {
			t.Fatalf("lockout %d longer than %v", i, want)
		}
	}
	// Failures outside the window do not accumulate.
	if err := l.success(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		b(l.fail(ctx, "carol"))
		c.Add(4 * time.Minute)
	}
	if b(l.locked(ctx, "carol")) {
		t.Fatal("spread-out failures caused a lockout")
	}
	// Success resets.
	failN(4)
	_ = l.success(ctx, "alice")
	if failN(4) {
		t.Fatal("success did not reset failures")
	}
}

func TestPerIPLimit(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	l := testLimiter(t, memory.New().RateLimits(), c)
	b := must[bool](t)
	for i := range 20 {
		if !b(l.allowIP(ctx, "192.0.2.1")) {
			t.Fatalf("request %d denied", i)
		}
	}
	if b(l.allowIP(ctx, "192.0.2.1")) {
		t.Fatal("21st request allowed")
	}
	if !b(l.allowIP(ctx, "192.0.2.2")) {
		t.Fatal("other IP limited")
	}
	// One IPv6 /64 shares a budget.
	for i := range 20 {
		b(l.allowIP(ctx, fmt.Sprintf("2001:db8::%x", i+1)))
	}
	if b(l.allowIP(ctx, "2001:db8::ffff")) {
		t.Fatal("IPv6 /64 not aggregated")
	}
	c.Add(time.Minute)
	if !b(l.allowIP(ctx, "192.0.2.1")) {
		t.Fatal("budget not refilled")
	}
}

// TestLimiterShared checks that two limiters on one store (two hub
// replicas) share the counters.
func TestLimiterShared(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	rl := memory.New().RateLimits()
	a, bl := testLimiter(t, rl, c), testLimiter(t, rl, c)
	b := must[bool](t)
	for i := range 20 {
		l := a
		if i%2 == 1 {
			l = bl
		}
		if !b(l.allowIP(ctx, "192.0.2.9")) {
			t.Fatalf("request %d denied", i)
		}
	}
	if b(a.allowIP(ctx, "192.0.2.9")) || b(bl.allowIP(ctx, "192.0.2.9")) {
		t.Fatal("per-IP budget not shared")
	}
	for i := range 5 {
		l := a
		if i%2 == 1 {
			l = bl
		}
		b(l.fail(ctx, "dave"))
	}
	if !b(a.locked(ctx, "dave")) || !b(bl.locked(ctx, "dave")) {
		t.Fatal("lockout not shared")
	}
}

// failingLimits is a store.RateLimits whose store is down.
type failingLimits struct{}

var errStoreDown = fmt.Errorf("store down")

func (failingLimits) Hit(context.Context, string, time.Duration, int, time.Time) (int, bool, error) {
	return 0, false, errStoreDown
}
func (failingLimits) Get(context.Context, string, time.Time) (int, time.Time, error) {
	return 0, time.Time{}, errStoreDown
}
func (failingLimits) Reset(context.Context, string) error { return errStoreDown }

func TestLimiterStoreDown(t *testing.T) {
	ctx := context.Background()
	l := testLimiter(t, failingLimits{}, newClock())
	if ok, err := l.allowIP(ctx, "192.0.2.1"); ok || err == nil {
		t.Fatalf("allowIP with the store down = %v, %v; want false and an error", ok, err)
	}
	if _, err := l.locked(ctx, "alice"); err == nil {
		t.Fatal("locked with the store down returned no error")
	}
}
