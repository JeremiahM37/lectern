package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// stripLines returns each line with its ANSI styling removed.
func stripLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// descColumn is the display column where a command's description starts in
// a plain popup row, or -1 if the description is not in the row.
func descColumn(row, desc string) int {
	i := strings.Index(row, desc)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(row[:i])
}

// popupRows returns the command rows of the slash popup for the current
// input, without the frame's top and bottom borders.
func popupRows(t *testing.T, m *Model, w int) []string {
	t.Helper()
	lines := stripLines(m.cur().in.popupLines(m, w))
	if len(lines) < 3 {
		t.Fatalf("popup has %d lines, want a framed list", len(lines))
	}
	return lines[1 : len(lines)-1]
}

func TestSlashPaletteDescriptionsShareOneColumn(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	in := m.cur().in
	in.setValue("/")

	cmds := in.slashMatches()
	if len(cmds) < 4 {
		t.Fatalf("only %d commands match \"/\"; need several rows to test alignment", len(cmds))
	}
	const w = 80
	lines := stripLines(in.popupLines(m, w))
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("popup line %d is %d cells wide, want %d: %q", i, got, w, l)
		}
	}

	rows := popupRows(t, m, w)
	var cols []int
	for i, row := range rows {
		if i >= len(cmds) || i >= 8 {
			break // the "n/total" footer row has no command
		}
		c := descColumn(row, cmds[i].Desc)
		if c < 0 {
			t.Fatalf("row %d does not show its description %q (truncated too early?): %q", i, cmds[i].Desc, row)
		}
		cols = append(cols, c)
	}
	if len(cols) < 4 {
		t.Fatalf("checked %d rows, want at least 4", len(cols))
	}
	for i, c := range cols {
		if c != cols[0] {
			t.Fatalf("description %d starts at column %d, row 0 at %d: the column is not aligned", i, c, cols[0])
		}
	}
}

func TestSlashPaletteTruncatesLongDescriptionsWithEllipsis(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	in := m.cur().in
	in.setValue("/")
	const w = 40

	lines := stripLines(in.popupLines(m, w))
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("narrow popup line %d is %d cells wide, want %d: %q", i, got, w, l)
		}
	}
	rows := popupRows(t, m, w)
	cut := 0
	var cols []int
	for i, row := range rows {
		if strings.Contains(row, "…") {
			cut++
		}
		// the description column must still line up when cut short
		desc := ""
		if i < len(in.slashMatches()) {
			desc = in.slashMatches()[i].Desc
		}
		if c := descColumn(row, desc[:min(len(desc), 4)]); desc != "" && c >= 0 {
			cols = append(cols, c)
		}
	}
	if cut == 0 {
		t.Fatalf("no description was cut with an ellipsis at width %d:\n%s", w, strings.Join(rows, "\n"))
	}
	for _, c := range cols {
		if c != cols[0] {
			t.Fatalf("narrow descriptions are not aligned: columns %v", cols)
		}
	}
}

func TestSlashPaletteSelectedRowIsMarkedAndFullWidth(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	in := m.cur().in
	in.setValue("/")
	in.acSel = 2

	const w = 70
	rows := popupRows(t, m, w)
	marked := 0
	for i, row := range rows {
		if strings.Contains(row, "› /") {
			marked++
			if i != 2 {
				t.Fatalf("selection marker on row %d, want row 2", i)
			}
		}
		if got := ansi.StringWidth(row); got != w {
			t.Fatalf("row %d is %d cells wide, want %d", i, got, w)
		}
	}
	if marked != 1 {
		t.Fatalf("%d rows carry the selection marker, want 1", marked)
	}
}
