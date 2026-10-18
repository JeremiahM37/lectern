package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// The landing tip is fitted to the landing width. At 120 columns every tip
// shows whole; at 80 columns a tip is cut at a word boundary with an ellipsis.
func TestW29LandingTipFitsTheLandingWidth(t *testing.T) {
	longest := 0
	for _, tip := range tips {
		longest = max(longest, ansi.StringWidth(tip))
	}
	if longest <= 68 {
		t.Fatalf("the longest tip is %d cells; the test needs one wider than the old 68-cell cap", longest)
	}
	for _, tip := range tips {
		got := landingTipText(tip, 120)
		if got != "✦ "+tip {
			t.Fatalf("120 cols: tip cut to %q", got)
		}
	}
	for _, tip := range tips {
		got := landingTipText(tip, 80)
		if ansi.StringWidth(got) > 70+2 {
			t.Fatalf("80 cols: %q is %d cells, the landing room is 70", got, ansi.StringWidth(got))
		}
		if got == "✦ "+tip {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(got, "✦ "), "…")
		if !strings.HasSuffix(got, "…") || !strings.HasPrefix(tip, stem) || len(tip) <= len(stem) ||
			!strings.ContainsRune(" -_/.+,;:()", rune(tip[len(stem)])) {
			t.Fatalf("80 cols: cut %q is not on a word boundary of %q", got, tip)
		}
	}
}

// The health popup counts every warn row it has. The summary stays on the first
// line while the rows scroll, so the count can be checked against the rows.
var w29ProbeLine = regexp.MustCompile(`^ {4}! `)
var w29SummaryWarn = regexp.MustCompile(`(\d+) warn`)

func w29HealthBackends() []HealthBackend {
	return []HealthBackend{
		{Name: "Model providers", Probes: []HealthProbe{
			{Name: "ollama", State: "pass", Reason: "answering on its address", Ms: 12},
			{Name: "anthropic", State: "warn", Reason: "no API key set", Fix: "store a key", Ms: -1},
		}},
		{Name: "Model routing", Probes: []HealthProbe{{Name: "main", State: "pass", Reason: "ok", Ms: -1}}},
		{Name: "Parsers", Probes: []HealthProbe{{Name: "go", State: "pass", Reason: "ok", Ms: -1}, {Name: "ts", State: "pass", Reason: "ok", Ms: -1}}},
		{Name: "Semantic index", Probes: []HealthProbe{{Name: "index", State: "info", Reason: "off", Ms: -1}}},
		{Name: "Language servers", Probes: []HealthProbe{{Name: "gopls", State: "pass", Reason: "ok", Ms: 4}}},
		{Name: "MCP servers", Probes: []HealthProbe{{Name: "none", State: "info", Reason: "no probes ran", Ms: -1}}},
		{Name: "Git", Probes: []HealthProbe{{Name: "repo", State: "pass", Reason: "clean", Ms: -1}}},
		{Name: "Disk", Probes: []HealthProbe{{Name: "fs", State: "pass", Reason: "40 GB", Ms: -1}}},
		{Name: "Scribe home", Probes: []HealthProbe{{Name: "home", State: "pass", Reason: "ok", Ms: -1}}},
		{Name: "Hooks", Probes: []HealthProbe{{Name: "hooks", State: "pass", Reason: "ok", Ms: -1}}},
	}
}

func TestW29HealthSummaryMatchesTheRowsShown(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: w29HealthBackends()}
	// Tall enough that every row is on screen at once.
	lines := strings.Split(ansi.Strip(p.View(76, 200)), "\n")
	m := w29SummaryWarn.FindStringSubmatch(lines[0])
	if m == nil || m[1] != "1" {
		t.Fatalf("summary = %q, want 1 warn", lines[0])
	}
	rows := 0
	for _, l := range lines {
		if w29ProbeLine.MatchString(l) {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("summary counts 1 warn but %d warn rows are drawn", rows)
	}
}

func TestW29HealthWarnRowIsReachableAndSummaryStaysPinned(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: w29HealthBackends()}
	_, h := p.Size(80, 30)
	found := false
	for off := 0; off < 80; off++ {
		p.off = off
		lines := strings.Split(ansi.Strip(p.View(76, h)), "\n")
		if len(lines) != h {
			t.Fatalf("offset %d: %d lines, want %d", off, len(lines), h)
		}
		if w29SummaryWarn.FindStringSubmatch(lines[0]) == nil {
			t.Fatalf("offset %d: the summary scrolled away: %q", off, lines[0])
		}
		for _, l := range lines {
			if strings.Contains(l, "no API key set") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the warn row never scrolls into view")
	}
}

// The git push failure reads as sentences in its alert. The " · " joins from
// the flash line are gone, and each command sits on a line of its own.
func TestW29GitPushAlertBodyIsPlainSentences(t *testing.T) {
	flash := "Push failed: fatal: No configured push destination. · Either specify the URL from the · " +
		"command-line or configure a remote repository using · git remote add <name> <url> · " +
		"and then push using the remote name · git push <name>"
	want := "Push failed: fatal: No configured push destination. Either specify the URL from the " +
		"command-line or configure a remote repository using\n  git remote add <name> <url>\n" +
		"and then push using the remote name\n  git push <name>"
	if got := alertBody(flash); got != want {
		t.Fatalf("alert body =\n%s\nwant\n%s", got, want)
	}
}

// A flash line with no joins is the alert body unchanged, so the other failure
// alerts keep their text.
func TestW29AlertBodyLeavesPlainTextAlone(t *testing.T) {
	for _, s := range []string{"Commit failed: nothing to commit", "Pull failed: no upstream branch"} {
		if got := alertBody(s); got != s {
			t.Fatalf("alertBody(%q) = %q", s, got)
		}
	}
}

// The alert that opens carries the plain body, and a repeat is still not added.
func TestW29RaiseFlashAlertShowsPlainBodyOnce(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	flash := "Push failed: fatal: No configured push destination. · Either specify the URL from the · git push <name>"
	m.raiseFlashAlert("Command failed", flash)
	m.raiseFlashAlert("Command failed", flash)
	var alerts []*Alert
	for _, md := range m.modals {
		if a, ok := md.(*Alert); ok {
			alerts = append(alerts, a)
		}
	}
	if len(alerts) != 1 {
		t.Fatalf("%d alerts, want 1", len(alerts))
	}
	if strings.Contains(alerts[0].message, " · ") {
		t.Fatalf("alert body still has the flash joins: %q", alerts[0].message)
	}
}
