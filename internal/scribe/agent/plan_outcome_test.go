package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestPlanDepthFollowsHalfTheWindow(t *testing.T) {
	cases := []struct {
		used, window int
		want         string
	}{
		{0, 0, PlanFull}, // unknown window: nothing is hidden
		{4200, 16384, PlanLight},
		{8192, 16384, PlanLight}, // exactly half is light
		{8193, 16384, PlanFull},
	}
	for _, c := range cases {
		if got := PlanDepth(c.used, c.window); got != c.want {
			t.Errorf("PlanDepth(%d, %d) = %q, want %q", c.used, c.window, got, c.want)
		}
	}
}

func TestLightPlanOffersNoClearChoice(t *testing.T) {
	if got := strings.Join(PlanOptionsFor(PlanLight), ","); got != "Execute,Revise,Cancel" {
		t.Fatalf("light options = %q", got)
	}
	if got := strings.Join(PlanOptionsFor(PlanFull), ","); got != "Execute,Clear context and execute,Revise,Cancel" {
		t.Fatalf("full options = %q", got)
	}
}

func TestPlanExecutionAllowlistExcludesDiscovery(t *testing.T) {
	for _, ok := range []string{"read", "edit_file", "multi_edit", "shell", "update_plan_step"} {
		if !PlanExecutionAllowed(ok) {
			t.Errorf("%s should be allowed while executing a plan", ok)
		}
	}
	for _, no := range []string{"dispatch", "explore", "discover_pattern", "web_search"} {
		if PlanExecutionAllowed(no) {
			t.Errorf("%s must not be allowed while executing a plan", no)
		}
	}
}

func TestCleanPlanFilesDropsUnknownActions(t *testing.T) {
	got := cleanPlanFiles([]PlanFile{
		{Path: " a.go ", Action: "CREATE"},
		{Path: "b.go", Action: "rename"},
		{Path: "", Action: "modify"},
		{Path: "c.go", Action: "delete", Description: "gone"},
	})
	if len(got) != 2 || got[0].Path != "a.go" || got[0].Action != "create" || got[1].Action != "delete" {
		t.Fatalf("cleanPlanFiles = %+v", got)
	}
}

func TestPlanMarkdownListsFilesAndRoundTripsSteps(t *testing.T) {
	pl := &Plan{Title: "T", Steps: []PlanStep{{ID: "1", Text: "Do it", Files: []string{"a.go"}}},
		Files: []PlanFile{{Path: "a.go", Action: "modify", Description: "add X"}}}
	md := renderPlan(pl, planExtras{})
	if !strings.Contains(md, "## Files\n\n- modify `a.go` — add X") {
		t.Fatalf("files section missing:\n%s", md)
	}
	steps := ParsePlanSteps(md)
	if len(steps) != 1 || steps[0].Text != "Do it" || len(steps[0].Files) != 1 {
		t.Fatalf("steps after round trip = %+v", steps)
	}
}

// outcomeEvents returns the plan events that carry an outcome snapshot.
func outcomeEvents(l *eventLog) []*Plan {
	var out []*Plan
	for _, e := range l.of(EvPlan) {
		if e.Plan != nil && e.Plan.Outcome != "" {
			out = append(out, e.Plan)
		}
	}
	return out
}

func TestCancelEmitsCancelledSnapshotAndKeepsNoPlan(t *testing.T) {
	h := newPlanHarness(t, "plan", "Cancel")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{text: []string{"Cancelled."}}),
	))
	if _, err := h.agent.Run(context.Background(), "add tests", nil); err != nil {
		t.Fatal(err)
	}
	snaps := outcomeEvents(h.log)
	if len(snaps) != 1 || snaps[0].Outcome != "cancelled" || snaps[0].Title != "Add parser tests" {
		t.Fatalf("outcome snapshots = %+v", snaps)
	}
	if h.planner.Plan() != nil {
		t.Fatal("a cancelled plan is still held by the planner")
	}
}

func TestReviseEmitsRevisionSnapshotWithFeedback(t *testing.T) {
	h := newPlanHarness(t, "plan", "Revise add a fuzz test", "Execute")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{calls: []sseCall{{id: "call_q", name: "plan", args: revisedPlanArgs}}}),
		turn(sseTurn{text: []string{"Done."}}),
	))
	if _, err := h.agent.Run(context.Background(), "add tests", nil); err != nil {
		t.Fatal(err)
	}
	snaps := outcomeEvents(h.log)
	if len(snaps) != 2 || snaps[0].Outcome != "revision" || snaps[0].Feedback != "add a fuzz test" ||
		snaps[1].Outcome != "approved" {
		t.Fatalf("outcome snapshots = %+v", snaps)
	}
}

func TestExecuteEmitsApprovedSnapshot(t *testing.T) {
	h := newPlanHarness(t, "plan", "Execute")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{text: []string{"Plan done."}}),
	))
	if _, err := h.agent.Run(context.Background(), "add tests", nil); err != nil {
		t.Fatal(err)
	}
	snaps := outcomeEvents(h.log)
	if len(snaps) != 1 || snaps[0].Outcome != "approved" || snaps[0].Title != "Add parser tests" {
		t.Fatalf("outcome snapshots = %+v", snaps)
	}
}

func TestTaskStepLinkIsStored(t *testing.T) {
	p := &Planner{}
	tool := taskTool{p}
	args := []byte(`{"action":"set","tasks":["write tests","run them"],"step_ids":["1",""]}`)
	if r := tool.Run(context.Background(), nil, args); r.IsError {
		t.Fatalf("set failed: %s", r.Output)
	}
	got := p.Tasks()
	if len(got) != 2 || got[0].StepID != "1" || got[1].StepID != "" {
		t.Fatalf("tasks = %+v", got)
	}
}

// TestApprovedPlanBlocksDiscoveryForTheRestOfTheTurn: after Execute the agent
// carries out the plan; a dispatch is refused, an edit is not, and the next
// turn is free again.
func TestApprovedPlanBlocksDiscoveryForTheRestOfTheTurn(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(stubDispatch{})
	a := New(Options{Tools: reg})
	a.BeginPlanExecution()
	res := a.execTool(context.Background(), llm.Part{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "dispatch", Args: []byte(`{}`)})
	if !res.IsError || !strings.Contains(res.Output, "approved plan") {
		t.Fatalf("dispatch during plan execution = %+v", res)
	}
	if !a.executingPlan() {
		t.Fatal("the flag should hold for the rest of the turn")
	}
}

// stubDispatch stands in for the dispatch tool; it must never run here.
type stubDispatch struct{}

func (stubDispatch) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "dispatch", Description: "stub", Schema: tools.Schema(map[string]any{})}
}
func (stubDispatch) ReadOnly() bool { return true }
func (stubDispatch) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{Output: "ran"}
}
