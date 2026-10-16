package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const fieldsPlanArgs = `{
  "title": "Add retry",
  "summary": "Retry 5xx responses.",
  "steps": [
    {"text": "Wrap the fetch", "files": ["fetch.go"],
     "symbols": [{"name": "Fetch", "kind": "func", "action": "modify"},
                 {"name": "retryPolicy", "kind": "type", "action": "add"}]},
    {"text": "Test it", "files": ["fetch_test.go"]}
  ],
  "verification": ["go test ./...", "curl the flaky endpoint"],
  "body": "Risks: none that matter.\n- not a verification item"
}`

// The plan event must carry the symbols and verification the planner records,
// so the live view and the saved plan JSON can show them.
func TestPlanEventCarriesSymbolsAndVerification(t *testing.T) {
	var got []Event
	p := &Planner{Sink: func(e Event) { got = append(got, e) }}
	var env tools.Env
	res := planTool{p}.Run(context.Background(), &env, json.RawMessage(fieldsPlanArgs))
	if res.IsError {
		t.Fatalf("plan failed: %s", res.Output)
	}
	if len(got) != 1 || got[0].Plan == nil {
		t.Fatalf("events = %+v", got)
	}
	pl := got[0].Plan
	if len(pl.Verification) != 2 || pl.Verification[1] != "curl the flaky endpoint" {
		t.Fatalf("verification = %q", pl.Verification)
	}
	if len(pl.Steps[0].Symbols) != 2 || pl.Steps[0].Symbols[0] != (PlanSymbol{Name: "Fetch", Kind: "func", Action: "modify"}) {
		t.Fatalf("step 1 symbols = %+v", pl.Steps[0].Symbols)
	}
	if len(pl.Steps[1].Symbols) != 0 {
		t.Fatalf("step 2 should have no symbols: %+v", pl.Steps[1].Symbols)
	}

	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"verification":["go test ./..."`, `"symbols":[{"name":"Fetch"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("event JSON lacks %s: %s", want, raw)
		}
	}
}

func TestParsePlanStepsReadsSymbolsBack(t *testing.T) {
	pl := &Plan{Title: "Add retry", Steps: []PlanStep{
		{ID: "1", Text: "Wrap the fetch", Files: []string{"fetch.go"}, Symbols: []PlanSymbol{
			{Name: "Fetch", Kind: "func", Action: "modify"},
			{Name: "retryPolicy", Action: "add"},
		}},
		{ID: "2", Text: "Test it"},
	}}
	ex := planExtras{symbols: map[string][]PlanSymbol{"1": pl.Steps[0].Symbols}}
	md := renderPlan(pl, ex)
	steps := ParsePlanSteps(md)
	if len(steps) != 2 {
		t.Fatalf("parsed %d steps: %+v", len(steps), steps)
	}
	if len(steps[0].Symbols) != 2 || steps[0].Symbols[0] != (PlanSymbol{Name: "Fetch", Kind: "func", Action: "modify"}) ||
		steps[0].Symbols[1] != (PlanSymbol{Name: "retryPolicy", Action: "add"}) {
		t.Fatalf("symbols = %+v", steps[0].Symbols)
	}
	if steps[0].Text != "Wrap the fetch" || len(steps[0].Files) != 1 {
		t.Fatalf("step 1 = %+v", steps[0])
	}
}

func TestParsePlanVerificationStopsAtBody(t *testing.T) {
	md := "# T\n\n## Steps\n\n1. one\n\n## Verification\n\n- go test ./...\n- make lint\n\nRisks follow.\n- not a check\n"
	got := ParsePlanVerification(md)
	if len(got) != 2 || got[0] != "go test ./..." || got[1] != "make lint" {
		t.Fatalf("verification = %q", got)
	}
	if v := ParsePlanVerification("# T\n\n## Steps\n\n1. one\n"); v != nil {
		t.Fatalf("plan without a checklist gave %q", v)
	}
}
