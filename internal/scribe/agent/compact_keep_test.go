package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// keepHistory is n exchanges, each a user prompt, a read call with its
// result, and an assistant reply. The read calls give the working state a
// file slot, so compaction needs no model call.
func keepHistory(n int) []llm.Message {
	var msgs []llm.Message
	for k := 0; k < n; k++ {
		id := fmt.Sprintf("r%d", k)
		msgs = append(msgs,
			userMsg(fmt.Sprintf("task number %d needs the package reviewed", k)),
			callMsg(id, "read", fmt.Sprintf(`{"path":"pkg%d.go"}`, k)),
			resultMsg(id, "read", "package p", false),
			asstMsg(fmt.Sprintf("Finished reviewing task %d and found nothing.", k)),
		)
	}
	return msgs
}

func TestSplitRecentKeepsAtLeastTheNewestMessages(t *testing.T) {
	msgs := keepHistory(5) // 20 messages, a user prompt at 0, 4, 8, 12, 16
	cases := []struct{ keep, want int }{
		{keep: 8, want: 12}, // 20-8=12 is a prompt: exactly 8 kept
		{keep: 6, want: 12}, // 14 is a reply: back to the prompt at 12, 8 kept
		{keep: 4, want: 16}, // 16 is a prompt: 4 kept
		{keep: 3, want: 16}, // 17 is a call, so the cut moves back to the prompt at 16
		{keep: 25, want: 0}, // more than the history: nothing older
		{keep: 0, want: 20}, // zero is not a count: nothing kept by this rule
	}
	for _, c := range cases {
		got := SplitRecent(msgs, c.keep)
		if got != c.want {
			t.Errorf("SplitRecent(keep=%d) = %d, want %d", c.keep, got, c.want)
		}
		if got < len(msgs) {
			if msgs[got].Role != llm.RoleUser {
				t.Errorf("keep=%d: split lands on %s, want a user prompt", c.keep, msgs[got].Role)
			}
			splitInvariants(t, msgs, got)
		}
	}
}

func TestSplitRecentNeverSeparatesCallFromResult(t *testing.T) {
	msgs := keepHistory(3)
	for keep := 1; keep <= len(msgs); keep++ {
		splitInvariants(t, msgs, SplitRecent(msgs, keep))
	}
}

func TestCompactKeepRecentLeavesTheTailVerbatim(t *testing.T) {
	msgs := keepHistory(5)
	// No model is configured: the state alone is the summary, so the test
	// fails if compaction tries to call one.
	c := SummaryCompactor{KeepRecent: 8}
	kept, summary, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	tail := kept[2:]
	want := msgs[12:]
	if len(tail) != len(want) {
		t.Fatalf("kept %d messages after the summary, want %d", len(tail), len(want))
	}
	for i := range want {
		if tail[i].Text() != want[i].Text() || tail[i].Role != want[i].Role {
			t.Fatalf("tail[%d] changed: %q", i, tail[i].Text())
		}
	}
	if !strings.Contains(summary, "pkg0.go") {
		t.Fatalf("summary does not record the older reads:\n%s", summary)
	}
}

func TestCompactKeepRecentNothingOlderIsNotEnough(t *testing.T) {
	msgs := keepHistory(1)
	c := SummaryCompactor{KeepRecent: 4}
	_, _, err := c.Compact(context.Background(), msgs)
	if !errors.Is(err, ErrNotEnoughHistory) {
		t.Fatalf("err = %v, want ErrNotEnoughHistory", err)
	}
}

func TestSetCompactionAppliesToSummaryCompactor(t *testing.T) {
	a := &Agent{}
	a.opts.Compactor = SummaryCompactor{Strategy: "v2", KeepTokens: 100}
	a.SetCompaction("v1", 6)
	sc, ok := a.opts.Compactor.(SummaryCompactor)
	if !ok {
		t.Fatalf("compactor type changed to %T", a.opts.Compactor)
	}
	if sc.Strategy != "v1" || sc.KeepRecent != 6 || sc.KeepTokens != 100 {
		t.Fatalf("compactor = %+v, want strategy v1, keep 6, tokens kept at 100", sc)
	}
	a.SetCompaction("v2", -3)
	sc = a.opts.Compactor.(SummaryCompactor)
	if sc.KeepRecent != 0 {
		t.Fatalf("negative keep should mean the token default, got %d", sc.KeepRecent)
	}
}

func TestSetCompactionLeavesDisabledCompactionAlone(t *testing.T) {
	a := &Agent{}
	a.SetCompaction("v1", 6)
	if a.opts.Compactor != nil {
		t.Fatalf("a disabled compaction gained a compactor: %T", a.opts.Compactor)
	}
}
