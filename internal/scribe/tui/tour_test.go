package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// tourHome points Lectern's home at a temp dir, so the tour never touches the
// real ~/.lectern, and returns it.
func tourHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SCRIBE_HOME", dir)
	return dir
}

// tourOpenModel is a landing-page model with the tour already open.
func tourOpenModel(t *testing.T) *Model {
	t.Helper()
	tourHome(t)
	m, _ := newTestModel(t, 100, 40)
	m.phase = phaseLanding
	m.maybeStartTour()
	if _, ok := m.topModal().(*tourModal); !ok {
		t.Fatal("the tour did not open on the first run")
	}
	return m
}

func tourPageOf(t *testing.T, m *Model) int {
	t.Helper()
	tm, ok := m.topModal().(*tourModal)
	if !ok {
		t.Fatal("the tour is not open")
	}
	return tm.page
}

func TestTourOpensOnFirstRun(t *testing.T) {
	home := tourHome(t)
	m, _ := newTestModel(t, 100, 40)
	m.phase = phaseLanding
	m.maybeStartTour()
	if _, ok := m.topModal().(*tourModal); !ok {
		t.Fatal("the tour should open on the first run")
	}
	b, err := os.ReadFile(filepath.Join(home, "tour.json"))
	if err != nil || !strings.Contains(string(b), `"tour_seen":true`) {
		t.Fatalf("tour_seen not recorded in the Lectern home: %q, %v", b, err)
	}
}

func TestTourStaysClosedOnceSeen(t *testing.T) {
	tourHome(t)
	if err := markTourSeen(); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestModel(t, 100, 40)
	m.phase = phaseLanding
	m.maybeStartTour()
	if m.topModal() != nil {
		t.Fatal("the tour opened again after tour_seen was set")
	}
}

func TestTourNotShownWithATranscript(t *testing.T) {
	tourHome(t)
	m, _ := newTestModel(t, 100, 40)
	m.phase = phaseChat
	m.maybeStartTour()
	if m.topModal() != nil || tourSeen() {
		t.Fatal("a session that is not on the landing page is not a first launch")
	}
}

func TestEscSkipsAndRecordsTourSeen(t *testing.T) {
	m := tourOpenModel(t)
	send(m, "esc")
	if m.topModal() != nil {
		t.Fatal("Esc should close the tour")
	}
	if !tourSeen() {
		t.Fatal("Esc should record tour_seen")
	}
	if m.cur().in.value() != "" {
		t.Fatalf("Esc should not type anything, input is %q", m.cur().in.value())
	}
}

// Any key that is not the tour's own closes it and reaches the input, so the
// first thing the person types is not held up by the tour.
func TestTypingClosesTourAndReachesInput(t *testing.T) {
	m := tourOpenModel(t)
	typeText(m, "hi")
	if m.topModal() != nil {
		t.Fatal("typing should close the tour")
	}
	if got := m.cur().in.value(); got != "hi" {
		t.Fatalf("input after typing over the tour is %q, want %q", got, "hi")
	}
}

func TestEnterPagesThenFinishesOnLastPage(t *testing.T) {
	m := tourOpenModel(t)
	for i := 0; i < len(tourPages)-1; i++ {
		send(m, "enter")
	}
	if got := tourPageOf(t, m); got != len(tourPages)-1 {
		t.Fatalf("Enter should reach the last page, got page %d", got)
	}
	send(m, "enter")
	if m.topModal() != nil {
		t.Fatal("Enter on the last page should finish the tour")
	}
}

func TestPagingWrapsAtBothEnds(t *testing.T) {
	m := tourOpenModel(t)
	send(m, "left")
	if got := tourPageOf(t, m); got != len(tourPages)-1 {
		t.Fatalf("left from the first page should wrap to the last, got %d", got)
	}
	send(m, "right")
	if got := tourPageOf(t, m); got != 0 {
		t.Fatalf("right from the last page should wrap to the first, got %d", got)
	}
	send(m, "right")
	send(m, "right")
	send(m, "left")
	if got := tourPageOf(t, m); got != 1 {
		t.Fatalf("right, right, left should land on page 2, got page %d", got+1)
	}
}

func TestTourHasElevenPagesThatFit(t *testing.T) {
	if len(tourPages) != 11 {
		t.Fatalf("the tour has %d pages, want 11", len(tourPages))
	}
	for i, p := range tourPages {
		for _, l := range p.lines {
			if w := ansi.StringWidth(l); w > tourLineWidth {
				t.Fatalf("page %d line %q is %d cells, the limit is %d", i+1, l, w, tourLineWidth)
			}
		}
	}
}

func TestTourViewShowsStepRail(t *testing.T) {
	m := tourOpenModel(t)
	tm := m.topModal().(*tourModal)
	out := ansi.Strip(tm.View(72, 12))
	if !strings.Contains(out, "Welcome to Lectern") || !strings.Contains(out, "1 of 11") {
		t.Fatalf("first page should show its heading and 1 of 11, got:\n%s", out)
	}
}

func TestSlashTourReopensTheTour(t *testing.T) {
	m := tourOpenModel(t)
	send(m, "esc")
	c, _ := findCommand("/tour")
	if c == nil {
		t.Fatal("/tour is not registered")
	}
	if group := commandGroupOf("tour"); group != "Help" {
		t.Fatalf("/tour is in group %q, want Help", group)
	}
	c.Run(m, "")
	if _, ok := m.topModal().(*tourModal); !ok {
		t.Fatal("/tour should reopen the tour after it was skipped")
	}
}

// commandGroupOf is the group a registered command was given. It reads the
// registry itself, not the palette, which lists only the groups in
// commandGroups.
func commandGroupOf(name string) string {
	for _, c := range commandTable {
		if c.Name == name {
			return c.Group
		}
	}
	return ""
}
