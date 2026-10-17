package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// An info view (where /storage prints) wraps a long path at a separator: every
// row but the last ends on one, so no word is split across two rows.
func TestInfoViewWrapsPathsAtSeparators(t *testing.T) {
	const path = "/tmp/claude-1000/-home-admin-projects-lectern/a05bd330-3cdc-49f8-94c-c07a611fc896/scratchpad"
	v := NewInfoView(theme.Default(), "Storage", "", []string{path})
	var rows []string
	for _, l := range strings.Split(ansi.Strip(v.View(40, 12)), "\n") {
		body := strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│"))
		if body != "" {
			rows = append(rows, body)
		}
	}
	if len(rows) < 3 {
		t.Fatalf("path should take several rows, got %q", rows)
	}
	if got := strings.Join(rows, ""); got != path {
		t.Fatalf("path not intact across rows: %q", rows)
	}
	// the path separator is the break; a row must not end inside a segment
	for _, r := range rows[:len(rows)-1] {
		if r[len(r)-1] != '/' {
			t.Errorf("row breaks inside a segment: %q (all rows %q)", r, rows)
		}
	}
}
