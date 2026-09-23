package auth

// Mode is the resolved (never "auto") auth mode a request is gated under.
type Mode string

const (
	ModeAuto      Mode = "auto"
	ModeNone      Mode = "none"
	ModeToken     Mode = "token"
	ModeTailscale Mode = "tailscale"
)

// isLoopbackHost reports whether a configured listen host binds only the
// loopback interface. Empty is treated as loopback: a Config{} zero value —
// what every existing test harness builds — must resolve to the safe,
// no-gating default rather than accidentally probing a real tailscaled on
// the machine running the test.
func isLoopbackHost(host string) bool {
	switch host {
	case "", "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

// ResolveMode turns the configured LECTERN_AUTH setting into a concrete mode.
//
// setting: "", "auto" (default), "none", "token", or "tailscale" — an
// explicit non-auto value always wins outright, so a forced mode never probes
// anything. host: the configured listen host (LECTERN_HOST). tokenSet: is
// LECTERN_AUTH_TOKEN non-empty. tailscaleUp: probes tailscaled's LocalAPI;
// nil or false skips straight to the token/none fallback. It is a func, not a
// bool, so an explicit mode never pays for a probe it doesn't need.
func ResolveMode(setting, host string, tokenSet bool, tailscaleUp func() bool) Mode {
	switch Mode(setting) {
	case ModeNone, ModeToken, ModeTailscale:
		return Mode(setting)
	}
	// "auto", "", or anything unrecognized falls through to auto-detection.
	if isLoopbackHost(host) {
		return ModeNone
	}
	if tailscaleUp != nil && tailscaleUp() {
		return ModeTailscale
	}
	if tokenSet {
		return ModeToken
	}
	return ModeNone
}
