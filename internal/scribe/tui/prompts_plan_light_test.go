package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// openLightPlanReview opens a review whose options came from a light plan.
func openLightPlanReview(t *testing.T) (*Model, chan PromptReply) {
	t.Helper()
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{
			Kind:          "plan_review",
			Question:      "Add retry",
			Body:          reviewBody,
			Options:       agent.PlanOptionsFor(agent.PlanLight),
			AllowFreeText: true,
		},
		Reply: reply,
	}})
	settle(m)
	return m, reply
}

func TestLightPlanHasNoClearChoice(t *testing.T) {
	m, _ := openLightPlanReview(t)
	s := screen(m)
	if strings.Contains(s, "Clear & Implement") {
		t.Fatalf("light plan still offers Clear & Implement:\n%s", s)
	}
	for _, want := range []string{"1  ⏎ Implement", "2  ✎ Revise", "3  ✗ Cancel"} {
		if !strings.Contains(s, want) {
			t.Fatalf("light plan lacks %q:\n%s", want, s)
		}
	}
}

func TestLightPlanKeyTwoIsRevise(t *testing.T) {
	m, reply := openLightPlanReview(t)
	runCmd(m, send(m, "2"))
	runCmd(m, send(m, "enter"))
	select {
	case r := <-reply:
		t.Fatalf("key 2 on a light plan answered %q; it should open Revise", r.Answer)
	default:
	}
	if !m.cur().pui.typing {
		t.Fatalf("key 2 on a light plan did not open the revision row:\n%s", screen(m))
	}
}

func TestFullPlanKeepsClearChoice(t *testing.T) {
	m, _ := openPlanReview(t)
	if s := screen(m); !strings.Contains(s, "2  ↻ Clear & Implement") {
		t.Fatalf("full plan lost Clear & Implement:\n%s", s)
	}
}

func TestOtherOptionHasNoDuplicateFreeTextRow(t *testing.T) {
	m, _ := openStorageQuestion(t)
	s := screen(m)
	if strings.Contains(s, "Type an answer") {
		t.Fatalf("an Other option should replace the free-text row:\n%s", s)
	}
	if !strings.Contains(s, "Other (I'll type my choice)") {
		t.Fatalf("Other option missing:\n%s", s)
	}
}

func TestQuestionWithoutOtherKeepsFreeTextRow(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{Kind: "ask_user", Question: "Which?", Options: []string{"A", "B"}},
		Reply:    reply,
	}})
	settle(m)
	if s := screen(m); !strings.Contains(s, "Type an answer") {
		t.Fatalf("question without Other lost its free-text row:\n%s", s)
	}
}
