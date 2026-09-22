package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Load reads the process environment, and every deployment of this service is
// configured entirely through it — a misread variable is a silent
// misconfiguration rather than a crash.

// isolate clears every LECTERN_* variable so a default test measures the
// defaults and not whatever the developer's shell happens to export. Setting a
// variable empty is exactly how the loader sees "unset".
func isolate(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "LECTERN_") {
			t.Setenv(k, "")
		}
	}
}

func TestLoadDefaultsAreTheDocumentedOnes(t *testing.T) {
	isolate(t)
	c := Load()
	if c.Port != 9110 {
		t.Errorf("port: %d", c.Port)
	}
	if c.Host != "0.0.0.0" {
		t.Errorf("host: %q", c.Host)
	}
	if c.BaseURL != "http://127.0.0.1:9110" {
		t.Errorf("base url: %q", c.BaseURL)
	}
	if c.TickInterval != 2*time.Second || c.SessionPoll != 3*time.Second {
		t.Errorf("intervals: %v %v", c.TickInterval, c.SessionPoll)
	}
	if c.ApprovalExpire != 15*time.Minute {
		t.Errorf("approval expiry: %v", c.ApprovalExpire)
	}
	if c.Mock {
		t.Error("mock must be off unless explicitly enabled — it fakes every executor")
	}
	if c.AuthToken != "" || c.GrimoireURL != "" {
		t.Error("optional integrations must default to off")
	}
}

func TestLoadReadsTheEnvironment(t *testing.T) {
	isolate(t)
	for k, v := range map[string]string{
		"LECTERN_PORT": "9999", "LECTERN_HOST": "127.0.0.1",
		"LECTERN_DB": "/tmp/x.db", "LECTERN_MOCK": "1",
		"LECTERN_AUTH_TOKEN": "sekret", "LECTERN_TICK": "0.5",
		"LECTERN_JANITOR_DAYS": "1.5", "LECTERN_GRIMOIRE_URL": "http://g:9111",
		"LECTERN_CODEX_BIN": "/home/admin/.local/bin/codex",
	} {
		t.Setenv(k, v)
	}
	c := Load()
	if c.Port != 9999 || c.Host != "127.0.0.1" || c.DBPath != "/tmp/x.db" {
		t.Errorf("%+v", c)
	}
	if !c.Mock || c.AuthToken != "sekret" || c.GrimoireURL != "http://g:9111" {
		t.Errorf("%+v", c)
	}
	if c.CodexBin != "/home/admin/.local/bin/codex" {
		t.Errorf("an absolute agent path must survive: %q", c.CodexBin)
	}
	// fractional seconds have to work: the hermetic suite runs the tick at 0.05s
	if c.TickInterval != 500*time.Millisecond {
		t.Errorf("tick: %v", c.TickInterval)
	}
	if c.JanitorDays != 1.5 {
		t.Errorf("janitor days: %v", c.JanitorDays)
	}
}

// The default BaseURL is derived from the port, so overriding the port without
// overriding the URL must not leave hooks calling back to the wrong place.
func TestBaseURLFollowsThePort(t *testing.T) {
	isolate(t)
	t.Setenv("LECTERN_PORT", "9123")
	if got := Load().BaseURL; got != "http://127.0.0.1:9123" {
		t.Errorf("base url did not follow the port: %q", got)
	}
	t.Setenv("LECTERN_BASE_URL", "https://deck.homelab.internal")
	if got := Load().BaseURL; got != "https://deck.homelab.internal" {
		t.Errorf("an explicit base url must win: %q", got)
	}
}

// An empty variable is how a systemd EnvironmentFile expresses "unset"; it must
// not override a default with the empty string.
func TestEmptyVariablesFallBackToDefaults(t *testing.T) {
	isolate(t)
	t.Setenv("LECTERN_HOST", "")
	t.Setenv("LECTERN_CLAUDE_BIN", "")
	c := Load()
	if c.Host != "0.0.0.0" || c.ClaudeBin != "claude" {
		t.Errorf("empty env overrode a default: host=%q claude=%q", c.Host, c.ClaudeBin)
	}
}

// Garbage in a numeric variable falls back rather than crashing at boot. That is
// deliberate — but it is silent, so the fallback value is worth pinning.
func TestUnparseableNumbersFallBack(t *testing.T) {
	isolate(t)
	t.Setenv("LECTERN_PORT", "not-a-port")
	t.Setenv("LECTERN_TICK", "soon")
	c := Load()
	if c.Port != 9110 || c.TickInterval != 2*time.Second {
		t.Errorf("port=%d tick=%v", c.Port, c.TickInterval)
	}
}

// Mock is the one flag that must be hard to turn on by accident: it replaces
// git, tmux and the agent binary with a script.
func TestMockRequiresExactlyOne(t *testing.T) {
	isolate(t)
	for _, v := range []string{"", "0", "true", "yes", "TRUE"} {
		t.Setenv("LECTERN_MOCK", v)
		if Load().Mock {
			t.Errorf("LECTERN_MOCK=%q must not enable mock mode", v)
		}
	}
	t.Setenv("LECTERN_MOCK", "1")
	if !Load().Mock {
		t.Error("LECTERN_MOCK=1 should enable mock mode")
	}
}

// Attempt ids collide across databases, so the diff store has to be per-database
// or a test run can overwrite a production patch.
func TestDiffDirIsScopedToItsDatabase(t *testing.T) {
	a := (&Config{DBPath: "/home/admin/projects/lectern/lectern.db"}).DiffDir()
	b := (&Config{DBPath: "/tmp/TestX123/lectern.db"}).DiffDir()
	if a == b {
		t.Fatal("two databases share a diff directory")
	}
	if want := "/home/admin/projects/lectern/lectern-diffs"; a != want {
		t.Errorf("got %q want %q", a, want)
	}
	if filepath.Dir(b) != "/tmp/TestX123" {
		t.Errorf("diffs must sit beside their database: %q", b)
	}
}

// Credentials are read from configurable paths precisely so the suite never
// touches the operator's real ~/.claude — a test that did could push live OAuth
// tokens to a target.
func TestCredentialPathsAreOverridable(t *testing.T) {
	isolate(t)
	t.Setenv("LECTERN_CREDS", "/tmp/fake/.credentials.json")
	t.Setenv("LECTERN_CODEX_CREDS", "/tmp/fake/auth.json")
	t.Setenv("LECTERN_HOST_CLAUDE_CONFIG", "/tmp/fake/.claude.json")
	c := Load()
	for _, p := range []string{c.ClaudeCredsPath, c.CodexCredsPath, c.HostClaudeConfig} {
		if !strings.HasPrefix(p, "/tmp/fake/") {
			t.Errorf("did not honour the override: %q", p)
		}
	}
}
