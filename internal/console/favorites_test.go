package console

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// favoriteRows answers a session list with a live favorite, a live plain
// session, an ended favorite that can be tracked again, and an ended favorite
// that cannot.
func favoriteRows() []row {
	return []row{
		{"id": float64(1), "name": "Alpha UI", "project_name": "Website", "target_name": "Laptop", "status": "running", "favorite": true},
		{"id": float64(2), "name": "API repair", "project_name": "Backend", "target_name": "Server", "status": "waiting"},
		{"id": float64(3), "name": "Old docs", "project_name": "Website", "target_name": "Laptop", "status": "idle", "favorite": true, "ended_at": float64(10), "can_restore": true},
		{"id": float64(4), "name": "Gone", "project_name": "Backend", "target_name": "Server", "status": "idle", "favorite": true, "ended_at": float64(10)},
	}
}

func rowsJSON(t *testing.T, rows []row) string {
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func favoriteDashboard(t *testing.T, srv string) *dashboard {
	m := sampleDashboard()
	m.client = New(srv, "")
	m.rows = favoriteRows()
	m.filter()
	return m
}

// A star is a PUT on the session, and the row shows the mark at once.
func TestStarKeyTogglesFavoriteAndCallsTheAPI(t *testing.T) {
	srv, rec := recordingServer(t, 200, `{}`)
	m := favoriteDashboard(t, srv.URL)
	selectID(m, "2")
	_, cmd := m.Update(key("*"))
	if cmd == nil {
		t.Fatal("star did nothing")
	}
	m.Update(cmd())
	if len(rec.requests) != 1 || rec.requests[0] != "PUT /api/sessions/2/favorite" {
		t.Fatalf("star requests: %v", rec.requests)
	}
	if rec.bodies[0]["favorite"] != true {
		t.Fatalf("star body: %v", rec.bodies[0])
	}
	if m.notice != "★ Added to favorites" {
		t.Fatalf("star notice: %q", m.notice)
	}
	if !strings.Contains(ansi.Strip(m.View()), "★ API repair") {
		t.Fatalf("starred row has no mark:\n%s", ansi.Strip(m.View()))
	}
	// Pressing again on the same row unstars it.
	selectID(m, "2")
	_, cmd = m.Update(key("*"))
	m.Update(cmd())
	if rec.bodies[1]["favorite"] != false || m.notice != "Removed from favorites" {
		t.Fatalf("unstar: body=%v notice=%q", rec.bodies[1], m.notice)
	}
}

// Five opens the Favorites tab with ended favorites listed and the live ready
// ones pre-selected, so Enter has something to open.
func TestFavoritesTabShowsOnlyStarredAndPreselectsLiveOnes(t *testing.T) {
	srv, _ := recordingServer(t, 200, rowsJSON(t, favoriteRows()))
	m := favoriteDashboard(t, srv.URL)
	_, cmd := m.Update(key("0"))
	run(m, cmd)
	if !m.favoritesOnly || !m.batchOpen {
		t.Fatalf("0 did not open favorites: favoritesOnly=%v batchOpen=%v", m.favoritesOnly, m.batchOpen)
	}
	if len(m.visible) != 3 {
		t.Fatalf("favorites view shows %d rows, want the 3 starred sessions", len(m.visible))
	}
	if !m.batchSelected["1"] || m.batchSelected["3"] || m.batchSelected["4"] || len(m.batchSelected) != 1 {
		t.Fatalf("pre-selection: %v", m.batchSelected)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"0 ★ Favorites (3)", "Old docs", "Enter open all"} {
		if !strings.Contains(view, want) {
			t.Fatalf("favorites view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "API repair") {
		t.Fatalf("a plain session leaked into favorites:\n%s", view)
	}
}

// Enter re-tracks each restorable ended favorite (the same call r makes),
// then opens them with the live ones. Ones that cannot be restored are
// skipped and the notice says so.
func TestFavoritesEnterRestoresEndedThenOpensAll(t *testing.T) {
	srv, rec := recordingServer(t, 200, rowsJSON(t, favoriteRows()))
	m := favoriteDashboard(t, srv.URL)
	var opened []string
	m.openBatch = func(ids []string) error { opened = append(opened, ids...); return nil }
	_, cmd := m.Update(key("0"))
	run(m, cmd)

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in favorites did nothing")
	}
	restored := cmd()
	_, next := m.Update(restored)
	if next == nil {
		t.Fatalf("restore did not chain to opening: %+v", restored)
	}
	m.Update(next())

	posts := 0
	for _, r := range rec.requests {
		if r == "POST /api/sessions/3/restore" {
			posts++
		}
		if r == "POST /api/sessions/4/restore" {
			t.Fatal("an unrestorable favorite was re-tracked")
		}
	}
	if posts != 1 {
		t.Fatalf("restore calls: %v", rec.requests)
	}
	if len(opened) != 2 || opened[0] != "1" || opened[1] != "3" {
		t.Fatalf("opened %v, want live 1 then restored 3", opened)
	}
	if !strings.Contains(m.notice, "Opened 2 terminals") || !strings.Contains(m.notice, "1 ended favorite(s) cannot be restored; r opens the Restore list") {
		t.Fatalf("notice: %q", m.notice)
	}
}

// 1 goes back to all sessions and leaves batch mode it turned on for favorites.
func TestOneKeyLeavesFavoritesTab(t *testing.T) {
	srv, _ := recordingServer(t, 200, rowsJSON(t, favoriteRows()))
	m := favoriteDashboard(t, srv.URL)
	_, cmd := m.Update(key("0"))
	run(m, cmd)
	_, cmd = m.Update(key("1"))
	run(m, cmd)
	if m.favoritesOnly || m.batchOpen || len(m.batchSelected) != 0 {
		t.Fatalf("1 did not leave favorites: favoritesOnly=%v batchOpen=%v selected=%v", m.favoritesOnly, m.batchOpen, m.batchSelected)
	}
	if len(m.visible) != 4 {
		t.Fatalf("all sessions not back: %d rows", len(m.visible))
	}
}
