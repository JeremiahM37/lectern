package remote

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// clock is a settable time source for the pairing and throttle tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newPairingAt(c *clock) *Pairing {
	p := NewPairing(10 * time.Minute)
	p.now = c.now
	return p
}

func TestPairCodeShapeAndAlphabet(t *testing.T) {
	p := NewPairing(0)
	seen := map[string]bool{}
	for range 200 {
		code, _, err := p.Mint()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != pairLen {
			t.Fatalf("code %q has length %d, want %d", code, len(code), pairLen)
		}
		for _, r := range code {
			if !strings.ContainsRune(pairAlphabet, r) {
				t.Fatalf("code %q has %q outside the alphabet", code, r)
			}
			if strings.ContainsRune("0O1I", r) {
				t.Fatalf("code %q contains an ambiguous character", code)
			}
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Fatalf("200 mints gave only %d distinct codes", len(seen))
	}
}

func TestPairRedeemIsOneTimeAndCaseInsensitive(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newPairingAt(c)
	code, _, _ := p.Mint()
	if err := p.Redeem("111", "  "+strings.ToLower(code)+" "); err != nil {
		t.Fatalf("correct code rejected: %v", err)
	}
	if err := p.Redeem("222", code); !errors.Is(err, ErrNoCode) {
		t.Fatalf("spent code redeemed again: %v", err)
	}
}

func TestPairCodeExpires(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newPairingAt(c)
	code, _, _ := p.Mint()
	c.advance(11 * time.Minute)
	if err := p.Redeem("111", code); err == nil {
		t.Fatal("expired code was accepted")
	}
}

func TestPairLockoutPerChat(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newPairingAt(c)
	code, _, _ := p.Mint()
	for range pairFailLimit {
		if err := p.Redeem("bad", "ZZZZZZ"); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("wrong code error = %v", err)
		}
	}
	// Locked: even the right code is refused for this chat.
	if err := p.Redeem("bad", code); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("locked chat redeemed the right code: %v", err)
	}
	// Another chat is not affected by that chat's lockout.
	if err := p.Redeem("good", code); err != nil {
		t.Fatalf("other chat blocked by someone else's lockout: %v", err)
	}
}

func TestPairLockoutLifts(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newPairingAt(c)
	p.Mint()
	for range pairFailLimit {
		_ = p.Redeem("bad", "ZZZZZZ")
	}
	c.advance(pairLockout + time.Second)
	code, _, _ := p.Mint()
	if err := p.Redeem("bad", code); err != nil {
		t.Fatalf("lockout did not lift: %v", err)
	}
}

func TestAllowlistDefaultDenyAndStatic(t *testing.T) {
	a, err := NewAllowlist("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Allowed("1") {
		t.Fatal("empty allowlist admitted a chat")
	}
	a, _ = NewAllowlist("", []string{"42", " "})
	if !a.Allowed("42") || a.Allowed("43") {
		t.Fatal("static allowlist not applied")
	}
}

func TestAllowlistPairingPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote", "telegram.json")
	a, err := NewAllowlist(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Add("777"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, want 0600", info.Mode().Perm())
	}
	b, err := NewAllowlist(path, []string{"42"})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Allowed("777") {
		t.Fatal("pairing did not survive a reload")
	}
	if got := b.Members(); len(got) != 2 || got[0] != "42" || got[1] != "777" {
		t.Fatalf("Members = %v", got)
	}
}

func TestAllowlistRejectsCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telegram.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAllowlist(path, nil); err == nil {
		t.Fatal("corrupt state file was silently accepted")
	}
}

func TestThrottleOnePerIntervalPerKey(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	th := NewThrottle(20 * time.Second)
	th.now = c.now
	if !th.Allow("progress") {
		t.Fatal("first event refused")
	}
	if th.Allow("progress") {
		t.Fatal("second event inside the interval allowed")
	}
	if !th.Allow("done") {
		t.Fatal("a different key was throttled by progress")
	}
	c.advance(20 * time.Second)
	if !th.Allow("progress") {
		t.Fatal("event after the interval refused")
	}
}

func TestChunkSplitsOnNewlinesAndRunes(t *testing.T) {
	text := strings.Repeat("line of text\n", 20)
	parts := Chunk(text, 50)
	for _, p := range parts {
		if len(p) > 50 {
			t.Fatalf("chunk of %d bytes exceeds 50", len(p))
		}
	}
	// Break points consume newlines, so compare the words, not the layout.
	if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.Join(strings.Fields(text), " ") {
		t.Fatalf("chunks lost text: %q", parts)
	}

	multi := strings.Repeat("é", 40) // 2 bytes each
	for _, p := range Chunk(multi, 15) {
		if !utf8.ValidString(p) {
			t.Fatalf("chunk %q split a rune", p)
		}
	}
	if got := Chunk("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short text = %q", got)
	}
}
