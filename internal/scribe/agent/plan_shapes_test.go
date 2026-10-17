package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Shapes a live model sent for the plan tool. Each must reach the review card.
func TestPlanToolAcceptsStringStepsAndFiles(t *testing.T) {
	cases := map[string]string{
		"string steps":   `{"title":"T","steps":["Write tests","Run go test"],"files":["parser.go","new.go"]}`,
		"steps per line": `{"title":"T","steps":"Write tests\nRun go test","files":"parser.go"}`,
		"one step obj":   `{"title":"T","steps":{"text":"Write tests"}}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPlanHarness(t, "plan", "Cancel")
			must(t, os.WriteFile(filepath.Join(h.dir, "parser.go"), []byte("package p\n"), 0o644))
			h.fake.setPick(scripted(t,
				turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: args}}}),
				turn(sseTurn{text: []string{"ok"}}),
			))
			_, err := h.agent.Run(context.Background(), "plan it", nil)
			must(t, err)
			if len(h.host.asked) != 1 || h.host.asked[0].Kind != "plan_review" {
				t.Fatalf("no plan review for %s: %+v", args, h.host.asked)
			}
			var pl *Plan
			for _, e := range h.log.snapshot() {
				if e.Kind == EvPlan && e.Plan != nil {
					pl = e.Plan
				}
			}
			if pl == nil || len(pl.Steps) == 0 || pl.Steps[0].Text != "Write tests" {
				t.Fatalf("plan steps = %+v", pl)
			}
			for _, f := range pl.Files {
				want := map[string]string{"parser.go": "modify", "new.go": "create"}[f.Path]
				if f.Action != want {
					t.Fatalf("file %s action %q, want %q", f.Path, f.Action, want)
				}
			}
		})
	}
}

func TestAskUserAllowSkipFalseSetsNoSkip(t *testing.T) {
	h := newPlanHarness(t, "default", "yes")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_q", name: "ask_user", args: `{"question":"Proceed?","options":["yes","no"],"allow_skip":false}`}}}),
		turn(sseTurn{text: []string{"ok"}}),
	))
	_, err := h.agent.Run(context.Background(), "go", nil)
	must(t, err)
	if len(h.host.asked) != 1 || !h.host.asked[0].NoSkip {
		t.Fatalf("asked = %+v, want one question with NoSkip", h.host.asked)
	}
}
