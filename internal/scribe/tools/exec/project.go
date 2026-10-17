package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// projectTimeout is the default for project commands (spec 06 section 9.2:
// config toolTimeout, default 2 minutes).
const projectTimeout = 2 * time.Minute

// Project output is capped at 10,000 characters: the first 3,000 and the last
// 5,000 are kept and the rest is cut, with the full text in a tee file.
const (
	projectHeadBytes = 3000
	projectTailBytes = 5000
)

type projectTool struct{ jobs *Jobs }

type projectArgs struct {
	Action    string            `json:"action"`
	Target    string            `json:"target"`
	Args      string            `json:"args"`
	Fix       bool              `json:"fix"`
	Env       map[string]string `json:"env"`
	TimeoutMS int               `json:"timeout_ms"`
	Cwd       string            `json:"cwd"`
}

func (t *projectTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "project",
		Description: "Run this project's own test, lint, typecheck, build, format or run command. " +
			"The toolchain is detected from the files in the project root (Go, Rust, Node with npm/pnpm/yarn/bun, Python, .NET, PHP, Swift, Xcode, Dart/Flutter, Elixir, Ruby, Gradle, Maven, CMake, Make, Zig, Haskell, Scala, Clojure, Deno). " +
			"Use action list to see the detected commands; in a monorepo it also lists the workspace packages, and cwd (a package directory relative to the project root) runs the command for one package. Failing tests and error locations are reported. " +
			"Action check runs typecheck, then lint, then test and gives one summary; lint warnings fail it. " +
			"fix: true applies the linter's auto-fix (lint, or the lint stage of check) where the toolchain has one. " +
			"Prefer this to shell for checks: it knows the right command for the project.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("What to run. check runs typecheck, lint and test in order.", "check", "test", "lint", "typecheck", "build", "format", "run", "list"),
			"target":     tools.Str("Optional file, package or test filter (test, lint), or script/target name (run)."),
			"args":       tools.Str("Optional extra arguments appended to the command, e.g. \"-run TestFoo -v\"."),
			"fix":        tools.Bool("lint: apply the linter's auto-fix (e.g. eslint --fix, ruff check --fix). check: apply it to the lint stage. Changes files."),
			"env":        map[string]any{"type": "object", "description": "Extra environment variables for the command, as name: value. FORCE_COLOR and NO_COLOR are always set.", "additionalProperties": map[string]any{"type": "string"}},
			"timeout_ms": tools.Int("Timeout in milliseconds (default 120000 = 2 min, max 1800000 = 30 min; per stage for check)."),
			"cwd":        tools.Str("Optional package directory relative to the project root, e.g. \"packages/api\" in a monorepo. The command runs there and the project is detected there."),
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
	dir, err := resolveCwd(env.Workdir, a.Cwd)
	if err != nil {
		return tools.Errorf("%v. In a monorepo, list the packages with action list, then pass one as cwd.", err)
	}
	if dir != env.Workdir && !within(env.Workdir, dir) {
		return tools.Errorf("cwd %q is outside the project. Pass a package directory inside it.", a.Cwd)
	}
	// A cwd (or a Workdir below the repo root) may hold no toolchain marker of
	// its own; the project's toolchain is in an ancestor, so look up to the root.
	profile, dir := detectNearest(dir, env.Workdir)

	if action == "list" {
		return listResult(profile, dir, env.Workdir)
	}
	if action == "check" {
		return t.runCheck(ctx, env, a, profile, dir)
	}
	if !isAction(action) {
		return tools.Errorf("unknown action %q. Use one of: check, test, lint, typecheck, build, format, run, list.", a.Action)
	}

	target := strings.TrimSpace(a.Target)
	cmd := commandFor(profile, action, target)
	if cmd == "" {
		msg := fmt.Sprintf("No %s command detected for this project. Use shell to run it manually.", action)
		if profile.Ecosystem == "" {
			msg = fmt.Sprintf("No project toolchain found in %s or above it, up to the project root. Use action list, or pass cwd as a package directory. Use shell to run it manually.", dir)
		}
		return tools.Result{
			Output:  msg,
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
	env0 := append([]string{"FORCE_COLOR=0", "NO_COLOR=1"}, extra...)
	fg := runForegroundShaped(ctx, env, dir, cmd, timeout, env0, projectShape)
	if a.Fix && fg.exit != 0 && fg.err == nil && !fg.timedOut && !fg.cancelled {
		if legacy, ok := legacyFixCommand(cmd, fg.raw); ok {
			fg = runForegroundShaped(ctx, env, dir, legacy, timeout, env0, projectShape)
			cmd = legacy
		}
	}
	return projectResult(action, cmd, fg, timeout)
}

// detectNearest returns the toolchain of dir. When dir has none, it walks up
// through its parents, stopping at root, and uses the first directory that has
// one: the project's commands live there, not in a package subdirectory. The
// directory it returns is where the command runs. Nothing found returns dir
// itself with an empty profile.
func detectNearest(dir, root string) (Profile, string) {
	cur := dir
	for {
		if p := Detect(cur); p.Ecosystem != "" {
			return p, cur
		}
		if cur == root {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur || !within(root, parent) {
			break
		}
		cur = parent
	}
	return Detect(dir), dir
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
	if action == "format" && p.Commands["formatAndLint"] != "" {
		cmd = p.Commands["formatAndLint"]
	}
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

// listResult shows the commands detected in dir. At the project root it also
// lists the workspace packages, so the model can pick one as cwd.
func listResult(p Profile, dir, root string) tools.Result {
	var b strings.Builder
	if p.Ecosystem == "" {
		fmt.Fprintf(&b, "No project toolchain detected in %s. Use shell to run commands manually.", dir)
	} else {
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
	}
	pkgs := workspacePackages(dir)
	if len(pkgs) > 0 {
		b.WriteString("\nWorkspace packages (pass one as cwd to run its commands):\n")
		for _, rel := range pkgs {
			eco := Detect(filepath.Join(dir, rel)).Ecosystem
			if eco == "" {
				eco = "no toolchain marker"
			}
			fmt.Fprintf(&b, "  %s (%s)\n", rel, eco)
		}
	}
	summary := p.Ecosystem
	if summary == "" {
		summary = "no toolchain"
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		Summary: summary,
		Meta:    map[string]any{"ecosystem": p.Ecosystem, "manager": p.Manager, "commands": p.Commands, "workspaces": pkgs, "root": root},
	}
}

// projectResult summarises a finished project command: pass or fail, the
// failing tests and the error locations.
func projectResult(action, cmd string, fg foreground, timeout time.Duration) tools.Result {
	if fg.err != nil {
		return tools.Errorf("could not start %s: %v", action, fg.err)
	}
	diags, failed := parseDiagnostics(fg.raw)
	// Plain lint fails on warnings too, not only on a non-zero exit: a linter
	// that reports warnings with exit 0 has still found problems (spec 06 9.2).
	var warnings []string
	if action == "lint" && fg.exit == 0 && !fg.timedOut && !fg.cancelled {
		warnings = warningLines(fg.raw)
	}
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

	if fg.timedOut || fg.cancelled || fg.exit != 0 || len(warnings) > 0 {
		var verdict string
		switch {
		case fg.timedOut || fg.cancelled:
			verdict = footerFor(fg, timeout, action)
		case fg.exit != 0:
			verdict = fmt.Sprintf("— failed (exit %d).", fg.exit)
		default:
			verdict = fmt.Sprintf("— failed: %d warning(s). Fix them or run with fix: true.", len(warnings))
		}
		summary := fmt.Sprintf("%s failed (%d errors)", action, len(diags))
		if len(diags) == 0 {
			summary = action + " failed"
		}
		if len(warnings) > 0 && fg.exit == 0 {
			summary = fmt.Sprintf("%s failed (%d warnings)", action, len(warnings))
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
