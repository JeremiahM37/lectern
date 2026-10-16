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

// SubagentFactory builds a subagent for role ("explore" or "code") whose
// events go to sink. The app wiring layer supplies it, because it knows
// which tools and models each role gets.
type SubagentFactory func(role, id string, sink Sink) *Agent

// Dispatcher provides the dispatch tool: parallel subagents.
type Dispatcher struct {
	New           SubagentFactory
	MaxConcurrent int // default 3, clamped to 1..8
	Sink          Sink

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
			"(each answers one question), code agents for independent, fully specified edits in different files. " +
			"Each task must be self-contained: the subagent sees only the task text, not this conversation.",
		Schema: tools.Schema(map[string]any{
			"tasks": tools.Arr("Tasks to run in parallel", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task":  tools.Str("Complete instructions for the subagent"),
					"role":  tools.Enum("explore = read-only investigation, code = make edits", "explore", "code"),
					"files": tools.Arr("Files the task is about (code tasks: files it may edit)", map[string]any{"type": "string"}),
				},
				"required": []string{"task", "role"},
			}),
		}, "tasks"),
	}
}

type subTask struct {
	Task  string   `json:"task"`
	Role  string   `json:"role"`
	Files []string `json:"files"`
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
		if st.Role != "explore" && st.Role != "code" {
			in.Tasks[i].Role = "explore"
		}
		if RestrictedModes[env.Mode] && in.Tasks[i].Role == "code" {
			return tools.Errorf("mode_restricted: %s mode is read-only; only explore tasks can be dispatched", env.Mode)
		}
		if strings.TrimSpace(st.Task) == "" {
			return tools.Errorf("task %d is empty", i+1)
		}
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
	type outcome struct {
		report string
		err    error
		usage  llm.Usage
		dur    time.Duration
	}
	results := make([]outcome, len(in.Tasks))
	infos := make([]*AgentInfo, len(in.Tasks))
	var mu sync.Mutex
	publish := func(i int, f func(*AgentInfo)) {
		mu.Lock()
		f(infos[i])
		c := *infos[i]
		mu.Unlock()
		if d.Sink != nil {
			d.Sink(Event{Kind: EvSubagent, At: time.Now(), Agent: &c, CallID: env.CallID})
		}
	}
	for i, st := range in.Tasks {
		infos[i] = &AgentInfo{ID: fmt.Sprintf("a%d", d.seq.Add(1)), Task: st.Task, Role: st.Role, Status: "pending"}
		publish(i, func(*AgentInfo) {})
	}
	var wg sync.WaitGroup
	for i, st := range in.Tasks {
		wg.Add(1)
		go func(i int, st subTask) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			defer func() { <-sem }()
			if st.Role == "code" {
				for _, f := range sortedUnique(st.Files) {
					locks[f].Lock()
					defer locks[f].Unlock()
				}
			}
			id := infos[i].ID
			sub := d.New(st.Role, id, func(e Event) {
				switch e.Kind {
				case EvStepFinish:
					publish(i, func(a *AgentInfo) { a.Step = e.Step; a.Tokens = e.Total })
				case EvToolRun:
					publish(i, func(a *AgentInfo) { a.Last = e.Tool + " " + argHint(e.Args) })
				case EvFallback:
					publish(i, func(a *AgentInfo) { a.Model = e.Model })
				}
				if env.Host != nil && e.Kind == EvToolRun {
					env.Host.Progress(env.CallID, fmt.Sprintf("[%s] %s %s", id, e.Tool, argHint(e.Args)))
				}
			})
			if sub == nil {
				results[i].err = fmt.Errorf("could not start a %s agent", st.Role)
				return
			}
			publish(i, func(a *AgentInfo) { a.Status = "running"; a.Model = sub.CurrentModel().Ref })
			task := st.Task
			if len(st.Files) > 0 {
				task += "\n\nRelevant files: " + strings.Join(st.Files, ", ")
			}
			start := time.Now()
			stats, err := sub.Run(ctx, task, nil)
			out := ""
			msgs := sub.Messages()
			for k := len(msgs) - 1; k >= 0; k-- {
				if msgs[k].Role == llm.RoleAssistant && strings.TrimSpace(msgs[k].Text()) != "" {
					out = msgs[k].Text()
					break
				}
			}
			results[i] = outcome{report: out, err: err, usage: stats.Tokens, dur: time.Since(start)}
			publish(i, func(a *AgentInfo) {
				a.Tokens = stats.Tokens
				a.Step = stats.Steps
				if err != nil {
					a.Status, a.Error = "error", err.Error()
				} else {
					a.Status = "done"
				}
			})
		}(i, st)
	}
	wg.Wait()

	var b strings.Builder
	var files []string
	ok := 0
	for i, st := range in.Tasks {
		r := results[i]
		fmt.Fprintf(&b, "## Agent %d (%s): %s\n", i+1, st.Role, firstLine(clip(st.Task, 120)))
		switch {
		case r.err != nil && r.report == "":
			fmt.Fprintf(&b, "FAILED: %v\n\n", r.err)
		case r.err != nil:
			fmt.Fprintf(&b, "(stopped early: %v)\n%s\n\n", r.err, r.report)
		default:
			ok++
			fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(r.report))
		}
		if st.Role == "code" {
			files = append(files, st.Files...)
		}
	}
	return tools.Result{
		Output:  strings.TrimSpace(b.String()),
		Summary: fmt.Sprintf("%d/%d agents finished", ok, len(in.Tasks)),
		Files:   sortedUnique(files),
		IsError: ok == 0,
	}
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
