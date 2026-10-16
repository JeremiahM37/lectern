package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

const symbolPlanArgs = `{"title":"Add retry","summary":"Retry the fetch.",` +
	`"steps":[{"text":"Wrap the fetch","files":["fetch.go"],"symbols":[{"name":"Fetch","kind":"func","action":"modify"},{"name":"retryPolicy","kind":"type","action":"add"}]},{"text":"Test it","files":["fetch_test.go"]}],` +
	`"verification":["go test ./...","manual: run scribe against a flaky host"]}`

func TestPlanClearContextLeavesOnlyThePlanMessage(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerClear)
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{text: []string{"Implemented."}}),
	))
	stats, err := RunClearing(context.Background(), h.agent, h.planner, "add tests", nil)
	must(t, err)
	if stats.Error != "" {
		t.Fatalf("stats.Error = %q", stats.Error)
	}
	if h.agent.Mode() != "default" {
		t.Fatalf("mode after clear = %q, want default", h.agent.Mode())
	}
	if h.fake.count() != 2 {
		t.Fatalf("model called %d times, want 2 (planning, then the cleared turn)", h.fake.count())
	}

	// The cleared turn sends exactly one non-system message: the plan.
	req := h.fake.requests()[1]
	conv := msgsWithRole(req, "user")
	if len(req.body.Messages) != len(msgsWithRole(req, "system"))+1 || len(conv) != 1 {
		t.Fatalf("cleared request carries %d messages, want the plan only: %s", len(req.body.Messages), req.raw)
	}
	first := conv[0].text()
	saved := filepath.Join(h.dir, ".scribe", "plans", "add-parser-tests.md")
	b, err := os.ReadFile(saved)
	must(t, err)
	if !strings.Contains(first, saved) || !strings.Contains(first, string(b)) {
		t.Fatalf("plan message lacks the saved path or content:\n%s", first)
	}
	if !strings.Contains(first, "Implement the plan now") {
		t.Fatalf("plan message lacks the instruction:\n%s", first)
	}
	for _, m := range msgsWithRole(req, "tool") {
		t.Fatalf("planning tool result leaked into the cleared context: %s", m.text())
	}

	// After the cleared turn the history is the plan message and the answer.
	msgs := h.agent.Messages()
	if len(msgs) != 2 || msgs[0].Role != llm.RoleUser || msgs[1].Text() != "Implemented." {
		t.Fatalf("history after clear = %d messages: %+v", len(msgs), msgs)
	}
	if h.planner.takeCleared() != nil {
		t.Fatal("clear request was not consumed")
	}
}

func TestPlanClearContextKeepsPlanState(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerClear)
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{text: []string{"Done."}}),
	))
	_, err := RunClearing(context.Background(), h.agent, h.planner, "add tests", nil)
	must(t, err)
	if pl := h.planner.Plan(); pl == nil || pl.Title != "Add parser tests" {
		t.Fatalf("plan after clear = %+v", pl)
	}
}

func TestRunClearingWithoutClearIsPlainRun(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerCancel)
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
	))
	_, err := RunClearing(context.Background(), h.agent, h.planner, "add tests", nil)
	must(t, err)
	if h.fake.count() != 1 || h.agent.Mode() != "plan" {
		t.Fatalf("requests=%d mode=%q after cancel", h.fake.count(), h.agent.Mode())
	}
}

func TestPlanSymbolsVerificationAndStepParsing(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerExecute)
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: symbolPlanArgs}}}),
		turn(sseTurn{text: []string{"Done."}}),
	))
	_, err := h.agent.Run(context.Background(), "add retry", nil)
	must(t, err)

	if len(h.host.asked) != 1 {
		t.Fatalf("asked %d questions", len(h.host.asked))
	}
	q := h.host.asked[0]
	for _, want := range []string{"## Verification", "- go test ./...", "- modify `Fetch` (func)", "- add `retryPolicy` (type)", "Saved to `"} {
		if !strings.Contains(q.Body, want) {
			t.Fatalf("review body lacks %q:\n%s", want, q.Body)
		}
	}

	steps := ParsePlanSteps(q.Body)
	if len(steps) != 2 {
		t.Fatalf("parsed %d steps, want 2: %+v", len(steps), steps)
	}
	if steps[0].Text != "Wrap the fetch" || strings.Join(steps[0].Files, ",") != "fetch.go" || steps[0].ID != "1" {
		t.Fatalf("step 1 = %+v", steps[0])
	}
	if steps[1].Text != "Test it" || strings.Join(steps[1].Files, ",") != "fetch_test.go" {
		t.Fatalf("step 2 = %+v", steps[1])
	}
}

func TestParsePlanStepsStopsAtBody(t *testing.T) {
	md := "# T\n\n## Steps\n\n1. one (`a.go`)\n2. two\n\nRisks:\n3. not a step\n"
	steps := ParsePlanSteps(md)
	if len(steps) != 2 || steps[1].Text != "two" {
		t.Fatalf("steps = %+v", steps)
	}
}
