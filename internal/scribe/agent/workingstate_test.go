package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestWorkingStateExtractsFilesCommandsAndFailures(t *testing.T) {
	msgs := []llm.Message{
		userMsg("Refactor the lexer please"),
		asstMsg("I will read the lexer and then edit it carefully."),
		callMsg("r1", "read", `{"path":"lexer.go"}`),
		resultMsg("r1", "read", "package lexer", false),
		callMsg("e1", "edit_file", `{"path":"lexer.go","old_string":"a","new_string":"b"}`),
		resultMsg("e1", "edit_file", "edited", false),
		callMsg("e2", "write_file", `{"path":"refused.go","content":"x"}`),
		resultMsg("e2", "write_file", "permission denied", true),
		callMsg("s1", "shell", `{"command":"go test ./..."}`),
		resultMsg("s1", "shell", "FAIL", true),
	}
	ws := BuildWorkingState(msgs)
	if ws.Task != "Refactor the lexer please" {
		t.Fatalf("task = %q", ws.Task)
	}
	if len(ws.Files) != 1 || ws.Files[0].Path != "lexer.go" {
		t.Fatalf("files = %+v (refused write must not appear)", ws.Files)
	}
	if got := strings.Join(ws.Files[0].Actions, " | "); got != "read | edited: a -> b" {
		t.Fatalf("actions = %q", got)
	}
	var ran, failed bool
	for _, r := range ws.ToolResults {
		if r.Tool == "shell" && r.Summary == "ran: go test ./..." {
			ran = true // a failed command still ran
		}
	}
	for _, f := range ws.Failures {
		if strings.HasPrefix(f, "write_file: permission denied") {
			failed = true
		}
	}
	if !ran || !failed {
		t.Fatalf("ran=%v failed=%v; tool results %+v failures %v", ran, failed, ws.ToolResults, ws.Failures)
	}
}

func TestWorkingStateSkipsEarlierSummaries(t *testing.T) {
	msgs := []llm.Message{
		userMsg("<conversation_summary>\nold summary text here\n</conversation_summary>"),
		userMsg("ok"),
		userMsg("now add the lexer tests"),
	}
	ws := BuildWorkingState(msgs)
	if len(ws.UserRequirements) != 1 || ws.UserRequirements[0] != "now add the lexer tests" {
		t.Fatalf("user requirements = %q", ws.UserRequirements)
	}
}

func TestAssistantNoteRules(t *testing.T) {
	short := "Done with the lexer change."
	if got := assistantNote(short); got != short {
		t.Fatalf("short note = %q", got)
	}
	if got := assistantNote("too short"); got != "" {
		t.Fatalf("under 20 chars must be dropped, got %q", got)
	}
	long := strings.Repeat("This sentence is here. ", 12)
	got := assistantNote(long)
	if n := strings.Count(got, "."); n > 5 || n == 0 || len(got) > 500 {
		t.Fatalf("long note has %d sentences, %d chars: %q", n, len(got), got)
	}
}

func TestWorkingStateCapsAndSerializationOrder(t *testing.T) {
	var msgs []llm.Message
	for i := 0; i < 40; i++ {
		msgs = append(msgs, userMsg(fmt.Sprintf("request number %d", i)))
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, callMsg(id, "read", fmt.Sprintf(`{"path":"f%d.go"}`, i)))
		msgs = append(msgs, resultMsg(id, "read", "ok", false))
	}
	ws := BuildWorkingState(msgs)
	if len(ws.UserRequirements) != maxUserRequirements || len(ws.ToolResults) != maxToolResults {
		t.Fatalf("caps: requirements=%d results=%d", len(ws.UserRequirements), len(ws.ToolResults))
	}
	if len(ws.Files) != 40 {
		t.Fatalf("files are unbounded, got %d", len(ws.Files))
	}
	if ws.Slots() < richStateSlots {
		t.Fatalf("slots = %d, want a rich state", ws.Slots())
	}

	out := WorkingState{
		Task:        "ship it",
		Files:       []fileState{{Path: "a.go", Actions: []string{"read"}}},
		Failures:    []string{"shell: boom"},
		Discoveries: []string{"the cache is per-process"},
	}.Serialize()
	order := []string{"## Task", "## Files Touched", "## Errors & Failures", "## Discoveries"}
	last := -1
	for _, h := range order {
		i := strings.Index(out, h)
		if i <= last {
			t.Fatalf("section %q out of order in:\n%s", h, out)
		}
		last = i
	}
	if strings.Contains(out, "## Key Decisions") {
		t.Fatal("empty sections must be omitted")
	}
}

// The harness appends a system-reminder block to a user message. It is not
// the user's requirement, so the working state must not record it.
func TestWorkingStateSkipsSystemReminder(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("read notes1.txt <system-reminder> Memories from earlier sessions that may apply"),
		llm.UserText("<system-reminder> Memories again </system-reminder> now fix the parser"),
	}
	ws := BuildWorkingState(msgs)
	if len(ws.UserRequirements) != 2 {
		t.Fatalf("requirements = %q", ws.UserRequirements)
	}
	if ws.UserRequirements[0] != "read notes1.txt" || ws.UserRequirements[1] != "now fix the parser" {
		t.Fatalf("reminder text leaked into requirements: %q", ws.UserRequirements)
	}
}
