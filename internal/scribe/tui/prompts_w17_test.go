package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// openChoiceQuestion opens a question with the given options and returns the
// model and the channel its answer arrives on.
func openChoiceQuestion(t *testing.T, opts ...string) (*Model, chan PromptReply) {
	t.Helper()
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{Question: "Pick one", Options: opts, NoSkip: nextNoSkip},
		Reply:    reply,
	}})
	settle(m)
	return m, reply
}

// answered returns the answer sent on reply, or false if none was sent.
func answered(reply chan PromptReply) (string, bool) {
	select {
	case r := <-reply:
		return r.Answer, true
	default:
		return "", false
	}
}

// nextNoSkip is the NoSkip flag openChoiceQuestion gives its question.
var nextNoSkip bool

// setSkippable makes the test's questions skippable or not.
func setSkippable(t *testing.T, ok bool) {
	t.Helper()
	nextNoSkip = !ok
	t.Cleanup(func() { nextNoSkip = false })
}

func TestEscSkipsAQuestionThatAllowsIt(t *testing.T) {
	setSkippable(t, true)
	m, reply := openChoiceQuestion(t, "alpha", "beta")
	runCmd(m, send(m, "esc"))
	if a, ok := answered(reply); !ok || a != "" {
		t.Fatalf("Esc on a skippable question should send an empty answer, got %q (sent=%v)", a, ok)
	}
	if !strings.Contains(openQuestionFooter(t), "Esc skip") {
		t.Fatal("a skippable question should still offer Esc skip in its footer")
	}
}

func TestEscDoesNotSkipAQuestionThatNeedsAnAnswer(t *testing.T) {
	setSkippable(t, false)
	m, reply := openChoiceQuestion(t, "alpha", "beta")
	cmd := send(m, "esc")
	if _, ok := answered(reply); ok {
		t.Fatal("Esc must not answer a question that needs an answer")
	}
	if m.cur().pui == nil {
		t.Fatal("the question must stay open after Esc")
	}
	if f := flashOf(t, cmd); f.kind != "info" || f.text != "This question needs an answer" {
		t.Fatalf("Esc should say the question needs an answer, got %q (%s)", f.text, f.kind)
	}
	if s := screen(m); strings.Contains(s, "Esc skip") {
		t.Fatalf("footer should not offer Esc skip when skipping is not allowed:\n%s", s)
	}
}

// openQuestionFooter returns the footer of a fresh question.
func openQuestionFooter(t *testing.T) string {
	t.Helper()
	m, _ := openChoiceQuestion(t, "alpha", "beta")
	return screen(m)
}

func TestSpaceChoosesTheHighlightedQuestionOption(t *testing.T) {
	m, reply := openChoiceQuestion(t, "alpha", "beta")
	runCmd(m, send(m, "down"))
	runCmd(m, send(m, " "))
	if a, ok := answered(reply); !ok || a != "beta" {
		t.Fatalf("Space on beta should answer beta like Enter, got %q (sent=%v)", a, ok)
	}
}

func TestSpaceOnTheOtherOptionOpensTextEntry(t *testing.T) {
	m, reply := openChoiceQuestion(t, "alpha", "Other")
	runCmd(m, send(m, "down"))
	runCmd(m, send(m, " "))
	if _, ok := answered(reply); ok {
		t.Fatal("Space on Other must open the text row, not answer")
	}
	if !strings.Contains(screen(m), "type your answer...") {
		t.Fatalf("Space on Other should open the text row:\n%s", screen(m))
	}
}

func TestSpaceChoosesThePlanOption(t *testing.T) {
	m, reply := openPlanReview(t)
	runCmd(m, send(m, "down"))
	runCmd(m, send(m, " "))
	if a, ok := answered(reply); !ok || a != agent.AnswerClear {
		t.Fatalf("Space on the second plan option should answer clear, got %q (sent=%v)", a, ok)
	}
}

func TestSpaceDoesNotAllowAnApproval(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Approval: &tools.Approval{Tool: "shell", Kind: "shell", Title: "Run shell command", Detail: "ls"},
		Reply:    reply,
	}})
	settle(m)
	runCmd(m, send(m, " "))
	if _, ok := answered(reply); ok {
		t.Fatal("Space must not answer an approval; Enter and y/a/n do that")
	}
	if m.cur().pui == nil {
		t.Fatal("the approval must stay open after Space")
	}
}
