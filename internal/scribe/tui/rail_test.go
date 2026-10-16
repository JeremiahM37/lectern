package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
)

// altKey is Alt plus a letter, as bubbletea reports it.
func altKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

func TestRailKeysFocusMoveAndRestore(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	tb := m.cur()

	m.Update(altKey('c'))
	if !tb.chat.RailFocused() {
		t.Fatal("Alt+C did not focus the checkpoint rail")
	}
	// The cursor starts on the latest checkpoint; Up moves to the one before.
	m.Update(key("up"))
	if i, cp, ok := tb.chat.RailSelected(); !ok || cp.Turn != 2 || i != 1 {
		t.Fatalf("after Up: idx=%d turn=%d ok=%v, want turn 2", i, cp.Turn, ok)
	}

	// Enter browses to that turn and offers the restore confirm.
	m.Update(key("enter"))
	if tb.chat.RailFocused() {
		t.Error("rail should give up focus after Enter")
	}
	if tb.browse != 1 {
		t.Errorf("browse index %d, want 1", tb.browse)
	}
	if !strings.Contains(screen(m), "Restore to turn 2?") {
		t.Errorf("restore confirm not offered:\n%s", screen(m))
	}
}

func TestRestoreConfirmNamesRestoreNotDelete(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	m.Update(altKey('c'))
	m.Update(key("up"))
	m.Update(key("enter"))
	top, ok := m.topModal().(*listConfirm)
	if !ok {
		t.Fatalf("restore should push a confirm, top is %T", m.topModal())
	}
	if top.yes {
		t.Error("restore confirm should default to No")
	}
	if top.Danger() {
		t.Error("restore is reversible (/redo), so it should not be a danger confirm")
	}
	view := plain(top.View(70, 12))
	if !strings.Contains(view, "Yes, restore") || strings.Contains(view, "delete") {
		t.Errorf("restore buttons should say restore:\n%s", view)
	}
	if strings.Contains(top.Footer(), "delete") {
		t.Errorf("restore footer = %q", top.Footer())
	}
}

func TestRailEscReturnsKeyboard(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	m.Update(altKey('c'))
	m.Update(key("esc"))
	if m.cur().chat.RailFocused() {
		t.Error("Esc should leave the rail")
	}
}

func TestRailOtherKeysGoToInput(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	m.Update(altKey('c'))
	typeText(m, "x")
	if m.cur().chat.RailFocused() {
		t.Error("typing should give the rail up")
	}
	if got := m.cur().in.value(); !strings.Contains(got, "x") {
		t.Errorf("typed key did not reach the input: %q", got)
	}
}

// clickRail sends a left press at column x, row y of the chat view.
func clickRail(m *Model, x, y int) {
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
}

// screenOf is the screen cell of a point given in transcript coordinates. The
// transcript starts below the header (and tab bar) and, framed, one cell in
// from its border on each side.
func screenOf(m *Model, x, y int) (int, int) {
	if m.chatFramed() {
		return x + 1, y + m.chatTop() + 1
	}
	return x, y + m.chatTop()
}

// seedRailTurns gives the chat n user turns and the fake backend a checkpoint
// for each, the last one undone when undoLast is set. Markers sit on the turns.
func seedRailTurns(m *Model, fb *FakeBackend, n int, undoLast bool) {
	tb := m.cur()
	m.phase = phaseChat // a transcript with turns is the chat screen, not the landing
	fb.checkpoints = nil
	for i := 1; i <= n; i++ {
		tb.chat.AddUser(fmt.Sprintf("question %d", i), 0, time.Now(), false)
		fb.checkpoints = append(fb.checkpoints, CheckpointInfo{Turn: i, Prompt: fmt.Sprintf("question %d", i), Files: []string{"a.go"}})
	}
	if undoLast {
		fb.checkpoints[n-1].Undone = true
	}
	m.syncRail(tb)
}

// railRow finds the chat-view row of the first marker that passes want.
func railRow(m *Model, want func(idx int, cp chat.Checkpoint) bool) (int, int, bool) {
	tb := m.cur()
	for y := 0; y < 40; y++ {
		if idx, cp, ok := tb.chat.RailHit(1, y); ok && want(idx, cp) {
			return idx, y, true
		}
	}
	return -1, -1, false
}

func TestRailMarkerClickBrowsesToCheckpoint(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	settle(m)
	seedRailTurns(m, fb, 3, false)
	tb := m.cur()
	m.View() // lay the transcript out so the markers have rows
	idx, row, ok := railRow(m, func(i int, cp chat.Checkpoint) bool { return i == 1 })
	if !ok {
		t.Fatalf("no marker for checkpoint 1 on screen:\n%s", screen(m))
	}
	{
		sx, sy := screenOf(m, 1, row)
		clickRail(m, sx, sy)
	}
	if tb.browse != idx {
		t.Errorf("click on marker %d left browse=%d", idx, tb.browse)
	}
	if tb.chat.RailFocused() {
		t.Error("a click should not take the keyboard")
	}
	if !strings.Contains(screen(m), "●") && !strings.Contains(screen(m), icons.Dot) {
		t.Errorf("the viewed marker should draw as the dot:\n%s", screen(m))
	}
}

func TestRailClickOutsideRailGoesToChat(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	settle(m)
	seedRailTurns(m, fb, 3, false)
	tb := m.cur()
	before := tb.browse
	{
		sx, sy := screenOf(m, 60, 5)
		clickRail(m, sx, sy)
	}
	if tb.browse != before {
		t.Errorf("a click in the chat changed the checkpoint browse index")
	}
}

func TestRailClickIgnoresUndoneMarker(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	settle(m)
	seedRailTurns(m, fb, 3, true)
	tb := m.cur()
	m.View()
	_, row, ok := railRow(m, func(_ int, cp chat.Checkpoint) bool { return cp.Undone })
	if !ok {
		t.Fatalf("no undone marker on screen:\n%s", screen(m))
	}
	before := tb.browse
	{
		sx, sy := screenOf(m, 1, row)
		clickRail(m, sx, sy)
	}
	if tb.browse != before {
		t.Errorf("click on an undone marker moved browse to %d", tb.browse)
	}
}

func TestRailClickAccountsForTabBar(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	settle(m)
	m.addTab(fb, "second")
	m.switchTab(0)
	seedRailTurns(m, fb, 2, false)
	tb := m.cur()
	m.View()
	if m.tabBarRows() != 1 || m.chatTop() != 2 {
		t.Fatalf("with two tabs the chat should start at row 2, got top=%d bar=%d", m.chatTop(), m.tabBarRows())
	}
	idx, row, ok := railRow(m, func(i int, _ chat.Checkpoint) bool { return i == 0 })
	if !ok {
		t.Fatalf("no marker for checkpoint 0:\n%s", screen(m))
	}
	// A click on the marker's screen row, two rows down, lands on it.
	{
		sx, sy := screenOf(m, 1, row)
		clickRail(m, sx, sy)
	}
	if tb.browse != idx {
		t.Errorf("click on the marker's screen row left browse=%d, want %d", tb.browse, idx)
	}
}
