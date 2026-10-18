package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// BusTools are the tools every subagent of a dispatch gets for the shared
// bus: report_finding posts a finding for the others and for the parent, and
// check_findings reads what the others have posted. Both are read-only: they
// change the bus, not the repository, so explore agents can use them.
func BusTools(id string, bus *AgentBus) []tools.Tool {
	return []tools.Tool{
		reportFindingTool{id: id, bus: bus},
		checkFindingsTool{id: id, bus: bus},
		cancelDispatchTool{id: id, bus: bus},
	}
}

// PeerFeed returns the hook that hands agent id the findings its peers
// posted since the last call, so each step sees them without a check_findings
// call (spec 03 section 3.5). It returns "" when nothing is new. Findings are
// append-only, so the hook keeps an index; it is called from the agent's loop
// only.
func PeerFeed(id string, bus *AgentBus) func() string {
	if bus == nil {
		return nil
	}
	seen := 0
	return func() string {
		all := bus.Findings()
		var lines []string
		for _, f := range all[min(seen, len(all)):] {
			if f.Agent != id {
				lines = append(lines, formatFinding(f))
			}
		}
		seen = len(all)
		if len(lines) == 0 {
			return ""
		}
		return "<system-reminder>New findings from other subagents of this dispatch:\n" + strings.Join(lines, "\n") + "\n</system-reminder>"
	}
}

// cancelDispatchTool stops the whole dispatch: every running subagent is
// cancelled and the pending ones never start (spec 03 section 3.2).
type cancelDispatchTool struct {
	id  string
	bus *AgentBus
}

func (cancelDispatchTool) ReadOnly() bool { return true }

func (cancelDispatchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "cancel_dispatch",
		Description: "Stop every subagent of this dispatch, including yourself, when the work cannot succeed: the task is impossible, " +
			"a provider keeps failing, or continuing would only waste the run. Other agents' unfinished work is discarded. " +
			"Prefer finishing your own task; use this only when the whole dispatch should stop.",
		Schema: tools.Schema(map[string]any{
			"reason": tools.Str("Why the dispatch should stop, in one sentence"),
		}),
	}
}

func (t cancelDispatchTool) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	var in struct {
		Reason string `json:"reason"`
	}
	_ = tools.Decode(raw, &in) // the reason is optional
	if t.bus == nil {
		return tools.Errorf("cancel_dispatch is not available here: this agent is not part of a dispatch")
	}
	if !t.bus.Abort() {
		return tools.Result{Output: "The dispatch is already cancelled.", Summary: "already cancelled"}
	}
	msg := "Cancelled the dispatch: running agents are stopping and pending agents will not start."
	if r := strings.TrimSpace(in.Reason); r != "" {
		msg += " Reason: " + r
	}
	return tools.Result{Output: msg, Summary: "dispatch cancelled"}
}

// DispatchCancelTool is cancel_dispatch for the lead agent (spec 03 section
// 1.1). It stops one running subagent by id, or the whole dispatch with id
// "all", while that dispatch is still running. It goes through the same
// Dispatcher calls as the TUI's /cancel, so both stop agents the same way.
func DispatchCancelTool(d *Dispatcher) tools.Tool { return leadCancelTool{d: d} }

type leadCancelTool struct{ d *Dispatcher }

// ReadOnly is true for the same reason dispatch is: the agent loop runs
// read-only calls together, so a cancel issued alongside a running dispatch
// runs during it instead of after it returns.
func (leadCancelTool) ReadOnly() bool { return true }

func (leadCancelTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "cancel_dispatch",
		Description: "Stop one subagent of the dispatch that is running now, by its id (the ids are in the dispatch progress and in /agents), " +
			`or stop the whole dispatch with id "all". A running subagent stops and reports cancelled; a pending one never starts. ` +
			"Subagents that already finished keep their results, and the others keep running unless you cancel them. " +
			"Use it when a subagent is stuck or its result is no longer needed.",
		Schema: tools.Schema(map[string]any{
			"id": tools.Str(`The subagent to stop, or "all" to stop every subagent of the running dispatch`),
		}, "id"),
	}
}

func (t leadCancelTool) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	var in struct {
		ID string `json:"id"`
	}
	if err := tools.Decode(raw, &in); err != nil {
		return tools.Errorf("arguments must be a JSON object with id: a subagent id, or \"all\"")
	}
	if t.d == nil {
		return tools.Errorf("cancel_dispatch is not available: this session has no subagents")
	}
	// Checked first so the message says what is wrong: a finished dispatch
	// has ids too, and CancelAgent would only say the agent already finished.
	if !t.d.DispatchActive() {
		return tools.Errorf("no dispatch is running, so there is nothing to cancel: cancel_dispatch stops a dispatch that is in progress")
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return tools.Errorf(`name the subagent to stop by its id, or pass "all" to stop the whole dispatch`)
	}
	if id == "all" {
		if err := t.d.CancelDispatch(); err != nil {
			return tools.Errorf("%v", err)
		}
		return tools.Result{Output: "Cancelled the running dispatch: running subagents are stopping and pending ones will not start. Finished subagents keep their results.", Summary: "dispatch cancelled"}
	}
	if err := t.d.CancelAgent(id); err != nil {
		return tools.Errorf("%v", err)
	}
	return tools.Result{Output: fmt.Sprintf("Cancelled subagent %s. The other subagents of the dispatch keep running.", id), Summary: "subagent " + id + " cancelled"}
}

type reportFindingTool struct {
	id  string
	bus *AgentBus
}

func (reportFindingTool) ReadOnly() bool { return true }

func (reportFindingTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "report_finding",
		Description: "Post one finding for the other subagents of this dispatch and for the parent agent: a short, concrete fact " +
			"they should know (a bug, a convention, the place something is defined). Give the file and line when you have them. " +
			"One finding per call; keep each under 2 KB. Post only what matters; the parent reads every finding.",
		Schema: tools.Schema(map[string]any{
			"content": tools.Str("The finding, in one to three sentences"),
			"file":    tools.Str("Optional: the file it is about, relative to the project root"),
			"line":    tools.Str("Optional: the line number in that file"),
		}, "content"),
	}
}

// findingArgs accepts the near-miss argument names a model tends to use.
type findingArgs struct {
	Text string
	File string
	Line int
}

func parseFindingArgs(raw json.RawMessage) (findingArgs, error) {
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return findingArgs{}, fmt.Errorf("arguments must be a JSON object with content, file and line")
		}
	}
	var a findingArgs
	a.Text = firstString(m, "content", "text", "finding", "message", "summary", "label")
	a.File = firstString(m, "file", "path", "file_path")
	a.Line = lineNumber(m)
	return a, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// lineNumber reads line as a number or a numeric string; anything else is 0.
func lineNumber(m map[string]any) int {
	for _, k := range []string{"line", "line_number", "lineNumber"} {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(strings.Split(v, "-")[0])); err == nil {
				return n
			}
		}
	}
	return 0
}

func (t reportFindingTool) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	a, err := parseFindingArgs(raw)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	f := Finding{Agent: t.id, File: a.File, Line: a.Line, Text: a.Text}
	if err := t.bus.PostFinding(f); err != nil {
		return tools.Errorf("%v", err)
	}
	n := len(t.bus.Findings())
	return tools.Result{Output: fmt.Sprintf("Finding %d recorded. Other subagents see it at their next check_findings call, and the parent gets it in the report.", n), Summary: "finding posted"}
}

type checkFindingsTool struct {
	id  string
	bus *AgentBus
}

func (checkFindingsTool) ReadOnly() bool { return true }

func (checkFindingsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "check_findings",
		Description: "List the findings the other subagents of this dispatch have posted with report_finding. Call it before you repeat work a peer may already have done.",
		Schema:      tools.Schema(map[string]any{}),
	}
}

func (t checkFindingsTool) Run(_ context.Context, _ *tools.Env, _ json.RawMessage) tools.Result {
	fs := t.bus.FindingsExcept(t.id)
	if len(fs) == 0 {
		return tools.Result{Output: "No findings from other subagents yet.", Summary: "no findings"}
	}
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(formatFinding(f))
		b.WriteString("\n")
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d finding(s)", len(fs))}
}

// formatFinding is one finding as a line: "- [a2] file:line: text".
func formatFinding(f Finding) string {
	anchor := ""
	if f.File != "" {
		anchor = f.File
		if f.Line > 0 {
			anchor += ":" + strconv.Itoa(f.Line)
		}
		anchor += ": "
	}
	return fmt.Sprintf("- [%s] %s%s", f.Agent, anchor, f.Text)
}
