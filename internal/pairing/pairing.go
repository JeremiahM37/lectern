// Package pairing lets a phone with no Tailscale reach Lectern through an
// ordinary public tunnel (Cloudflare Tunnel, `tailscale funnel`, ...) without
// handing it the operator's static LECTERN_AUTH_TOKEN.
//
// Lectern's normal identity story — trust whoever tailscaled says you are —
// has no answer for a device that was never on the tailnet at all. Full
// end-to-end encryption (as github.com/slopus/happy does, for a relay it does
// not own) buys little here: this server already belongs to the person
// connecting to it. The simplest secure equivalent is device pairing: the
// owner, already authenticated in a browser or session that can decide
// approvals, mints a short-lived, single-use code; the new device exchanges
// it once for its own long-lived, individually revocable credential. From
// then on the device authenticates as that same owner — it IS the owner's
// phone, so it can do everything the owner's browser can, including deciding
// approvals.
//
// Off by default (LECTERN_DEVICE_PAIRING=1, or the "pairing_enabled" setting
// toggled from Settings → Devices). Everything here follows the store
// conventions the rest of the codebase already uses: opaque random secrets,
// stored hashed (internal/oauth), rows in the shared lectern.db
// (internal/store) rather than a database of their own.
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
)

// Now is overridden in tests that need to move expiry/idle timers around
// without sleeping, matching internal/oauth's Now.
var Now = time.Now

const (
	// CodeTTL is how long a minted pairing code stays valid. Short by
	// design: it only has to survive the seconds between showing a QR code
	// and a phone scanning it.
	CodeTTL = 5 * time.Minute

	// DefaultIdleDays is how long a paired device may go unused before its
	// token stops working, absent an explicit setting.
	DefaultIdleDays = 30

	// codeBytes is 128 bits of entropy, as specified: enough that showing
	// the whole thing (not a truncated "short form") is the exchange's only
	// job, so nothing here trades entropy for typability.
	codeBytes = 16
	// deviceTokenBytes matches internal/oauth's randomToken — 256 bits,
	// this codebase's standard for a bearer secret.
	deviceTokenBytes = 32
)

// nowUnix is store.Now()'s exact formula (float64 epoch seconds), but built
// from this package's own, test-overridable Now — so a test can move time
// forward for idle-expiry checks without also having to fake store.Now,
// which every other package's timestamps still depend on being real.
func nowUnix() float64 { return float64(Now().UnixNano()) / 1e9 }

func randomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// newCode returns a fresh pairing code as uppercase hex — 32 characters for
// 128 bits, with no encoding ambiguity (unlike base32/base64, every
// character is unambiguously 0-9A-F, which matters for a code someone may
// have to type by hand from a screen).
func newCode() (string, error) {
	b, err := randomBytes(codeBytes)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}

// newDeviceToken returns a fresh device bearer token, URL-safe so it can be
// carried in a cookie value or an Authorization header without escaping.
func newDeviceToken() (string, error) {
	b, err := randomBytes(deviceTokenBytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// FormatCode groups a raw code into hyphenated blocks for display —
// "A1B2C3D4-E5F6A7B8-..." — purely cosmetic; NormalizeCode strips it back
// out before any comparison, so a client may send the code with or without
// the hyphens.
func FormatCode(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	var b strings.Builder
	for i, r := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeCode undoes FormatCode and normalizes case/whitespace, so manual
// entry, a pasted value, and the QR-encoded value all hash identically.
func NormalizeCode(input string) string {
	input = strings.ToUpper(strings.TrimSpace(input))
	var b strings.Builder
	for _, r := range input {
		if r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hashOf is internal/oauth's exact convention: a raw secret is never stored,
// only its SHA-256 hex digest.
func hashOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
