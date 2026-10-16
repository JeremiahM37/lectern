package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// The live plan shows the symbols of the step that is running, and nothing
// for the steps around it, so the eye lands on what is changing now.
func TestLivePlanShowsActiveStepSymbols(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvPlan, At: at(c), Plan: &agent.Plan{Title: "Add retry", Steps: []agent.PlanStep{
		{ID: "1", Text: "Wrap the fetch", Status: "done", Symbols: []agent.PlanSymbol{{Name: "Done", Action: "add"}}},
		{ID: "2", Text: "Retry policy", Status: "active", Symbols: []agent.PlanSymbol{
			{Name: "Fetch", Kind: "func", Action: "modify"},
			{Name: "Sleep", Action: "rename"},
			{Name: "retryPolicy", Kind: "type", Action: "add"},
			{Name: "oldBackoff", Action: "remove"}, // the fourth: held behind "+1 more"
		}},
		{ID: "3", Text: "Test", Status: "pending", Symbols: []agent.PlanSymbol{{Name: "Pending", Action: "add"}}},
	}}})
	out := plain(v)
	for _, want := range []string{"~ Fetch (func)", "→ Sleep", "+ retryPolicy (type)", "+1 more"} {
		if !strings.Contains(out, want) {
			t.Errorf("active step lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Done") || strings.Contains(out, "Pending") || strings.Contains(out, "oldBackoff") {
		t.Errorf("symbols of inactive steps should stay hidden:\n%s", out)
	}
	checkShape(t, v)
}
