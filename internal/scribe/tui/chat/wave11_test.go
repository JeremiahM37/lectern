package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// The top of a transcript that starts after earlier messages opens with the
// count divider (spec 01 section 1, message area).
func TestEarlierDividerShowsCount(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "hello", At: at(c)})
	text(v, "Hi there.", c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}, At: at(c)})
	if strings.Contains(plain(v), "earlier") {
		t.Fatalf("no divider expected when nothing came before")
	}
	v.SetEarlier(12)
	out := plain(v)
	first := strings.Split(out, "\n")[0]
	if !strings.HasPrefix(first, "── 12 earlier messages ──") {
		t.Fatalf("divider should open the transcript, got %q", first)
	}
	if !strings.Contains(out, "Hi there.") {
		t.Fatalf("records must still render below the divider:\n%s", out)
	}
	checkShape(t, v)
	v.SetEarlier(1)
	if first := strings.Split(plain(v), "\n")[0]; !strings.HasPrefix(first, "── 1 earlier message ──") {
		t.Fatalf("singular divider = %q", first)
	}
	v.Clear()
	if v.earlier != 0 {
		t.Fatalf("Clear should drop the earlier count")
	}
}

// Finished read, grep and glob rows show counts when the tool sets no summary.
func TestResultSuffixCountsForKnownTools(t *testing.T) {
	v := &View{}
	cases := []struct {
		tool, out, want string
	}{
		{"read", "  1  package x\n  2  func f()\n  3  }", "3 lines"},
		{"read", "one line", "1 line"},
		{"grep", "a.go:1: TODO\na.go:9: TODO\nb.go:2: TODO", "3 hits in 2 files"},
		{"grep", "", "0 hits"},
		{"glob", "a.go\nb.go", "2 files"},
		{"soul_find", "Foo\n", "1 result"},
	}
	for _, tc := range cases {
		c := &Call{Tool: tc.tool, Status: "done", Result: &tools.Result{Output: tc.out}}
		if got := v.resultSuffix(c); got != tc.want {
			t.Errorf("%s suffix = %q, want %q", tc.tool, got, tc.want)
		}
	}
}

// A failed read keeps 27 cells of its message; a failed web search keeps 50.
func TestErrorSuffixLengths(t *testing.T) {
	v := &View{}
	long := strings.Repeat("x", 80)
	read := &Call{Tool: "read", Status: "error", Result: &tools.Result{Output: long, IsError: true}}
	if got := v.resultSuffix(read); len([]rune(got)) != 27 || !strings.HasSuffix(got, "...") {
		t.Errorf("read error suffix = %q (%d cells)", got, len([]rune(got)))
	}
	web := &Call{Tool: "web_search", Status: "error", Result: &tools.Result{Output: long, IsError: true}}
	if got := v.resultSuffix(web); len([]rune(got)) != 50 {
		t.Errorf("web_search error suffix = %q", got)
	}
}

// An explicit tool summary still wins over the count.
func TestSummaryBeatsCount(t *testing.T) {
	v := &View{}
	c := &Call{Tool: "read", Status: "done", Result: &tools.Result{Output: "a\nb", Summary: "lines 1-2 of 300"}}
	if got := v.resultSuffix(c); got != "lines 1-2 of 300" {
		t.Errorf("summary should win, got %q", got)
	}
}

// Streamed text and reasoning feed a rough token estimate at 3.5 characters
// per token, and a new turn starts it again.
func TestStreamTokenEstimate(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "q", At: at(c)})
	text(v, strings.Repeat("a", 35), c)
	if got := v.StreamTokenEstimate(); got != 10 {
		t.Fatalf("35 chars at 3.5/token = %d, want 10", got)
	}
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "again", At: at(c)})
	if got := v.StreamTokenEstimate(); got != 0 {
		t.Fatalf("new turn should reset the estimate, got %d", got)
	}
}

// Pasted images are drawn at 60 columns.
func TestImageColumnsMatchSpec(t *testing.T) {
	if imageCols != 60 {
		t.Fatalf("imageCols = %d, spec 01 section 2.2 says 60", imageCols)
	}
}
