package tui

import (
	"strings"
	"testing"
)

// "!!command" is passthrough: the terminal is handed over and the transcript
// is left alone. "!command" keeps the captured form and still adds its "$" line.
func TestBangBangHandsOverTheTerminal(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	before := m.cur().chat.Len()
	if cmd := m.submit("!!true"); cmd == nil {
		t.Fatal("!!true returned no command, so the terminal was not handed over")
	}
	if got := m.cur().chat.Len(); got != before {
		t.Fatalf("!!true added %d transcript entries; passthrough output is not mirrored", got-before)
	}
}

func TestBangCommandStaysCaptured(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	before := m.cur().chat.Len()
	m.submit("!echo hi")
	if got := m.cur().chat.Len(); got != before+1 {
		t.Fatalf("!echo hi added %d entries, want one captured \"$\" line", got-before)
	}
}

func TestBangBangWithoutCommandIsUsageNotPassthrough(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	before := m.cur().chat.Len()
	if cmd := m.submit("!!"); cmd != nil {
		t.Fatal("a bare !! started something")
	}
	if !strings.HasPrefix(m.flash, "Usage: !!") {
		t.Fatalf("flash = %q, want the !! usage", m.flash)
	}
	if got := m.cur().chat.Len(); got != before {
		t.Fatalf("a bare !! wrote %d transcript entries", got-before)
	}
}
