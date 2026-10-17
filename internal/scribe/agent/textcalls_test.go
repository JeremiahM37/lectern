package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

var textCallTools = map[string]bool{"shell": true, "read": true}

func TestRecoverHermesJSONCall(t *testing.T) {
	clean, calls := recoverTextCalls(`<tool_call>{"name":"shell","arguments":{"command":"ls"}}</tool_call>`, textCallTools)
	if clean != "" || len(calls) != 1 {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
	if calls[0].name != "shell" || string(calls[0].args) != `{"command":"ls"}` {
		t.Fatalf("call = %s %s", calls[0].name, calls[0].args)
	}
}

func TestRecoverHermesStringArguments(t *testing.T) {
	// Some servers send the arguments as a JSON string holding the object.
	text := `<tool_call>{"name":"shell","arguments":"{\"command\":\"pwd\"}"}</tool_call>`
	_, calls := recoverTextCalls(text, textCallTools)
	if len(calls) != 1 || string(calls[0].args) != `{"command":"pwd"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestRecoverFunctionBlock(t *testing.T) {
	text := "<function=shell>\n<parameter=command>\nls\n</parameter>\n</function>"
	clean, calls := recoverTextCalls(text, textCallTools)
	if clean != "" || len(calls) != 1 {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
	if calls[0].name != "shell" || string(calls[0].args) != `{"command":"ls"}` {
		t.Fatalf("call = %s %s", calls[0].name, calls[0].args)
	}
}

func TestRecoverFunctionBlockInsideToolCall(t *testing.T) {
	text := "<tool_call>\n<function=read>\n<parameter=path>\nnotes.txt\n</parameter>\n</function>\n</tool_call>"
	clean, calls := recoverTextCalls(text, textCallTools)
	if clean != "" || len(calls) != 1 {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
	if calls[0].name != "read" || string(calls[0].args) != `{"path":"notes.txt"}` {
		t.Fatalf("call = %s %s", calls[0].name, calls[0].args)
	}
}

func TestRecoverMultilineParameterKeepsItsLines(t *testing.T) {
	text := "<function=shell>\n<parameter=command>\necho a\necho b\n</parameter>\n</function>"
	_, calls := recoverTextCalls(text, textCallTools)
	if len(calls) != 1 || string(calls[0].args) != `{"command":"echo a\necho b"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestRecoverToolCodeShellFence(t *testing.T) {
	text := "<tool_code>\n```shell\nls -la\n```\n</tool_code>"
	clean, calls := recoverTextCalls(text, textCallTools)
	if clean != "" || len(calls) != 1 {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
	if calls[0].name != "shell" || string(calls[0].args) != `{"command":"ls -la"}` {
		t.Fatalf("call = %s %s", calls[0].name, calls[0].args)
	}
}

func TestToolCodeWithoutShellFenceIsNotRecovered(t *testing.T) {
	for _, text := range []string{
		"<tool_code>\n```python\nprint(1)\n```\n</tool_code>",
		"<tool_code>\nls\n</tool_code>",
	} {
		clean, calls := recoverTextCalls(text, textCallTools)
		if len(calls) != 0 || clean != text {
			t.Fatalf("%q: clean=%q calls=%+v", text, clean, calls)
		}
	}
}

func TestUnofferedToolStaysText(t *testing.T) {
	for _, text := range []string{
		`<tool_call>{"name":"rm_everything","arguments":{}}</tool_call>`,
		"<function=rm_everything>\n<parameter=path>\n/\n</parameter>\n</function>",
		"<tool_code>\n```rm_everything\nx\n```\n</tool_code>",
	} {
		clean, calls := recoverTextCalls(text, textCallTools)
		if len(calls) != 0 || clean != text {
			t.Fatalf("%q: clean=%q calls=%+v", text, clean, calls)
		}
	}
}

func TestProseAroundCallsIsKept(t *testing.T) {
	text := `Before. <tool_call>{"name":"shell","arguments":{"command":"ls"}}</tool_call> Middle ` +
		"<function=read>\n<parameter=path>\na.txt\n</parameter>\n</function> After."
	clean, calls := recoverTextCalls(text, textCallTools)
	if clean != "Before.  Middle  After." {
		t.Fatalf("clean = %q", clean)
	}
	if len(calls) != 2 || calls[0].name != "shell" || calls[1].name != "read" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestMalformedCallsStayText(t *testing.T) {
	for _, text := range []string{
		`<tool_call>{"name":"shell","arguments":{"command":"ls"</tool_call>`,
		`<tool_call>{"name":"shell","arguments":{"command":"ls"}}`,
		`<tool_call>{"name":"shell","arguments":["ls"]}</tool_call>`,
		"<function=shell>\n<parameter=command>\nls\n</function>",
		"<function=shell>\nnot a parameter\n</function>",
	} {
		clean, calls := recoverTextCalls(text, textCallTools)
		if len(calls) != 0 || clean != text {
			t.Fatalf("%q: clean=%q calls=%+v", text, clean, calls)
		}
	}
}

func TestMalformedCallDoesNotHideALaterValidCall(t *testing.T) {
	text := `<tool_call>{broken</tool_call> <tool_call>{"name":"shell","arguments":{"command":"ls"}}</tool_call>`
	clean, calls := recoverTextCalls(text, textCallTools)
	if len(calls) != 1 || calls[0].name != "shell" {
		t.Fatalf("calls = %+v", calls)
	}
	if clean != `<tool_call>{broken</tool_call> ` {
		t.Fatalf("clean = %q", clean)
	}
}

func TestGateHoldsMarkupButShowsProse(t *testing.T) {
	g := newLeakGate("")
	var shown strings.Builder
	for _, d := range []string{"Let me check. ", "<tool_", "call>", `{"name":"shell","arguments":{"command":"ls"}}`, "</tool_call>"} {
		shown.WriteString(g.feed(d))
	}
	if shown.String() != "Let me check. " {
		t.Fatalf("shown while streaming = %q", shown.String())
	}
}

func TestGateReleasesPlainLessThan(t *testing.T) {
	g := newLeakGate("")
	if got := g.feed("a < b"); got != "a < b" {
		t.Fatalf("feed = %q", got)
	}
}

func TestGateReleasesAPartialOpenerThatBecomesPlain(t *testing.T) {
	g := newLeakGate("")
	first := g.feed("x <tool")
	second := g.feed("ing well")
	if first+second != "x <tooling well" {
		t.Fatalf("released %q + %q", first, second)
	}
}

func TestStepRecoveryShowsOnlyWhatIsLeft(t *testing.T) {
	g := newLeakGate("")
	var shown strings.Builder
	for _, d := range []string{"Checking. ", `<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call>`, " Then done."} {
		shown.WriteString(g.feed(d))
	}
	parts, unseen := recoverStepCalls([]llm.Part{{Type: llm.PartText, Text: g.text()}}, g, textCallTools)
	if shown.String()+unseen != "Checking.  Then done." {
		t.Fatalf("shown %q + unseen %q", shown.String(), unseen)
	}
	if len(parts) != 2 || parts[0].Text != "Checking.  Then done." || parts[1].ToolName != "read" {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestRecoveryLeavesStepWithRealCallAlone(t *testing.T) {
	g := newLeakGate("")
	markup := `<tool_call>{"name":"shell","arguments":{"command":"ls"}}</tool_call>`
	shown := g.feed("Working. " + markup)
	_ = shown
	g.stop()
	real := llm.Part{Type: llm.PartToolCall, ToolCallID: "real_1", ToolName: "read", Args: json.RawMessage(`{"path":"a"}`)}
	in := []llm.Part{{Type: llm.PartText, Text: g.text()}, real}
	parts, unseen := recoverStepCalls(in, g, textCallTools)
	if unseen != "" {
		t.Fatalf("unseen = %q", unseen)
	}
	if len(parts) != 2 || parts[0].Text != g.text() || parts[1].ToolCallID != "real_1" {
		t.Fatalf("parts changed: %+v", parts)
	}
}

func TestRecoveredCallIDsAreUnique(t *testing.T) {
	text := `<tool_call>{"name":"shell","arguments":{"command":"a"}}</tool_call>` +
		`<tool_call>{"name":"shell","arguments":{"command":"b"}}</tool_call>`
	g := newLeakGate("")
	g.feed(text)
	parts, _ := recoverStepCalls([]llm.Part{{Type: llm.PartText, Text: text}}, g, textCallTools)
	seen := map[string]bool{}
	for _, p := range parts {
		if p.Type != llm.PartToolCall {
			continue
		}
		if !strings.HasPrefix(p.ToolCallID, "textcall_") || seen[p.ToolCallID] {
			t.Fatalf("bad or repeated id %q", p.ToolCallID)
		}
		seen[p.ToolCallID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("ids = %v", seen)
	}
}

// A reply that writes its call as text, split across stream deltas, runs the
// tool. The markup never reaches the reader.
func TestTextCallInReplyRunsTool(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{text: []string{"Checking. ", `<tool_call>{"name":"read","arguments":{"path":"notes.txt"}}</tool_call>`}}),
		turn(sseTurn{text: []string{"It says hi."}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, reg, log, dir))
	_, err := a.Run(context.Background(), "read notes.txt", nil)
	must(t, err)

	var streamed strings.Builder
	for _, e := range log.of(EvText) {
		streamed.WriteString(e.Text)
	}
	if strings.Contains(streamed.String(), "<tool_call>") || !strings.HasPrefix(streamed.String(), "Checking. ") {
		t.Fatalf("streamed text = %q", streamed.String())
	}
	ran := false
	for _, e := range log.of(EvToolResult) {
		if e.Tool == "read" && strings.Contains(e.Result.Output, "hi") {
			ran = true
		}
	}
	if !ran {
		t.Fatal("the recovered read call did not run")
	}
	if got := a.Messages()[len(a.Messages())-1].Text(); got != "It says hi." {
		t.Fatalf("final = %q", got)
	}
	if f.count() != 2 {
		t.Fatalf("model calls = %d, want 2", f.count())
	}
}
