package pairing

import (
	"testing"
	"time"
)

func TestRateLimiterPerIPTripsAndLocksOut(t *testing.T) {
	real := Now
	defer func() { Now = real }()
	now := real()
	Now = func() time.Time { return now }

	l := NewRateLimiter()
	for i := 0; i < PerIPLimit; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("attempt %d should still be within the per-IP limit", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("the attempt over the per-IP limit must be refused")
	}
	// Still locked out even once the plain 1-minute window would have
	// rolled over — the lockout is deliberately longer than one window.
	now = now.Add(windowLength + time.Second)
	if l.Allow("1.2.3.4") {
		t.Fatal("expected the IP to still be locked out shortly after the window rolls over")
	}
	// After the full lockout duration, it recovers.
	now = now.Add(LockoutDuration)
	if !l.Allow("1.2.3.4") {
		t.Fatal("expected the IP to recover once its lockout has elapsed")
	}
}

func TestRateLimiterPerIPIsIndependent(t *testing.T) {
	l := NewRateLimiter()
	for i := 0; i < PerIPLimit; i++ {
		if !l.Allow("1.1.1.1") {
			t.Fatalf("attempt %d for 1.1.1.1 should succeed", i)
		}
	}
	if l.Allow("1.1.1.1") {
		t.Fatal("1.1.1.1 should now be locked out")
	}
	if !l.Allow("2.2.2.2") {
		t.Fatal("a different IP must not be affected by another IP's lockout")
	}
}

func TestRateLimiterGlobalCapAppliesAcrossIPs(t *testing.T) {
	l := NewRateLimiter()
	allowed := 0
	// Enough distinct IPs that no single IP's own limit is the thing that
	// bites — only the shared global budget should be.
	for i := 0; i < GlobalLimit+10; i++ {
		ip := "10.0.0." + string(rune('A'+i%200))
		if l.Allow(ip) {
			allowed++
		}
	}
	if allowed > GlobalLimit {
		t.Fatalf("expected at most %d allowed across all IPs, got %d", GlobalLimit, allowed)
	}
}
