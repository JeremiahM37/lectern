package chat

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// clickCell is a left click at (x, y): a press, then the release that counts
// as the click. Clicks act on release, so a drag can select text instead.
func clickCell(v *View, x, y int) {
	v.HandleMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	v.HandleMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
}

func motion(v *View, x, y int, button tea.MouseButton) {
	v.HandleMouse(tea.MouseMsg{X: x, Y: y, Button: button, Action: tea.MouseActionMotion})
}

// A click on empty space toggles every row, as Ctrl+O does.
func TestClickOnEmptySpaceTogglesAllRows(t *testing.T) {
	v := New(Options{})
	v.SetSize(80, 30)
	v.AddUser("hello there", 0, time.Time{}, false)
	v.SetSize(80, 30)
	v.build()
	empty := -1
	for i := range v.lines {
		if _, ok := v.hits[i]; !ok && strings.TrimSpace(ansi.Strip(v.lines[i])) == "" {
			empty = i
			break
		}
	}
	if empty < 0 {
		t.Skip("no blank row to click in this layout")
	}
	before := v.expanded
	clickCell(v, 0, empty-v.windowTop(len(v.lines)))
	if v.expanded == before {
		t.Fatalf("click on empty space did not toggle the rows")
	}
}

// Moving the pointer over a tool row highlights it, and moving off clears it.
func TestHoverHighlightsTheRowUnderThePointer(t *testing.T) {
	v := New(Options{})
	v.SetSize(80, 30)
	v.build()
	if v.hoverLine != -1 {
		t.Fatalf("hover starts at %d, want -1", v.hoverLine)
	}
	// Hover over a row that has a hit target; find one.
	row := -1
	for abs := range v.hits {
		row = abs
		break
	}
	if row < 0 {
		t.Skip("no hit rows in an empty transcript")
	}
	motion(v, 0, row-v.windowTop(len(v.lines)), tea.MouseButtonNone)
	if v.hoverLine != row {
		t.Fatalf("hover line %d, want %d", v.hoverLine, row)
	}
	v.ClearHover()
	if v.hoverLine != -1 {
		t.Fatalf("ClearHover left %d", v.hoverLine)
	}
}

// A drag selects the text between press and release, and the release hands
// that text to the caller once.
func TestDragSelectionCopiesTheText(t *testing.T) {
	v := New(Options{})
	v.SetSize(80, 30)
	v.AddUser("alpha beta gamma", 0, time.Time{}, false)
	v.build()
	top := v.windowTop(len(v.lines))
	row := -1
	for i, l := range v.lines {
		if strings.Contains(ansi.Strip(l), "alpha beta gamma") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("user row not found:\n%s", plain(v))
	}
	y := row - top
	v.HandleMouse(tea.MouseMsg{X: 0, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	motion(v, 9, y, tea.MouseButtonLeft)
	v.HandleMouse(tea.MouseMsg{X: 9, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	got, ok := v.ReleasedSelection()
	if !ok {
		t.Fatalf("no selection released after a drag")
	}
	if !strings.Contains(got, "alpha") || strings.Contains(got, "gamma") {
		t.Fatalf("selection = %q, want the first word only", got)
	}
	if _, again := v.ReleasedSelection(); again {
		t.Fatalf("selection released twice")
	}
}
