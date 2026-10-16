package lecternbridge

import (
	"context"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// askHost is a local host that answers questions with ans. When wait is
// set, it answers only after wait is closed (or ctx ends), so the test can
// make the remote answer the first.
type askHost struct {
	interactive bool
	ans         string
	wait        chan struct{}
}

func (h *askHost) Interactive() bool                                      { return h.interactive }
func (h *askHost) Approve(context.Context, tools.Approval) tools.Decision { return tools.Deny }
func (h *askHost) Progress(string, string)                                {}
func (h *askHost) Ask(ctx context.Context, _ tools.Question) (string, error) {
	if h.wait != nil {
		select {
		case <-h.wait:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return h.ans, nil
}

// A Lectern answer to an ask_user question wins while the local host waits,
// and the question reaches Lectern with its options.
func TestLecternAnswersQuestionWhileLocalWaits(t *testing.T) {
	f := newFakeLectern(t)
	f.ask = func(map[string]any) map[string]any {
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "AskUser", "answer": "SQLite"}}
	}
	local := &askHost{interactive: true, wait: make(chan struct{})}
	h := newTestHost(f, local)

	ans, err := h.Ask(context.Background(), tools.Question{
		Question: "Which database?", Options: []string{"Postgres", "SQLite"}, AllowFreeText: true,
	})
	if err != nil || ans != "SQLite" {
		t.Fatalf("Ask = %q, %v; want SQLite from Lectern", ans, err)
	}
	rec := f.next(t)
	if rec.Path != "AskUser" || rec.Body["kind"] != "question" || rec.Body["question"] != "Which database?" {
		t.Fatalf("AskUser body = %+v", rec)
	}
	if opts, _ := rec.Body["options"].([]any); len(opts) != 2 || opts[1] != "SQLite" {
		t.Fatalf("options = %v", rec.Body["options"])
	}
	if rec.Body["allow_free_text"] != true {
		t.Errorf("allow_free_text = %v", rec.Body["allow_free_text"])
	}
}

// A plan review is sent as kind plan_review with the plan as its body, and
// the answer it gets back is the option text the plan tool reads.
func TestPlanReviewGoesToLecternWithBody(t *testing.T) {
	f := newFakeLectern(t)
	f.ask = func(map[string]any) map[string]any {
		return map[string]any{"hookSpecificOutput": map[string]any{"answer": "Execute"}}
	}
	local := &askHost{interactive: true, wait: make(chan struct{})}
	h := newTestHost(f, local)

	ans, err := h.Ask(context.Background(), tools.Question{
		Kind: "plan_review", Question: "Add retries", Body: "# Add retries\n\n1. wrap the call",
		Options: []string{"Execute", "Revise", "Cancel"}, AllowFreeText: true,
	})
	if err != nil || ans != "Execute" {
		t.Fatalf("Ask = %q, %v; want Execute", ans, err)
	}
	rec := f.next(t)
	if rec.Body["kind"] != "plan_review" {
		t.Fatalf("kind = %v, want plan_review", rec.Body["kind"])
	}
	if body, _ := rec.Body["body"].(string); body != "# Add retries\n\n1. wrap the call" {
		t.Fatalf("plan body = %q", body)
	}
}

// With no answer from Lectern, an interactive local host decides alone.
func TestLecternSilenceLeavesQuestionToLocalHost(t *testing.T) {
	f := newFakeLectern(t) // ask is nil: Lectern answers {}
	local := &askHost{interactive: true, ans: "Postgres"}
	h := newTestHost(f, local)

	ans, err := h.Ask(context.Background(), tools.Question{Question: "Which?", Options: []string{"Postgres"}})
	if err != nil || ans != "Postgres" {
		t.Fatalf("Ask = %q, %v; want the local answer", ans, err)
	}
}

// A headless local host waits for Lectern before it answers, so a remote
// answer still wins over the headless default.
func TestHeadlessAskWaitsForLectern(t *testing.T) {
	f := newFakeLectern(t)
	f.ask = func(map[string]any) map[string]any {
		return map[string]any{"hookSpecificOutput": map[string]any{"answer": "from the phone"}}
	}
	local := &askHost{interactive: false, ans: "headless default"}
	h := newTestHost(f, local)

	ans, err := h.Ask(context.Background(), tools.Question{Question: "Which?", Options: []string{"A"}})
	if err != nil || ans != "from the phone" {
		t.Fatalf("Ask = %q, %v; want the Lectern answer", ans, err)
	}
}

// A headless local host with no Lectern answer uses its own default.
func TestHeadlessAskFallsBackWhenLecternIsSilent(t *testing.T) {
	f := newFakeLectern(t)
	local := &askHost{interactive: false, ans: "headless default"}
	h := newTestHost(f, local)

	ans, err := h.Ask(context.Background(), tools.Question{Question: "Which?"})
	if err != nil || ans != "headless default" {
		t.Fatalf("Ask = %q, %v; want the headless default", ans, err)
	}
}
