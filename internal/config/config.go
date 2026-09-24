// Package config holds every knob lectern reads from the environment.
//
// Values are resolved once at startup into a Config, then passed explicitly.
// Tests build their own Config instead of mutating globals, which is what makes
// the whole suite able to run in parallel against isolated databases.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	DBPath  string
	Port    int
	Host    string
	BaseURL string // what a target uses to reach the control plane (hook callbacks)
	// HookBase is the host part an agent's own hooks/statusline call back to
	// (LECTERN_HOOK_URL is built from it). Defaults to BaseURL; only needs
	// its own value when a remote target cannot reach the control plane the
	// same way the operator's browser does.
	HookBase string
	// WorktreeNamespace scopes automatically-created local worktrees and
	// branches to one durable local runtime. Empty preserves hosted behavior.
	WorktreeNamespace string

	// Mock swaps every executor for the scripted MockExecutor: no real git,
	// tmux or agent binary. Powers the hermetic suite and the UI demo mode.
	Mock bool

	AuthToken string // single bearer for the API/PWA; empty = open

	// Auth is LECTERN_AUTH: "", "auto" (default), "none", "token", or
	// "tailscale" — see internal/auth. TailscaleSocket overrides tailscaled's
	// LocalAPI socket path; TailscaleUsers/TailscaleTags are comma-separated
	// allowlists (LECTERN_TAILSCALE_USERS / LECTERN_TAILSCALE_TAGS).
	Auth            string
	TailscaleSocket string
	TailscaleUsers  string
	TailscaleTags   string

	// TrustServeHeaders is LECTERN_TRUST_SERVE_HEADERS — see its doc comment
	// on auth.Settings. Off by default: unsafe wherever an untrusted process
	// (an agent included) can reach this host's loopback interface.
	TrustServeHeaders bool

	// TLS turns on a second listener bound to this node's tailnet addresses,
	// so a phone gets a secure context without needing `tailscale serve` to
	// front it. LECTERN_TLS: "" (off) or "tailscale". TLSPort
	// (LECTERN_TLS_PORT) is required for it to actually start — "tailscale"
	// with no port configured is a no-op, same as leaving TLS unset. It is
	// also auto-enabled when the resolved auth mode is tailscale and TLSPort
	// is set, so setting just LECTERN_TLS_PORT is enough in the common case.
	TLS     string
	TLSPort int

	TickInterval time.Duration
	// HandoffPoll is how often a session switch checks for the agent's wrap;
	// zero keeps the session manager's default. Tests shorten it.
	HandoffPoll    time.Duration
	ApprovalPoll   time.Duration
	ApprovalExpire time.Duration
	JanitorDays    float64
	// ScratchDays is how long an empty, unowned scratch workspace sits idle
	// before the sweep trashes it (0 disables the sweep); ScratchTrashDays is how
	// long it stays recoverable after that.
	ScratchDays      float64
	ScratchTrashDays float64
	// Live turns on forwarded ports and live desktops. They open listening
	// ports of their own and let an agent start a desktop that can be driven
	// from the network, so an operator asks for them; they are not a default.
	Live           bool
	MockAgentDelay time.Duration

	VAPIDPrivateKey string
	VAPIDPublicKey  string
	VAPIDEmail      string

	// HostClaudeConfig is the control plane user's own Claude Code config. It is
	// read ONLY to learn which MCP servers a local-target agent already inherits,
	// so the parity profile can grant permission to call them. Never copied
	// anywhere — it holds live credentials.
	HostClaudeConfig string

	ClaudeBin string
	// Agent binaries often live in ~/.local/bin, which a systemd unit's PATH does
	// not include — set these when a probe reports an agent missing that you know
	// is installed.
	CodexBin  string
	GeminiBin string

	// AnthropicAPIKey is rotation-proof agent auth. When set it is injected as
	// ANTHROPIC_API_KEY into every launch and no OAuth credentials are pushed to
	// targets. When empty, the control plane's current OAuth creds are pushed at
	// dispatch instead.
	AnthropicAPIKey string

	// ClaudeCredsPath / CodexCredsPath are where the control plane keeps agent
	// OAuth credentials. Overridable so tests never depend on a real ~/.claude.
	ClaudeCredsPath string
	CodexCredsPath  string

	// GrimoireURL points at a Grimoire instance for durable project memory.
	// Empty means lectern runs with no memory provider, which is a supported
	// configuration and not a degraded one — the two tools compose, they do not
	// depend on each other.
	GrimoireURL             string
	GrimoireToken           string
	GrimoireContextMode     string
	GrimoireContextProjects string

	// SessionPoll is how often live interactive sessions are refreshed. It is
	// separate from TickInterval because a session poll costs one exec per
	// target, whether or not anything is dispatched.
	SessionPoll time.Duration

	// MediaPath overrides MediaDir; MediaMaxBytes caps one posted file.
	MediaPath     string
	MediaMaxBytes int64

	// CheckTimeout bounds one run of a project's check command
	// (internal/checks), for both a session's Stop-triggered check and a
	// task's auto-verify. LECTERN_CHECK_TIMEOUT, seconds; zero means the
	// package's own 15-minute default (checks.DefaultTimeout).
	CheckTimeout time.Duration
}

// DiffDir is where captured patches live — derived from the DB path so each
// database (including every test's temp DB) gets its own store. Attempt ids
// collide across databases, and a shared store once let a mock run overwrite
// production diffs.
func (c *Config) DiffDir() string {
	return filepath.Join(filepath.Dir(c.DBPath), "lectern-diffs")
}

// MediaDir holds what agents post back for the operator to look at: recordings,
// screenshots, reports. It sits beside the database for the same reason DiffDir
// does, but is overridable because a demo video is far larger than a patch and
// an operator may want it on a bulk volume.
func (c *Config) MediaDir() string {
	if c.MediaPath != "" {
		return c.MediaPath
	}
	return filepath.Join(filepath.Dir(c.DBPath), "lectern-media")
}

// MediaLimit is the largest single file an agent may post. A Config built by
// hand, as tests do, gets the same default Load applies.
func (c *Config) MediaLimit() int64 {
	if c.MediaMaxBytes > 0 {
		return c.MediaMaxBytes
	}
	return 1 << 30
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(env(key, ""), 64); err == nil {
		return v
	}
	return def
}

func envSeconds(key string, def float64) time.Duration {
	return time.Duration(envFloat(key, def) * float64(time.Second))
}

// Load resolves configuration from the process environment.
func Load() *Config {
	home, _ := os.UserHomeDir()
	port := 9110
	if p, err := strconv.Atoi(env("LECTERN_PORT", "")); err == nil {
		port = p
	}
	cwd, _ := os.Getwd()
	c := &Config{
		DBPath:                  env("LECTERN_DB", filepath.Join(cwd, "lectern.db")),
		Port:                    port,
		Host:                    env("LECTERN_HOST", "0.0.0.0"),
		Mock:                    os.Getenv("LECTERN_MOCK") == "1",
		MediaPath:               os.Getenv("LECTERN_MEDIA_DIR"),
		MediaMaxBytes:           int64(envFloat("LECTERN_MEDIA_MAX_MB", 1024)) << 20,
		AuthToken:               os.Getenv("LECTERN_AUTH_TOKEN"),
		Auth:                    env("LECTERN_AUTH", "auto"),
		TailscaleSocket:         os.Getenv("LECTERN_TAILSCALE_SOCKET"),
		TailscaleUsers:          os.Getenv("LECTERN_TAILSCALE_USERS"),
		TailscaleTags:           os.Getenv("LECTERN_TAILSCALE_TAGS"),
		TrustServeHeaders:       os.Getenv("LECTERN_TRUST_SERVE_HEADERS") == "1",
		TLS:                     os.Getenv("LECTERN_TLS"),
		TLSPort:                 int(envFloat("LECTERN_TLS_PORT", 0)),
		TickInterval:            envSeconds("LECTERN_TICK", 2.0),
		HandoffPoll:             envSeconds("LECTERN_HANDOFF_POLL", 0),
		ApprovalPoll:            envSeconds("LECTERN_APPROVAL_POLL", 25),
		ApprovalExpire:          envSeconds("LECTERN_APPROVAL_EXPIRE", 900),
		JanitorDays:             envFloat("LECTERN_JANITOR_DAYS", 7),
		ScratchDays:             envFloat("LECTERN_SCRATCH_DAYS", 7),
		ScratchTrashDays:        envFloat("LECTERN_SCRATCH_TRASH_DAYS", 14),
		Live:                    os.Getenv("LECTERN_LIVE") == "1",
		MockAgentDelay:          envSeconds("LECTERN_MOCK_DELAY", 0.4),
		VAPIDPrivateKey:         os.Getenv("LECTERN_VAPID_PRIVATE"),
		VAPIDPublicKey:          os.Getenv("LECTERN_VAPID_PUBLIC"),
		VAPIDEmail:              env("LECTERN_VAPID_EMAIL", "admin@example.com"),
		HostClaudeConfig:        env("LECTERN_HOST_CLAUDE_CONFIG", filepath.Join(home, ".claude.json")),
		ClaudeBin:               env("LECTERN_CLAUDE_BIN", "claude"),
		CodexBin:                env("LECTERN_CODEX_BIN", "codex"),
		GeminiBin:               env("LECTERN_GEMINI_BIN", "gemini"),
		AnthropicAPIKey:         os.Getenv("LECTERN_ANTHROPIC_API_KEY"),
		ClaudeCredsPath:         env("LECTERN_CREDS", filepath.Join(home, ".claude", ".credentials.json")),
		CodexCredsPath:          env("LECTERN_CODEX_CREDS", filepath.Join(home, ".codex", "auth.json")),
		GrimoireURL:             os.Getenv("LECTERN_GRIMOIRE_URL"),
		GrimoireToken:           os.Getenv("LECTERN_GRIMOIRE_TOKEN"),
		GrimoireContextMode:     env("LECTERN_GRIMOIRE_CONTEXT_MODE", "project"),
		GrimoireContextProjects: os.Getenv("LECTERN_GRIMOIRE_CONTEXT_PROJECTS"),
		SessionPoll:             envSeconds("LECTERN_SESSION_POLL", 3.0),
		CheckTimeout:            envSeconds("LECTERN_CHECK_TIMEOUT", 900),
	}
	c.BaseURL = env("LECTERN_BASE_URL", "http://127.0.0.1:"+strconv.Itoa(port))
	c.HookBase = env("LECTERN_HOOK_BASE", c.BaseURL)
	return c
}
