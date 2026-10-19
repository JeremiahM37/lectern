// Package codex runs models from a ChatGPT subscription through the official
// codex CLI: model calls go through `codex exec`, and sign-in, account status,
// rate limits and the model list go through `codex app-server` (JSON-RPC over
// stdio). Lectern never sees or stores the ChatGPT tokens; the CLI keeps them
// in its own home (~/.codex).
package codex

import "time"

// ProviderID is the registry id of this provider.
const ProviderID = "codex"

// DefaultBinary is the command looked up on PATH when no binary is set.
const DefaultBinary = "codex"

// Status is what `/codex status` shows. Fields the CLI did not report stay
// zero; Err explains why the rest could not be read.
type Status struct {
	Installed bool   // the binary was found and ran
	Binary    string // resolved path, "" when not installed
	Version   string // e.g. "codex-cli 0.163.0"
	LoggedIn  bool
	AuthMode  string // "chatgpt", "apiKey", ... as the CLI reports it
	Email     string
	Plan      string // e.g. "plus", "pro"
	Limits    []RateLimit
	Credits   *Credits // nil when the CLI did not report credits
	Err       error    // a read that failed (status still says what is known)
}

// RateLimit is one usage window, e.g. the 5-hour and the weekly limit.
type RateLimit struct {
	Name        string // "5h", "weekly", or the window length in words
	UsedPercent float64
	Window      time.Duration
	ResetsAt    time.Time // zero when unknown
}

// Credits is the account's credit balance, when it has one.
type Credits struct {
	HasCredits bool
	Unlimited  bool
	Balance    string // as reported, a decimal string
}

// LoginMode chooses the sign-in flow.
type LoginMode int

const (
	// LoginAuto picks Browser when a browser can open on this machine
	// (a local display and not an SSH session), else Device.
	LoginAuto LoginMode = iota
	// LoginBrowser opens ChatGPT in a browser on this machine; the CLI
	// listens for the callback on localhost.
	LoginBrowser
	// LoginDevice shows a URL and a one-time code to enter on any device.
	LoginDevice
)

// LoginEventKind says what a LoginEvent carries.
type LoginEventKind string

const (
	LoginStarted LoginEventKind = "started" // URL (and Code for device) are set
	LoginInfo    LoginEventKind = "info"    // Message is progress text
	LoginDone    LoginEventKind = "done"    // signed in; Status may be filled
	LoginFailed  LoginEventKind = "failed"  // Err is set
)

// LoginEvent is one step of a sign-in. The channel returned by Login is
// closed after a LoginDone or LoginFailed event.
type LoginEvent struct {
	Kind   LoginEventKind
	Mode   LoginMode // the flow actually used (never LoginAuto)
	URL    string    // where to sign in
	Code   string    // one-time code (device flow only)
	Opened bool      // a browser was opened for URL
	// AlreadySignedIn marks a LoginDone sent at once because the CLI was
	// signed in before the login started; no sign-in took place.
	AlreadySignedIn bool
	Message         string
	Status          *Status
	Err             error
}
