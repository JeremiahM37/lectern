package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A step packed with a numbered list becomes one step per item, and a leading
// number is dropped, since the plan numbers its steps itself.
func TestSplitStepText(t *testing.T) {
	cases := map[string][]string{
		"1. Add X 2. Add Y":              {"Add X", "Add Y"},
		"1. Add X. 2. Add Y. 3. Test Z.": {"Add X.", "Add Y.", "Test Z."},
		"3. Add the thing":               {"Add the thing"},
		"3) Add the thing":               {"Add the thing"},
		"Step 2: Wire it up":             {"Wire it up"},
		"Bump to v2.1 and keep 3.5 MB":   {"Bump to v2.1 and keep 3.5 MB"},
		"Add X 2. Add Y":                 {"Add X 2. Add Y"},
		"  Plain step  ":                 {"Plain step"},
		"1.":                             {"1."},
	}
	for in, want := range cases {
		if got := splitStepText(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitStepText(%q) = %q, want %q", in, got, want)
		}
	}
}

// The plan tool stores what splitStepText returns: numbered-list steps are
// split, each part keeps the step's files, and ids run in order.
func TestPlanToolSplitsPackedSteps(t *testing.T) {
	args := `{"title":"Two things","steps":[
	  {"text":"1. Add the parser 2. Add the printer","files":["parse.go"]},
	  {"text":"3. Update the docs"}]}`
	var got []Event
	p := &Planner{Sink: func(e Event) { got = append(got, e) }}
	res := planTool{p}.Run(context.Background(), &tools.Env{}, json.RawMessage(args))
	if res.IsError {
		t.Fatalf("plan failed: %s", res.Output)
	}
	steps := got[len(got)-1].Plan.Steps
	if len(steps) != 3 {
		t.Fatalf("steps = %+v, want 3", steps)
	}
	want := []PlanStep{
		{ID: "1", Text: "Add the parser", Files: []string{"parse.go"}, Status: "pending"},
		{ID: "2", Text: "Add the printer", Files: []string{"parse.go"}, Status: "pending"},
		{ID: "3", Text: "Update the docs", Status: "pending"},
	}
	for i := range want {
		if steps[i].ID != want[i].ID || steps[i].Text != want[i].Text || !reflect.DeepEqual(steps[i].Files, want[i].Files) {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}
