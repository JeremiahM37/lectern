package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Wave 6 live QA: each test pins one thing a person saw on the screen.

// Enter on an MCP row used to do nothing, though the footer promised details.
func TestMCPEnterOpensServerDetails(t *testing.T) {
	m := modalModel(t, 140, 36, serversFixture())
	m.runCommand("/mcp")
	send(m, "enter")
	if got := topTitle(m); got != "MCP · github" {
		t.Fatalf("enter on an MCP server should open its details, top=%q", got)
	}
	mustContain(t, screen(m), "State", "ready", "Tools")
}

// The footer must keep "Esc close" at the narrow width, where the full list
// used to be cut off mid-word.
func TestMCPFooterKeepsCloseHintNarrow(t *testing.T) {
	m := modalModel(t, 80, 30, serversFixture())
	m.runCommand("/mcp")
	mustContain(t, screen(m), "Esc close")
	checkWidths(t, m, 80, 30)
}

func TestMCPFooterWideListsEveryKey(t *testing.T) {
	m := modalModel(t, 140, 36, serversFixture())
	m.runCommand("/mcp")
	mustContain(t, screen(m), "⏎ details", "r restart", "Esc close")
}

// The "Environment" label is the longest; its value must not run into it.
func TestMCPFormLabelsLeaveAGap(t *testing.T) {
	f := newMCPForm(theme.Default(), nil, func(MCPServerSpec, *MCPServerSpec) error { return nil })
	f.focus = mcpFEnv
	out := plain(strings.Join(f.rows(mcpFEnv, 60), "\n"))
	if strings.Contains(out, "EnvironmentKEY") || !strings.Contains(out, "Environment  ") {
		t.Fatalf("environment label should be spaced from its hint: %q", out)
	}
}

// The caret of an empty focused field sits where typing starts, not after the hint.
func TestMCPFormCaretBeforeEmptyHint(t *testing.T) {
	f := newMCPForm(theme.Default(), nil, func(MCPServerSpec, *MCPServerSpec) error { return nil })
	out := plain(strings.Join(f.rows(mcpFName, 60), "\n"))
	if !strings.Contains(out, "▌unique id") {
		t.Fatalf("caret should come before the placeholder: %q", out)
	}
}

// The commit footer's cancel hint must survive a narrow frame.
func TestCommitFooterKeepsCancelNarrow(t *testing.T) {
	c := &commitModal{}
	c.Size(80, 30)
	f := c.Footer()
	if !strings.Contains(f, "Esc cancel") {
		t.Fatalf("narrow commit footer lost the cancel hint: %q", f)
	}
	if w := ansi.StringWidth(f); w > 58 {
		t.Fatalf("narrow commit footer is %d cells, frame fits 58: %q", w, f)
	}
}

func TestCommitFooterWideShowsEveryHint(t *testing.T) {
	c := &commitModal{}
	c.Size(120, 36)
	f := c.Footer()
	for _, want := range []string{"Enter new line", "Tab body", "Ctrl+S commit", "Esc cancel"} {
		if !strings.Contains(f, want) {
			t.Fatalf("wide commit footer missing %q: %q", want, f)
		}
	}
}

// A memory with no timestamp must not read "— ago".
func TestMemUpdatedWordsUnknownAge(t *testing.T) {
	now := time.Now()
	if got := memUpdated(time.Time{}, now); got != "updated unknown" {
		t.Fatalf("unknown age = %q", got)
	}
	if got := memUpdated(now.Add(-3*time.Minute), now); got != "updated 3m ago" {
		t.Fatalf("known age = %q", got)
	}
}

// /preset lists all presets in one system row; a row each repeated the heading.
func TestPresetListIsOneRow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRIBE_HOME", dir)
	pdir := filepath.Join(dir, "presets")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"careful", "fast"} {
		if err := os.WriteFile(filepath.Join(pdir, name+".json"), []byte(`{"mode":"plan"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var rows []string
	listPresets(func(kind, text string) { rows = append(rows, text) }, t.TempDir())
	if len(rows) != 1 {
		t.Fatalf("want one row for the whole list, got %d: %q", len(rows), rows)
	}
	if !strings.Contains(rows[0], "careful") || !strings.Contains(rows[0], "fast") {
		t.Fatalf("list should name each preset: %q", rows[0])
	}
}

// The settings footer names a and d only on rows that take them. General
// rows take neither, and the Permissions rows take both.
func TestSettingsFooterFollowsRowKeys(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	s := openSettings(t, m)
	s.tab = stGeneral
	selectRow(t, s, "Mode")
	f := s.Footer()
	if strings.Contains(f, "a add") || strings.Contains(f, "d del") {
		t.Fatalf("general row should not advertise add/del: %q", f)
	}
	if !strings.HasSuffix(f, "Esc close") {
		t.Fatalf("footer should end with the close label: %q", f)
	}
	s.tab = stPermissions
	s.cur[s.tab] = 0
	if f := s.Footer(); !strings.Contains(f, "a add") && !strings.Contains(f, "d del") {
		t.Fatalf("permissions rows should advertise a key they take: %q", f)
	}
}
