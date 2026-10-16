package tui

import (
	"strings"
	"testing"
	"time"
)

func memFixture() *recBackend {
	b := newRecBackend()
	b.mems = []MemoryItem{
		{ID: "p1", Scope: "project", Category: "convention", Title: "Tests live next to the code", Body: "Keep _test.go files beside the package.\nRun go test -race.", Pinned: true, Updated: stamp},
		{ID: "p2", Scope: "project", Category: "decision", Title: "Use bubbletea for the TUI", Body: "Chosen over tview.", Updated: time.Now().Add(-2 * time.Minute)},
		{ID: "g1", Scope: "global", Category: "preference", Title: "Prefer short answers", Body: "Calm, specific, no filler.", Updated: time.Now().Add(-40 * 24 * time.Hour)},
	}
	return b
}

func openMemory(t *testing.T, w, h int, b *recBackend) *Model {
	t.Helper()
	m := modalModel(t, w, h, b)
	m.runCommand("/memory")
	if topTitle(m) != "Memory" {
		t.Fatalf("/memory should open the memory browser, top=%q", topTitle(m))
	}
	return m
}

func TestMemoryBrowserShowsProjectThenGlobal(t *testing.T) {
	m := openMemory(t, 140, 36, memFixture())
	s := screen(m)
	mustContain(t, s, "Project 2", "Global 1", "Tests live next to the code", "Use bubbletea", "[convention]", "★")
	mustNotContain(t, s, "Prefer short answers")

	send(m, "tab")
	s = screen(m)
	mustContain(t, s, "Prefer short answers", "Global 1")
	mustNotContain(t, s, "Tests live next to the code")
	mustContain(t, s, "Tab switches")
}

func TestMemorySearchFiltersAndEscClears(t *testing.T) {
	m := openMemory(t, 140, 36, memFixture())
	send(m, "/")
	typeText(m, "bubble")
	s := screen(m)
	mustContain(t, s, "Use bubbletea", "1 / 2")
	mustNotContain(t, s, "Tests live next to the code")

	typeText(m, "zzz")
	mustContain(t, screen(m), "No memories match")

	send(m, "esc") // clears the query, keeps the browser open
	if topTitle(m) != "Memory" {
		t.Fatalf("first esc should clear the search, not close")
	}
	mustContain(t, screen(m), "Tests live next to the code")
	send(m, "esc")
	if m.topModal() != nil {
		t.Fatalf("second esc should close the browser")
	}
}

func TestMemoryPinTogglePersists(t *testing.T) {
	b := memFixture()
	m := openMemory(t, 140, 36, b)
	send(m, "j") // second visible: "Use bubbletea" (pinned first, so the pinned one is at 0)
	send(m, "p")
	if len(b.saved) != 1 || b.saved[0].ID != "p2" || !b.saved[0].Pinned {
		t.Fatalf("p should pin the selected memory, saved=%+v", b.saved)
	}
	mustContain(t, screen(m), "Pinned Use bubbletea")
}

func TestMemoryDeleteNeedsConfirm(t *testing.T) {
	b := memFixture()
	m := openMemory(t, 140, 36, b)
	send(m, "d")
	if topTitle(m) != "Delete memory" {
		t.Fatalf("d should ask for confirmation, top=%q", topTitle(m))
	}
	send(m, "n")
	if len(b.deleted) != 0 {
		t.Fatalf("answering no must not delete")
	}
	send(m, "d")
	send(m, "y")
	if len(b.deleted) != 1 || b.deleted[0] != "p1" {
		t.Fatalf("confirmed delete should remove p1, deleted=%v", b.deleted)
	}
	mustContain(t, screen(m), "Project 1")
}

func TestMemoryEditUpdatesTitleAndKeepsOtherFields(t *testing.T) {
	b := memFixture()
	m := openMemory(t, 140, 36, b)
	send(m, "enter") // edit the pinned first item
	if topTitle(m) != "Edit memory" {
		t.Fatalf("enter should open the editor, top=%q", topTitle(m))
	}
	send(m, "ctrl+u") // clears the title (focus starts on it)
	typeText(m, "Tests sit beside code")
	send(m, "ctrl+s")
	if len(b.saved) != 1 {
		t.Fatalf("ctrl+s should save once, saved=%d", len(b.saved))
	}
	got := b.saved[0]
	if got.ID != "p1" || got.Title != "Tests sit beside code" || got.Category != "convention" ||
		!strings.Contains(got.Body, "Keep _test.go") || !got.Pinned {
		t.Fatalf("unexpected saved memory: %+v", got)
	}
	if topTitle(m) != "Memory" {
		t.Fatalf("saving should return to the browser, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Tests sit beside code")
}

func TestMemoryEditorRequiresTitle(t *testing.T) {
	b := memFixture()
	m := openMemory(t, 140, 36, b)
	send(m, "n")
	send(m, "ctrl+s")
	if len(b.saved) != 0 {
		t.Fatalf("an empty title must not save")
	}
	mustContain(t, screen(m), "A title is needed.")
}

func TestMemoryNewMemoryInCurrentTab(t *testing.T) {
	b := memFixture()
	m := openMemory(t, 140, 36, b)
	send(m, "tab") // global tab
	send(m, "n")
	typeText(m, "Remember the release day")
	send(m, "tab") // to category
	send(m, "right")
	send(m, "right") // fact -> decision -> convention
	send(m, "ctrl+s")
	if len(b.saved) != 1 {
		t.Fatalf("expected one save, got %d", len(b.saved))
	}
	got := b.saved[0]
	if got.ID != "" || got.Scope != "global" || got.Category != "convention" || got.Title != "Remember the release day" {
		t.Fatalf("unexpected new memory: %+v", got)
	}
}

func TestMemoryEditorBodyIsMultiline(t *testing.T) {
	b := memFixture()
	m := openMemory(t, 140, 36, b)
	send(m, "n")
	typeText(m, "Two lines")
	send(m, "tab")
	send(m, "tab") // body
	typeText(m, "first")
	send(m, "enter")
	typeText(m, "second")
	send(m, "ctrl+s")
	if got := b.saved[0].Body; got != "first\nsecond" {
		t.Fatalf("body should keep its line break, got %q", got)
	}
}

func TestMemoryEmptyStates(t *testing.T) {
	m := openMemory(t, 100, 30, newRecBackend())
	mustContain(t, screen(m), "No project memories yet", "one.")
	send(m, "tab")
	mustContain(t, screen(m), "No global memories yet")
}

func TestMemoryPreviewBesideListWhenWide(t *testing.T) {
	m := openMemory(t, 140, 36, memFixture())
	mustContain(t, screen(m), "Keep _test.go files beside the package.")
	m2 := openMemory(t, 80, 36, memFixture())
	mustContain(t, screen(m2), "Keep _test.go files beside the package.")
}

func TestMemoryWidthsAt80And140(t *testing.T) {
	for _, w := range []int{80, 140} {
		m := openMemory(t, w, 30, memFixture())
		checkWidths(t, m, w, 30)
		send(m, "enter")
		checkWidths(t, m, w, 30)
		send(m, "esc")
		send(m, "d")
		checkWidths(t, m, w, 30)
	}
}
