// Package hooks runs user hook commands on agent events. Hook definitions
// come from Scribe's config files and from Claude Code-compatible
// settings.json files, so existing Claude hooks work unchanged.
//
// Trust: the user's own files (global Scribe config and ~/.claude/settings.json)
// always run. Anything inside a project (its .scribe/config.json and
// .claude/settings*.json) runs only when the project is trusted.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Event names, as used in both config and Claude settings files.
const (
	evPreTool      = "PreToolUse"
	evPostTool     = "PostToolUse"
	evPrompt       = "UserPromptSubmit"
	evStop         = "Stop"
	evSessionStart = "SessionStart"
	evSessionEnd   = "SessionEnd"
	evPreCompact   = "PreCompact"
	evNotification = "Notification"
)

const (
	defaultTimeout = 10 * time.Second
	maxStdout      = 256 << 10
)

// pipeMatcher is a plain name or a pipe list, matched by exact name.
var pipeMatcher = regexp.MustCompile(`^[A-Za-z0-9_|]+$`)

// claudeNames maps Scribe tool names to the Claude Code names hooks are
// written against. Aliases list extra Claude names a tool answers to.
var claudeNames = map[string]string{
	"shell":      "Bash",
	"edit_file":  "Edit",
	"multi_edit": "MultiEdit",
	"write_file": "Write",
	"read":       "Read",
	"list_dir":   "ListDirectory",
	"grep":       "Grep",
	"glob":       "Glob",
	"git":        "Git",
	"web_search": "WebSearch",
	"fetch_page": "WebFetch",
	"dispatch":   "Agent",
	"ask_user":   "AskFollowupQuestion",
	"memory":     "Memory",
}

var claudeAliases = map[string][]string{
	"multi_edit": {"Edit"},
	"write_file": {"Edit"},
}

// entry is one hook command with its matcher compiled.
type entry struct {
	event   string
	matcher string
	re      *regexp.Regexp // nil when the matcher is a name list or empty
	command string
	timeout time.Duration
	source  string    // where it came from, for warnings
	async   bool      // fire and forget: its result is ignored
	once    bool      // runs at most once per process
	cond    *toolCond // the `if` condition, nil when there is none
	status  string    // statusMessage, shown while it runs
}

func newEntry(event, matcher, command string, secs int, source string) (entry, error) {
	e := entry{event: event, matcher: matcher, command: command, timeout: timeoutOf(secs), source: source}
	if matcher == "" || matcher == "*" || pipeMatcher.MatchString(matcher) {
		return e, nil
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return e, fmt.Errorf("hook from %s: bad matcher %q skipped: %v", source, matcher, err)
	}
	e.re = re
	return e, nil
}

// matches reports whether the entry applies to a tool with these names.
func (e entry) matches(names []string) bool {
	switch {
	case e.matcher == "" || e.matcher == "*":
		return true
	case e.re != nil:
		for _, n := range names {
			if e.re.MatchString(n) {
				return true
			}
		}
		return false
	default:
		for _, part := range strings.Split(e.matcher, "|") {
			for _, n := range names {
				if n == part {
					return true
				}
			}
		}
		return false
	}
}

// toolNames returns the names a matcher is compared with: the Scribe name,
// its Claude Code name and any aliases.
func toolNames(tool string) []string {
	names := []string{tool}
	if c, ok := claudeNames[tool]; ok {
		names = append(names, c)
	}
	return append(names, claudeAliases[tool]...)
}

// Options configures a Runner. Use NewFromDisk for the normal case.
type Options struct {
	Root      string // project root; hooks run here
	SessionID string
	// ConfigGlobal and ConfigProject are the hooks arrays of the Scribe
	// global and project config files (see LoadConfigHooks).
	ConfigGlobal  []config.Hook
	ConfigProject []config.Hook
	// UserSettings is a Claude settings.json (default ~/.claude/settings.json).
	UserSettings string
	// Trusted allows project-level hooks (project config, repo settings).
	Trusted bool
}

// Runner runs hooks. It implements agent.Hooks. Methods are safe for
// concurrent use.
type Runner struct {
	root      string
	sessionID string
	entries   []entry // hooks that run, in order

	mu       sync.Mutex
	warnings []string
	fired    map[string]bool // once-hooks that have been taken
}

var _ agent.Hooks = (*Runner)(nil)

// NewFromDisk builds a Runner from the config files, ~/.claude/settings.json
// and the project's trust state.
func NewFromDisk(root, sessionID string) (*Runner, error) {
	g, p, err := LoadConfigHooks(root)
	if err != nil {
		return nil, err
	}
	return New(Options{
		Root: root, SessionID: sessionID,
		ConfigGlobal: g, ConfigProject: p,
		Trusted: Trusted(root),
	}), nil
}

// New builds a Runner from explicit options.
func New(o Options) *Runner {
	r := &Runner{root: o.Root, sessionID: o.SessionID, fired: map[string]bool{}}
	add := func(es []entry, ws []string) {
		r.entries = append(r.entries, es...)
		r.warnings = append(r.warnings, ws...)
	}
	us := o.UserSettings
	if us == "" {
		us = userSettingsPath()
	}
	// The user's own files always run.
	ges, gws := fromConfig(o.ConfigGlobal, "scribe config")
	add(ges, gws)
	if es, ws, err := readClaudeSettingsSafe(us, "user settings"); err == nil {
		add(es, ws)
	} else {
		r.warnings = append(r.warnings, err.Error())
	}

	// Project files run only when the project is trusted.
	projectEntries, projectWarns := fromConfig(o.ConfigProject, "project config")
	if o.Root != "" {
		for _, f := range []string{"settings.json", "settings.local.json"} {
			es, ws, err := readClaudeSettingsSafe(filepath.Join(o.Root, ".claude", f), filepath.Join(".claude", f))
			if err != nil {
				projectWarns = append(projectWarns, err.Error())
				continue
			}
			projectEntries = append(projectEntries, es...)
			projectWarns = append(projectWarns, ws...)
		}
	}
	if len(projectEntries) > 0 && !o.Trusted {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%d project hook(s) in %s are not run: the project is not trusted yet", len(projectEntries), o.Root))
	} else {
		add(projectEntries, projectWarns)
	}
	return r
}

func readClaudeSettingsSafe(path, source string) ([]entry, []string, error) {
	es, ws, err := readClaudeSettings(path, source)
	if err != nil {
		return nil, nil, fmt.Errorf("hooks: cannot read %s: %v", source, err)
	}
	return es, ws, nil
}

// Warnings returns and clears the warnings collected so far: skipped
// hooks, non-zero exits, timeouts. The UI shows them.
func (r *Runner) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.warnings
	r.warnings = nil
	return w
}

// Count reports how many hooks will run.
func (r *Runner) Count() int { return len(r.entries) }

func (r *Runner) warn(format string, a ...any) {
	r.mu.Lock()
	r.warnings = append(r.warnings, fmt.Sprintf(format, a...))
	r.mu.Unlock()
}

// outcome is what one hook said.
type outcome struct {
	block   string          // non-empty: refuse
	context string          // extra text
	input   json.RawMessage // PreToolUse: replacement tool input (updatedInput)
}

// result of running a hook process.
type result struct {
	code     int
	stdout   []byte
	stderr   []byte
	timedOut bool
	err      error
}

// PreTool implements agent.Hooks. Sequential; the first block wins.
func (r *Runner) PreTool(ctx context.Context, tool string, args json.RawMessage) string {
	names := toolNames(tool)
	in := r.input(evPreTool)
	in["tool_name"] = displayName(tool)
	in["tool_input"] = rawObject(args)
	for _, e := range r.entries {
		if e.event != evPreTool || !e.matches(names) || !r.take(e) || !e.cond.ok(displayName(tool), args) {
			continue
		}
		o := r.exec(ctx, e, in)
		if o.block != "" {
			return o.block
		}
	}
	return ""
}

// PreToolUpdate implements the agent's toolInputRewriter. It runs the same
// PreToolUse hooks as PreTool, in order, and a hook's
// hookSpecificOutput.updatedInput replaces the tool input for the hooks after
// it and for the call. The first block wins, and the input returned is the
// one the call runs with. An updatedInput that is not a JSON object is
// ignored with a warning, since a tool takes an object of named arguments.
func (r *Runner) PreToolUpdate(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, string) {
	names := toolNames(tool)
	for _, e := range r.entries {
		if e.event != evPreTool || !e.matches(names) || !r.take(e) || !e.cond.ok(displayName(tool), args) {
			continue
		}
		in := r.input(evPreTool)
		in["tool_name"] = displayName(tool)
		in["tool_input"] = rawObject(args)
		o := r.exec(ctx, e, in)
		if o.block != "" {
			return args, o.block
		}
		if len(o.input) == 0 {
			continue
		}
		if !isJSONObject(o.input) {
			r.warn("hook %q from %s returned an updatedInput that is not a JSON object; the input was kept", e.command, e.source)
			continue
		}
		args = o.input
	}
	return args, ""
}

// isJSONObject reports whether b is a single JSON object.
func isJSONObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

// PostTool implements agent.Hooks. Every matching hook runs; their extra
// text is joined. Blocks are not meaningful after a call and are ignored.
func (r *Runner) PostTool(ctx context.Context, tool string, args json.RawMessage, res tools.Result) string {
	names := toolNames(tool)
	in := r.input(evPostTool)
	in["tool_name"] = displayName(tool)
	in["tool_input"] = rawObject(args)
	in["tool_response"] = map[string]any{"output": res.Output, "is_error": res.IsError}
	var extra []string
	for _, e := range r.entries {
		if e.event != evPostTool || !e.matches(names) || !r.take(e) || !e.cond.ok(displayName(tool), args) {
			continue
		}
		if o := r.exec(ctx, e, in); o.context != "" {
			extra = append(extra, o.context)
		}
	}
	return strings.Join(extra, "\n")
}

// Prompt implements agent.Hooks.
func (r *Runner) Prompt(ctx context.Context, prompt string) (extra, block string) {
	in := r.input(evPrompt)
	in["prompt"] = prompt
	var extras []string
	for _, e := range r.entries {
		if e.event != evPrompt || !r.take(e) {
			continue
		}
		o := r.exec(ctx, e, in)
		if o.block != "" && block == "" {
			block = o.block
		}
		if o.context != "" {
			extras = append(extras, o.context)
		}
	}
	return strings.Join(extras, "\n"), block
}

// Stop implements agent.Hooks. All hooks run; blocks are ignored.
func (r *Runner) Stop(ctx context.Context, stats agent.TurnStats) {
	in := r.input(evStop)
	in["stats"] = map[string]any{
		"steps": stats.Steps, "tool_calls": stats.ToolCalls, "files_edited": stats.FilesEdited,
		"error": stats.Error, "aborted": stats.Aborted,
	}
	r.fireAll(ctx, evStop, in)
}

// SessionStart runs SessionStart hooks.
func (r *Runner) SessionStart(ctx context.Context) {
	r.fireAll(ctx, evSessionStart, r.input(evSessionStart))
}

// SessionEnd runs SessionEnd hooks.
func (r *Runner) SessionEnd(ctx context.Context) { r.fireAll(ctx, evSessionEnd, r.input(evSessionEnd)) }

// PreCompact runs PreCompact hooks before the history is compacted.
func (r *Runner) PreCompact(ctx context.Context) { r.fireAll(ctx, evPreCompact, r.input(evPreCompact)) }

// Notification runs Notification hooks with msg.
func (r *Runner) Notification(ctx context.Context, msg string) {
	in := r.input(evNotification)
	in["message"] = msg
	r.fireAll(ctx, evNotification, in)
}

// fireAll runs every hook for an event that is not a tool event. Matchers
// do not apply to these events.
func (r *Runner) fireAll(ctx context.Context, event string, in map[string]any) {
	for _, e := range r.entries {
		if e.event == event && r.take(e) {
			r.exec(ctx, e, in)
		}
	}
}

// take claims the one run of a once-hook. It is called before the hook's
// `if` is tested, so a hook whose condition fails has still used its shot
// (spec 7.5). Ordinary hooks are always taken.
func (r *Runner) take(e entry) bool {
	if !e.once {
		return true
	}
	key := e.event + "\x00" + e.command
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fired[key] {
		return false
	}
	r.fired[key] = true
	return true
}

func (r *Runner) input(event string) map[string]any {
	return map[string]any{
		"session_id":      r.sessionID,
		"cwd":             r.root,
		"hook_event_name": event,
	}
}

// displayName is the Claude-style name for a tool, or the name itself.
func displayName(tool string) string {
	if c, ok := claudeNames[tool]; ok {
		return c
	}
	return tool
}

// rawObject turns tool arguments into a JSON value for stdin, always an object.
func rawObject(args json.RawMessage) any {
	if len(args) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil || v == nil {
		return map[string]any{}
	}
	return v
}

// exec runs one hook and interprets its result.
func (r *Runner) exec(ctx context.Context, e entry, in map[string]any) outcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if e.async {
		// Fire and forget: the run outlives the event, with its own timeout,
		// and whatever it returns is discarded.
		bg := e
		bg.async = false
		go r.exec(context.WithoutCancel(ctx), bg, in)
		return outcome{}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		r.warn("hook %q: cannot encode input: %v", e.command, err)
		return outcome{}
	}
	res := r.run(ctx, e, payload)
	if res.timedOut {
		r.warn("hook %q from %s timed out after %s and was killed", e.command, e.source, e.timeout)
		return outcome{}
	}
	if res.err != nil {
		r.warn("hook %q from %s did not start: %v", e.command, e.source, res.err)
		return outcome{}
	}
	switch res.code {
	case 0:
		return parseStdout(e, res.stdout, in["hook_event_name"].(string))
	case 2:
		reason := ""
		if j := parseJSON(res.stdout); j != nil {
			reason = j.Reason
		}
		if reason == "" {
			reason = strings.TrimSpace(string(res.stderr))
		}
		if reason == "" {
			reason = fmt.Sprintf("hook %q blocked this action", e.command)
		}
		return outcome{block: reason}
	default:
		msg := strings.TrimSpace(string(res.stderr))
		if msg == "" {
			msg = "no stderr"
		}
		r.warn("hook %q from %s exited %d (not blocking): %s", e.command, e.source, res.code, firstLine(msg))
		return outcome{}
	}
}

// hookJSON is the subset of a hook's JSON stdout we honour.
type hookJSON struct {
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
	Continue     *bool  `json:"continue"`
	StopReason   string `json:"stopReason"`
	HookSpecific *struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		AdditionalContext        string          `json:"additionalContext"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
}

func parseJSON(b []byte) *hookJSON {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || t[0] != '{' {
		return nil
	}
	var j hookJSON
	if err := json.Unmarshal(t, &j); err != nil {
		return nil
	}
	return &j
}

// parseStdout interprets a hook that exited 0. JSON is honoured; plain text
// is extra context for the events where that means something.
func parseStdout(e entry, out []byte, event string) outcome {
	if j := parseJSON(out); j != nil {
		var o outcome
		switch {
		case j.Decision == "block":
			o.block = firstNonEmpty(j.Reason, "blocked by hook")
		case j.Continue != nil && !*j.Continue:
			o.block = firstNonEmpty(j.StopReason, j.Reason, "stopped by hook")
		case j.HookSpecific != nil && j.HookSpecific.PermissionDecision == "deny":
			o.block = firstNonEmpty(j.HookSpecific.PermissionDecisionReason, j.Reason, "denied by hook")
		}
		if j.HookSpecific != nil {
			o.context = j.HookSpecific.AdditionalContext
			o.input = j.HookSpecific.UpdatedInput
		}
		return o
	}
	if event == evPrompt || event == evPostTool {
		return outcome{context: strings.TrimSpace(string(out))}
	}
	return outcome{}
}

// run starts the hook's shell command with a minimal environment and
// returns once it exits or its timeout kills it.
func (r *Runner) run(ctx context.Context, e entry, stdin []byte) result {
	cctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	name, args := shellCommand(e.command)
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = r.root
	cmd.Env = hookEnv(r.root, r.sessionID)
	cmd.Stdin = bytes.NewReader(stdin)
	setProcGroup(cmd)
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	var out, errb capBuf
	out.max, errb.max = maxStdout, maxStdout
	cmd.Stdout, cmd.Stderr = &out, &errb

	err := cmd.Run()
	res := result{stdout: out.b.Bytes(), stderr: errb.b.Bytes()}
	if cctx.Err() == context.DeadlineExceeded {
		res.timedOut = true
		return res
	}
	if err == nil {
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.code = ee.ExitCode()
		return res
	}
	res.err = err
	return res
}

// capBuf keeps the first max bytes written and discards the rest.
type capBuf struct {
	b   bytes.Buffer
	max int
}

func (c *capBuf) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

// hookEnv is a scrubbed environment: an allowlist of inherited variables
// plus the project directory markers hooks expect.
func hookEnv(root, sessionID string) []string {
	var env []string
	for _, k := range inheritedEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"SCRIBE_PROJECT_DIR="+root,
		"CLAUDE_PROJECT_DIR="+root,
		"SCRIBE_SESSION_ID="+sessionID,
	)
	return env
}

// inheritedEnv is what a hook may see from the parent process. Secrets such
// as API keys are not on the list.
var inheritedEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TERM",
	"TMPDIR", "TMP", "TEMP", "SHELL", "SystemRoot", "SYSTEMROOT", "USERPROFILE",
	"COMSPEC", "PATHEXT", "WINDIR", "APPDATA", "LOCALAPPDATA",
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
