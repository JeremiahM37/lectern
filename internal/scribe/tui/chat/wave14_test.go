package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestStepLabelDropsItsOwnNumber(t *testing.T) {
	cases := map[string]string{
		"1. Add a function": "Add a function",
		"2) Wire the tab":   "Wire the tab",
		"12. Ship it":       "Ship it",
		"1.5 GB cache":      "1.5 GB cache",
		"Step 1 wires it":   "Step 1 wires it",
		"Plain step":        "Plain step",
	}
	for in, want := range cases {
		if got := stepLabel(in); got != want {
			t.Errorf("stepLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A plan step whose text starts with its own number reads "1. Add ...", not
// "1. 1. Add ...", in both the review card and the running card.
func TestPlanCardsDoNotDoubleTheStepNumber(t *testing.T) {
	p := proposal()
	p.Steps[0].Text = "1. Wrap the fetch"
	p.Steps[1].Text = "2) Add the backoff"
	v, c := newView(t, Options{})
	planTurn(v, c, p)
	out := plain(v)
	for _, bad := range []string{"1. 1.", "2. 2)", "2. 2."} {
		if strings.Contains(out, bad) {
			t.Fatalf("review card repeats a step number (%q):\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "Wrap the fetch") || !strings.Contains(out, "Add the backoff") {
		t.Fatalf("step text lost:\n%s", out)
	}

	run := proposal()
	run.Steps[0].Text = "1. Wrap the fetch"
	run.Steps[0].Status = "active"
	run.Steps[1].Status = "pending"
	v2, c2 := newView(t, Options{})
	v2.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c2)})
	v2.Apply(agent.Event{Kind: agent.EvPlan, Plan: run, At: at(c2)})
	out2 := plain(v2)
	if strings.Contains(out2, "1. 1.") || !strings.Contains(out2, "Wrap the fetch") {
		t.Fatalf("running plan card repeats the step number:\n%s", out2)
	}
}

// Esc on plan review ends the turn, but the footer must not say "Done".
func TestCancelledPlanTurnFooterIsNotDone(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "p", "plan", map[string]any{"title": "Add retry"}, c)
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: proposal(), At: at(c)})
	snap := proposal()
	snap.Outcome = "cancelled"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: snap, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	out := plain(v)
	if !strings.Contains(out, "Plan cancelled") {
		t.Fatalf("cancelled plan not shown:\n%s", out)
	}
	if strings.Contains(out, "✓ Done") || strings.Contains(out, "Completed in") {
		t.Fatalf("cancelled turn reads as success:\n%s", out)
	}
	if !strings.Contains(out, "⊘ Cancelled") {
		t.Fatalf("cancelled turn has no footer saying so:\n%s", out)
	}
}

// A subagent's read shows the file it read, whichever arg shape it used.
func TestReadRowKeepsItsFileForEveryArgShape(t *testing.T) {
	shapes := map[string]map[string]any{
		"files array": {"files": []any{map[string]any{"path": "internal/pkg/alpha.go"}}},
		"paths list":  {"paths": []any{"internal/pkg/alpha.go"}},
		"file_path":   {"file_path": "internal/pkg/alpha.go"},
	}
	for name, args := range shapes {
		v, c := newView(t, Options{})
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		tool(v, "r", "read", args, c)
		out := plain(v)
		if !strings.Contains(out, "alpha.go") {
			t.Errorf("%s: read row lost its file:\n%s", name, out)
		}
		if strings.Contains(out, "Reading  (") {
			t.Errorf("%s: read row shows a bare range with no file:\n%s", name, out)
		}
	}
}

// A failed edit names its file. With no file in its args at all it says so,
// with its error, rather than a blank label.
func TestFailedEditRowIsNeverBlank(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "e1", "edit_file", map[string]any{"file_path": "internal/calc/calc.go"}, c)
	result(v, "e1", "edit_file", tools.Result{IsError: true, Output: "old_string not found"}, 0, c)
	tool(v, "e2", "edit_file", map[string]any{"path": "internal/calc/calc_test.go"}, c)
	result(v, "e2", "edit_file", tools.Result{Output: "edited internal/calc/calc_test.go"}, 0, c)
	out := plain(v)
	if !strings.Contains(out, "calc.go") {
		t.Fatalf("failed edit lost its file name:\n%s", out)
	}

	v2, c2 := newView(t, Options{})
	v2.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c2)})
	tool(v2, "e1", "edit_file", map[string]any{}, c2)
	result(v2, "e1", "edit_file", tools.Result{IsError: true, Output: "old_string not found"}, 0, c2)
	tool(v2, "e2", "edit_file", map[string]any{"path": "internal/calc/calc_test.go"}, c2)
	result(v2, "e2", "edit_file", tools.Result{Output: "edited internal/calc/calc_test.go"}, 0, c2)
	out2 := plain(v2)
	if !strings.Contains(out2, "file not named: old_string not found") {
		t.Fatalf("unnamed failed edit has no label:\n%s", out2)
	}
}

// A "!command" is one system row with one header: the command and its output.
func TestShellCommandHasOneHeader(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddSystem("shell", "$ ls pkg && git log --oneline | head -3")
	v.AddSystem("shell", "a.go\nb.go")
	c.t = c.t.Add(time.Second) // the newest system row types itself out first
	out := plain(v)
	if n := strings.Count(out, "› System"); n != 1 {
		t.Fatalf("want one System header, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "$ ls pkg") || !strings.Contains(out, "a.go") {
		t.Fatalf("command or output missing:\n%s", out)
	}
}

// The transcript keeps one column clear of its right border at 80 and 120
// columns, for answers, plan cards and system rows alike.
func TestTranscriptLeavesAColumnBeforeTheBorder(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("calc.Average returns the mean of the values as an int. ", 6))
	for _, width := range []int{80, 120} {
		inner := width - 2 // the chat frame takes one cell on each side
		v, c := newView(t, Options{})
		v.SetSize(inner, 60)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		text(v, long, c)
		v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
		v.AddSystem("info", long)
		planTurn(v, c, proposal())
		for _, line := range strings.Split(ansi.Strip(v.View()), "\n") {
			if w := ansi.StringWidth(strings.TrimRight(line, " ")); w > inner-1 {
				t.Fatalf("width %d: content reaches the border (%d cells of %d):\n%q", width, w, inner, line)
			}
		}
	}
}
