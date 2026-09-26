// Package auth resolves who is making a request — a local process on this
// box, a tailnet user identified through tailscaled, or a bearer token — and
// decides what mode of gating the control plane runs under.
//
// The owner's stated requirement is the whole design: "use tailscale identity
// so I never have to log in on my own devices, and for people using lectern
// on a single machine, don't require auth." Mode resolution (auto/none/token/
// tailscale) picks the right default without configuration; principal
// resolution answers "who is this" for the one request in hand.
package auth

// Kind values a Principal can carry.
const (
	KindLocal     = "local"     // an unauthenticated process on this machine (CLI, MCP, an agent)
	KindTailscale = "tailscale" // identified via tailscaled's LocalAPI whois, or the Tailscale-User-Login header
	KindToken     = "token"     // LECTERN_AUTH_TOKEN, presented by a script or an off-tailnet client
	// KindDevice is a phone or browser that exchanged a one-time pairing code
	// (internal/pairing) for a device token. It carries the identity of
	// whichever owner minted the code — Login/Node/Human are copied from that
	// owner's own Principal at mint time — because the whole point of pairing
	// is that the paired device IS the owner, reachable without Tailscale.
	KindDevice = "device"
)

// Principal is the resolved identity of one request.
//
// Human is true only for a person: a token holder, a tailnet login that
// matched the allowlist, or a paired device (which inherits its minting
// owner's Human bit). It is false for KindLocal and for a tagged (service)
// node — those may use the ordinary API but may not decide approvals, which
// the contract requires a human for.
type Principal struct {
	Kind  string
	Login string
	Node  string
	Human bool
}
