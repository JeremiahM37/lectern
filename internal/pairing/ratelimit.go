package pairing

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Exchange attempts are the one unauthenticated write this package adds
// (mint requires an owner already signed in); a code is only 128 bits but an
// unlimited number of guesses is still an unlimited number of guesses.
// PerIPLimit/GlobalLimit bound the guessing rate; LockoutDuration is how long
// an IP that tripped the limit stays blocked once it does, deliberately
// longer than the plain per-minute window so tripping the limit costs more
// than waiting out one window.
const (
	PerIPLimit      = 10
	GlobalLimit     = 30
	windowLength    = time.Minute
	LockoutDuration = 5 * time.Minute
	// sweepEvery bounds how often the whole per-IP map is scanned for
	// entries that have long since expired — cheap, and keeps a public
	// endpoint's attacker-controlled key space (source IPs) from growing
	// the map forever.
	sweepEvery = 256
)

type bucket struct {
	count       int
	windowEnds  time.Time
	lockedUntil time.Time
}

func (b *bucket) allow(now time.Time, limit int) bool {
	if now.Before(b.lockedUntil) {
		return false
	}
	if now.After(b.windowEnds) {
		b.count = 0
		b.windowEnds = now.Add(windowLength)
	}
	b.count++
	if b.count > limit {
		b.lockedUntil = now.Add(LockoutDuration)
		return false
	}
	return true
}

func (b *bucket) stale(now time.Time) bool {
	return now.After(b.lockedUntil) && now.After(b.windowEnds)
}

// RateLimiter is a per-IP-plus-global limiter for the pairing exchange
// endpoint — the one place an unauthenticated caller gets repeated attempts.
type RateLimiter struct {
	mu    sync.Mutex
	perIP map[string]*bucket
	all   bucket
	calls int
}

// NewRateLimiter builds a limiter ready for concurrent use.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{perIP: map[string]*bucket{}}
}

// Allow reports whether a request from ip may proceed, charging both the
// per-IP and the global budget. A request that fails the global check still
// counts against the per-IP one, and vice versa — both are real costs paid
// regardless of which bound bites.
func (l *RateLimiter) Allow(ip string) bool {
	now := Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%sweepEvery == 0 {
		for k, b := range l.perIP {
			if b.stale(now) {
				delete(l.perIP, k)
			}
		}
	}
	b, ok := l.perIP[ip]
	if !ok {
		b = &bucket{}
		l.perIP[ip] = b
	}
	ipOK := b.allow(now, PerIPLimit)
	globalOK := l.all.allow(now, GlobalLimit)
	return ipOK && globalOK
}

// ClientIP extracts the caller's address for rate-limiting purposes. It
// deliberately reads only RemoteAddr, never X-Forwarded-For: this endpoint
// is reachable with no auth at all, so trusting a client-supplied header
// here would let an attacker pick a fresh "IP" on every request and step
// around the whole limiter — the header is only trustworthy behind the
// operator's own reverse proxy, which internal/auth's TrustServeHeaders
// opt-in already establishes is not a safe default assumption.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
