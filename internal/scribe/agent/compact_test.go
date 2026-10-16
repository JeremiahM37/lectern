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

func userMsg(s string) llm.Message { return llm.UserText(s) }
func asstMsg(s string) llm.Message { return llm.AssistantText(s) }

func callMsg(id, name, args string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{
		Type: llm.PartToolCall, ToolCallID: id, ToolName: name, Args: json.RawMessage(args),
	}}}
}

func resultMsg(id, name, result string, isErr bool) llm.Message {
	return llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{
		Type: llm.PartToolResult, ToolCallID: id, ToolName: name, Result: result, IsError: isErr,
	}}}
}

// splitInvariants checks that a cut never separates a tool call from its
// result, and that a kept tail starts at a plain user message.
func splitInvariants(t *testing.T, msgs []llm.Message, split int) {
	t.Helper()
	if split < 0 || split > len(msgs) {
		t.Fatalf("split %d out of range for %d messages", split, len(msgs))
	}
	if split < len(msgs) && msgs[split].Role != llm.RoleUser {
		t.Fatalf("split %d lands on a %s message, not a user prompt", split, msgs[split].Role)
	}
	callAt := map[string]int{}
	for i, m := range msgs {
		for _, c := range m.ToolCalls() {
			callAt[c.ToolCallID] = i
		}
	}
	for i, m := range msgs {
		for _, p := range m.Parts {
			if p.Type != llm.PartToolResult {
				continue
			}
			ci, ok := callAt[p.ToolCallID]
			if !ok {
				continue
			}
			if (ci < split) != (i < split) {
				t.Fatalf("split %d separates call (msg %d) from its result (msg %d)", split, ci, i)
			}
		}
	}
}

func TestSplitPointNeverSplitsAToolPair(t *testing.T) {
	big := strings.Repeat("lorem ipsum ", 300) // about 900 estimated tokens
	msgs := []llm.Message{
		userMsg("first prompt " + big),
		asstMsg("working"),
		callMsg("c1", "read", `{"path":"a.go"}`),
		resultMsg("c1", "read", "package a", false),
		asstMsg("looking at b"),
		userMsg("second prompt " + big),
		callMsg("c2", "shell", `{"command":"go test ./..."}`),
		resultMsg("c2", "shell", "FAIL", true),
		userMsg("retry: nudge"),
		callMsg("c3", "write_file", `{"path":"a.go","content":"x"}`),
		resultMsg("c3", "write_file", "wrote a.go", false),
		asstMsg("done"),
	}
	for keep := 0; keep <= 4000; keep += 7 {
		split := SplitPoint(msgs, keep)
		splitInvariants(t, msgs, split)
	}
	// A tiny budget keeps only the final exchange, starting at its user prompt.
	if got := SplitPoint(msgs, 10); msgs[got].Role != llm.RoleUser || got < 8 {
		t.Fatalf("tiny budget split = %d", got)
	}
}

func TestSplitPointTailFitsBudget(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []llm.Message{
		userMsg(big), asstMsg("a"), userMsg("middle"), callMsg("c1", "read", `{"path":"x"}`),
		resultMsg("c1", "read", "contents", false), asstMsg("b"), userMsg("last"), asstMsg("c"),
	}
	split := SplitPoint(msgs, 60)
	splitInvariants(t, msgs, split)
	if EstimateTokens(msgs[split:]) > 60 {
		t.Fatalf("kept tail is %d tokens, over the 60 budget", EstimateTokens(msgs[split:]))
	}
	if split != 2 {
		t.Fatalf("split = %d, want 2 (the user prompt after the big one)", split)
	}
	if split == 0 || msgs[split].Text() == big {
		t.Fatalf("the big early prompt should have been dropped, split=%d", split)
	}
}

func TestSummaryCompactorKeepsTailAndWritesLedger(t *testing.T) {
	big := strings.Repeat("y", 4000)
	msgs := []llm.Message{
		userMsg("Build the parser " + big),
		asstMsg("Working on it"),
		userMsg("Also add tests"),
		callMsg("c1", "read", `{"path":"parser.go"}`),
		resultMsg("c1", "read", "package parser", false),
		callMsg("c2", "write_file", `{"path":"parser_test.go","content":"x"}`),
		resultMsg("c2", "write_file", "permission denied", true),
		userMsg("retry the test file"),
		callMsg("c3", "shell", `{"command":"go test ./..."}`),
		resultMsg("c3", "shell", "ok", false),
		asstMsg("done"),
	}

	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"Goal: build the parser."}})))
	// A 12-token budget keeps the last user exchange (msgs[7:]) and cuts before
	// the read/write calls, so those appear in the summarised transcript.
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced), KeepTokens: 12}
	kept, summary, err := c.Compact(context.Background(), msgs)
	must(t, err)

	// The summary request carries the summary prompt and a transcript of the cut.
	req := f.requests()[0]
	if systemText(req) != summaryPrompt {
		t.Fatalf("summary request system prompt = %q", systemText(req))
	}
	if !strings.Contains(lastUserText(req), "[tool call] read") {
		t.Fatalf("transcript missing the read call: %q", lastUserText(req))
	}

	// Kept: a summary head, its acknowledgement, then the untouched tail.
	if len(kept) != 6 {
		t.Fatalf("kept %d messages, want 6", len(kept))
	}
	if !strings.Contains(kept[0].Text(), "<conversation_summary>") || !strings.Contains(kept[0].Text(), "Goal: build the parser.") {
		t.Fatalf("summary head = %q", kept[0].Text())
	}
	for i, m := range kept[2:] {
		if m.Text() != msgs[7+i].Text() || m.Role != msgs[7+i].Role {
			t.Fatalf("tail message %d was altered", i)
		}
	}

	// Ledger: reads and commands are recorded; a failed write is not an edit.
	// The ledger covers only the summarised part; the shell call stays in the tail.
	for _, want := range []string{"## Ledger", "Files read: parser.go", "- write_file: permission denied"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "Files edited") {
		t.Fatalf("failed write_file was reported as an edit:\n%s", summary)
	}
}

func TestLedgerCountsOnlySuccessfulFileOps(t *testing.T) {
	msgs := []llm.Message{
		callMsg("r1", "read", `{"path":"ok.go"}`),
		resultMsg("r1", "read", "package ok", false),
		callMsg("r2", "read", `{"path":"missing.go"}`),
		resultMsg("r2", "read", "no such file", true),
		callMsg("e1", "edit_file", `{"path":"saved.go"}`),
		resultMsg("e1", "edit_file", "edited", false),
		callMsg("e2", "edit_file", `{"path":"refused.go"}`),
		resultMsg("e2", "edit_file", "old_string not found", true),
	}
	got := Ledger(msgs)
	if !strings.Contains(got, "Files read: ok.go\n") || strings.Contains(got, "missing.go") {
		t.Fatalf("read list wrong:\n%s", got)
	}
	if !strings.Contains(got, "Files edited: saved.go\n") || strings.Contains(got, "refused.go") {
		t.Fatalf("edit list wrong:\n%s", got)
	}
	if !strings.Contains(got, "- read: no such file") || !strings.Contains(got, "- edit_file: old_string not found") {
		t.Fatalf("failures not listed:\n%s", got)
	}
}

func TestCompactionTriggersWhenContextCrossesThreshold(t *testing.T) {
	cases := []struct {
		name        string
		input       int
		wantCompact bool
	}{
		{"below threshold", 100, false},
		{"above threshold", 900, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			must(t, os.WriteFile(filepath.Join(dir, "parser.go"), []byte("package parser"), 0o644))
			reg := tools.NewRegistry()
			reg.Register(readTool{})

			first := turn(sseTurn{
				calls: []sseCall{{id: "call_1", name: "read", args: `{"path":"parser.go"}`}},
				usage: &sseUsage{prompt: tc.input, completion: 10},
			})
			final := turn(sseTurn{text: []string{"all done"}})
			replies := []reply{first, final}
			if tc.wantCompact {
				replies = []reply{first, turn(sseTurn{text: []string{"SUMMARY-TEXT"}}), final}
			}
			f := newFake(t)
			f.setPick(scripted(t, replies...))

			log := &eventLog{}
			opts := baseOptions(f, reg, log, dir)
			opts.Model.Info.ContextWindow = 1000
			opts.CompactAt = 0.5
			opts.Compactor = SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced), KeepTokens: 60}
			a := New(opts)
			a.SetMessages([]llm.Message{
				userMsg(strings.Repeat("old ", 1000)),
				asstMsg("ok"),
				userMsg("next"),
			})

			_, err := a.Run(context.Background(), "read the parser", nil)
			must(t, err)
			if f.count() != len(replies) {
				t.Fatalf("requests = %d, want %d", f.count(), len(replies))
			}
			compacted := len(log.of(EvCompactStart)) > 0
			if compacted != tc.wantCompact {
				t.Fatalf("compaction happened=%v, want %v", compacted, tc.wantCompact)
			}
			if !tc.wantCompact {
				return
			}
			if len(log.of(EvCompactDone)) != 1 {
				t.Fatalf("compact_done events = %d", len(log.of(EvCompactDone)))
			}
			// The step after compaction sees the summary in place of the old turns.
			after := f.requests()[2]
			if !strings.Contains(strings.Join(allTexts(after), "\n"), "SUMMARY-TEXT") {
				t.Fatal("post-compaction request does not carry the summary")
			}
			if strings.Contains(strings.Join(allTexts(after), "\n"), strings.Repeat("old ", 50)) {
				t.Fatal("post-compaction request still carries the old prompt")
			}
		})
	}
}

func allTexts(req recordedReq) []string {
	var out []string
	for _, m := range req.body.Messages {
		out = append(out, m.text())
	}
	return out
}
