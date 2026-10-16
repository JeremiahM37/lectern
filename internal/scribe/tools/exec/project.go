package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// projectTimeout is the default for project commands, which are slower than
// ordinary shell calls.
const projectTimeout = 10 * time.Minute

type projectTool struct{ jobs *Jobs }

type projectArgs struct {
	Action    string            `json:"action"`
	Target    string            `json:"target"`
	Args      string            `json:"args"`
	Fix       bool              `json:"fix"`
	Env       map[string]string `json:"env"`
	TimeoutMS int               `json:"timeout_ms"`
}

func (t *projectTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "project",
		Description: "Run this project's own test, lint, typecheck, build, format or run command. " +
			"The toolchain is detected from the files in the project root (Go, Rust, Node with npm/pnpm/yarn/bun, Python, .NET, PHP, Swift, Xcode, Dart/Flutter, Elixir, Ruby, Gradle, Maven, CMake, Make, Zig, Haskell, Scala, Clojure, Deno). " +
			"Use action list to see the detected commands. Failing tests and error locations are reported. " +
			"Action check runs typecheck, then lint, then test and gives one summary; lint warnings fail it. " +
			"fix: true applies the linter's auto-fix (lint, or the lint stage of check) where the toolchain has one. " +
			"Prefer this to shell for checks: it knows the right command for the project.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("What to run. check runs typecheck, lint and test in order.", "check", "test", "lint", "typecheck", "build", "format", "run", "list"),
			"target":     tools.Str("Optional file, package or test filter (test, lint), or script/target name (run)."),
			"args":       tools.Str("Optional extra arguments appended to the command, e.g. \"-run TestFoo -v\"."),
			"fix":        tools.Bool("lint: apply the linter's auto-fix (e.g. eslint --fix, ruff check --fix). check: apply it to the lint stage. Changes files."),
			"env":        map[string]any{"type": "object", "description": "Extra environment variables for the command, as name: value. FORCE_COLOR and NO_COLOR are always set.", "additionalProperties": map[string]any{"type": "string"}},
			"timeout_ms": tools.Int("Timeout in milliseconds (default 600000, max 1800000 = 30 min; per stage for check)."),
		}, "action"),
	}
}

func (t *projectTool) ReadOnly() bool { return false }

// ReadOnlyCall reports whether a call only reads the project. check is
// read-only unless fix is set; fix changes files, so it is not.
func (t *projectTool) ReadOnlyCall(raw json.RawMessage) bool {
	var a projectArgs
	if tools.Decode(raw, &a) != nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(a.Action)) == "check" && !a.Fix
}

func (t *projectTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var a projectArgs
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if env == nil {
		return tools.Errorf("project needs a project environment; this call has none")
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	profile := Detect(env.Workdir)

	if action == "list" {
		return listResult(profile, env.Workdir)
	}
	if action == "check" {
		return t.runCheck(ctx, env, a, profile)
	}
	if !isAction(action) {
		return tools.Errorf("unknown action %q. Use one of: check, test, lint, typecheck, build, format, run, list.", a.Action)
	}

	target := strings.TrimSpace(a.Target)
	cmd := commandFor(profile, action, target)
	if cmd == "" {
		return tools.Result{
			Output:  fmt.Sprintf("No %s command detected for this project. Use shell to run it manually.", action),
			IsError: true,
			Summary: "no " + action + " command",
			Meta:    map[string]any{"error": "no command", "ecosystem": profile.Ecosystem},
		}
	}
	if a.Fix {
		if action != "lint" {
			return tools.Errorf("fix applies to lint and check only. Use the format action to rewrite files.")
		}
		fixed, ok := fixCommand(profile.Commands[action])
		if !ok {
			return tools.Errorf("no auto-fix is known for %q. Run lint without fix to see the findings, then fix them by hand.", profile.Commands[action])
		}
		cmd = commandFor(Profile{Ecosystem: profile.Ecosystem, Manager: profile.Manager, Commands: map[string]string{action: fixed}}, action, target)
	}
	if extra := strings.TrimSpace(a.Args); extra != "" {
		cmd += " " + extra
	}

	if res, blocked := gate(ctx, env, t.jobs.opts, "project", cmd, ""); blocked {
		return res
	}

	extra, err := envPairs(a.Env)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	timeout := clampTimeout(a.TimeoutMS, t.jobs.defaultTimeout(projectTimeout))
	fg := runForeground(ctx, env, cmd, timeout, append([]string{"FORCE_COLOR=0", "NO_COLOR=1"}, extra...))
	return projectResult(action, cmd, fg, timeout)
}

// envPairs turns the env argument into NAME=value pairs. Names must be plain
// identifiers; the scrubbed credential variables cannot be set back.
func envPairs(env map[string]string) ([]string, error) {
	var out []string
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00 \t") {
			return nil, fmt.Errorf("env name %q is not a valid variable name", k)
		}
		if !keepEnv(k) {
			return nil, fmt.Errorf("env %s cannot be set: credential variables are removed from the environment", k)
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out, nil
}

// commandFor returns the command for action, with target applied. For run,
// a target names a script (node) or a Makefile target.
func commandFor(p Profile, action, target string) string {
	cmd := p.Commands[action]
	if target == "" {
		return cmd
	}
	switch {
	case action == "run" && p.Ecosystem == "make":
		return "make " + shellQuote(target)
	case action == "run" && p.Manager != "":
		if p.Manager == "npm" {
			return "npm run " + shellQuote(target)
		}
		return p.Manager + " run " + shellQuote(target)
	case cmd == "":
		return ""
	}
	return cmd + " " + shellQuote(target)
}

// shellQuote quotes s for the platform shell: single quotes for POSIX sh and
// bash, double quotes for cmd.exe and PowerShell.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!&;|<>()*?[]{}~#") {
		return s
	}
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isAction(a string) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

func listResult(p Profile, dir string) tools.Result {
	if p.Ecosystem == "" {
		return tools.Result{
			Output:  fmt.Sprintf("No project toolchain detected in %s. Use shell to run commands manually.", dir),
			Summary: "no toolchain",
			Meta:    map[string]any{"ecosystem": ""},
		}
	}
	var b strings.Builder
	head := p.Ecosystem
	if p.Manager != "" {
		head += " (" + p.Manager + ")"
	}
	fmt.Fprintf(&b, "Detected: %s\n", head)
	for _, action := range Actions {
		if c, ok := p.Commands[action]; ok {
			fmt.Fprintf(&b, "  %-9s %s\n", action, c)
		} else {
			fmt.Fprintf(&b, "  %-9s (none)\n", action)
		}
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		Summary: p.Ecosystem,
		Meta:    map[string]any{"ecosystem": p.Ecosystem, "manager": p.Manager, "commands": p.Commands},
	}
}

// projectResult summarises a finished project command: pass or fail, the
// failing tests and the error locations.
func projectResult(action, cmd string, fg foreground, timeout time.Duration) tools.Result {
	if fg.err != nil {
		return tools.Errorf("could not start %s: %v", action, fg.err)
	}
	diags, failed := parseDiagnostics(fg.raw)
	body := strings.TrimRight(fg.body, "\n")
	if strings.TrimSpace(body) == "" {
		body = "(no output)"
	}
	header := fmt.Sprintf("[%s] %s\n%s\n", action, cmd, body)
	meta := map[string]any{
		"errors":  diags,
		"failed":  failed,
		"action":  action,
		"command": cmd,
		"lines":   fg.lines,
	}

	if fg.timedOut || fg.cancelled || fg.exit != 0 {
		var verdict string
		if fg.timedOut || fg.cancelled {
			verdict = footerFor(fg, timeout, action)
		} else {
			verdict = fmt.Sprintf("— failed (exit %d).", fg.exit)
		}
		summary := fmt.Sprintf("%s failed (%d errors)", action, len(diags))
		if len(diags) == 0 {
			summary = action + " failed"
		}
		if fg.timedOut {
			summary = action + " timed out"
		}
		meta["passed"] = false
		meta["exit_code"] = fg.exit
		return tools.Result{
			Output:  header + verdict,
			IsError: true,
			Summary: summary,
			Meta:    meta,
		}
	}

	meta["passed"] = true
	meta["exit_code"] = 0
	return tools.Result{
		Output:  header + "— passed",
		Summary: action + " passed",
		Meta:    meta,
	}
}
