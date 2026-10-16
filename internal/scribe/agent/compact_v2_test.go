package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// richHistory is a long read-heavy session: enough slots for the state alone.
func richHistory() []llm.Message {
	msgs := []llm.Message{userMsg("review the package layout please")}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			asstMsg(fmt.Sprintf("Reading file number %d of the package now.", i)),
			callMsg(id, "read", fmt.Sprintf(`{"path":"f%d.go"}`, i)),
			resultMsg(id, "read", "package p", false),
		)
	}
	return append(msgs, userMsg("now summarize the review"))
}

func TestV2RichStateNeedsNoModelCall(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t)) // any request fails the test
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced), KeepTokens: 8}
	kept, summary, err := c.Compact(context.Background(), richHistory())
	must(t, err)
	if f.count() != 0 {
		t.Fatalf("requests = %d, want none", f.count())
	}
	for _, want := range []string{"## Files Touched", "- f0.go: read", "## Assistant Notes"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary lacks %q:\n%s", want, summary)
		}
	}
	if kept[1].Text() != "Continuing." {
		t.Fatalf("acknowledgement = %q", kept[1].Text())
	}
}

func TestV2SparseStateAddsGapFill(t *testing.T) {
	msgs := []llm.Message{
		userMsg("fix the bug in parser module"),
		asstMsg("Looking into the parser module now."),
		userMsg("ok thanks, also check the lexer"),
	}
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"## Missing Decisions\n- The user chose to keep the old API."}})))
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced), KeepTokens: 8}
	kept, summary, err := c.Compact(context.Background(), msgs)
	must(t, err)
	if f.count() != 1 {
		t.Fatalf("requests = %d, want one gap fill", f.count())
	}
	if got := systemText(f.requests()[0]); got != gapFillPrompt {
		t.Fatalf("gap fill used the wrong system prompt: %q", got)
	}
	if !strings.Contains(summary, "## Additional Details") || !strings.Contains(summary, "Missing Decisions") {
		t.Fatalf("gap fill not appended:\n%s", summary)
	}
	if len(kept) < 3 {
		t.Fatalf("kept %d messages", len(kept))
	}
}

func TestV2GapFillCompleteAddsNothing(t *testing.T) {
	msgs := []llm.Message{
		userMsg("fix the bug in parser module"),
		asstMsg("Looking into the parser module now."),
		userMsg("ok thanks, also check the lexer"),
	}
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"COMPLETE"}})))
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced), KeepTokens: 8}
	_, summary, err := c.Compact(context.Background(), msgs)
	must(t, err)
	if strings.Contains(summary, "Additional Details") {
		t.Fatalf("COMPLETE should add nothing:\n%s", summary)
	}
}

func TestTooShortSummaryAborts(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"ok"}})))
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced), KeepTokens: 12, Strategy: "v1"}
	msgs := []llm.Message{
		userMsg("Build the parser " + strings.Repeat("y", 400)),
		asstMsg("Working on it"),
		userMsg("Also add tests"),
	}
	kept, _, err := c.Compact(context.Background(), msgs)
	if err == nil || !strings.Contains(err.Error(), "too-short summary") {
		t.Fatalf("err = %v, want the too-short abort", err)
	}
	if kept != nil {
		t.Fatal("an aborted compaction must return no replacement history")
	}
}

func TestNotEnoughHistoryIsASystemNote(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t))
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/summarizer", priced)}
	_, _, err := c.Compact(context.Background(), []llm.Message{userMsg("hi there")})
	if err != ErrNotEnoughHistory {
		t.Fatalf("err = %v", err)
	}
	if f.count() != 0 {
		t.Fatal("nothing to compact must not call a model")
	}
}

func TestSanitizeTail(t *testing.T) {
	got := sanitizeTail([]llm.Message{
		resultMsg("x", "read", "orphan", false),
		asstMsg("ack"),
		userMsg("y"),
	})
	if len(got) != 1 || got[0].Text() != "y" {
		t.Fatalf("leading orphans not dropped: %v", got)
	}
	got = sanitizeTail([]llm.Message{
		userMsg("u"),
		resultMsg("x", "read", "orphan", false),
		asstMsg("a"),
		resultMsg("y", "read", "kept", false),
	})
	if len(got) != 3 || got[1].Role != llm.RoleAssistant || got[2].Role != llm.RoleTool {
		t.Fatalf("tail = %v", got)
	}
}
