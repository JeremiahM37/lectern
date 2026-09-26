// pairing.go backs Settings → Devices ("Pair a phone") and the public /pair
// exchange it feeds: minting a short-lived, single-use code as an
// already-signed-in owner, and trading that code for a long-lived device
// credential from an unauthenticated browser. See internal/pairing for the
// design rationale and docs/remote-access.md for the operator-facing story
// (running Lectern behind a public tunnel with pairing on).
//
// mintPairingCode and the device-list/revoke/settings handlers below all
// require CanDecide, exactly like decideApproval — minting a credential that
// can decide approvals is at least as sensitive as deciding one. exchangePairingCode
// is the one deliberate exception: see its own doc comment and the exemption
// in withAuth.
package api

import (
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/pairing"
)

// pairingEnabled reports whether device pairing is live for this process:
// LECTERN_DEVICE_PAIRING=1 (s.Cfg.DevicePairing) or the persisted
// "pairing_enabled" setting flipped on from Settings → Devices.
func (s *Server) pairingEnabled() bool {
	return s.Pairing != nil && pairing.Enabled(s.Cfg.DevicePairing, s.DB)
}

// untrustedOrigin reports whether the request reaching this handler arrived
// over neither loopback (a reverse proxy on this host, e.g. `tailscale
// serve`) nor a tailnet address — i.e. some other path, most plausibly a
// public tunnel forwarding to a non-loopback bind, or a direct LAN/port
// exposure. It is a hint, not a security boundary: a well-behaved tunnel
// that forwards to loopback (Cloudflare Tunnel's default) looks identical to
// any other local process here, which is exactly why docs/remote-access.md
// tells operators to enable pairing themselves rather than relying on this
// to catch every case.
func untrustedOrigin(r *http.Request) bool {
	loopback, tailscale := auth.ClassifyRemote(r.RemoteAddr)
	return !loopback && !tailscale
}

func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(principal) {
		httpError(w, 403, "device pairing requires a signed-in human (tailscale identity or access token)")
		return auth.Principal{}, false
	}
	return principal, true
}

// mintPairingCode is Settings → Devices' "Pair a phone" button: an
// already-authenticated owner asks for a fresh code, shown as a QR code and
// as typed text (frontend/src/pairing). Owner-only — see requireOwner.
func (s *Server) mintPairingCode(w http.ResponseWriter, r *http.Request) {
	if !s.pairingEnabled() {
		httpError(w, 409, "device pairing is turned off — enable it in Settings → Devices first")
		return
	}
	owner, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	code, expiresAt, err := s.Pairing.MintCode(owner)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Pairing.PruneExpiredCodes()
	writeJSON(w, 200, map[string]any{
		"code":       code,
		"expires_at": float64(expiresAt.Unix()),
		"ttl_s":      int(pairing.CodeTTL.Seconds()),
	})
}

type pairingExchangeIn struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// pairingDeviceCookieMaxAge is deliberately generous (Chrome's own cap on a
// cookie's lifetime is ~400 days) so the cookie itself is never the thing
// that logs a device out. The real, renewing control is server-side idle
// expiry (internal/pairing.Store.LookupDevice, refreshed on every request),
// which a revoke also bypasses immediately regardless of what the cookie
// still says.
const pairingDeviceCookieMaxAge = 400 * 24 * 3600

// exchangePairingCode is the ONE unauthenticated write device pairing adds
// (alongside serving the /pair page itself, a static asset) — see the
// exemption in withAuth. A caller here has proven nothing except that it
// knows a code that was displayed, briefly, on the owner's own screen; that
// is the entire trust model, so this handler is the one place pairing's
// hardening requirements (rate limit, constant-time code comparison via
// hashing, single-use, short TTL) all have to hold.
func (s *Server) exchangePairingCode(w http.ResponseWriter, r *http.Request) {
	if !s.pairingEnabled() {
		httpError(w, 409, "device pairing is turned off")
		return
	}
	if !s.Pairing.Limiter().Allow(pairing.ClientIP(r)) {
		httpError(w, 429, "too many pairing attempts — try again in a few minutes")
		return
	}
	var body pairingExchangeIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(body.Code) == "" {
		httpError(w, 400, "code is required")
		return
	}
	token, dev, err := s.Pairing.ExchangeCode(body.Code, body.Name, r.Header.Get("User-Agent"))
	if err != nil {
		// One message for "wrong", "reused" and "expired" alike — telling
		// them apart would make this endpoint an oracle for guessing a live
		// code, the exact thing the rate limit and the hashed lookup are
		// already there to prevent.
		httpError(w, 400, "invalid or expired code")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "lectern_device",
		Value:    token,
		Path:     "/",
		MaxAge:   pairingDeviceCookieMaxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, 200, map[string]any{
		// Returned once, for a non-browser API client that cannot read the
		// cookie jar — see internal/auth's Authorization-bearer path for
		// device tokens. A browser client should ignore this field; the
		// cookie just set is what it authenticates with from here on.
		"token":  token,
		"device": dev,
	})
}

// listPairedDevices backs Settings → Devices' list. Owner-only.
func (s *Server) listPairedDevices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	if s.Pairing == nil {
		writeJSON(w, 200, []pairing.Device{})
		return
	}
	rows, err := s.Pairing.ListDevices()
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

// revokePairedDevice deletes a device's token immediately; its next request
// gets a plain 401. Owner-only.
func (s *Server) revokePairedDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such device")
		return
	}
	if err := s.Pairing.RevokeDevice(id); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

type pairingSettingsOut struct {
	Enabled       bool    `json:"enabled"`
	EnvForced     bool    `json:"env_forced"`
	IdleDays      float64 `json:"idle_days"`
	DeviceCount   int     `json:"device_count"`
	PendingHint   bool    `json:"untrusted_origin_hint"`
	CodeTTLSecond int     `json:"code_ttl_s"`
}

// getPairingSettings backs Settings → Devices' toggle and idle-expiry field,
// plus PendingHint (docs/remote-access.md): true when THIS request itself
// arrived over neither loopback nor a tailnet address while pairing is off —
// the signal that the owner may already be reaching Lectern over a public
// path with no device-pairing safety net under it.
func (s *Server) getPairingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	out := pairingSettingsOut{
		Enabled:       s.pairingEnabled(),
		EnvForced:     s.Cfg.DevicePairing,
		IdleDays:      pairing.IdleDays(s.DB),
		PendingHint:   untrustedOrigin(r) && !s.pairingEnabled(),
		CodeTTLSecond: int(pairing.CodeTTL.Seconds()),
	}
	if s.Pairing != nil {
		if rows, err := s.Pairing.ListDevices(); err == nil {
			out.DeviceCount = len(rows)
		}
	}
	writeJSON(w, 200, out)
}

type pairingSettingsIn struct {
	Enabled  *bool    `json:"enabled"`
	IdleDays *float64 `json:"idle_days"`
}

func (s *Server) putPairingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	var body pairingSettingsIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if body.Enabled != nil {
		if err := pairing.SetEnabled(s.DB, *body.Enabled); err != nil {
			respondErr(w, err)
			return
		}
	}
	if body.IdleDays != nil {
		if err := pairing.SetIdleDays(s.DB, *body.IdleDays); err != nil {
			respondErr(w, err)
			return
		}
	}
	s.getPairingSettings(w, r)
}
