package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// installHint says how to get the binary when none is found. The release
// page is the one spec 05 names for CLIProxyAPI.
const installHint = "Install CLIProxyAPI from its GitHub releases (github.com/router-for-me/CLIProxyAPI) and put cliproxyapi on PATH"

// helpWait bounds the "does this build have the login flag" check.
const helpWait = 5 * time.Second

// loginFlow is one login the proxy binary can run. Flag is the CLIProxyAPI
// command-line flag that starts it. Account is the prefix of the credential
// files that login writes, used by Logout. Empty means the login writes into
// another provider's files, so Logout refuses it.
type loginFlow struct {
	ID      string
	Name    string
	Flag    string
	Account string
}

// loginFlows are the logins spec 03 section 11 names, in the order it lists
// them. The flag names are CLIProxyAPI's. Plan checks each one against the
// binary's own -help output before running it, so a build without a flag
// reports that instead of starting the server.
var loginFlows = []loginFlow{
	{ID: "claude", Name: "Claude", Flag: "-claude-login", Account: "claude"},
	{ID: "google", Name: "Google", Flag: "-login", Account: "gemini"},
	{ID: "codex", Name: "OpenAI Codex", Flag: "-codex-login", Account: "codex"},
	{ID: "codex-device", Name: "OpenAI Codex (device code)", Flag: "-codex-device-login"},
	{ID: "antigravity", Name: "Antigravity", Flag: "-antigravity-login", Account: "antigravity"},
	{ID: "xai", Name: "xAI", Flag: "-xai-login", Account: "xai"},
	{ID: "kimi", Name: "Kimi", Flag: "-kimi-login", Account: "kimi"},
}

// LoginProviders returns the ids /proxy login accepts, in display order.
func LoginProviders() []string {
	out := make([]string, 0, len(loginFlows))
	for _, f := range loginFlows {
		out = append(out, f.ID)
	}
	return out
}

func lookupFlow(id string) (loginFlow, bool) {
	for _, f := range loginFlows {
		if f.ID == id {
			return f, true
		}
	}
	return loginFlow{}, false
}

func unknownLoginErr(id string) error {
	return fmt.Errorf("unknown proxy login %q. Valid providers: %s", id, strings.Join(LoginProviders(), ", "))
}

// LoginPlan is one login about to run in the terminal. Command is a shell
// command line for the user's terminal. Before is the credential files as
// they were, so FinishLogin can tell what the login wrote.
type LoginPlan struct {
	Provider string
	Name     string
	Command  string
	before   map[string]time.Time
}

// LoginResult says what a login changed and how the proxy looks afterwards.
type LoginResult struct {
	Provider string
	Name     string
	// Added and Updated are credential file names that the login created or
	// rewrote, sorted. Both empty means no login was saved.
	Added   []string
	Updated []string
	State   State
}

// PlanLogin prepares the login for provider id: it finds the binary, makes
// sure the config exists, checks the binary knows the flag, and returns the
// command to run. Nothing is started here. A missing binary returns
// ErrNotInstalled and touches no files.
func (s *Sidecar) PlanLogin(ctx context.Context, id string) (LoginPlan, error) {
	f, ok := lookupFlow(id)
	if !ok {
		return LoginPlan{}, unknownLoginErr(id)
	}
	binary, err := FindBinary(s.vendored)
	if err != nil {
		return LoginPlan{}, fmt.Errorf("%w: no %s on PATH or at %s. %s", ErrNotInstalled, binaryName, s.vendored, installHint)
	}
	if _, err := s.ensureConfig(); err != nil {
		return LoginPlan{}, err
	}
	has, err := hasFlag(ctx, binary, f.Flag)
	if err != nil {
		return LoginPlan{}, err
	}
	if !has {
		return LoginPlan{}, fmt.Errorf("proxy: %s has no %s option, so it cannot log in to %s. Upgrade CLIProxyAPI", binary, f.Flag, f.Name)
	}
	return LoginPlan{
		Provider: f.ID,
		Name:     f.Name,
		Command:  ShellCommand(binary, "-config", s.configPath(), f.Flag),
		before:   s.authSnapshot(),
	}, nil
}

// FinishLogin runs after the login command has exited. It compares the
// credential files with the snapshot from PlanLogin, and checks the proxy
// again. A login counts as saved when a file is new or was rewritten.
func (s *Sidecar) FinishLogin(ctx context.Context, plan LoginPlan) LoginResult {
	res := LoginResult{Provider: plan.Provider, Name: plan.Name}
	for name, mt := range s.authSnapshot() {
		old, existed := plan.before[name]
		switch {
		case !existed:
			res.Added = append(res.Added, name)
		case !mt.Equal(old):
			res.Updated = append(res.Updated, name)
		}
	}
	sort.Strings(res.Added)
	sort.Strings(res.Updated)
	res.State = s.probeNow(ctx)
	return res
}

// Logout removes the credential files of one provider from the auth
// directory and returns their names. Only regular files named
// <prefix>-*.json are removed. The proxy may keep a removed login in memory
// until it restarts.
func (s *Sidecar) Logout(id string) ([]string, error) {
	f, ok := lookupFlow(id)
	if !ok {
		return nil, unknownLoginErr(id)
	}
	if f.Account == "" {
		return nil, fmt.Errorf("%s signs in to the same accounts as codex. Use /proxy logout codex", f.Name)
	}
	entries, err := os.ReadDir(s.authDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("proxy: cannot read %s: %w", s.authDir, err)
	}
	var removed []string
	prefix := f.Account + "-"
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(s.authDir, name)); err != nil {
			sort.Strings(removed)
			return removed, fmt.Errorf("proxy: cannot remove %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	sort.Strings(removed)
	return removed, nil
}

// SidecarStatus is what /proxy status shows.
type SidecarStatus struct {
	// Base is the OpenAI-compatible root the provider uses.
	Base string
	// Binary is the executable Scribe would run, or "" when none is installed.
	Binary string
	State  State
	// Owned is true when this Scribe started the proxy that is running.
	Owned bool
	// Accounts lists the credential file names in the auth directory, sorted.
	Accounts []string
}

// Status reports the proxy as it is now. It never starts or stops anything.
func (s *Sidecar) Status(ctx context.Context) SidecarStatus {
	st := SidecarStatus{Base: s.base, State: s.probeNow(ctx), Accounts: s.accounts()}
	if bin, err := FindBinary(s.vendored); err == nil {
		st.Binary = bin
	}
	if rec, ok := readRecord(s.pidPath()); ok && rec.Owner == os.Getpid() && rec.identifies() {
		st.Owned = true
	}
	return st
}

// probeNow checks the proxy with the keys in its config. An unreadable config
// is probed without a key, since the probe is only a status check.
func (s *Sidecar) probeNow(ctx context.Context) State {
	cfg, _, _ := loadSidecarConfig(s.configPath())
	st, _ := s.probeKeys(ctx, candidateKeys(cfg))
	return st
}

// authSnapshot maps each regular file in the auth directory to its mtime.
// A missing directory is an empty snapshot, not an error.
func (s *Sidecar) authSnapshot() map[string]time.Time {
	out := map[string]time.Time{}
	entries, err := os.ReadDir(s.authDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = info.ModTime()
	}
	return out
}

// accounts lists the .json credential files, sorted.
func (s *Sidecar) accounts() []string {
	var out []string
	for name := range s.authSnapshot() {
		if strings.HasSuffix(name, ".json") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// hasFlag reports whether the binary's -help output lists flag. Go's flag
// package prints each flag alone on a line, so the match is on that line.
func hasFlag(ctx context.Context, binary, flag string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, helpWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "-help").CombinedOutput()
	if ctx.Err() != nil {
		return false, fmt.Errorf("proxy: %s -help did not finish in %s", binary, helpWait)
	}
	// -help exits 0 with Go's flag package, but other builds may exit
	// non-zero after printing usage. Only empty output is a failure.
	if len(out) == 0 && err != nil {
		return false, fmt.Errorf("proxy: cannot run %s -help: %w", binary, err)
	}
	re := regexp.MustCompile(`(?m)^\s*--?` + regexp.QuoteMeta(strings.TrimPrefix(flag, "-")) + `\s*$`)
	return re.Match(out), nil
}

// ShellCommand joins args into one shell command line. Each argument is
// single-quoted, so paths with spaces or quotes reach the binary unchanged.
func ShellCommand(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}
