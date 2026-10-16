// Package exec holds Scribe's execution tools: shell (foreground and
// background), project (detected test, lint, build and run commands) and git.
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

const (
	defaultShellTimeout = 120 * time.Second
	maxShellTimeout     = 30 * time.Minute
)

// Register adds the shell, shell_output, shell_kill, project and git tools to
// r. jobs holds background processes and the command rules; nil means no
// rules and a fresh job table.
func Register(r *tools.Registry, jobs *Jobs) {
	if jobs == nil {
		jobs = NewJobs(Options{})
	}
	r.Register(&shellTool{jobs: jobs})
	r.Register(&shellOutputTool{jobs: jobs})
	r.Register(&shellKillTool{jobs: jobs})
	r.Register(&projectTool{jobs: jobs})
	r.Register(&gitTool{})
}

// gate runs the checks every command passes before it starts: refusals,
// config deny, then the destructive-command prompt. It returns a result and
// true when the command must not run.
func gate(ctx context.Context, env *tools.Env, opts Options, tool, command, description string) (tools.Result, bool) {
	if msg, refused := refusalFor(command); refused {
		return tools.Errorf("%s", msg), true
	}
	if rule, denied := opts.denied(command); denied {
		return tools.Errorf("Denied by config: the command matches the deny rule %q. Do not retry it; use another approach.", rule), true
	}
	desc, destructive, severe := classifySeverity(command, env.Workdir)
	if !destructive || opts.allowed(command) || (env.Mode == "auto" && !severe) {
		return tools.Result{}, false
	}
	if env.Host == nil {
		return tools.Errorf("Denied: %s. No one is available to approve it. Pick a narrower command.", desc), true
	}
	title := desc
	if description != "" {
		title = description + ": " + desc
	}
	dec := env.Host.Approve(ctx, tools.Approval{
		Tool:   tool,
		Kind:   "destructive",
		Title:  title,
		Detail: command,
		Danger: true,
	})
	if dec == tools.Deny {
		return tools.Errorf("Denied: %s. Do not retry this command. Ask the user what they want, or use a narrower command.", desc), true
	}
	return tools.Result{}, false
}

// clampTimeout turns a timeout_ms argument into a duration in [1s, 30 min].
// The 30 minute ceiling is the spec's upper clamp for tool timeouts.
// Zero or negative means the default.
func clampTimeout(ms int, def time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	d := time.Duration(ms) * time.Millisecond
	if d > maxShellTimeout {
		return maxShellTimeout
	}
	if d < time.Second {
		return time.Second
	}
	return d
}

// foreground is the outcome of a foreground command.
type foreground struct {
	raw       string // full collected output, for parsing
	body      string // output, truncated to outputLimit
	omitted   int    // lines dropped by truncation
	lines     int    // lines produced
	exit      int
	timedOut  bool
	cancelled bool
	err       error
}

// runForeground runs command in the project root; see runForegroundIn.
func runForeground(ctx context.Context, env *tools.Env, command string, timeout time.Duration, extraEnv []string) foreground {
	return runForegroundIn(ctx, env, env.Workdir, command, timeout, extraEnv)
}

// runForegroundIn runs command in dir with the scrubbed environment, streams
// lines to env.Host.Progress, and waits for it to finish or for timeout. The
// output the model reads is capped at outputLimit; the full text goes to a
// tee file when it was cut.
func runForegroundIn(ctx context.Context, env *tools.Env, dir, command string, timeout time.Duration, extraEnv []string) foreground {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	onLine := func(line string) {
		if env.Host != nil {
			env.Host.Progress(env.CallID, line)
		}
	}
	environ := append(ScrubEnv(os.Environ()), extraEnv...)
	res := runProcess(tctx, procSpec{
		argv:         shellArgv(command),
		dir:          dir,
		env:          environ,
		onLine:       onLine,
		collectBytes: collectLimit,
	})
	text := res.output
	if res.truncated {
		text += fmt.Sprintf("\n[output truncated — %d KB total, showing the first %d KB]", res.total/1024, collectLimit/1024)
	}
	body, omitted := spill(text, outputLimit, firstWord(command))
	return foreground{
		raw:       res.output,
		body:      body,
		omitted:   omitted,
		lines:     strings.Count(res.output, "\n"),
		exit:      res.exitCode,
		timedOut:  res.cancelled && tctx.Err() == context.DeadlineExceeded && ctx.Err() == nil,
		cancelled: res.cancelled && ctx.Err() != nil,
		err:       res.err,
	}
}

// ---------------------------------------------------------------- shell

type shellTool struct{ jobs *Jobs }

type shellArgs struct {
	Command     string `json:"command"`
	Cwd         string `json:"cwd"`
	TimeoutMS   int    `json:"timeout_ms"`
	Background  bool   `json:"background"`
	Description string `json:"description"`
}

func (t *shellTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "shell",
		Description: "Run a shell command in the project directory (bash -c on unix, PowerShell or cmd on Windows). " +
			"Output is streamed while it runs and capped at 30000 characters, keeping the head and tail; the exit code is appended. " +
			"Credentials are removed from the environment. Destructive commands ask the user first. " +
			"Use background: true for servers, watchers or anything long-running: it returns a job id; read it with shell_output and stop it with shell_kill. " +
			"Use the project tool for tests, lint and builds when the project has them.",
		Schema: tools.Schema(map[string]any{
			"command":     tools.Str("The shell command to run."),
			"cwd":         tools.Str("Directory to run in, relative to the project root (default: the project root). Outside the project it asks the user first."),
			"timeout_ms":  tools.Int("Timeout in milliseconds (default 120000, max 1800000 = 30 min). Ignored for background jobs unless set."),
			"background":  tools.Bool("Run in the background and return a job id instead of waiting."),
			"description": tools.Str("Short purpose of the command, shown to the user when it asks for approval."),
		}, "command"),
	}
}

func (t *shellTool) ReadOnly() bool { return false }

func (t *shellTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var a shellArgs
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if env == nil {
		return tools.Errorf("shell needs a project environment; this call has none")
	}
	command := strings.TrimSpace(a.Command)
	if command == "" {
		return tools.Errorf("command is empty. Pass the shell command in the 'command' field.")
	}
	dir, err := resolveCwd(env.Workdir, a.Cwd)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	// Forbidden paths come from the same list the file tools use (fs.Patterns).
	pats, err := fs.Patterns(env)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if hit, found, err := fs.MatchCommand(pats, command); err != nil {
		return tools.Errorf("%v", err)
	} else if found {
		return tools.Errorf("Access denied: %q matches forbidden pattern %q. Do not read or change it; work without it.", hit.Path, hit.Pattern)
	}
	if kw, hit := suspiciousSubshell(command); hit {
		return tools.Errorf("refused: suspicious subshell referencing %q. Do not expand secret-looking values in a command; name the file the value is in instead.", kw)
	}
	if msg, hit := binaryReadRefusal(command, dir); hit {
		return tools.Errorf("%s", msg)
	}
	if mutatingGit.MatchString(command) && dispatchActive(env) {
		return tools.Errorf("%s", dispatchBlockedMsg)
	}
	if !within(env.Workdir, dir) {
		if res, blocked := approveOutsideCwd(ctx, env, dir, command); blocked {
			return res
		}
	}
	if res, blocked := gate(ctx, env, t.jobs.opts, "shell", command, a.Description); blocked {
		return res
	}
	if gitCommitMessage.MatchString(command) {
		if res, blocked := runPreCommit(ctx, env, dir); blocked {
			return res
		}
	}
	written := claimWrites(env, dir, command)

	if a.Background {
		var timeout time.Duration
		if a.TimeoutMS > 0 {
			timeout = clampTimeout(a.TimeoutMS, 0)
		}
		job := t.jobs.start(ctx, command, dir, ScrubEnv(os.Environ()), timeout)
		return tools.Result{
			Output: fmt.Sprintf("Started background job %s: %s\nRead its output with shell_output (id %q). Stop it with shell_kill.",
				job.ID, command, job.ID),
			Summary: "started " + job.ID,
			Meta:    map[string]any{"job_id": job.ID},
		}
	}

	timeout := clampTimeout(a.TimeoutMS, t.jobs.defaultTimeout(defaultShellTimeout))
	fg := runForegroundIn(ctx, env, dir, command, timeout, nil)
	res := foregroundResult(fg, timeout, "shell", command)
	res.Files = written
	if !res.IsError {
		if hint := toolchainHint(command); hint != "" {
			res.Output += "\n" + hint
		}
	}
	return res
}

// resolveCwd turns a shell cwd argument into an absolute directory. An empty
// argument is the project root. The directory must exist.
func resolveCwd(workdir, cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return workdir, nil
	}
	dir := cwd
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workdir, dir)
	}
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory. Pass a directory that exists, relative to the project root.", cwd)
	}
	return dir, nil
}

// approveOutsideCwd asks the user before a command runs outside the project.
// Without a host, or when the user says no, it refuses.
func approveOutsideCwd(ctx context.Context, env *tools.Env, dir, command string) (tools.Result, bool) {
	if env.Mode == "auto" {
		return tools.Result{}, false
	}
	deny := tools.Errorf("Denied: %s is outside the project directory. Run it from inside the project.", dir)
	if env.Host == nil {
		return deny, true
	}
	dec := env.Host.Approve(ctx, tools.Approval{
		Tool:   "shell",
		Kind:   "shell",
		Title:  "Run shell command outside the project: " + dir,
		Detail: command,
	})
	if dec == tools.Deny {
		return deny, true
	}
	return tools.Result{}, false
}

// claimWrites snapshots the files a command is about to write, inside the
// project, through the checkpoint tracker, and returns them relative to the
// project root so the agent can record them.
func claimWrites(env *tools.Env, dir, command string) []string {
	var rel []string
	for _, p := range writtenFiles(command) {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, p)
		}
		abs = filepath.Clean(abs)
		if !within(env.Workdir, abs) {
			continue
		}
		if env.Files != nil {
			env.Files.BeforeWrite(abs)
		}
		if r, err := filepath.Rel(env.Workdir, abs); err == nil {
			rel = append(rel, filepath.ToSlash(r))
		}
	}
	return rel
}

// firstWord is the first word of command, used to name tee files.
func firstWord(command string) string {
	if f := strings.Fields(command); len(f) > 0 {
		return f[0]
	}
	return "output"
}

// footerFor is the closing line of a foreground command. what names the
// command in the timeout message.
func footerFor(fg foreground, timeout time.Duration, what string) string {
	switch {
	case fg.timedOut:
		return fmt.Sprintf("[%s timed out after %s; its process group was killed. Raise timeout_ms, or run it in the background.]",
			what, timeout)
	case fg.cancelled:
		return "[cancelled; the process group was killed]"
	default:
		return fmt.Sprintf("[exit %d]", fg.exit)
	}
}

// foregroundResult formats a finished foreground command for the model.
func foregroundResult(fg foreground, timeout time.Duration, what, command string) tools.Result {
	if fg.err != nil {
		return tools.Errorf("could not start %s: %v", what, fg.err)
	}
	body := fg.body
	if strings.TrimSpace(body) == "" {
		body = "(no output)"
	}
	out := strings.TrimRight(body, "\n") + "\n" + footerFor(fg, timeout, what)
	failed := fg.timedOut || fg.cancelled || fg.exit != 0
	summary := fmt.Sprintf("exit %d, %d lines", fg.exit, fg.lines)
	if fg.timedOut {
		summary = "timed out"
	} else if fg.cancelled {
		summary = "cancelled"
	}
	return tools.Result{
		Output:  out,
		IsError: failed,
		Summary: summary,
		Meta: map[string]any{
			"exit_code": fg.exit,
			"timed_out": fg.timedOut,
			"lines":     fg.lines,
			"omitted":   fg.omitted,
			"command":   command,
		},
	}
}

// ---------------------------------------------------------- shell_output

type shellOutputTool struct{ jobs *Jobs }

func (t *shellOutputTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "shell_output",
		Description: "Read output of a background job started with shell (id like bg-1). " +
			"Pass since (the cursor from the previous call) to get only new lines. Keeps the last 2000 lines.",
		Schema: tools.Schema(map[string]any{
			"id":    tools.Str("Job id, e.g. bg-1."),
			"since": tools.Int("Line index to read from (the 'next' cursor from the last call). Default 0."),
		}, "id"),
	}
}

func (t *shellOutputTool) ReadOnly() bool { return true }

func (t *shellOutputTool) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	var a struct {
		ID    string `json:"id"`
		Since int    `json:"since"`
	}
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	job, ok := t.jobs.Get(strings.TrimSpace(a.ID))
	if !ok {
		return tools.Errorf("no background job %q. Use shell with background: true to start one.", a.ID)
	}
	lines, next, dropped, status, code := job.Read(a.Since)
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", job.ID, status)
	if status != StatusRunning {
		fmt.Fprintf(&b, ", exit %d", code)
	}
	fmt.Fprintf(&b, ", %d lines so far\n", next)
	if dropped > 0 {
		fmt.Fprintf(&b, "… %d earlier lines were dropped (only the last %d are kept) …\n", dropped, ringSize)
	}
	text := strings.Join(lines, "\n")
	if text != "" {
		text, _ = truncateOutput(text)
		b.WriteString(text)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "[next since=%d]", next)
	return tools.Result{Output: b.String(), Summary: status, Meta: map[string]any{"next": next, "status": status}}
}

// ----------------------------------------------------------- shell_kill

type shellKillTool struct{ jobs *Jobs }

func (t *shellKillTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "shell_kill",
		Description: "Stop a background job started with shell (id like bg-1). Its whole process group is terminated.",
		Schema:      tools.Schema(map[string]any{"id": tools.Str("Job id, e.g. bg-1.")}, "id"),
	}
}

func (t *shellKillTool) ReadOnly() bool { return false }

func (t *shellKillTool) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	var a struct {
		ID string `json:"id"`
	}
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	id := strings.TrimSpace(a.ID)
	if _, ok := t.jobs.Get(id); !ok {
		return tools.Errorf("no background job %q", id)
	}
	if err := t.jobs.Kill(id); err != nil {
		return tools.Errorf("%v", err)
	}
	job, _ := t.jobs.Get(id)
	info := job.info()
	return tools.Result{
		Output:  fmt.Sprintf("[%s] %s (exit %d)", id, info.Status, info.ExitCode),
		Summary: info.Status,
	}
}
