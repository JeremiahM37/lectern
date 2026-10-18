package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// planProvider records the request it is given and answers with a fixed
// summary. It lets a test see what the V1 summary prompt received.
type planProvider struct {
	got  llm.Request
	text string
}

func (p *planProvider) ID() string { return "plan-test" }

func (p *planProvider) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (p *planProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.got = req
	ch := make(chan llm.Event, 2)
	ch <- llm.Event{Type: llm.EvText, Text: p.text}
	ch <- llm.Event{Type: llm.EvFinish}
	close(ch)
	return ch, nil
}

func samplePlan() *Plan {
	return &Plan{Title: "Move the cache\nto the new store", Steps: []PlanStep{
		{ID: "s1", Text: "add the store", Status: "done"},
		{ID: "s2", Text: "port the reads", Status: "active"},
		{ID: "s3", Text: "drop the old map", Status: "skipped"},
		{ID: "s4", Text: "write the docs", Status: "pending"},
		{ID: "s5", Text: "odd status", Status: "weird"},
	}}
}

// The Plan section sits after User Requirements and before Environment, lists
// the title, and marks each step with its status icon and id.
func TestWorkingStatePlanSectionRendersTitleAndStatuses(t *testing.T) {
	ws := WorkingState{
		UserRequirements: []string{"move the cache"},
		Environment:      []string{"go 1.25"},
		Plan:             samplePlan(),
	}
	out := ws.Serialize()
	for _, want := range []string{
		"## Plan\nTitle: Move the cache to the new store\n",
		"- [x] s1: add the store",
		"- [>] s2: port the reads",
		"- [-] s3: drop the old map",
		"- [ ] s4: write the docs",
		"- [ ] s5: odd status",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("serialized state lacks %q:\n%s", want, out)
		}
	}
	req := strings.Index(out, "## User Requirements")
	plan := strings.Index(out, "## Plan")
	env := strings.Index(out, "## Environment")
	if !(req < plan && plan < env) {
		t.Errorf("section order wrong: requirements %d, plan %d, environment %d", req, plan, env)
	}
}

// With no plan there is no Plan heading, so states without one are unchanged.
func TestWorkingStateWithoutPlanHasNoPlanSection(t *testing.T) {
	ws := WorkingState{Environment: []string{"go 1.25"}}
	if strings.Contains(ws.Serialize(), "## Plan") {
		t.Fatalf("a state without a plan should have no Plan section:\n%s", ws.Serialize())
	}
}

// The plan is not a slot: the spec's slot count (the rich-state test) does not
// include it, so adding a plan never changes whether a model call is made.
func TestPlanDoesNotCountAsASlot(t *testing.T) {
	ws := WorkingState{Plan: samplePlan()}
	if ws.Slots() != 0 {
		t.Fatalf("a plan alone should not be a slot, got %d", ws.Slots())
	}
}

// A long title and a long step are clipped, so one plan cannot swamp the
// summary.
func TestPlanLinesAreClipped(t *testing.T) {
	long := strings.Repeat("x", 500)
	lines := planLines(&Plan{Title: long, Steps: []PlanStep{{ID: "s1", Text: long, Status: "done"}}})
	if len(lines) != 2 {
		t.Fatalf("want title and one step, got %d lines", len(lines))
	}
	// clip keeps the text and appends a "more chars" note, so the bound is the
	// budget plus that note.
	if len(lines[0]) > 340 || len(lines[1]) > 240 || !strings.Contains(lines[0], "more chars") {
		t.Fatalf("lines not clipped: title %d, step %d", len(lines[0]), len(lines[1]))
	}
}

// The v2 summary carries the active plan from the ActivePlan source at
// compaction time, not from the messages.
func TestSummaryV2CarriesActivePlan(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("please move the cache to the new store, it is slow today"),
		llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartText, Text: "I will start with the store and then the reads."}}},
	}
	c := SummaryCompactor{
		Model:      Model{ID: "m", Provider: &planProvider{}},
		ActivePlan: samplePlan,
	}
	out, err := c.summarise(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## Plan\nTitle: Move the cache") || !strings.Contains(out, "- [>] s2: port the reads") {
		t.Fatalf("summary lacks the active plan:\n%s", out)
	}
}

// With no ActivePlan source, or one that returns nil, the summary has no
// Plan section.
func TestSummaryV2WithoutPlanHasNoPlanSection(t *testing.T) {
	msgs := []llm.Message{llm.UserText("please move the cache to the new store, it is slow today")}
	for name, c := range map[string]SummaryCompactor{
		"no source": {Model: Model{ID: "m", Provider: &planProvider{}}},
		"nil plan":  {Model: Model{ID: "m", Provider: &planProvider{}}, ActivePlan: func() *Plan { return nil }},
	} {
		out, err := c.summarise(context.Background(), msgs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(out, "## Plan") {
			t.Errorf("%s: summary should have no Plan section:\n%s", name, out)
		}
	}
}

// The V1 path sends the plan ahead of the transcript, so the model can carry
// it into Current State.
func TestSummaryV1SendsActivePlanToTheModel(t *testing.T) {
	p := &planProvider{text: strings.Repeat("A summary of the earlier work with enough words. ", 2)}
	c := SummaryCompactor{
		Model:      Model{ID: "m", Provider: p},
		Strategy:   "v1",
		ActivePlan: samplePlan,
	}
	if _, err := c.summarise(context.Background(), []llm.Message{llm.UserText("move the cache")}); err != nil {
		t.Fatal(err)
	}
	text := p.got.Messages[0].Parts[0].Text
	if !strings.HasPrefix(text, "Active plan (carry it into Current State verbatim):") || !strings.Contains(text, "- [>] s2: port the reads") {
		t.Fatalf("v1 request does not carry the plan first:\n%s", text)
	}
}

// Without a plan the V1 request is the transcript alone, as before.
func TestSummaryV1WithoutPlanIsUnchanged(t *testing.T) {
	p := &planProvider{text: strings.Repeat("A summary of the earlier work with enough words. ", 2)}
	c := SummaryCompactor{Model: Model{ID: "m", Provider: p}, Strategy: "v1"}
	if _, err := c.summarise(context.Background(), []llm.Message{llm.UserText("move the cache")}); err != nil {
		t.Fatal(err)
	}
	text := p.got.Messages[0].Parts[0].Text
	if !strings.HasPrefix(text, "Session transcript to summarise:") {
		t.Fatalf("v1 request without a plan should start with the transcript:\n%s", text)
	}
}
