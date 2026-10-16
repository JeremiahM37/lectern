package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// SubagentFactory builds a subagent for role ("explore", "code" or "web")
// whose events go to sink. The app wiring layer supplies it, because it knows
// which tools and models each role gets.
type SubagentFactory func(role, id string, sink Sink) *Agent

// Dispatcher provides the dispatch tool: parallel subagents.
type Dispatcher struct {
	New           SubagentFactory
	MaxConcurrent int // default 3, clamped to 1..8
	Sink          Sink

	// retryWait is the pause before a code task's retry. Zero means the
	// default (2 s).
	retryWait time.Duration

	seq atomic.Int64
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
					"id":         tools.Str("Optional name for this task, used in other tasks' depends_on"),
					"task":       tools.Str("Complete instructions for the subagent"),
					"role":       tools.Enum("explore = read-only investigation of the code, code = make edits, web = research on the internet (web search and page fetch only)", "explore", "code", "web"),
					"files":      tools.Arr("Files the task is about (code tasks: files it may edit)", map[string]any{"type": "string"}),
					"depends_on": tools.Arr("Ids of tasks that must finish before this one starts", map[string]any{"type": "string"}),
				},
				"required": []string{"task", "role"},
			}),
		}, "tasks"),
	}
}

type subTask struct {
	ID        string   `json:"id"`
	Task      string   `json:"task"`
	Role      string   `json:"role"`
	Files     []string `json:"files"`
	DependsOn []string `json:"depends_on"`
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
	if len(in.Tasks) > 8 {
		return tools.Errorf("dispatch takes at most 8 tasks per call; got %d", len(in.Tasks))
	}
	for i, st := range in.Tasks {
		if st.Role != "explore" && st.Role != "code" && st.Role != "web" {
			in.Tasks[i].Role = "explore"
		}
		if RestrictedModes[env.Mode] && in.Tasks[i].Role == "code" {
			return tools.Errorf("mode_restricted: %s mode is read-only; only explore tasks can be dispatched", env.Mode)
		}
		if strings.TrimSpace(st.Task) == "" {
			return tools.Errorf("task %d is empty", i+1)
		}
	}
	plan, err := planDispatch(in.Tasks)
	if err != nil {
		return tools.Errorf("dispatch: %v", err)
	}
	d := t.d
	if d.New == nil {
		return tools.Errorf("subagents are not available in this session")
	}
	limit := d.MaxConcurrent
	if limit <= 0 {
		limit = 3
	}
	if limit > 8 {
		limit = 8
	}

	n := len(in.Tasks)
	run := newDispatchRun(d, env)
	for _, st := range in.Tasks {
		run.board.add(st.Role, st.Task)
	}
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
			// Wait for dependencies before taking a slot, so a waiting task
			// never holds one that its dependency needs.
			for _, dep := range plan.deps[i] {
				select {
				case <-done[dep]:
				case <-ctx.Done():
					skip(i, ctx.Err())
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
				skip(i, ctx.Err())
				return
			}
			defer func() { <-sem }()
			if st.Role == "code" {
				for _, f := range sortedUnique(st.Files) {
					locks[f].Lock()
					defer locks[f].Unlock()
				}
			}
			task := st.Task
			if len(st.Files) > 0 {
				task += "\n\nRelevant files: " + strings.Join(st.Files, ", ")
			}
			task = withDependencyReports(task, plan, i, results)
			results[i] = run.runTask(ctx, i, st.Role, task)
		}(i, st)
	}
	wg.Wait()

	// Files that code tasks changed, and the project check that follows them.
	var changed []string
	var reg *tools.Registry
	for _, r := range results {
		changed = append(changed, r.edited...)
		if r.reg != nil {
			reg = r.reg
		}
	}
	changed = sortedUnique(changed)
	var check verifyOutcome
	if len(changed) > 0 {
		check = run.verify(ctx, env, reg, changed)
		changed = sortedUnique(append(changed, check.edited...))
	}

	ok := 0
	for _, r := range results {
		if r.err == nil {
			ok++
		}
	}
	var b strings.Builder
	b.WriteString(renderTaskReports(in.Tasks, plan, results))
	if run.failureCount() >= breakerThreshold {
		fmt.Fprintf(&b, "## Circuit breaker\nOpened after %d provider failures; tasks that had not started were not run.\n\n", run.failureCount())
	}
	if len(changed) > 0 {
		fmt.Fprintf(&b, "## Verification\n%s\n\n", check.text)
	}
	summary := fmt.Sprintf("%d/%d agents finished", ok, n)
	if check.failing {
		summary += ", check failing"
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
	return res
}

// renderTaskReports writes one section per task: its report, or why it
// failed, and any retry notes.
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
		switch {
		case r.err != nil && r.report == "":
			fmt.Fprintf(&b, "FAILED: %v\n\n", r.err)
		case r.err != nil:
			fmt.Fprintf(&b, "(stopped early: %v)\n%s\n\n", r.err, r.report)
		default:
			fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(r.report))
		}
	}
	return b.String()
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
