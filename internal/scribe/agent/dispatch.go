package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
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
	Bus *AgentBus
	// ReadCache says whether its reads go through the bus cache (the
	// dispatch_cache feature). Findings are shared either way.
	ReadCache bool
	// Task is the task text, matched against the config's router_rules.
	Task string
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
	// Hooks, when set, sees each subagent start and finish.
	Hooks SubagentObserver
	// ArchiveDir receives the full text of a subagent report too long to show
	// inline (dispatch_archive.go). Empty saves nothing.
	ArchiveDir string
	// TaskStatus is told how a task that names a taskId ended: "done" when
	// it finished, "blocked" when it failed or never ran. The host uses it to
	// mark the task list item. Nil does nothing.
	TaskStatus func(taskID, status string)
	// Map is the code map. Task targets the map knows are kept without a disk
	// check, get their symbol ranges in a "Target files:" block, and get
	// warnings about the importers left out of the dispatch (spec 03 §3.1).
	// Nil means no map. Set it only to a real map: a typed nil is not nil here.
	Map CodeMap
	// Skills supplies the skill content that matches each task (spec 03 §3.1).
	// Nil adds nothing.
	Skills TaskSkills
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

	// WaitTimeout bounds how long a task waits for the tasks it depends on.
	// Zero takes defaultDependencyWait. A task that gives up is skipped with
	// the reason, and so are its dependents.
	WaitTimeout time.Duration

	// active counts dispatch calls in flight. It is the guard that keeps
	// git mutations out while subagents may be editing (DispatchActive).
	active atomic.Int32
	// lastBoard is the board of the newest dispatch, for Status.
	lastBoard atomic.Pointer[board]
	// lastBus is the bus of the newest dispatch, for CancelDispatch.
	lastBus atomic.Pointer[AgentBus]
	// reads is the read cache the newest dispatch leaves for the next one.
	reads cacheRef
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
// A running subagent is cancelled and reports "cancelled"; a pending one,
// including one waiting on its depends_on tasks, is marked cancelled and
// never starts. Other subagents keep running.
func (d *Dispatcher) CancelAgent(id string) error {
	b := d.lastBoard.Load()
	if b == nil {
		return fmt.Errorf("no dispatch has run in this session")
	}
	return b.cancelAgent(id)
}

// CancelDispatch stops the newest dispatch while it runs: every subagent is
// cancelled and the pending ones never start, as when one of them calls
// cancel_dispatch. It is the host's stop for a dispatch whose tool call is
// blocking the model, which cannot make a call of its own until it returns.
func (d *Dispatcher) CancelDispatch() error {
	b := d.lastBus.Load()
	if d.active.Load() == 0 || b == nil {
		return fmt.Errorf("no dispatch is running")
	}
	if !b.Abort() {
		return fmt.Errorf("the dispatch is already cancelled")
	}
	return nil
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
					"taskId":       tools.Str("Optional id of a task-list item this task is for; the item is marked done when the task finishes, or blocked when it fails"),
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
	// TaskID links the task to an item of the task list (spec 03 §3.1).
	TaskID string `json:"taskId"`
	// claimNotes are the cross-tab warnings for this task's target files,
	// set at planning and handed to its subagent (claimConflicts).
	claimNotes []string
}

// maxTasksPerDispatch is the most tasks one dispatch call runs (spec 03 §3.1).
const maxTasksPerDispatch = 8

// defaultDependencyWait is how long a task waits for its depends_on tasks
// (spec 03 §3.7: the agent wait, 300 s at the default tool timeout).
const defaultDependencyWait = 300 * time.Second

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
	// A code task with a long numbered list on one file is run as two
	// serial halves (spec 03 §3.1 rule 5).
	var split []string
	in.Tasks, split = splitOversizedCode(in.Tasks, maxTasksPerDispatch)
	notes = append(notes, split...)
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
		notes = append(notes, checkTargetsIn(in.Tasks, env.Workdir, d.Map)...)
		notes = append(notes, dependentNotes(in.Tasks, d.Map)...)
	}
	notes = append(notes, sharedFileNotes(in.Tasks)...)
	// Files another tab is editing: warned about on the task, never blocked.
	if env.Claims != nil {
		notes = append(notes, claimConflicts(in.Tasks, env.Workdir, env.Claims.OtherClaims())...)
	}

	n := len(in.Tasks)
	run := newDispatchRun(d, env)
	feats := d.features()
	if feats.DispatchCache {
		d.reads.seed(run.bus)
	}
	for i, st := range in.Tasks {
		if st.Role == "code" {
			run.targets[i] = codeLockKeys(st.Files)
		}
	}
	observe := cacheObserver(run.board)
	run.bus.SetCacheObserver(func(n CacheNote) {
		// A tool-result hit is not a file read: it must not change a board
		// row's cache state.
		if !n.Tool {
			observe(n)
		}
		run.cache.note(n)
	})
	d.lastBoard.Store(run.board)
	d.lastBus.Store(run.bus)
	for _, st := range in.Tasks {
		run.board.add(st.Role, st.Task)
	}
	// The dispatch's own context is cancelled when the breaker opens, which
	// stops the subagents still running. The check below uses the caller's.
	callerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	run.cancel = cancel
	run.bus.SetAbort(cancel)
	sem := make(chan struct{}, limit)
	// Code tasks that touch the same file run one after another. A range
	// does not split the lock: the file is what two edits can clash on.
	locks := map[string]*sync.Mutex{}
	for _, st := range in.Tasks {
		if st.Role == "code" {
			for _, f := range codeLockKeys(st.Files) {
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
		status := "error"
		if errors.Is(err, errCancelledByUser) || errors.Is(err, errDispatchCancelled) {
			status = "cancelled"
		}
		run.board.update(i, func(a *AgentInfo) { a.Status, a.Error = status, err.Error() })
	}
	waitFor := d.WaitTimeout
	if waitFor <= 0 {
		waitFor = defaultDependencyWait
	}
	ids := plan.ids

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
			if err := run.awaitDeps(ctx, i, plan.deps[i], done, results, ids, waitFor); err != nil {
				skip(i, err)
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				skip(i, run.stopErr(ctx))
				return
			}
			defer func() { <-sem }()
			if st.Role == "code" {
				for _, f := range codeLockKeys(st.Files) {
					locks[f].Lock()
					defer locks[f].Unlock()
				}
			}
			task := withDependencyReports(taskText(st), plan, i, results)
			task = withPeerObjectives(task, in.Tasks, plan, i)
			task = withTargetSymbols(task, st, d.Map)
			task = withSkillText(task, st.Task, d.Skills)
			results[i] = run.runTask(ctx, i, st.Role, PurposeTask, task)
		}(i, st)
	}
	wg.Wait()
	if feats.DispatchCache {
		d.reads.keep(run.bus)
	}
	archiveReports(results, runIDs(run.board, n), d.ArchiveDir, env.CallID)
	links := archiveLinks(runIDs(run.board, n), d.ArchiveDir, env.CallID)
	markTaskItems(d.TaskStatus, in.Tasks, results)

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
	// Optional passes (agentFeatures). Desloppify is the deterministic lint fix
	// on the changed files, then a cleanup agent over them, both before the
	// project check reads the files (spec 03 §3.8, spec 06 §9.5). The review
	// runs read-only after the check.
	var lint, cleanup string
	var cleanupEdited []string
	if len(changed) > 0 && feats.Desloppify && d.New != nil {
		lint = lintFix(callerCtx, env, reg, changed)
		cleanup, cleanupEdited = run.desloppify(callerCtx, changed)
		changed = sortedUnique(append(changed, cleanupEdited...))
	}
	// The project check runs after every dispatch that changed files: it is
	// cheap and catches a broken build at once. Spec 03 §3.8 step 4 puts it
	// behind verify_edits; Lectern keeps it on, and only the read-only model
	// review (which costs a model call) waits for verify_edits.
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
	if fs := run.bus.Findings(); len(fs) > 0 {
		b.WriteString("## Findings\n")
		for _, f := range fs {
			fmt.Fprintf(&b, "%s\n", formatFinding(f))
		}
		b.WriteString("\n")
	}
	b.WriteString(filesEditedSection(results))
	if len(links) > 0 {
		b.WriteString("## Archived reports\n")
		for _, l := range links {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		b.WriteString("\n")
	}
	if forced := run.locks.forcedReleases(); len(forced) > 0 {
		b.WriteString("## Edit locks\n")
		for _, f := range forced {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	if cache := run.cache.render(run.board.labels()); cache != "" {
		fmt.Fprintf(&b, "## Cache\n%s\n", cache)
	}
	if run.bus.Aborted() {
		b.WriteString("## Cancelled\nAn agent of this dispatch called cancel_dispatch. Agents that had not finished were stopped.\n\n")
	}
	if run.tripped() {
		fmt.Fprintf(&b, "## Circuit breaker\nOpened after %d provider failures; tasks that had not started were not run.\n\n", run.failureCount())
	}
	if text := renderCleanup(lint, cleanup, cleanupEdited); text != "" {
		fmt.Fprintf(&b, "## Cleanup\n%s\n", text)
	}
	if check.status != "" {
		fmt.Fprintf(&b, "## Verification\n%s\n\n", check.text)
	}
	if verdict != "" {
		fmt.Fprintf(&b, "## Review\n%s\n\n", renderReview(review, verdict))
	}
	summary := fmt.Sprintf("%d/%d agents finished", ok, n)
	if run.bus.Aborted() {
		summary += ", cancelled"
	}
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
	if check.status != "" {
		res.Meta["verification"] = check.status
	}
	if verdict != "" {
		res.Meta["review"] = verdict
	}
	return res
}

// cacheObserver puts each cache outcome on the row of the subagent it is about.
// A note for an id the board does not hold (a read made outside a subagent) is
// dropped, since no row could show it.
func cacheObserver(b *board) func(CacheNote) {
	return func(n CacheNote) {
		b.mu.Lock()
		idx := -1
		for i, info := range b.infos {
			if info.ID == n.Agent {
				idx = i
				break
			}
		}
		b.mu.Unlock()
		if idx < 0 {
			return
		}
		b.update(idx, func(a *AgentInfo) { a.Cache, a.CacheFrom = n.Outcome, n.From })
	}
}

// runIDs is the subagent id of each of the first n tasks, as the board names them.
func runIDs(b *board, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = b.id(i)
	}
	return out
}

// markTaskItems reports each task that names a task-list item as done or
// blocked. A task that never ran is blocked too.
func markTaskItems(status func(taskID, status string), tasks []subTask, results []taskResult) {
	if status == nil {
		return
	}
	for i, st := range tasks {
		if st.TaskID == "" {
			continue
		}
		if results[i].err != nil {
			status(st.TaskID, "blocked")
		} else {
			status(st.TaskID, "done")
		}
	}
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
		if item := strings.TrimSpace(st.TaskID); item != "" {
			idPart += " taskId=" + item
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
			fmt.Fprintf(&b, "(stopped early%s: %v)\n%s\n", attemptsSuffix(r.attempts), r.err, report)
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

// attemptsSuffix names how many provider attempts a stopped task made, so a
// partial report says how hard the task was tried.
func attemptsSuffix(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return " after 1 attempt"
	}
	return fmt.Sprintf(" after %d attempts", n)
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
		task += "\n\nRelevant files: " + strings.Join(describeFiles(st.Files), ", ")
	}
	for _, note := range st.claimNotes {
		task += "\n\n" + note
	}
	if fmtText := returnFormatText[st.ReturnFormat]; fmtText != "" {
		task += "\n\n" + fmtText
	} else {
		task += "\n\n" + returnFormatText["summary"]
	}
	return task
}

// checkTargets drops code-task target files that do not exist, since an
// edit to them cannot start (spec 03 §3.1 rule 1). It is checkTargetsIn with
// no code map.
func checkTargets(tasks []subTask, workdir string) []string {
	return checkTargetsIn(tasks, workdir, nil)
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

// DisposeLastBus ends the newest dispatch's bus caches and forgets the bus, so
// a closed tab does not keep its reads and results alive, and CancelDispatch
// no longer reaches it. It is safe to call with no dispatch on record.
func (d *Dispatcher) DisposeLastBus() {
	d.lastBus.Swap(nil).Dispose()
}
