package tui

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func openStorageQuestion(t *testing.T) (*Model, chan PromptReply) {
	t.Helper()
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{
			Kind:     "ask_user",
			Question: "Which storage?",
			Options:  []string{"JSON file", "SQLite", "Other (I'll type my choice)"},
		},
		Reply: reply,
	}})
	settle(m)
	return m, reply
}

func TestOtherOptionOpensTextEntryInsteadOfAnswering(t *testing.T) {
	m, reply := openStorageQuestion(t)
	runCmd(m, send(m, "down"))
	runCmd(m, send(m, "down"))
	runCmd(m, send(m, "enter"))
	select {
	case r := <-reply:
		t.Fatalf("choosing Other answered %q before any text was typed", r.Answer)
	default:
	}
	if !m.cur().pui.typing {
		t.Fatalf("choosing Other did not open the text row:\n%s", screen(m))
	}
	runCmd(m, send(m, "P"))
	runCmd(m, send(m, "enter"))
	r := <-reply
	if r.Answer != "P" {
		t.Fatalf("typed answer = %q, want P", r.Answer)
	}
}

func TestNormalOptionStillAnswersDirectly(t *testing.T) {
	m, reply := openStorageQuestion(t)
	runCmd(m, send(m, "enter"))
	if r := <-reply; r.Answer != "JSON file" {
		t.Fatalf("answer = %q, want JSON file", r.Answer)
	}
}

func TestIsOtherOption(t *testing.T) {
	for opt, want := range map[string]bool{
		"Other":                       true,
		"Other (I'll type my choice)": true,
		"other: something":            true,
		"Otherwise use SQLite":        false,
		"SQLite":                      false,
	} {
		if got := isOtherOption(opt); got != want {
			t.Errorf("isOtherOption(%q) = %v, want %v", opt, got, want)
		}
	}
}
