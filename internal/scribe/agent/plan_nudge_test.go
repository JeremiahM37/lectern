package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// lookSteps answers the first n requests with a call to the look tool, then
// with a final text reply.
func lookSteps(t *testing.T, n int) func(int, recordedReq) reply {
	return func(i int, _ recordedReq) reply {
		if i < n {
			return turn(sseTurn{calls: []sseCall{{id: fmt.Sprintf("c%d", i), name: "look", args: fmt.Sprintf(`{"n":%d}`, i)}}})
		}
		return turn(sseTurn{text: []string{"done"}})
	}
}

// requestsWith returns the indexes of the requests whose messages contain text.
func requestsWith(reqs []recordedReq, text string) []int {
	var out []int
	for i, r := range reqs {
		for _, m := range r.body.Messages {
			if strings.Contains(m.text(), text) {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

func lookRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(descTool{name: "look", desc: "Look."})
	return reg
}

// A plan-mode turn with no plan reaches step 10 and step 20 and is reminded
// at each one, once. The system prompt is the same bytes on every request.
func TestPlanNudgesAtStepTenAndTwentyWithoutAPlan(t *testing.T) {
	f := newFake(t)
	f.setPick(lookSteps(t, 24))
	opts := baseOptions(f, lookRegistry(), &eventLog{}, t.TempDir())
	opts.Mode = "plan"
	a := New(opts)
	if _, err := a.Run(context.Background(), "work", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 25 {
		t.Fatalf("%d requests, want 25", len(reqs))
	}

	// The reminders are latched into the history, so later requests carry
	// them too. What matters is where each first appears and that each is
	// present once in the last request.
	if first := requestsWith(reqs, "plan-mode turn is at step 10"); len(first) == 0 || first[0] != 9 {
		t.Fatalf("step 10 reminder first sent in requests %v, want the request for step 10 (index 9)", first)
	}
	if first := requestsWith(reqs, "plan-mode turn is at step 20"); len(first) == 0 || first[0] != 19 {
		t.Fatalf("step 20 reminder first sent in requests %v, want the request for step 20 (index 19)", first)
	}
	last := string(reqs[len(reqs)-1].raw)
	if n := strings.Count(last, "plan-mode turn is at step 10"); n != 1 {
		t.Errorf("step 10 reminder appears %d times in the last request, want 1", n)
	}
	if n := strings.Count(last, "plan-mode turn is at step 20"); n != 1 {
		t.Errorf("step 20 reminder appears %d times in the last request, want 1", n)
	}
	if !strings.Contains(last, "Stop exploring") {
		t.Errorf("the step 20 reminder should be the firmer one")
	}

	sys := systemText(reqs[0])
	for i, r := range reqs {
		if systemText(r) != sys {
			t.Fatalf("system prompt changed at request %d", i+1)
		}
	}
}

// Once the turn has called the plan tool, no reminder is sent.
func TestPlanNudgeIsSkippedOnceAPlanExists(t *testing.T) {
	f := newFake(t)
	f.setPick(func(i int, _ recordedReq) reply {
		if i == 0 {
			return turn(sseTurn{calls: []sseCall{{id: "t0", name: "plan", args: "{}"}}})
		}
		return lookSteps(t, 24)(i-1, recordedReq{})
	})
	reg := lookRegistry()
	reg.Register(descTool{name: "plan", desc: "Plan."})
	opts := baseOptions(f, reg, &eventLog{}, t.TempDir())
	opts.Mode = "plan"
	a := New(opts)
	if _, err := a.Run(context.Background(), "work", nil); err != nil {
		t.Fatal(err)
	}
	if hits := requestsWith(f.requests(), "plan-mode turn is at step"); len(hits) != 0 {
		t.Fatalf("reminders sent to a turn with a task list: requests %v", hits)
	}
}

// Only the main agent in plan mode is nudged (spec 03 §2.2), and a
// subagent's budget already limits its steps.
func TestPlanNudgeOnlyForTheMainAgentInPlanMode(t *testing.T) {
	cases := map[string]func(*Options){
		"default mode": func(o *Options) { o.Mode = "default" },
		"subagent":     func(o *Options) { o.Mode = "plan"; o.Budget = &StepBudget{Kind: "explore"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.setPick(lookSteps(t, 24))
			opts := baseOptions(f, lookRegistry(), &eventLog{}, t.TempDir())
			change(&opts)
			a := New(opts)
			if _, err := a.Run(context.Background(), "work", nil); err != nil {
				t.Fatal(err)
			}
			if hits := requestsWith(f.requests(), "plan-mode turn is at step"); len(hits) != 0 {
				t.Fatalf("reminders sent: requests %v", hits)
			}
		})
	}
}
