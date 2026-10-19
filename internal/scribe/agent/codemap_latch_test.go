package agent

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func countUpdates(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.Text(), codeMapUpdateOpen) {
			n++
		}
	}
	return n
}

func countSnapshots(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.Text(), codeMapOpen) {
			n++
		}
	}
	return n
}

// A new map is latched once as a snapshot pair; the same map again adds nothing.
func TestCodeMapLatchesOnceAsASnapshot(t *testing.T) {
	var l codeMapLatch
	var msgs []llm.Message
	msgs = append(msgs, l.notices(msgs, "- a.go\n  func A() (line 1)\n")...)
	if len(msgs) != 2 || countSnapshots(msgs) != 1 {
		t.Fatalf("first latch = %d messages, want a snapshot pair", len(msgs))
	}
	if msgs[1].Role != llm.RoleAssistant {
		t.Error("the snapshot must be acknowledged by the assistant")
	}
	if got := l.notices(msgs, "- a.go\n  func A() (line 1)\n"); got != nil {
		t.Fatalf("an unchanged map latched %d messages", len(got))
	}
}

// For a live provider a change arrives as one update holding only the changed lines.
func TestCodeMapChangeLatchesOnlyTheChangedLines(t *testing.T) {
	var l codeMapLatch
	var msgs []llm.Message
	first := "- a.go\n  func A() (line 1)\n- b.go\n  func B() (line 4)\n"
	msgs = append(msgs, l.notices(msgs, first)...)
	next := "- a.go\n  func A() (line 1)\n- b.go\n  func C() (line 4)\n"
	msgs = append(msgs, l.notices(msgs, next)...)
	if countUpdates(msgs) != 1 {
		t.Fatalf("want one update pair, got %d updates", countUpdates(msgs))
	}
	var update string
	for _, m := range msgs {
		if strings.HasPrefix(m.Text(), codeMapUpdateOpen) {
			update = m.Text()
		}
	}
	if !strings.Contains(update, "- ") || !strings.Contains(update, "func B()") {
		t.Errorf("update does not show the removed line:\n%s", update)
	}
	if !strings.Contains(update, "+ ") || !strings.Contains(update, "func C()") {
		t.Errorf("update does not show the added line:\n%s", update)
	}
	if strings.Contains(update, "func A()") {
		t.Errorf("update repeats an unchanged line:\n%s", update)
	}
	if got := l.notices(msgs, next); got != nil {
		t.Error("the same map after an update must add nothing")
	}
}

// A compaction empties the history. The next latch sends the whole map again,
// not an update against a snapshot the model can no longer see.
func TestCodeMapIsLatchedAgainAfterHistoryIsReplaced(t *testing.T) {
	var l codeMapLatch
	var msgs []llm.Message
	msgs = append(msgs, l.notices(msgs, "- a.go\n  func A()\n")...)
	msgs = nil // compaction
	got := l.notices(msgs, "- a.go\n  func A()\n- z.go\n  func Z()\n")
	if len(got) != 2 || countSnapshots(got) != 1 {
		t.Fatalf("after compaction the map was not latched in full: %d messages", len(got))
	}
	if !strings.Contains(got[0].Text(), "func Z()") {
		t.Error("the full snapshot after compaction lacks the new symbol")
	}
}

// Turning the map off is one note; the snapshot already in the history stays.
func TestCodeMapOffIsLatchedOnce(t *testing.T) {
	var l codeMapLatch
	var msgs []llm.Message
	msgs = append(msgs, l.notices(msgs, "- a.go\n  func A()\n")...)
	off := l.notices(msgs, "")
	if len(off) != 2 || !strings.HasPrefix(off[0].Text(), codeMapUpdateOpen) {
		t.Fatalf("turning the map off latched %d messages", len(off))
	}
	msgs = append(msgs, off...)
	if got := l.notices(msgs, ""); got != nil {
		t.Error("the off note was latched twice")
	}
}

// A repeated line counts as many times as it appears: removing one of two
// identical lines is one removal, not none.
func TestCodeMapDeltaCountsRepeatedLines(t *testing.T) {
	if got := codeMapDelta("x\nx\n", "x\n"); got != "- x\n" {
		t.Errorf("delta = %q, want one removal", got)
	}
	if got := codeMapDelta("x\n", "x\n\n"); got != "" {
		t.Errorf("a blank line alone must not produce a delta: %q", got)
	}
}

// Only providers that cache the prompt prefix freeze the map; the others are live.
func TestPromptCachesFollowsTheFamily(t *testing.T) {
	cases := map[string]bool{
		"anthropic/claude-x":     true,
		"openai/gpt-5":           true,
		"google/gemini-2.5":      true,
		"deepseek/deepseek-chat": true,
		"ollama/qwen3.6":         false,
		"fake/test-model":        false,
	}
	for ref, want := range cases {
		if got := PromptCaches(ref); got != want {
			t.Errorf("PromptCaches(%q) = %v, want %v", ref, got, want)
		}
	}
}

// The system prompt must be byte-identical for a whole session: the same input
// gives the same bytes whatever the map holds, and the map is never in it.
func TestSystemPromptIgnoresTheMap(t *testing.T) {
	in := PromptInput{Workdir: "/w", ModelRef: "anthropic/claude-x", CodeMap: "- a.go\n"}
	base := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "anthropic/claude-x"}, "default")
	if BuildSystem(in, "default") != base {
		t.Fatal("the code map changed the system prompt")
	}
}
