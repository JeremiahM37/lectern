package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func burst(m *Model, keys string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keys)})
}

func TestVimBurstInNormalModeRunsEachKey(t *testing.T) {
	m := vimModel(t)
	typeText(m, "hello world")
	send(m, "esc") // normal, cursor on d
	burst(m, "hh") // two moves, no text
	if got := inputText(m); got != "hello world" {
		t.Fatalf("a burst of motions was typed: %q", got)
	}
	send(m, "x") // cursor was on r
	if got := inputText(m); got != "hello wold" {
		t.Fatalf("hh then x: %q, want %q", got, "hello wold")
	}
}

func TestVimBurstAfterInsertKeyIsText(t *testing.T) {
	m := vimModel(t)
	typeText(m, "ab")
	send(m, "esc")
	burst(m, "0ixy") // to start, insert, then text
	if got := inputText(m); got != "xyab" {
		t.Fatalf("burst with i: %q, want %q", got, "xyab")
	}
	if m.cur().in.vim.normal {
		t.Fatalf("i in a burst should leave normal mode")
	}
}
