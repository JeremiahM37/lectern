package console

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The Favorites tab is a filtered view of the Sessions pane, not a section of
// its own: every code path that assumes m.section == 0 for sessions keeps
// working, and favoritesOnly only narrows which session rows are listed.
// A star lives on the session row, so it survives the session ending and an
// ended favorite can be brought back from here.

// favoriteMsg is the answer to a star or unstar request.
type favoriteMsg struct {
	id       string
	favorite bool
	err      error
}

// favoritesRestoredMsg is the answer to re-tracking every restorable ended
// favorite before the Favorites tab opens them all.
type favoritesRestoredMsg struct {
	live     []string
	restored []string
	skipped  int
}

// favoriteMark is the ★ shown before a favorited session's name.
func favoriteMark(r row) string {
	if r["favorite"] == true {
		return "★ "
	}
	return ""
}

// rowLabel is the name a list row shows, with the favorite mark in front.
func (m *dashboard) rowLabel(r row) string { return favoriteMark(r) + m.rowTitle(r) }

// readyLive is a session that can be attached right now: its process is
// running and its workspace is not still being set up or has failed.
func readyLive(r row) bool {
	return r["ended_at"] == nil && r["setup_state"] != "creating" && r["setup_state"] != "failed"
}

func countFavorites(rows []row) int {
	n := 0
	for _, r := range rows {
		if r["favorite"] == true {
			n++
		}
	}
	return n
}

// toggleFavorite stars or unstars the selected session. A star is allowed on
// ended sessions too: that is how an ended session gets kept for later.
func (m *dashboard) toggleFavorite() tea.Cmd {
	if m.section != 0 || m.selectedGroup() != nil {
		return nil
	}
	r := m.current()
	if r == nil {
		return nil
	}
	want := r["favorite"] != true
	c, sid := m.client, id(r)
	return func() tea.Msg {
		_, err := c.JSON("PUT", "/sessions/"+sid+"/favorite", map[string]any{"favorite": want})
		return favoriteMsg{id: sid, favorite: want, err: err}
	}
}

func (m *dashboard) receiveFavorite(v favoriteMsg) {
	if v.err != nil {
		m.notice = "Favorite not saved: " + clean(v.err.Error())
		return
	}
	for _, r := range m.rows {
		if id(r) != v.id {
			continue
		}
		if v.favorite {
			r["favorite"] = true
			r["favorite_at"] = float64(time.Now().Unix())
		} else {
			r["favorite"] = false
			r["favorite_at"] = nil
		}
	}
	m.favoriteCount = countFavorites(m.rows)
	// Unstarring in the Favorites tab removes the row from view right away.
	m.filter()
	if v.favorite {
		m.notice = "★ Added to favorites"
	} else {
		m.notice = "Removed from favorites"
	}
}

// enterFavorites opens the Favorites tab with batch mode on and every live,
// ready favorite selected, so Enter opens them all at once.
func (m *dashboard) enterFavorites() tea.Cmd {
	if m.controlOnly {
		return m.nativeDisabled()
	}
	cmd := m.openSection(0, true)
	m.notice = "★ Favorites · Enter opens them all · Space unselects · 1 goes back"
	return cmd
}

// preselectFavorites marks the live, ready favorites once their rows arrive.
func (m *dashboard) preselectFavorites() {
	m.batchSelected = map[string]bool{}
	for _, r := range m.rows {
		if r["favorite"] == true && readyLive(r) {
			m.batchSelected[id(r)] = true
		}
	}
}

// openFavorites is Enter in the Favorites tab. Ended favorites that can be
// tracked again are restored first (the same call r makes), then every
// selected live session and every restored one opens together.
func (m *dashboard) openFavorites() tea.Cmd {
	if m.busy || m.controlOnly {
		return nil
	}
	if m.openBatch == nil {
		m.notice = "Terminal launcher unavailable"
		return m.attachSelected(false)
	}
	var restorable []string
	skipped := 0
	for _, r := range m.rows {
		if r["favorite"] != true || r["ended_at"] == nil {
			continue
		}
		if r["can_restore"] == true && r["archived_at"] == nil {
			restorable = append(restorable, id(r))
		} else {
			skipped++
		}
	}
	live := m.batchReadyIDs()
	if len(restorable) == 0 {
		m.reportSkipped(skipped)
		return m.openBatchIDs(live)
	}
	c := m.client
	m.busy = true
	m.notice = fmt.Sprintf("Tracking %d ended favorite(s) again", len(restorable))
	return func() tea.Msg {
		msg := favoritesRestoredMsg{live: live, skipped: skipped}
		for _, sid := range restorable {
			if _, err := c.JSON("POST", "/sessions/"+sid+"/restore", map[string]any{}); err != nil {
				msg.skipped++
				continue
			}
			msg.restored = append(msg.restored, sid)
		}
		return msg
	}
}

func (m *dashboard) receiveFavoritesRestored(v favoritesRestoredMsg) tea.Cmd {
	m.busy = false
	m.reportSkipped(v.skipped)
	ids := append(append([]string{}, v.live...), v.restored...)
	if len(ids) == 0 {
		m.notice = "No favorites are ready to open"
		return nil
	}
	return m.openBatchIDs(ids)
}

// reportSkipped says how many ended favorites were left out. The batch open
// that follows replaces the notice, so the note is kept for it to add back.
func (m *dashboard) reportSkipped(n int) {
	if n == 0 {
		return
	}
	m.favoriteSkipNote = fmt.Sprintf("%d ended favorite(s) cannot be restored; r opens the Restore list", n)
	m.notice = m.favoriteSkipNote
}

// favoritesTab is the label of the Favorites tab, shown on every pane.
func favoritesTab(n int) string { return fmt.Sprintf("0 ★ Favorites (%d)", n) }
