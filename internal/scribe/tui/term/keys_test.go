package term

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTranslate(t *testing.T) {
	cases := []struct {
		name string
		km   tea.KeyMsg
		app  bool
		want string
	}{
		{"letter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, false, "a"},
		{"runes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("héllo")}, false, "héllo"},
		{"space", tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}, false, " "},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, false, "\r"},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, false, "\x7f"},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, false, "\t"},
		{"shift tab", tea.KeyMsg{Type: tea.KeyShiftTab}, false, "\x1b[Z"},
		{"escape", tea.KeyMsg{Type: tea.KeyEsc}, false, "\x1b"},
		{"up", tea.KeyMsg{Type: tea.KeyUp}, false, "\x1b[A"},
		{"down", tea.KeyMsg{Type: tea.KeyDown}, false, "\x1b[B"},
		{"right", tea.KeyMsg{Type: tea.KeyRight}, false, "\x1b[C"},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, false, "\x1b[D"},
		{"up app cursor", tea.KeyMsg{Type: tea.KeyUp}, true, "\x1bOA"},
		{"left app cursor", tea.KeyMsg{Type: tea.KeyLeft}, true, "\x1bOD"},
		{"home", tea.KeyMsg{Type: tea.KeyHome}, false, "\x1b[H"},
		{"end", tea.KeyMsg{Type: tea.KeyEnd}, false, "\x1b[F"},
		{"home app cursor", tea.KeyMsg{Type: tea.KeyHome}, true, "\x1bOH"},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, false, "\x1b[3~"},
		{"insert", tea.KeyMsg{Type: tea.KeyInsert}, false, "\x1b[2~"},
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}, false, "\x1b[5~"},
		{"pgdown", tea.KeyMsg{Type: tea.KeyPgDown}, false, "\x1b[6~"},
		{"ctrl+a", tea.KeyMsg{Type: tea.KeyCtrlA}, false, "\x01"},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}, false, "\x03"},
		{"ctrl+d", tea.KeyMsg{Type: tea.KeyCtrlD}, false, "\x04"},
		{"ctrl+l", tea.KeyMsg{Type: tea.KeyCtrlL}, false, "\x0c"},
		{"ctrl+z", tea.KeyMsg{Type: tea.KeyCtrlZ}, false, "\x1a"},
		{"ctrl+space", tea.KeyMsg{Type: tea.KeyCtrlAt}, false, "\x00"},
		{"alt+x", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x"), Alt: true}, false, "\x1bx"},
		{"alt+left", tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, false, "\x1b\x1b[D"},
		{"f1", tea.KeyMsg{Type: tea.KeyF1}, false, "\x1bOP"},
		{"f5", tea.KeyMsg{Type: tea.KeyF5}, false, "\x1b[15~"},
		{"f12", tea.KeyMsg{Type: tea.KeyF12}, false, "\x1b[24~"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Translate(c.km, c.app)
			if !bytes.Equal(got, []byte(c.want)) {
				t.Fatalf("Translate(%v) = %q, want %q", c.km, got, c.want)
			}
		})
	}
}

func TestTranslateNoMeaning(t *testing.T) {
	if got := Translate(tea.KeyMsg{Type: tea.KeyCtrlHome}, false); got != nil {
		t.Fatalf("ctrl+home has no byte form, got %q", got)
	}
}
