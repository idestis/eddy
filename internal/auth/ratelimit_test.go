package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/config"
)

func testLimiter(c *fakeClock) *loginLimiter {
	return newLoginLimiter(config.RateLimit{
		PerUsernameFailures: 5,
		Window:              config.Duration{Duration: 15 * time.Minute},
		Lockout:             config.Duration{Duration: 15 * time.Minute},
		PerIPPerMinute:      20,
	}, c.Now)
}

func TestUsernameLockoutBackoff(t *testing.T) {
	c := newClock()
	l := testLimiter(c)
	failN := func(n int) bool {
		var locked bool
		for range n {
			locked = l.fail("Alice")
		}
		return locked
	}
	if failN(4) || l.locked("alice") {
		t.Fatal("locked before 5 failures")
	}
	if !failN(1) || !l.locked("ALICE") {
		t.Fatal("not locked after 5 failures (case-insensitive)")
	}
	if l.locked("bob") {
		t.Fatal("lockout leaked to another user")
	}
	// Exponential: 15m, 30m, 1h, 2h, 4h, 4h.
	for i, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 4 * time.Hour} {
		if i > 0 {
			failN(5)
		}
		c.Add(want - time.Second)
		if !l.locked("alice") {
			t.Fatalf("lockout %d shorter than %v", i, want)
		}
		c.Add(2 * time.Second)
		if l.locked("alice") {
			t.Fatalf("lockout %d longer than %v", i, want)
		}
	}
	// Failures outside the window do not accumulate.
	l.success("alice")
	for range 10 {
		l.fail("carol")
		c.Add(4 * time.Minute)
	}
	if l.locked("carol") {
		t.Fatal("spread-out failures caused a lockout")
	}
	// Success resets.
	failN(4)
	l.success("alice")
	if failN(4) {
		t.Fatal("success did not reset failures")
	}
}

func TestPerIPLimit(t *testing.T) {
	c := newClock()
	l := testLimiter(c)
	for i := range 20 {
		if !l.allowIP("192.0.2.1") {
			t.Fatalf("request %d denied", i)
		}
	}
	if l.allowIP("192.0.2.1") {
		t.Fatal("21st request allowed")
	}
	if !l.allowIP("192.0.2.2") {
		t.Fatal("other IP limited")
	}
	// One IPv6 /64 shares a budget.
	for i := range 20 {
		l.allowIP(fmt.Sprintf("2001:db8::%x", i+1))
	}
	if l.allowIP("2001:db8::ffff") {
		t.Fatal("IPv6 /64 not aggregated")
	}
	c.Add(time.Minute)
	if !l.allowIP("192.0.2.1") {
		t.Fatal("budget not refilled")
	}
}

func TestLimiterBounded(t *testing.T) {
	c := newClock()
	l := testLimiter(c)
	l.max = 100
	for range 5 {
		l.fail("victim")
	}
	for i := range 1000 {
		l.fail(fmt.Sprintf("spray-%d", i))
		l.allowIP(fmt.Sprintf("198.51.%d.%d", i/256, i%256))
	}
	if len(l.users) > 100 || len(l.ips) > 100 {
		t.Fatalf("maps grew to %d/%d", len(l.users), len(l.ips))
	}
	if !l.locked("victim") {
		t.Fatal("a username spray evicted an active lockout")
	}
	c.Add(5 * time.Hour)
	l.gc()
	if len(l.users) != 0 || len(l.ips) != 0 {
		t.Fatalf("gc left %d/%d entries", len(l.users), len(l.ips))
	}
}
