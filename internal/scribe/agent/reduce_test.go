package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const serviceTierRejected = `{"error":{"message":"Unsupported parameter: service_tier is not supported with this model.","type":"invalid_request_error"}}`

// A provider that refuses an optional field gets the same step again without
// it, once. The drop holds for the rest of the turn, and a warning names it.
func TestRejectedOptionIsDroppedForTheStepAndTheTurn(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(400, "", serviceTierRejected),                                // turn 1, step 1
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "look", args: "{}"}}}), // turn 1, retry of step 1
		turn(sseTurn{text: []string{"done"}}),                                 // turn 1, step 2
		apiError(400, "", serviceTierRejected),                                // turn 2 tries the option again
		turn(sseTurn{text: []string{"again"}}),                                // turn 2, retry
	))
	reg := tools.NewRegistry()
	reg.Register(descTool{name: "look", desc: "Look."})
	log := &eventLog{}
	opts := baseOptions(f, reg, log, t.TempDir())
	opts.Tune = func(_ Model, r *llm.Request) { r.ServiceTier = "priority" }
	a := New(opts)
	ctx := context.Background()

	if _, err := a.Run(ctx, "go", nil); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("turn 1 sent %d requests, want 3 (rejected, retry, next step)", len(reqs))
	}
	if !strings.Contains(string(reqs[0].raw), "service_tier") {
		t.Fatalf("first request should carry service_tier")
	}
	for i := 1; i < 3; i++ {
		if strings.Contains(string(reqs[i].raw), "service_tier") {
			t.Errorf("request %d still carries service_tier after the rejection", i+1)
		}
	}
	warnings := log.of(EvWarning)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Text, "service_tier") {
		t.Fatalf("want one warning naming service_tier, got %+v", warnings)
	}

	if _, err := a.Run(ctx, "again", nil); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	reqs = f.requests()
	if len(reqs) != 5 {
		t.Fatalf("after turn 2 %d requests, want 5", len(reqs))
	}
	if !strings.Contains(string(reqs[3].raw), "service_tier") {
		t.Errorf("a new turn should try service_tier again")
	}
	if strings.Contains(string(reqs[4].raw), "service_tier") {
		t.Errorf("the retry in turn 2 should leave service_tier out")
	}
	if n := len(log.of(EvWarning)); n != 2 {
		t.Errorf("%d warnings after two turns, want 2", n)
	}
}

// A 400 that is not an option rejection is returned as it always was.
func TestOtherBadRequestsAreNotRetried(t *testing.T) {
	cases := map[string]string{
		"names no option":               `{"error":{"message":"messages must not be empty","type":"invalid_request_error"}}`,
		"names an option, no rejection": `{"error":{"message":"service_tier must be one of auto or default","type":"invalid_request_error"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.setPick(scripted(t, apiError(400, "", body)))
			log := &eventLog{}
			opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
			opts.Tune = func(_ Model, r *llm.Request) { r.ServiceTier = "priority" }
			a := New(opts)
			if _, err := a.Run(context.Background(), "hi", nil); err == nil {
				t.Fatal("want the 400 returned as an error")
			}
			if n := f.count(); n != 1 {
				t.Fatalf("%d requests, want 1 (no retry)", n)
			}
			if w := log.of(EvWarning); len(w) != 0 {
				t.Fatalf("unexpected warnings: %+v", w)
			}
		})
	}
}

// An option the request did not carry cannot be the reason for a rejection,
// so the step is not retried.
func TestRejectionOfAnOptionNotSentIsNotRetried(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, apiError(400, "", `{"error":{"message":"Unsupported parameter: verbosity","type":"invalid_request_error"}}`)))
	a := New(baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir()))
	if _, err := a.Run(context.Background(), "hi", nil); err == nil {
		t.Fatal("want the 400 returned as an error")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

func TestRejectedGroupsNamesTheOptions(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"reasoning_effort", &llm.APIError{StatusCode: 400, Message: "Unsupported value: reasoning_effort 'high'"}, []string{"reasoning"}},
		{"thinking type", &llm.APIError{StatusCode: 400, Message: "thinking.type: unsupported for this model"}, []string{"reasoning"}},
		{"parallel tool calls", &llm.APIError{StatusCode: 400, Message: "parallel_tool_calls is not supported"}, []string{"parallel tool calls"}},
		{"temperature", &llm.APIError{StatusCode: 400, Message: "Unknown parameter: temperature"}, []string{"temperature"}},
		{"server error", &llm.APIError{StatusCode: 503, Message: "reasoning is not supported"}, nil},
		{"plain error", &llm.APIError{StatusCode: 400, Message: "model not found"}, nil},
		{"not an api error", context.DeadlineExceeded, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rejectedGroups(c.err)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("rejectedGroups = %q, want %q", got, c.want)
			}
		})
	}
}
