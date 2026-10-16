package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func openQuestion(t *testing.T, q string, options ...string) *Model {
	t.Helper()
	m, _ := newTestModel(t, 100, 40)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{Question: q, Options: options, AllowFreeText: true},
		Reply:    make(chan PromptReply, 1),
	}})
	settle(m)
	return m
}

// The question body is Markdown, not wrapped plain text.
func TestQuestionBodyRendersMarkdown(t *testing.T) {
	m := openQuestion(t, "Use **bold** here?", "yes", "no")
	s := screen(m)
	if !strings.Contains(s, "Use bold here?") || strings.Contains(s, "**") {
		t.Fatalf("question body was not rendered as markdown:\n%s", s)
	}
}

// Options use the three-cell marker so selected and unselected labels align.
func TestQuestionOptionsAlignUnderOneMarker(t *testing.T) {
	m := openQuestion(t, "Pick", "alpha", "beta")
	s := screen(m)
	if !strings.Contains(s, " › alpha") || !strings.Contains(s, "   beta") {
		t.Fatalf("options lack the three-cell marker:\n%s", s)
	}
}

// Opening the free-text row shows the placeholder until something is typed.
func TestQuestionTextEntryShowsPlaceholder(t *testing.T) {
	m := openQuestion(t, "Pick", "alpha", "beta")
	// the free-text row is the last option; move onto it and open it
	runCmd(m, send(m, "up"))
	runCmd(m, send(m, "enter"))
	if !strings.Contains(screen(m), "type your answer...") {
		t.Fatalf("text entry lacks its placeholder:\n%s", screen(m))
	}
	typeText(m, "x")
	if strings.Contains(screen(m), "type your answer...") {
		t.Fatalf("placeholder stayed after typing:\n%s", screen(m))
	}
}
