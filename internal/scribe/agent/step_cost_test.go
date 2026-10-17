package agent

import (
	"context"
	"math"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Each step_finish carries the cost of that one call, priced at the model that
// ran it, next to the running total. A view can sum StepCostUSD per model.
func TestStepFinishCarriesItsOwnCostAndModel(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{text: []string{"one"}, usage: &sseUsage{prompt: 1_000_000}}),
		turn(sseTurn{text: []string{"two"}, usage: &sseUsage{prompt: 2_000_000}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	ctx := context.Background()

	// model-a is priced at 3 per million input tokens (the test default).
	if _, err := a.Run(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	// model-b costs twice as much per input token.
	a.SetModel(fakeModel(f.srv.URL, "fake/model-b", llm.ModelInfo{ContextWindow: 100000, InputPerMTok: 6}))
	if _, err := a.Run(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}

	steps := log.of(EvStepFinish)
	if len(steps) != 2 {
		t.Fatalf("%d step_finish events, want 2", len(steps))
	}
	cases := []struct {
		model   string
		step    float64 // StepCostUSD
		running float64 // CostUSD, cumulative
	}{
		{"fake/model-a", 3, 3},
		{"fake/model-b", 12, 15},
	}
	for i, c := range cases {
		e := steps[i]
		if e.Model != c.model {
			t.Errorf("step %d: model %q, want %q", i, e.Model, c.model)
		}
		if math.Abs(e.StepCostUSD-c.step) > 1e-9 {
			t.Errorf("step %d: StepCostUSD %.6f, want %.6f", i, e.StepCostUSD, c.step)
		}
		if math.Abs(e.CostUSD-c.running) > 1e-9 {
			t.Errorf("step %d: CostUSD (running total) %.6f, want %.6f", i, e.CostUSD, c.running)
		}
	}
}
