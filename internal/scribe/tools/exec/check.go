package exec

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// checkStages are the stages of project check, in the order they run.
var checkStages = []string{"typecheck", "lint", "test"}

// checkPreviewLines caps how much of a failing stage check repeats.
const checkPreviewLines = 10

// stageOutcome is one stage of project check after it ran.
type stageOutcome struct {
	action  string
	cmd     string
	skipped bool
	passed  bool
	note    string   // one line: the verdict and why
	preview []string // lines that explain a failure
	diags   []Diagnostic
	failed  []string // failing test names
}

// maxCheckParallel bounds how many check stages run at once. There are only
// three stages, so this is the number that can run together; it is named so
// the bound is visible where the stages are started.
const maxCheckParallel = 3

// stagePlan is one stage before it runs: the command after any fix rewrite,
// and whether it may change files.
type stagePlan struct {
	action string
	cmd    string
	writes bool // lint with an auto-fix rewrites files
	done   bool // already decided: skipped, or blocked by the gate
	out    stageOutcome
}

// runCheck runs typecheck, lint and test and reports one summary. Every stage
// runs even when an earlier one fails, so the model sees all the problems at
// once. Stages that do not write files run at the same time, bounded by
// maxCheckParallel; a lint with fix writes files, so it runs alone first and
// the others start after it. The report keeps the stage order whatever order
// they finish in. Lint warnings fail the lint stage.
func (t *projectTool) runCheck(ctx context.Context, env *tools.Env, a projectArgs, p Profile, dir string) tools.Result {
	if strings.TrimSpace(a.Target) != "" || strings.TrimSpace(a.Args) != "" {
		return tools.Errorf("check runs typecheck, lint and test in full and takes no target or args. Run test or lint on its own to narrow it.")
	}
	present := 0
	for _, s := range checkStages {
		if p.Commands[s] != "" {
			present++
		}
	}
	if present == 0 {
		return tools.Result{
			Output:  "No typecheck, lint, or test commands detected.",
			IsError: true,
			Summary: "no check commands",
			Meta:    map[string]any{"error": "no command", "ecosystem": p.Ecosystem},
		}
	}

	timeout := clampTimeout(a.TimeoutMS, t.jobs.defaultTimeout(projectTimeout))
	plans := make([]stagePlan, len(checkStages))
	for i, action := range checkStages {
		plans[i] = t.planStage(ctx, env, a.Fix, p, action)
	}

	// Gate prompts were answered above, one at a time; the stages below run
	// without asking anything.
	var writers, readers []int
	for i, pl := range plans {
		switch {
		case pl.done:
		case pl.writes:
			writers = append(writers, i)
		default:
			readers = append(readers, i)
		}
	}
	for _, i := range writers {
		plans[i].out = t.execStage(ctx, env, dir, plans[i], timeout, "")
	}
	t.runParallel(ctx, env, dir, plans, readers, timeout)

	outcomes := make([]stageOutcome, len(plans))
	for i, pl := range plans {
		outcomes[i] = pl.out
	}
	return checkResult(outcomes)
}

// runParallel runs the stages at idx together, at most maxCheckParallel at a
// time. Each writes its outcome into plans[i], so the order is fixed by the
// index, not by which finishes first. A stage that runs with others gets its
// progress lines labelled with its name.
func (t *projectTool) runParallel(ctx context.Context, env *tools.Env, dir string, plans []stagePlan, idx []int, timeout time.Duration) {
	label := len(idx) > 1
	sem := make(chan struct{}, maxCheckParallel)
	var wg sync.WaitGroup
	for _, i := range idx {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			prefix := ""
			if label {
				prefix = "[" + plans[i].action + "] "
			}
			plans[i].out = t.execStage(ctx, env, dir, plans[i], timeout, prefix)
		}(i)
	}
	wg.Wait()
}

// planStage decides what a stage will run and asks the gate about it. A stage
// with no command is skipped, not failed: a project without a typechecker is
// not a broken project. A command the gate refuses ends the stage here.
func (t *projectTool) planStage(ctx context.Context, env *tools.Env, fix bool, p Profile, action string) stagePlan {
	pl := stagePlan{action: action, out: stageOutcome{action: action, cmd: p.Commands[action]}}
	if pl.out.cmd == "" {
		pl.done = true
		pl.out.skipped = true
		pl.out.note = "skipped, no " + action + " command detected"
		return pl
	}
	if action == "lint" && fix {
		if fixed, ok := fixCommand(pl.out.cmd); ok {
			pl.out.cmd = fixed
			pl.writes = true
		} else {
			pl.out.note = "no auto-fix for this linter; "
		}
	}
	pl.cmd = pl.out.cmd
	if res, blocked := gate(ctx, env, t.jobs.opts, "project", pl.cmd, ""); blocked {
		pl.done = true
		pl.out.note += "blocked: " + firstLine(res.Output)
		pl.out.preview = []string{firstLine(res.Output)}
	}
	return pl
}

// execStage runs a planned stage and parses its output. prefix, when set,
// labels the progress lines it streams.
func (t *projectTool) execStage(ctx context.Context, env *tools.Env, dir string, pl stagePlan, timeout time.Duration, prefix string) stageOutcome {
	out := pl.out
	e := *env
	if prefix != "" && env.Host != nil {
		e.Host = stageHost{Host: env.Host, prefix: prefix}
	}
	fg := runForegroundIn(ctx, &e, dir, pl.cmd, timeout, []string{"FORCE_COLOR=0", "NO_COLOR=1"})
	if pl.writes && fg.exit != 0 && fg.err == nil && !fg.timedOut && !fg.cancelled {
		// A fix flag this linter version does not know: retry once with the
		// spelling older versions used.
		if legacy, ok := legacyFixCommand(pl.cmd, fg.raw); ok {
			out.note += "retried with legacy fix flags; "
			fg = runForegroundIn(ctx, &e, dir, legacy, timeout, []string{"FORCE_COLOR=0", "NO_COLOR=1"})
			out.cmd = legacy
		}
	}
	switch {
	case fg.err != nil:
		out.note += fmt.Sprintf("could not start: %v", fg.err)
		return out
	case fg.timedOut || fg.cancelled:
		out.note += footerFor(fg, timeout, pl.action)
		return out
	}

	diags, failed := parseDiagnostics(fg.raw)
	out.diags, out.failed = diags, failed
	var warnings []string
	if pl.action == "lint" {
		warnings = warningLines(fg.raw)
	}
	switch {
	case fg.exit != 0:
		out.note += fmt.Sprintf("failed (exit %d)", fg.exit)
	case len(warnings) > 0:
		out.note += fmt.Sprintf("failed: %d warning(s); warnings count as failure under check", len(warnings))
	default:
		out.passed = true
		out.note += "passed"
		return out
	}
	if len(failed) > 0 {
		out.note += fmt.Sprintf(", %d failing test(s)", len(failed))
	}
	out.preview = previewOf(fg.raw, warnings, diags)
	return out
}

// stageHost labels the progress lines of a stage that runs beside others.
type stageHost struct {
	tools.Host
	prefix string
}

func (h stageHost) Progress(callID, text string) {
	h.Host.Progress(callID, h.prefix+text)
}

// previewOf picks the lines that explain a failure: warnings for lint, then
// parsed locations, then the tail of the output.
func previewOf(raw string, warnings []string, diags []Diagnostic) []string {
	switch {
	case len(warnings) > 0:
		return capLines(warnings)
	case len(diags) > 0:
		lines := make([]string, 0, len(diags))
		for _, d := range diags {
			lines = append(lines, fmt.Sprintf("%s:%d: %s", d.File, d.Line, d.Msg))
		}
		return capLines(lines)
	}
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimRight(l, "\r"))
		}
	}
	if len(lines) > checkPreviewLines {
		lines = lines[len(lines)-checkPreviewLines:]
	}
	return lines
}

func capLines(lines []string) []string {
	if len(lines) > checkPreviewLines {
		return lines[:checkPreviewLines]
	}
	return lines
}

// checkResult formats the finished stages as one report.
func checkResult(outcomes []stageOutcome) tools.Result {
	var b strings.Builder
	var failedStages []string
	meta := map[string]any{"action": "check"}
	stages := make([]map[string]any, 0, len(outcomes))

	for _, o := range outcomes {
		status := "fail"
		switch {
		case o.skipped:
			status = "skip"
		case o.passed:
			status = "pass"
		default:
			failedStages = append(failedStages, o.action)
		}
		label := strings.ToUpper(status)
		if o.cmd != "" {
			fmt.Fprintf(&b, "%-10s %-4s  %s\n", o.action, label, o.cmd)
		} else {
			fmt.Fprintf(&b, "%-10s %-4s\n", o.action, label)
		}
		if o.note != "" {
			fmt.Fprintf(&b, "           %s\n", o.note)
		}
		for _, line := range o.preview {
			fmt.Fprintf(&b, "    %s\n", line)
		}
		stages = append(stages, map[string]any{
			"action": o.action, "command": o.cmd, "status": status, "note": o.note,
			"failed": o.failed,
		})
	}

	passed := len(failedStages) == 0
	meta["stages"] = stages
	meta["passed"] = passed
	meta["failed_stages"] = failedStages
	meta["errors"] = allErrorDiags(outcomes)

	var summary string
	if passed {
		b.WriteString("— check passed.")
		summary = "check passed"
	} else {
		fmt.Fprintf(&b, "— check failed: %s.", strings.Join(failedStages, ", "))
		summary = "check failed: " + strings.Join(failedStages, ", ")
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		IsError: !passed,
		Summary: summary,
		Meta:    meta,
	}
}

// allErrorDiags gathers the parsed error locations of every stage, capped.
func allErrorDiags(outcomes []stageOutcome) []Diagnostic {
	var out []Diagnostic
	for _, o := range outcomes {
		for _, d := range o.diags {
			if len(out) >= maxDiagnostics {
				return out
			}
			out = append(out, d)
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Warning lines in linter output. A linter that reports warnings often exits
// 0, so check reads the output as well as the exit code.
var (
	reEslintWarn  = regexp.MustCompile(`^\s*\d+:\d+\s+warning\s`)
	reCargoWarn   = regexp.MustCompile(`^warning(\[[^\]]*\])?: `)
	reLocWarn     = regexp.MustCompile(`^\S+?:\d+(:\d+)?:\s*warning\b`)
	reWarnSummary = regexp.MustCompile(`generated \d+ warnings?|\d+ warnings? emitted|^warning: build failed`)
)

// warningLines returns the warning lines in linter output, in order. Summary
// lines such as cargo's "generated 2 warnings" are not counted.
func warningLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if reWarnSummary.MatchString(line) {
			continue
		}
		if reEslintWarn.MatchString(line) || reCargoWarn.MatchString(line) || reLocWarn.MatchString(line) {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}
