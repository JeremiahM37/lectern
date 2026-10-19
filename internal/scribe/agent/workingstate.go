package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// WorkingState is the structured record of a conversation that compaction
// keeps instead of asking a model to summarise it. It is extracted from the
// messages being compacted, with the caps the spec sets for each field.
type WorkingState struct {
	Task             string
	UserRequirements []string
	// Plan is the active plan at compaction time: its title and each step's
	// status (spec 03 section 8.5). Nil when there is no plan. It comes from
	// the Planner, not the messages, so it is set by the caller.
	Plan           *Plan
	Environment    []string
	Files          []fileState
	Decisions      []string
	AssistantNotes []string
	ToolResults    []toolSummary
	Failures       []string
	Discoveries    []string
}

// fileState lists what happened to one path, in the order it happened.
type fileState struct {
	Path    string
	Actions []string
}

type toolSummary struct {
	Tool    string
	Summary string
}

// Caps from the spec's working-state model.
const (
	maxUserRequirements = 25
	maxToolResults      = 30
	maxAssistantNotes   = 25
	maxFailures         = 25
	maxTaskChars        = 300
	maxCommandChars     = 200
	richStateSlots      = 15 // at or above this the state is the summary, no model call
)

// summaryHeadPrefix marks a message that is itself a compaction summary, so
// a second compaction does not record the summary as a user requirement.
const summaryHeadPrefix = "<conversation_summary>"

var sentenceEnd = regexp.MustCompile(`[.!?](\s+|$)`)

// systemReminder is the context the harness appends to a user message (memory
// hints and the like). It is not something the user asked for, so it is not
// recorded as a requirement. An unclosed block runs to the end of the text.
var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?(?:</system-reminder>|\z)`)

// BuildWorkingState extracts the working state from msgs in one pass.
// BuildWorkingState keeps the default number of tool-result summaries.
func BuildWorkingState(msgs []llm.Message) WorkingState {
	return buildWorkingState(msgs, maxToolResults)
}

// buildWorkingState builds the state, keeping at most toolCap tool-result
// summaries. max_tool_results sets toolCap, so a config value above the default
// is honoured.
func buildWorkingState(msgs []llm.Message, toolCap int) WorkingState {
	var ws WorkingState
	files := map[string]*fileState{}
	var fileOrder []string
	addAction := func(path, action string) {
		fs, ok := files[path]
		if !ok {
			fs = &fileState{Path: path}
			files[path] = fs
			fileOrder = append(fileOrder, path)
		}
		if n := len(fs.Actions); n == 0 || fs.Actions[n-1] != action {
			fs.Actions = append(fs.Actions, action)
		}
	}
	// names maps a call id to its tool name, so results can be labelled.
	names := map[string]string{}
	failed := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == llm.PartToolResult && p.IsError && !shellRan(p.Result) {
				failed[p.ToolCallID] = true
			}
		}
	}

	for _, m := range msgs {
		for _, p := range m.Parts {
			switch p.Type {
			case llm.PartText:
				text := strings.TrimSpace(systemReminder.ReplaceAllString(p.Text, ""))
				switch m.Role {
				case llm.RoleUser:
					if strings.HasPrefix(text, summaryHeadPrefix) {
						continue
					}
					if len(text) >= 5 {
						ws.UserRequirements = appendCapped(ws.UserRequirements, clip(flatten(text), 300), maxUserRequirements)
						ws.Task = clip(flatten(text), maxTaskChars)
					}
				case llm.RoleAssistant:
					if note := assistantNote(text); note != "" {
						ws.AssistantNotes = appendCapped(ws.AssistantNotes, note, maxAssistantNotes)
					}
				}
			case llm.PartToolCall:
				names[p.ToolCallID] = p.ToolName
				// A failed file operation changed nothing. A shell command
				// that exited non-zero still ran and is recorded; one that was
				// denied or refused never started (see shellRan).
				if failed[p.ToolCallID] {
					continue
				}
				recordCall(p, addAction, &ws, toolCap)
			case llm.PartToolResult:
				tool := p.ToolName
				if tool == "" {
					tool = names[p.ToolCallID]
				}
				first := clip(firstLine(p.Result), 120)
				if p.IsError {
					ws.Failures = appendCapped(ws.Failures, clip(tool+": "+firstLine(p.Result), 200), maxFailures)
					continue
				}
				ws.ToolResults = appendToolResult(ws.ToolResults, toolSummary{Tool: tool, Summary: first}, toolCap)
			}
		}
	}
	for _, path := range fileOrder {
		ws.Files = append(ws.Files, *files[path])
	}
	return ws
}

// shellRan reports whether a failed tool result is a command that started:
// a finished command ends with its exit footer, a stopped one says its
// process group was killed. Denials and refusals have neither.
func shellRan(result string) bool {
	return strings.Contains(result, "[exit ") || strings.Contains(result, "process group was killed]")
}

// recordCall turns one successful tool call into state: files touched, and
// for shell calls a "ran:" line in the tool results.
func recordCall(p llm.Part, addAction func(path, action string), ws *WorkingState, toolCap int) {
	var a struct {
		Path      string   `json:"path"`
		Paths     []string `json:"paths"`
		Command   string   `json:"command"`
		OldString string   `json:"old_string"`
		NewString string   `json:"new_string"`
	}
	_ = json.Unmarshal(p.Args, &a)
	switch p.ToolName {
	case "read":
		if a.Path != "" {
			addAction(a.Path, "read")
		}
		for _, x := range a.Paths {
			addAction(x, "read")
		}
	case "edit_file", "multi_edit", "ast_edit", "rename_symbol":
		if a.Path != "" {
			addAction(a.Path, "edited: "+clip(flatten(a.OldString), 60)+" -> "+clip(flatten(a.NewString), 60))
		}
	case "write_file":
		if a.Path != "" {
			addAction(a.Path, "created")
		}
	case "shell":
		if a.Command != "" {
			ws.ToolResults = appendToolResult(ws.ToolResults, toolSummary{Tool: "shell", Summary: "ran: " + clip(a.Command, maxCommandChars)}, toolCap)
		}
	}
}

// assistantNote keeps assistant text that is at least 20 characters. Short
// texts (up to three sentences) are kept whole up to 300 characters; longer
// ones keep up to five sentences within a 500-character budget.
func assistantNote(text string) string {
	if len(text) < 20 {
		return ""
	}
	flat := flatten(text)
	ends := sentenceEnd.FindAllStringIndex(flat, -1)
	if len(ends) <= 3 {
		return clip(flat, 300)
	}
	var b strings.Builder
	n, start := 0, 0
	for _, loc := range ends {
		if n == 5 {
			break
		}
		sentence := strings.TrimSpace(flat[start:loc[1]])
		start = loc[1]
		if b.Len()+len(sentence)+1 > 500 {
			break
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(sentence)
		n++
	}
	if b.Len() == 0 {
		return clip(text, 300)
	}
	return b.String()
}

// Slots is the count the spec uses to decide whether the structured state
// is rich enough to be the whole summary.
func (ws WorkingState) Slots() int {
	n := len(ws.Files) + len(ws.Decisions) + len(ws.Failures) + len(ws.Discoveries) +
		len(ws.ToolResults) + len(ws.Environment) + len(ws.UserRequirements) + len(ws.AssistantNotes)
	if ws.Task != "" {
		n++
	}
	return n
}

// Serialize writes the state in the fixed section order, omitting empty
// sections. The output is the summary body used by compaction.
func (ws WorkingState) Serialize() string {
	var b strings.Builder
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n", title)
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	if ws.Task != "" {
		section("Task", []string{ws.Task})
	}
	section("User Requirements", bullets(ws.UserRequirements))
	section("Plan", planLines(ws.Plan))
	section("Environment", bullets(ws.Environment))
	var files []string
	for _, f := range ws.Files {
		files = append(files, "- "+f.Path+": "+strings.Join(f.Actions, "; "))
	}
	section("Files Touched", files)
	section("Key Decisions", bullets(ws.Decisions))
	section("Assistant Notes", bullets(ws.AssistantNotes))
	var results []string
	for _, r := range ws.ToolResults {
		results = append(results, "- "+r.Tool+": "+r.Summary)
	}
	section("Tool Results", results)
	section("Errors & Failures", bullets(ws.Failures))
	section("Discoveries", bullets(ws.Discoveries))
	return strings.TrimSpace(b.String())
}

// planStatusIcons are the marks the Plan section uses for each step status.
// An unknown status shows as pending, the safest reading for a model.
var planStatusIcons = map[string]string{
	"done":    "[x]",
	"active":  "[>]",
	"skipped": "[-]",
	"pending": "[ ]",
}

// planLines renders the active plan: its title, then one line per step with
// the step id, so a model that resumes after compaction can still name the
// step to update. A nil plan renders nothing.
func planLines(p *Plan) []string {
	if p == nil {
		return nil
	}
	var out []string
	if title := clip(flatten(p.Title), maxTaskChars); title != "" {
		out = append(out, "Title: "+title)
	}
	for _, st := range p.Steps {
		icon, ok := planStatusIcons[st.Status]
		if !ok {
			icon = planStatusIcons["pending"]
		}
		out = append(out, "- "+icon+" "+st.ID+": "+clip(flatten(st.Text), maxCommandChars))
	}
	return out
}

func bullets(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, "- "+x)
	}
	return out
}

func appendCapped(xs []string, v string, max int) []string {
	xs = append(xs, v)
	if len(xs) > max {
		xs = xs[len(xs)-max:]
	}
	return xs
}

func appendToolResult(xs []toolSummary, v toolSummary, max int) []toolSummary {
	xs = append(xs, v)
	if len(xs) > max {
		xs = xs[len(xs)-max:]
	}
	return xs
}

// flatten collapses whitespace so a value fits on one bullet.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
