package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A dispatch row that is still running shows its live agent count after the
// elapsed time (spec 01 2.9 suffix: " · 12s · 2/3 agents").
func TestRunningDispatchShowsAgentProgress(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "split it", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolStart, CallID: "d", Tool: "dispatch", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolRun, CallID: "d", Tool: "dispatch", At: at(c),
		Args: json.RawMessage(`{"tasks":[]}`)})
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d", At: at(c), Agent: &agent.AgentInfo{ID: "a", Task: "one", Role: "code", Status: "done"}})
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d", At: at(c), Agent: &agent.AgentInfo{ID: "b", Task: "two", Role: "code", Status: "running"}})
	c.t = c.t.Add(5 * time.Second)
	if out := plain(v); !strings.Contains(out, "1/2 agents") {
		t.Fatalf("running dispatch should show 1/2 agents:\n%s", out)
	}
}

// A finished dispatch with a failed task says how many attempts it made, and a
// child agent shows the prompt-cache reads it got back in amber.
func TestDispatchShowsAttemptsAndCacheReads(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "split it", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolStart, CallID: "d", Tool: "dispatch", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolRun, CallID: "d", Tool: "dispatch", At: at(c), Args: json.RawMessage(`{"tasks":[]}`)})
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d", At: at(c), Agent: &agent.AgentInfo{
		ID: "a", Task: "cached work", Role: "code", Status: "done",
		Tokens: llm.Usage{InputTokens: 900, OutputTokens: 100, CacheReadTokens: 5100}}})
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d", At: at(c), Agent: &agent.AgentInfo{ID: "b", Task: "failing", Role: "code", Status: "error", Error: "timeout"}})
	v.Apply(agent.Event{Kind: agent.EvToolResult, CallID: "d", Tool: "dispatch", At: at(c), Result: &tools.Result{
		Output: "FAILED after 2 attempts: timeout"}})
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	v.ToggleExpand() // finished dispatch children and tool details fold until expanded
	out := plain(v)
	if !strings.Contains(out, "failed after 2 attempts") {
		t.Fatalf("attempt count missing:\n%s", out)
	}
	if !strings.Contains(out, "[cache 5.1k]") {
		t.Fatalf("cache tag missing on the child row:\n%s", out)
	}
}

func TestAttemptNote(t *testing.T) {
	cases := map[string]string{
		"nothing failed":            "",
		"FAILED after 1 attempt: x": "failed after 1 attempt",
		"FAILED after 3 attempts: a\nFAILED after 2 attempts: b": "2 failed, up to 3 attempts",
	}
	for in, want := range cases {
		if got := attemptNote(in); got != want {
			t.Errorf("attemptNote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A multi_edit diff is one joined before/after from Meta, so both edits sit in
// one diff, and an impact line follows the box.
func TestMultiEditJoinedDiffAndImpact(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "edit", At: at(c)})
	before := "a\nb\nc\nd\ne\nf\ng\nh\n"
	after := "A\nb\nc\nd\ne\nf\ng\nH\n"
	tool(v, "m", "multi_edit", map[string]any{"path": "x.go"}, c)
	result(v, "m", "multi_edit", tools.Result{Output: "Applied 2 edits",
		Meta: map[string]any{"before": before, "after": after, "impact": "3 callers"}}, time.Millisecond, c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	v.ToggleExpand()
	out := plain(v)
	if !strings.Contains(out, "x.go +2 -2") || !strings.Contains(out, "│ 8   - h") {
		t.Fatalf("both edits should be in one joined diff:\n%s", out)
	}
	if !strings.Contains(out, "[impact: 3 callers]") {
		t.Fatalf("impact line missing:\n%s", out)
	}
}

func TestFocusedTextIsMessageAtBottom(t *testing.T) {
	v, c := newView(t, Options{})
	if _, ok := v.FocusedText(); ok {
		t.Fatalf("empty transcript has no focused message")
	}
	v.AddUser("first question", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "first question", At: at(c)})
	text(v, "First answer.", c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	got, ok := v.FocusedText()
	if !ok || got != "First answer." {
		t.Fatalf("FocusedText = %q, %v; want the newest answer", got, ok)
	}
}

func TestSessionHeaderForForeignAndCompactedSessions(t *testing.T) {
	v, _ := newView(t, Options{})
	v.Restore([]Record{{Role: roleUser, Text: "hi"}, {Role: roleSystem, Kind: "compact", Before: 40, After: 6}})
	if v.SessionHeader() != "resumed after compaction" {
		t.Fatalf("compacted session header = %q", v.SessionHeader())
	}
	if first := strings.Split(plain(v), "\n")[0]; !strings.Contains(first, "── resumed after compaction ──") {
		t.Fatalf("header should open the transcript, got %q", first)
	}
	v.SetSessionHeader("from ~/other-project")
	if first := strings.Split(plain(v), "\n")[0]; !strings.Contains(first, "── from ~/other-project ──") {
		t.Fatalf("foreign header should replace it, got %q", first)
	}
	v.SetSessionHeader("")
	if strings.Contains(plain(v), "──") {
		t.Fatalf("empty header should hide the line")
	}
}

// Ctrl+Y copies the answer as written: line breaks and markdown kept.
func TestFocusedTextKeepsLinesAndMarkdown(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("summaries", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "summaries", At: at(c)})
	text(v, "- **calc.go**: arithmetic\n- **greet.go**: greeting", c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	got, _ := v.FocusedText()
	if got != "- **calc.go**: arithmetic\n- **greet.go**: greeting" {
		t.Fatalf("copied %q", got)
	}
}
