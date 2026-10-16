// Package remote holds what every phone surface shares: one-time pairing
// codes, the per-chat allowlist, and the throttle that keeps progress
// messages from flooding a phone. The surfaces themselves live in
// remote/telegram and remote/discord.
package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
)

// pairAlphabet has no 0, O, I or 1, so codes read aloud or copied by hand
// are unambiguous. It is 32 symbols, and 256 is a multiple of 32, so
// masking random bytes gives a uniform code.
const pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	pairLen       = 6
	pairFailLimit = 5
	pairLockout   = 10 * time.Minute
	// DefaultPairTTL is how long a minted code can be redeemed.
	DefaultPairTTL = 10 * time.Minute
)

// Errors returned by Redeem. They are safe to show to the person in the chat.
var (
	ErrNoCode     = errors.New("no pairing code is active; run the pairing command on the host again")
	ErrWrongCode  = errors.New("that code is not valid")
	ErrLockedOut  = errors.New("too many wrong codes; wait a few minutes and try again")
	errCodeExpire = errors.New("the pairing code has expired")
)

// Pairing mints one-time codes and redeems them from chats. A code is
// shown on the host and typed into the chat that should be paired. Five
// wrong attempts from one chat lock that chat out for ten minutes. The
// lockout is per chat, so one chat cannot exhaust another's attempts.
type Pairing struct {
	mu      sync.Mutex
	ttl     time.Duration
	code    string
	expires time.Time
	fails   map[string]int
	locked  map[string]time.Time
	now     func() time.Time
}

// NewPairing returns a Pairing whose codes last ttl (DefaultPairTTL if zero).
func NewPairing(ttl time.Duration) *Pairing {
	if ttl <= 0 {
		ttl = DefaultPairTTL
	}
	return &Pairing{ttl: ttl, fails: map[string]int{}, locked: map[string]time.Time{}, now: time.Now}
}

// Mint replaces any active code with a new one and returns it with its expiry.
func (p *Pairing) Mint() (string, time.Time, error) {
	buf := make([]byte, pairLen)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	code := make([]byte, pairLen)
	for i, b := range buf {
		code[i] = pairAlphabet[int(b)%len(pairAlphabet)]
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code = string(code)
	p.expires = p.now().Add(p.ttl)
	return p.code, p.expires, nil
}

// Redeem checks input from chat against the active code. A correct code
// is spent: it cannot pair a second chat. Matching ignores case and
// surrounding space.
func (p *Pairing) Redeem(chat, input string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if until, ok := p.locked[chat]; ok {
		if now.Before(until) {
			return ErrLockedOut
		}
		delete(p.locked, chat)
		delete(p.fails, chat)
	}
	if p.code == "" {
		return ErrNoCode
	}
	if now.After(p.expires) {
		p.code = ""
		return errCodeExpire
	}
	want := []byte(p.code)
	got := []byte(strings.ToUpper(strings.TrimSpace(input)))
	if subtle.ConstantTimeCompare(want, got) != 1 {
		p.fails[chat]++
		if p.fails[chat] >= pairFailLimit {
			p.locked[chat] = now.Add(pairLockout)
			delete(p.fails, chat)
		}
		return ErrWrongCode
	}
	p.code = ""
	delete(p.fails, chat)
	delete(p.locked, chat)
	return nil
}
