package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Purpose says why a subagent runs. The factory uses it, with the role, to
// choose the subagent's model slot.
type Purpose string

const (
	// PurposeTask is a dispatch task. Its role says which kind of agent.
	PurposeTask Purpose = "task"
	// PurposeCleanup is the desloppify pass over the files code tasks edited.
	PurposeCleanup Purpose = "cleanup"
	// PurposeReview is the verifyEdits reviewer, run after the project check.
	PurposeReview Purpose = "review"
	// PurposeFix is the one follow-up agent that repairs a failing check.
	PurposeFix Purpose = "fix"
)

// SubagentSpec describes the subagent the dispatcher wants built.
type SubagentSpec struct {
	Role    string  // "explore", "code" or "web"
	Purpose Purpose // why it runs
	ID      string  // its id in events
	// MaxRetries and MaxStallRetries are the Options of the same names, from
	// the dispatcher's Retries. Zero takes the agent default.
	MaxRetries      int
	MaxStallRetries int
	// Bus is the state this subagent shares with the others of its dispatch.
	// Nil when the dispatch cache is off.
	Bus *AgentBus
}

// SubagentFactory builds the subagent a spec describes, whose events go to
// sink. The app wiring layer supplies it, because it knows which tools and
// models each role and purpose get.
type SubagentFactory func(spec SubagentSpec, sink Sink) *Agent

// Dispatcher provides the dispatch tool: parallel subagents.
type Dispatcher struct {
	New           SubagentFactory
	MaxConcurrent int // default 3, clamped to 2..8
	Sink          Sink
	// WorkDir is the project root that task file targets are checked against.
	// It is also in env, but a dispatch with no env workdir skips the check.

	// retryWait is the pause before a code task's retry. Zero means the
	// default (2 s).
	retryWait time.Duration

	seq atomic.Int64

	// Features are the agentFeatures switches. Nil takes DefaultFeatures.
	Features *Features

	// MaxRetries and MaxStallRetries are passed to every subagent (see
	// SubagentSpec). Zero takes the agent default.
	MaxRetries      int
	MaxStallRetries int

	// active counts dispatch calls in flight. It is the guard that keeps
	// git mutations out while subagents may be editing (DispatchActive).
	active atomic.Int32
	// lastBoard is the board of the newest dispatch, for Status.
	lastBoard atomic.Pointer[board]
}

// features is the feature set in force.
func (d *Dispatcher) features() Features {
	if d.Features == nil {
		return DefaultFeatures()
	}
	return *d.Features
}

// DispatchActive reports whether a dispatch is running subagents. It
// implements DispatchGuard.
func (d *Dispatcher) DispatchActive() bool { return d.active.Load() > 0 }

// Status lists the subagents of the newest dispatch, running or finished,
// in the order they were started. Nil when no dispatch has run.
func (d *Dispatcher) Status() []AgentInfo {
	b := d.lastBoard.Load()
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AgentInfo, 0, len(b.infos))
	for _, info := range b.infos {
		out = append(out, *info)
	}
	return out
}

// CancelAgent stops one subagent of the newest dispatch (the /cancel command).
// A running subagent is cancelled and reports "cancelled"; a pending one is
// marked cancelled and never starts. Other subagents keep running.
func (d *Dispatcher) CancelAgent(id string) error {
	b := d.lastBoard.Load()
	if b == nil {
		return fmt.Errorf("no dispatch has run in this session")
	}
	return b.cancelAgent(id)
}

// Register adds the dispatch tool.
func (d *Dispatcher) Register(r *tools.Registry) { r.Register(dispatchTool{d}) }

type dispatchTool struct{ d *Dispatcher }

// Dispatch is read-only from the mode gate's point of view only when every
// task is an explore task; Run enforces that in restricted modes.
func (dispatchTool) ReadOnly() bool { return true }

func (dispatchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "dispatch",
		Description: "Run up to 8 subagents in parallel and get their reports. Use explore agents for independent investigations " +
			"(each answers one question), code agents for independent, fully specified edits in different files, web agents for research outside the repository. " +
			"Each task must be self-contained: the subagent sees only the task text, not this conversation. " +
			"A task can name an id and list depends_on ids: it starts only after those tasks finish, and their reports are added to its text.",
		Schema: tools.Schema(map[string]any{
			"tasks": tools.Arr("Tasks to run in parallel", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":           tools.Str("Optional name for this task, used in other tasks' depends_on"),
					"task":         tools.Str("Complete instructions for the subagent"),
					"role":         tools.Enum("explore = read-only investigation of the code (investigate is the same), code = make edits, web = research on the internet (web search and page fetch only)", "explore", "investigate", "code", "web"),
					"returnFormat": tools.Enum("What the report should look like: summary (default, findings with file:line), code (pasteable snippets), files (paths only), full (complete analysis), verdict (yes or no and 1-3 sentences)", "summary", "code", "files", "full", "verdict"),
					"files":        tools.Arr("Files the task is about, optionally with a line range as path:start-end (code tasks: files it may edit)", map[string]any{"type": "string"}),
					"depends_on":   tools.Arr("Ids of tasks that must finish before this one starts", map[string]any{"type": "string"}),
				},
				"required": []string{"task", "role"},
			}),
		}, "tasks"),
	}
}

type subTask struct {
	ID           string   `json:"id"`
	Task         string   `json:"task"`
	Role         string   `json:"role"`
	ReturnFormat string   `json:"returnFormat"`
	Files        []string `json:"files"`
	DependsOn    []string `json:"depends_on"`
}

// maxTasksPerDispatch is the most tasks one dispatch call runs (spec 03 §3.1).
const maxTasksPerDispatch = 8

// staggerStep and staggerJitter space out the starts of tasks that have no
// dependencies, so they do not all hit the provider in the same instant
// (spec 03 §3.2).
const (
	staggerStep   = 100 * time.Millisecond
	staggerJitter = 100 * time.Millisecond
)

// returnFormatText is the instruction each returnFormat adds to a task.
var returnFormatText = map[string]string{
	"summary": "Return format: summary. Give concise findings, each with a file:line anchor.",
	"code":    "Return format: code. Give pasteable code snippets, each with its file path.",
	"files":   "Return format: files. Give only the paths of the relevant files, one per line.",
	"full":    "Return format: full. Give a complete analysis.",
	"verdict": "Return format: verdict. Answer yes or no, then one to three sentences of reasoning.",
}

func (t dispatchTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Tasks []subTask `json:"tasks"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	if len(in.Tasks) == 0 {
		return tools.Errorf("dispatch needs at least one task")
	}
	for i, st := range in.Tasks {
		if st.Role != "explore" && st.Role != "code" && st.Role != "web" {
			// investigate is an alias of explore, and anything unknown
			// becomes explore.
			in.Tasks[i].Role = "explore"
		}
		if RestrictedModes[env.Mode] && in.Tasks[i].Role == "code" {
			return tools.Errorf("mode_restricted: %s mode is read-only; only explore tasks can be dispatched", env.Mode)
		}
		if strings.TrimSpace(st.Task) == "" {
			return tools.Errorf("task %d is empty", i+1)
		}
		if st.ReturnFormat != "" && returnFormatText[st.ReturnFormat] == "" {
			in.Tasks[i].ReturnFormat = "summary"
		}
	}
	var notes []string
	tasks, merged, err := mergeOverLimit(in.Tasks, maxTasksPerDispatch)
	if err != nil {
		return tools.Errorf("dispatch: %v", err)
	}
	notes = append(notes, merged...)
	in.Tasks = tasks
	plan, err := planDispatch(in.Tasks)
	if err != nil {
		return tools.Errorf("dispatch: %v", err)
	}
	d := t.d
	if d.New == nil {
		return tools.Errorf("subagents are not available in this session")
	}
	d.active.Add(1)
	defer d.active.Add(-1)
	limit := min(max(d.MaxConcurrent, 2), 8)
	if d.MaxConcurrent <= 0 {
		limit = 3
	}
	if d.features().TargetFileValidation {
		notes = append(notes, checkTargets(in.Tasks, env.Workdir)...)
	}
	notes = append(notes, sharedFileNotes(in.Tasks)...)

	n := len(in.Tasks)
	run := newDispatchRun(d, env)
	d.lastBoard.Store(run.board)
	for _, st := range in.Tasks {
		run.board.add(st.Role, st.Task)
	}
	// The dispatch's own context is cancelled when the breaker opens, which
	// stops the subagents still running. The check below uses the caller's.
	callerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	run.cancel = cancel
	sem := make(chan struct{}, limit)
	// Code tasks that touch the same file run one after another.
	locks := map[string]*sync.Mutex{}
	for _, st := range in.Tasks {
		if st.Role == "code" {
			for _, f := range st.Files {
				if locks[f] == nil {
					locks[f] = &sync.Mutex{}
				}
			}
		}
	}
	done := make([]chan struct{}, n)
	for i := range done {
		done[i] = make(chan struct{})
	}
	results := make([]taskResult, n)
	skip := func(i int, err error) {
		results[i] = taskResult{err: err}
		run.board.update(i, func(a *AgentInfo) { a.Status, a.Error = "error", err.Error() })
	}

	var wg sync.WaitGroup
	for i, st := range in.Tasks {
		wg.Add(1)
		go func(i int, st subTask) {
			defer wg.Done()
			defer close(done[i])
			// Tasks without dependencies start staggered, so they do not all
			// reach the provider in the same instant.
			if len(plan.deps[i]) == 0 && i > 0 {
				d := time.Duration(i)*staggerStep + time.Duration(rand.Int64N(int64(staggerJitter)))
				select {
				case <-time.After(d):
				case <-ctx.Done():
					skip(i, run.stopErr(ctx))
					return
				}
			}
			// Wait for dependencies before taking a slot, so a waiting task
			// never holds one that its dependency needs.
			for _, dep := range plan.deps[i] {
				select {
				case <-done[dep]:
				case <-ctx.Done():
					skip(i, run.stopErr(ctx))
					return
				}
				if results[dep].err != nil {
					skip(i, fmt.Errorf("dependency %s failed", plan.ids[dep]))
					return
				}
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				skip(i, run.stopErr(ctx))
				return
			}
			defer func() { <-sem }()
			if st.Role == "code" {
				for _, f := range sortedUnique(st.Files) {
					locks[f].Lock()
					defer locks[f].Unlock()
				}
			}
			task := withDependencyReports(taskText(st), plan, i, results)
			results[i] = run.runTask(ctx, i, st.Role, PurposeTask, task)
		}(i, st)
	}
	wg.Wait()

	// Files that code tasks changed, and the project check that follows them.
	// The check runs with the caller's context: the breaker must not cancel it.
	var changed []string
	var reg *tools.Registry
	for _, r := range results {
		changed = append(changed, r.edited...)
		if r.reg != nil {
			reg = r.reg
		}
	}
	changed = sortedUnique(changed)
	// Optional passes (agentFeatures): a cleanup over the edited files
	// before the project check, and a read-only review after it.
	feats := d.features()
	var cleanup string
	if len(changed) > 0 && feats.Desloppify && d.New != nil {
		var edited []string
		cleanup, edited = run.desloppify(callerCtx, changed)
		changed = sortedUnique(append(changed, edited...))
	}
	var check verifyOutcome
	if len(changed) > 0 {
		check = run.verify(callerCtx, env, reg, changed)
		changed = sortedUnique(append(changed, check.edited...))
	}
	var review, verdict string
	if len(changed) > 0 && feats.VerifyEdits && d.New != nil {
		review, verdict = run.review(callerCtx, changed, check.text)
	}

	ok := 0
	for _, r := range results {
		if r.err == nil {
			ok++
		}
	}
	var b strings.Builder
	if len(notes) > 0 {
		b.WriteString("## Dispatch notes\n")
		for _, nt := range notes {
			fmt.Fprintf(&b, "- %s\n", nt)
		}
		b.WriteString("\n")
	}
	b.WriteString(renderTaskReports(in.Tasks, plan, results))
	if conflicts := editConflicts(results); len(conflicts) > 0 {
		b.WriteString("## Files edited by more than one agent\n")
		for _, c := range conflicts {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	if run.tripped() {
		fmt.Fprintf(&b, "## Circuit breaker\nOpened after %d provider failures; tasks that had not started were not run.\n\n", run.failureCount())
	}
	if cleanup != "" {
		fmt.Fprintf(&b, "## Cleanup\n%s\n\n", cleanup)
	}
	if len(changed) > 0 {
		fmt.Fprintf(&b, "## Verification\n%s\n\n", check.text)
	}
	if review != "" {
		fmt.Fprintf(&b, "## Review\n%s\n\n", review)
	}
	summary := fmt.Sprintf("%d/%d agents finished", ok, n)
	if check.failing {
		summary += ", check failing"
	}
	if verdict == "fail" {
		summary += ", review failing"
	}
	res := tools.Result{
		Output:  strings.TrimSpace(b.String()),
		Summary: summary,
		Files:   changed,
		IsError: ok == 0,
		Meta:    map[string]any{"subagent_usage": run.usage(), "subagent_cost_usd": run.costUSD()},
	}
	if len(changed) > 0 {
		res.Meta["verification"] = check.status
	}
	if verdict != "" {
		res.Meta["review"] = verdict
	}
	return res
}

// renderTaskReports writes one section per task: its report, or why it
// failed, its notes, and the files it read and edited.
func renderTaskReports(tasks []subTask, plan dispatchPlan, results []taskResult) string {
	var b strings.Builder
	for i, st := range tasks {
		r := results[i]
		idPart := ""
		if given := strings.TrimSpace(st.ID); given != "" {
			idPart = " id=" + given
		}
		fmt.Fprintf(&b, "## Agent %d (%s%s): %s\n", i+1, st.Role, idPart, firstLine(clip(st.Task, 120)))
		for _, note := range r.notes {
			fmt.Fprintf(&b, "(%s)\n", note)
		}
		report := strings.TrimSpace(r.report)
		if report == "" {
			report = "No output from agent"
		}
		if r.noDone {
			report = "[no-done] " + report
		}
		switch {
		case r.err != nil && r.report == "":
			fmt.Fprintf(&b, "%s\n", failureLine(r))
		case r.err != nil:
			fmt.Fprintf(&b, "(stopped early: %v)\n%s\n", r.err, report)
		default:
			fmt.Fprintf(&b, "%s\n", report)
		}
		if len(r.read) > 0 {
			fmt.Fprintf(&b, "Files read: %s\n", strings.Join(r.read, ", "))
		}
		if len(r.edited) > 0 {
			fmt.Fprintf(&b, "Files edited: %s\n", strings.Join(r.edited, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// failureLine says how a task with no report failed. A task the breaker kept
// from starting is not counted as an attempt.
func failureLine(r taskResult) string {
	if r.attempts > 0 {
		word := "attempts"
		if r.attempts == 1 {
			word = "attempt"
		}
		return fmt.Sprintf("FAILED after %d %s: %v", r.attempts, word, r.err)
	}
	return fmt.Sprintf("FAILED: %v", r.err)
}

// taskText is the text a subagent receives: the task, the files it is
// about, and the return format instruction.
func taskText(st subTask) string {
	task := st.Task
	if len(st.Files) > 0 {
		task += "\n\nRelevant files: " + strings.Join(st.Files, ", ")
	}
	if fmtText := returnFormatText[st.ReturnFormat]; fmtText != "" {
		task += "\n\n" + fmtText
	} else {
		task += "\n\n" + returnFormatText["summary"]
	}
	return task
}

// checkTargets drops code-task target files that do not exist, since an
// edit to them cannot start (spec 03 §3.1 rule 1). A target is a path with a
// dot in it; an optional path:start-end range is ignored for the check. It
// returns a note for each file it dropped. Dropped files are removed from the
// tasks in place. An empty workdir skips the check.
func checkTargets(tasks []subTask, workdir string) []string {
	if workdir == "" {
		return nil
	}
	var notes []string
	for i := range tasks {
		if tasks[i].Role != "code" || len(tasks[i].Files) == 0 {
			continue
		}
		var keep []string
		for _, f := range tasks[i].Files {
			p := stripRange(f)
			if !strings.Contains(p, ".") || fileExists(filepath.Join(workdir, p)) {
				keep = append(keep, f)
				continue
			}
			notes = append(notes, fmt.Sprintf("dropped target %s from agent %d: no such file in the project", p, i+1))
		}
		tasks[i].Files = keep
	}
	return notes
}

// sharedFileNotes says which files more than one task works on. It is
// added to the notes so the user sees the overlap, and tasks get the names.
func sharedFileNotes(tasks []subTask) []string {
	owners := map[string][]int{}
	for i, st := range tasks {
		for _, f := range st.Files {
			p := stripRange(f)
			if !slices.Contains(owners[p], i+1) {
				owners[p] = append(owners[p], i+1)
			}
		}
	}
	var out []string
	for _, p := range sortedKeys(owners) {
		if len(owners[p]) > 1 {
			out = append(out, fmt.Sprintf("agents %s all target %s; their edits are not coordinated", joinInts(owners[p]), p))
		}
	}
	return out
}

// editConflicts lists files that more than one task edited.
func editConflicts(results []taskResult) []string {
	owners := map[string][]int{}
	for i, r := range results {
		for _, f := range r.edited {
			if !slices.Contains(owners[f], i+1) {
				owners[f] = append(owners[f], i+1)
			}
		}
	}
	var out []string
	for _, f := range sortedKeys(owners) {
		if len(owners[f]) > 1 {
			out = append(out, fmt.Sprintf("%s: edited by agents %s", f, joinInts(owners[f])))
		}
	}
	return out
}

func sortedUnique(s []string) []string {
	m := map[string]bool{}
	var out []string
	for _, x := range s {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	// stable order prevents lock-order deadlocks
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// argHint picks the most telling argument of a tool call for one-line
// display (path, pattern, command, query).
func argHint(args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	for _, k := range []string{"path", "pattern", "command", "query", "name", "url", "action"} {
		if v, ok := m[k].(string); ok && v != "" {
			return clip(v, 80)
		}
	}
	return ""
}
