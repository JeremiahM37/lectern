package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

func proposal() *agent.Plan {
	return &agent.Plan{
		Title:   "Add retry",
		Path:    ".scribe/plans/add-retry.md",
		Summary: "Retry transient fetch errors with backoff.",
		Files: []agent.PlanFile{
			{Path: "fetch.go", Action: "modify", Description: "wrap the call"},
			{Path: "retry.go", Action: "create"},
			{Path: "old.go", Action: "delete"},
		},
		Steps: []agent.PlanStep{
			{ID: "1", Text: "Wrap the fetch", Status: "pending", Files: []string{"fetch.go"}},
			{ID: "2", Text: "Add the backoff", Status: "pending", Files: []string{"retry.go"}},
		},
		Verification: []string{"go test ./..."},
	}
}

func TestProposedPlanIsStructuredArtifact(t *testing.T) {
	v, c := newView(t, Options{})
	planTurn(v, c, proposal())
	out := plain(v)
	for _, want := range []string{
		"Add retry", "Context", "Retry transient fetch errors",
		"Files (3)", "~ fetch.go", "wrap the call", "+ retry.go", "- old.go",
		"Steps (2)", "1. Wrap the fetch", "2. Add the backoff",
		"Verification", "✓ go test ./...",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("structured plan lacks %q:\n%s", want, out)
		}
	}
}

func TestStructuredPlanCapsRowsAtFive(t *testing.T) {
	p := proposal()
	p.Files = nil
	for i := 0; i < 7; i++ {
		p.Steps = append(p.Steps, agent.PlanStep{ID: "x", Text: "step extra", Status: "pending"})
	}
	v, c := newView(t, Options{})
	planTurn(v, c, p)
	out := plain(v)
	if !strings.Contains(out, "Steps (9)") || !strings.Contains(out, "+4 more") {
		t.Fatalf("steps not capped at five:\n%s", out)
	}
}

func TestPlanFileWithNoActionIsPlainDot(t *testing.T) {
	p := proposal()
	p.Files = nil // steps name files, but no action is known
	v, c := newView(t, Options{})
	planTurn(v, c, p)
	if out := plain(v); !strings.Contains(out, "· fetch.go") {
		t.Fatalf("file without action should show a dot:\n%s", out)
	}
}

func TestCancelledPlanShowsCancelledState(t *testing.T) {
	v, c := newView(t, Options{})
	planTurn(v, c, proposal())
	snap := proposal()
	snap.Outcome = "cancelled"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: snap, At: at(c)})
	out := plain(v)
	if !strings.Contains(out, "Plan cancelled") || strings.Contains(out, "Steps (2)") {
		t.Fatalf("cancelled plan not shown as cancelled:\n%s", out)
	}
}

func TestRevisionKeepsItsLineAndRevisedPlanGetsNewSegment(t *testing.T) {
	v, c := newView(t, Options{})
	planTurn(v, c, proposal())
	snap := proposal()
	snap.Outcome = "revision"
	snap.Feedback = "add a fuzz test"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: snap, At: at(c)})
	revised := proposal()
	revised.Title = "Add retry v2"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: revised, At: at(c)})
	out := plain(v)
	if !strings.Contains(out, "Revision requested: add a fuzz test") {
		t.Fatalf("revision line lost when the revised plan arrived:\n%s", out)
	}
	if !strings.Contains(out, "Add retry v2") || strings.Count(out, "Steps (2)") != 1 {
		t.Fatalf("revised plan missing or duplicated:\n%s", out)
	}
}

func TestTasksNestUnderTheActiveStep(t *testing.T) {
	v, c := newView(t, Options{})
	p := proposal()
	p.Steps[0].Status = "active"
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: p, At: at(c)})
	tasks := []agent.Task{
		{ID: "1", Text: "write the wrapper", Status: "done", StepID: "1"},
		{ID: "2", Text: "handle context cancel", Status: "pending", StepID: "1"},
		{ID: "3", Text: "unrelated chore", Status: "pending"},
	}
	v.Apply(agent.Event{Kind: agent.EvTasks, Tasks: tasks, At: at(c)})
	out := plain(v)
	if !strings.Contains(out, "+1 done") || !strings.Contains(out, "handle context cancel") {
		t.Fatalf("linked tasks not nested under the active step:\n%s", out)
	}
	// The standalone list keeps only the unlinked task.
	if strings.Count(out, "write the wrapper") > 0 || strings.Count(out, "handle context cancel") != 1 {
		t.Fatalf("linked tasks shown twice or the done one leaked:\n%s", out)
	}
	if !strings.Contains(out, "unrelated chore") || !strings.Contains(out, "Tasks") {
		t.Fatalf("unlinked task lost from the standalone list:\n%s", out)
	}
}

func TestTaskListKeepsLinkedTasksWhenNoPlanExists(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvTasks, Tasks: []agent.Task{{ID: "1", Text: "orphan", Status: "pending", StepID: "9"}}, At: at(c)})
	if out := plain(v); !strings.Contains(out, "orphan") {
		t.Fatalf("a task linked to no plan must still show:\n%s", out)
	}
}

func TestApprovedPlanFoldsToSummaryUntilStepsReport(t *testing.T) {
	v, c := newView(t, Options{})
	planTurn(v, c, proposal())
	ap := proposal()
	ap.Outcome = "approved"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: ap, At: at(c)})
	out := plain(v)
	if !strings.Contains(out, "Add retry (3 files, 2 steps)") || strings.Contains(out, "Steps (2)") {
		t.Fatalf("approved plan should fold to its summary line:\n%s", out)
	}
	// The first step report replaces the approved summary with the progress box.
	prog := proposal()
	prog.Steps[0].Status = "active"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: prog, At: at(c)})
	out = plain(v)
	if strings.Contains(out, "(3 files, 2 steps)") || !strings.Contains(out, "Wrap the fetch") {
		t.Fatalf("progress did not replace the approved summary:\n%s", out)
	}
	if strings.Count(out, "Add retry") != 1 {
		t.Fatalf("approved plan split into two segments:\n%s", out)
	}
}
