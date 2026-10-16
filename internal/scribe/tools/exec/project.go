package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
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
	Action    string `json:"action"`
	Target    string `json:"target"`
	Args      string `json:"args"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (t *projectTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "project",
		Description: "Run this project's own test, lint, typecheck, build, format or run command. " +
			"The toolchain is detected from the files in the project root (Go, Rust, Node with npm/pnpm/yarn/bun, Python, .NET, PHP, Swift, Xcode, Dart/Flutter, Elixir, Ruby, Gradle, Maven, CMake, Make, Zig, Haskell, Scala, Clojure, Deno). " +
			"Use action list to see the detected commands. Failing tests and error locations are reported. " +
			"Prefer this to shell for checks: it knows the right command for the project.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("What to run.", "test", "lint", "typecheck", "build", "format", "run", "list"),
			"target":     tools.Str("Optional file, package or test filter (test, lint), or script/target name (run)."),
			"args":       tools.Str("Optional extra arguments appended to the command, e.g. \"-run TestFoo -v\"."),
			"timeout_ms": tools.Int("Timeout in milliseconds (default 600000, max 600000)."),
		}, "action"),
	}
}

func (t *projectTool) ReadOnly() bool { return false }

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
	if !isAction(action) {
		return tools.Errorf("unknown action %q. Use one of: test, lint, typecheck, build, format, run, list.", a.Action)
	}

	cmd := commandFor(profile, action, strings.TrimSpace(a.Target))
	if cmd == "" {
		return tools.Result{
			Output:  fmt.Sprintf("No %s command detected for this project. Use shell to run it manually.", action),
			IsError: true,
			Summary: "no " + action + " command",
			Meta:    map[string]any{"error": "no command", "ecosystem": profile.Ecosystem},
		}
	}
	if extra := strings.TrimSpace(a.Args); extra != "" {
		cmd += " " + extra
	}

	if res, blocked := gate(ctx, env, t.jobs.opts, "project", cmd, ""); blocked {
		return res
	}

	timeout := clampTimeout(a.TimeoutMS, projectTimeout)
	fg := runForeground(ctx, env, cmd, timeout, []string{"FORCE_COLOR=0", "NO_COLOR=1"})
	return projectResult(action, cmd, fg, timeout)
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
