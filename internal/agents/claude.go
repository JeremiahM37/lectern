// Package agents is the coding-agent adapter seam. Everything CLI-format
// specific lives here, so agent drift touches one package.
//
//	claude — first-class: stream-json events, hook approvals, session resume.
//	codex  — experimental: `codex exec --json` JSONL; no gated mode, no resume.
//	gemini — experimental: plain-text output mapped to timeline lines.
//
// The run protocol itself is agent-agnostic (worktree + tmux + events file +
// exit_code), so adapters only build the inner command and normalise output.
package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Paths of the runtime files every attempt gets, relative to its worktree.
const (
	SettingsRel = ".lectern/settings.json"
	MCPRel      = ".lectern/mcp.json"
)

// DefaultGateMatcher is which tools the approval hook intercepts in gated mode.
//
// '*' is the only value that makes gated mode usable: anything NOT matched falls
// through to the normal permission system, and headless `claude -p` has no way
// to ask — so an unmatched tool is silently DENIED. The old narrow matcher meant
// MCP tools, WebFetch and Task could never run in gated mode and never said why.
const DefaultGateMatcher = "*"

// PermissionKeys are the settings.json permission keys we pass through. Anything
// else is rejected so a typo fails loudly at config time instead of silently
// widening or narrowing access mid-run.
var PermissionKeys = []string{"allow", "deny", "ask", "defaultMode", "additionalDirectories"}

// CapabilityProfiles are the two shapes a project's agent capability can take.
var CapabilityProfiles = []string{"restricted", "parity"}

// ParityTools are the tools an interactive session takes for granted.
//
// Headless grants NONE of them: with no rules a dispatched agent cannot pipe a
// command, read a file outside its worktree, or call a single MCP tool — it just
// produces worse work and says nothing. 'Bash' is listed bare on purpose: a
// prefix rule like Bash(git*) makes Claude Code split compound commands and
// refuse the parts it cannot match, so `ls | head` dies with "This Bash command
// contains multiple operations".
var ParityTools = []string{"Bash", "Read", "Write", "Edit", "MultiEdit", "NotebookEdit",
	"Glob", "Grep", "WebFetch", "WebSearch", "TodoWrite", "Task", "Skill"}

// Permissions is the settings.json permissions block.
type Permissions struct {
	Allow                 []string `json:"allow,omitempty"`
	Deny                  []string `json:"deny,omitempty"`
	Ask                   []string `json:"ask,omitempty"`
	DefaultMode           string   `json:"defaultMode,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
}

// PrivateMCPRel is private to one attempt/session and lives in Lectern's
// per-user state directory rather than the Git checkout. The nonce prevents
// collisions across restarts and across multiple control-plane instances.
func PrivateMCPRel(id int64, nonce string) string {
	return fmt.Sprintf("lectern/mcp/%d-%s/mcp.json", id, nonce)
}

// InteractiveMCPRel is private to one interactive session.
func InteractiveMCPRel(sessionID int64, nonce string) string {
	return PrivateMCPRel(sessionID, nonce)
}

// TaskMCPRel is private to one background attempt.
func TaskMCPRel(attemptID int64, nonce string) string { return PrivateMCPRel(attemptID, nonce) }

func (p Permissions) isEmpty() bool {
	return len(p.Allow) == 0 && len(p.Deny) == 0 && len(p.Ask) == 0 &&
		p.DefaultMode == "" && len(p.AdditionalDirectories) == 0
}

// ParsePermissions decodes a project's permissions_json, rejecting unknown keys.
func ParsePermissions(raw string) (Permissions, error) {
	var p Permissions
	if strings.TrimSpace(raw) == "" {
		return p, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return p, fmt.Errorf("permissions must be an object: %w", err)
	}
	var unknown []string
	for k, v := range probe {
		if !contains(PermissionKeys, k) {
			unknown = append(unknown, k)
			continue
		}
		// an empty list is the same as not setting the key at all
		if string(v) == "[]" || string(v) == `""` || string(v) == "null" {
			delete(probe, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return p, fmt.Errorf("unknown permission keys: %v", unknown)
	}
	clean, _ := json.Marshal(probe)
	if err := json.Unmarshal(clean, &p); err != nil {
		return p, err
	}
	return p, nil
}

// ParityPermissions are the rules that put a dispatched agent on par with a
// terminal session.
//
// MCP servers are enumerated rather than wildcarded because there is no
// wildcard: `mcp__*` is accepted into settings.json and still denies every call
// (verified against the CLI). Each server needs its own `mcp__<name>` rule.
func ParityPermissions(mcpServers, extraDirs []string) Permissions {
	allow := append([]string(nil), ParityTools...)
	seen := map[string]bool{}
	names := append([]string(nil), mcpServers...)
	sort.Strings(names)
	for _, s := range names {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		allow = append(allow, "mcp__"+s)
	}
	p := Permissions{Allow: allow}
	for _, d := range dedupe(extraDirs) {
		if d != "" {
			p.AdditionalDirectories = append(p.AdditionalDirectories, d)
		}
	}
	return p
}

// MergePermissions layers explicit project rules on top of a profile: lists are
// unioned, scalars overridden. The explicit rules win where they conflict, so a
// profile can never quietly re-grant something an operator denied.
func MergePermissions(base, override Permissions) Permissions {
	out := Permissions{
		Allow: dedupe(append(append([]string(nil), base.Allow...), override.Allow...)),
		Deny:  dedupe(append(append([]string(nil), base.Deny...), override.Deny...)),
		Ask:   dedupe(append(append([]string(nil), base.Ask...), override.Ask...)),
		AdditionalDirectories: dedupe(append(append([]string(nil), base.AdditionalDirectories...),
			override.AdditionalDirectories...)),
		DefaultMode: base.DefaultMode,
	}
	if override.DefaultMode != "" {
		out.DefaultMode = override.DefaultMode
	}
	if len(out.Deny) > 0 { // an explicit deny beats an inherited allow
		denied := map[string]bool{}
		for _, d := range out.Deny {
			denied[d] = true
		}
		kept := out.Allow[:0]
		for _, a := range out.Allow {
			if !denied[a] {
				kept = append(kept, a)
			}
		}
		out.Allow = kept
	}
	return out
}

// SettingsInput is everything build-settings needs to know about one attempt.
type SettingsInput struct {
	BaseURL     string
	Token       string
	Gated       bool
	Permissions Permissions
	Matcher     string
	Profile     string
	MCPServers  []string
	MemoryDir   string
	// ExpireSeconds sizes the hook's own timeout.
	ExpireSeconds int
}

// BuildSettings renders the settings.json every attempt gets: permission rules
// always, the approval hook when gated.
//
// It is written unconditionally (it used to be gated-mode only) because without
// it an acceptEdits run cannot be granted Bash at all: headless has no prompt,
// so any tool needing permission is denied with no signal to the operator.
//
// MemoryDir goes into additionalDirectories whenever it is set: the symlink that
// shares a memory store lands OUTSIDE the worktree, and the filesystem sandbox
// refuses paths outside it — so without this the store is linked and then
// unreadable, which is worse than not sharing it at all.
func BuildSettings(in SettingsInput) (map[string]any, error) {
	profile := in.Profile
	if profile == "" {
		profile = "restricted"
	}
	if !contains(CapabilityProfiles, profile) {
		return nil, fmt.Errorf("unknown capability profile %q", profile)
	}
	perms := in.Permissions
	switch {
	case profile == "parity":
		var dirs []string
		if in.MemoryDir != "" {
			dirs = []string{in.MemoryDir}
		}
		perms = MergePermissions(ParityPermissions(in.MCPServers, dirs), perms)
	case in.MemoryDir != "":
		perms = MergePermissions(
			Permissions{AdditionalDirectories: []string{in.MemoryDir}}, perms)
	}

	settings := map[string]any{}
	if !perms.isEmpty() {
		settings["permissions"] = perms
	}
	if in.Gated {
		for k, v := range HookSettings(in.BaseURL, in.Token, in.Matcher, in.ExpireSeconds) {
			settings[k] = v
		}
	}
	return settings, nil
}

// HookSettings is the PreToolUse gate; only used when permission mode is
// 'default'.
func HookSettings(baseURL, token, matcher string, expireSeconds int) map[string]any {
	if matcher == "" {
		matcher = DefaultGateMatcher
	}
	if expireSeconds <= 0 {
		expireSeconds = 900
	}
	cmd := fmt.Sprintf("LECTERN_URL=%s LECTERN_TOKEN=%s python3 .lectern/hook.py",
		baseURL, token)
	return map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{map[string]any{
			"matcher": matcher,
			"hooks": []any{map[string]any{
				"type": "command", "command": cmd, "timeout": expireSeconds + 30,
			}},
		}},
	}}
}

// HostMCPServers lists the user-scope MCP server names the control plane's own
// Claude Code has.
//
// An agent on a LOCAL target runs as the same user and inherits these servers
// already — it simply cannot call one without a matching permission rule, which
// is the whole gap the parity profile closes. Remote targets have none of them,
// and these definitions are NOT portable anyway: they point at local binaries
// and carry live secrets in their env blocks, so they are never copied.
func HostMCPServers(configPath string) []string {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil // no host config is normal (containers, CI) — not an error
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	out := make([]string, 0, len(cfg.MCPServers))
	for k := range cfg.MCPServers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RuntimeDir is where every attempt's staged files live inside its worktree.
func RuntimeDir(worktree string) string { return worktree + "/.lectern" }

var nonSlug = regexp.MustCompile(`[^A-Za-z0-9-]`)

// ProjectSlug is Claude Code's per-cwd session directory name under
// ~/.claude/projects. Every non-alphanumeric character collapses to '-'
// (verified empirically against the running CLI). This mirrors an internal
// layout, not a public API — which is why memory sharing is opt-in per target.
func ProjectSlug(path string) string { return nonSlug.ReplaceAllString(path, "-") }

// MemoryLinkCommand points this attempt's session memory at a shared store.
//
// A fresh worktree per attempt starts memory-blind and everything an agent
// learns dies with the worktree. There is no CLI flag for this, so the only
// mechanism is symlinking the session's memory dir at a store the operator
// nominates.
//
// Memory is keyed to the git MAIN worktree, not to cwd — verified against the
// CLI by running it inside a linked worktree under /tmp and asking for its own
// memory path, which came back as the parent repo's slug. Sessions are keyed by
// cwd, so the two diverge, and linking the cwd slug (what this did before)
// created a symlink the agent never opened. The main worktree is resolved on the
// target rather than guessed here, so worktrees, plain clones and sandbox
// checkouts all land on the same answer.
//
// A real non-empty directory at the link path is left ALONE: that path can be a
// repo the operator also works in interactively, and silently deleting their
// memories to install a symlink is not a trade worth making.
func MemoryLinkCommand(worktree, memoryDir string) string {
	wt, tgt := shellQuote(worktree), shellQuote(memoryDir)
	return "root=$(git -C " + wt + " rev-parse --path-format=absolute --git-common-dir " +
		"2>/dev/null) || root=\"\"; " +
		"root=${root%/.git}; [ -n \"$root\" ] || root=" + wt + "; " +
		"slug=$(printf %s \"$root\" | sed 's/[^A-Za-z0-9-]/-/g'); " +
		"proj=\"$HOME/.claude/projects/$slug\"; link=\"$proj/memory\"; " +
		"mkdir -p \"$proj\" " + tgt + " && " +
		"if [ -L \"$link\" ] || [ ! -e \"$link\" ]; then ln -sfn " + tgt + " \"$link\"; " +
		"elif [ -z \"$(ls -A \"$link\" 2>/dev/null)\" ]; then " +
		"rm -rf \"$link\" && ln -sfn " + tgt + " \"$link\"; " +
		"else echo \"refusing to replace non-empty memory dir $link\" >&2; exit 3; fi"
}

// shellQuote renders one shell word, quoting only when the value could
// otherwise be reinterpreted as syntax.
func shellQuote(s string) string { return shellq.Quote(s) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	out := list[:0:0]
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
