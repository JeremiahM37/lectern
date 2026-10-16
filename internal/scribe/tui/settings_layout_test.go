package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// valueCol returns the screen column where the value of row starts, found by
// the value's text in the stripped line. It fails if the value is not there.
func valueCol(t *testing.T, line, value string) int {
	t.Helper()
	i := strings.Index(line, value)
	if i < 0 {
		t.Fatalf("value %q not in line %q", value, line)
	}
	return ansi.StringWidth(line[:i])
}

// layoutSettings is a settings screen with a real theme, for drawing rows.
func layoutSettings(t *testing.T) *settings {
	t.Helper()
	m, _ := newTestModel(t, 100, 40)
	return &settings{m: m}
}

func TestSettingsLayoutValuesShareOneColumn(t *testing.T) {
	rows := []setRow{
		{label: "Default model", value: "anthropic/claude-sonnet-5", note: "picker"},
		{label: "Mode", value: "default", note: "how tools are approved"},
		{label: "Max tokens per response", value: "default", note: "0 = model default"},
		{label: "Auto-compact", value: "on", note: "summarise when context fills"},
		{label: "Chat style", value: "accent", note: "accent or bubble"},
	}
	for _, w := range []int{76, 84} {
		s := layoutSettings(t)
		cols := layoutCols(rows, w)
		want := -1
		for _, r := range rows {
			line := ansi.Strip(s.rowLine(r, false, cols))
			got := valueCol(t, line, r.value)
			if want < 0 {
				want = got
			}
			if got != want {
				t.Fatalf("width %d: value %q starts at column %d, want %d\n%s", w, r.value, got, want, line)
			}
			if ansi.StringWidth(line) > w {
				t.Fatalf("width %d: row is %d cells wide: %q", w, ansi.StringWidth(line), line)
			}
		}
	}
}

func TestSettingsLayoutHintNeverTouchesValue(t *testing.T) {
	rows := []setRow{
		{label: "Default model", value: "anthropic/claude-sonnet-5", note: "picker"},
		{label: "Concurrent agents", value: "3", note: "2 to 8 agents"},
	}
	for _, w := range []int{80, 120} {
		s := layoutSettings(t)
		cols := layoutCols(rows, w-4)
		for _, r := range rows {
			line := ansi.Strip(s.rowLine(r, false, cols))
			valEnd := valueCol(t, line, r.value) + ansi.StringWidth(r.value)
			hintStart := strings.Index(line, r.note)
			if hintStart < 0 {
				t.Fatalf("hint %q missing at width %d: %q", r.note, w, line)
			}
			if gap := ansi.StringWidth(line[:hintStart]) - valEnd; gap < colGap {
				t.Fatalf("hint %q touches value %q (gap %d) at width %d: %q", r.note, r.value, gap, w, line)
			}
		}
	}
}

func TestSettingsLayoutTruncatesHintWithEllipsis(t *testing.T) {
	long := "this hint is much too long to fit in a narrow settings modal and must be cut"
	rows := []setRow{{label: "Verbose tool rows", value: "off", note: long}}
	s := layoutSettings(t)
	cols := layoutCols(rows, 60)
	line := ansi.Strip(s.rowLine(rows[0], true, cols))
	if ansi.StringWidth(line) > 60 {
		t.Fatalf("line is %d cells, want at most 60: %q", ansi.StringWidth(line), line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "…") {
		t.Fatalf("a cut hint should end with …: %q", line)
	}
	if strings.Contains(line, long) {
		t.Fatal("the full hint should not fit at 60 columns")
	}
}

func TestSettingsLayoutDropsHintWhenNoRoom(t *testing.T) {
	rows := []setRow{{label: "Default model", value: "anthropic/claude-sonnet-5", note: "picker"}}
	cols := layoutCols(rows, 22)
	if cols.hint != 0 {
		t.Fatalf("a 22-column modal has no room for a hint, got %+v", cols)
	}
	s := layoutSettings(t)
	line := ansi.Strip(s.rowLine(rows[0], false, cols))
	if ansi.StringWidth(line) > 22 {
		t.Fatalf("line is %d cells, want at most 22: %q", ansi.StringWidth(line), line)
	}
	if strings.Contains(line, "picker") {
		t.Fatalf("the dropped hint must not appear: %q", line)
	}
}

// The real General tab at 80 and 120 columns: every value sits in one column
// and no line is wider than the modal.
func TestSettingsGeneralTabFitsAtTwoWidths(t *testing.T) {
	for _, cols := range []int{80, 120} {
		m, _ := newTestModel(t, cols, 40)
		s := openSettings(t, m)
		s.tab = stGeneral
		size := min(84, cols-4)
		view := ansi.Strip(s.View(size, 30))
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size {
				t.Fatalf("at %d cols a line is %d cells, modal is %d: %q", cols, ansi.StringWidth(line), size, line)
			}
		}
		for _, want := range []string{"Default model", "Max tokens per response"} {
			if !strings.Contains(view, want) {
				t.Fatalf("at %d cols the General tab should show %q:\n%s", cols, want, view)
			}
		}
	}
}
