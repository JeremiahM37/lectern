package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// updateTopLevel is the key map of a pane with nothing open over it
// (docs/design/simple-tui.md). Old keys that do not clash with a new meaning
// are kept as silent aliases for this release.
func (m *dashboard) updateTopLevel(v tea.KeyMsg) tea.Cmd {
	section := sections[m.section]
	k := v.String()
	switch k {
	case "y", "a", "n":
		if cmd, handled := m.approvalKey(k); handled {
			return cmd
		}
	}
	switch k {
	case "q":
		if m.popup {
			return tea.Quit
		}
		if m.previewFocus && m.width < 100 {
			// A narrow terminal shows the preview instead of the list, so
			// it is a sub-view there: q steps back to the list first.
			m.leaveDetail()
			return nil
		}
		return tea.Quit
	case "?":
		m.openHelp()
	case ":", "ctrl+k":
		m.openPalette("")
	case "m":
		m.openMenu()
	case "/":
		m.searching = true
		return m.query.Focus()
	case "esc":
		if m.popup && m.detailKey == "" {
			return tea.Quit
		}
		if m.batchOpen {
			return m.toggleBatchMode()
		}
		m.query.SetValue("")
		m.leaveDetail()
		m.filter()
	case "ctrl+r":
		m.notice = "Refreshed"
		return tea.Batch(m.refresh(), m.pollApprovals())
	case "r":
		if section != "sessions" {
			// Refresh keeps its old key where restore means nothing.
			return tea.Batch(m.refresh(), m.pollApprovals())
		}
		return m.restoreKey()
	case "R":
		// The old revive key keeps its old, narrower meaning.
		if r := m.current(); section == "sessions" && r != nil && m.selectedGroup() == nil {
			if !agentExited(r) {
				m.notice = "R starts an agent that exited again; this one is still running"
				return nil
			}
			return m.choose(reviveAction(r))
		}
	case "C":
		return m.loadRecentSessions()
	case "x", "d", "delete":
		return m.endSelected()
	case "1", "2", "3", "4", "5", "6":
		n := int(k[0] - '1')
		if k == "5" {
			n = 5 // Machines, where 5 always led.
		}
		if k == "6" {
			n = 1 // Approvals, where 6 always led.
		}
		return m.switchSection(n)
	case "tab", "right":
		return m.switchSection(m.nextPane(1))
	case "shift+tab", "left":
		return m.switchSection(m.nextPane(-1))
	case "p":
		m.detailKey = ""
		m.previewFocus = !m.previewFocus
		m.updatePreview()
	case "up", "k":
		if m.previewFocus {
			m.preview.LineUp(1)
		} else {
			m.selected--
			m.ensureSelection()
			m.updatePreview()
		}
	case "down", "j":
		if m.previewFocus {
			m.preview.LineDown(1)
		} else {
			m.selected++
			m.ensureSelection()
			m.updatePreview()
		}
	case "pgup", "ctrl+u":
		m.preview.HalfPageUp()
	case "pgdown", "ctrl+d":
		m.preview.HalfPageDown()
	case "home":
		if m.previewFocus {
			m.preview.GotoTop()
		} else {
			m.selected = 0
			m.ensureSelection()
			m.updatePreview()
		}
	case "end":
		if m.previewFocus {
			m.preview.GotoBottom()
		} else {
			m.selected = len(m.visible) - 1
			m.ensureSelection()
			m.updatePreview()
		}
	case "g":
		m.cycleGrouping()
	case " ":
		if m.batchOpen && m.section == 0 && m.selectedGroup() == nil {
			m.toggleBatchSelection()
		} else {
			m.toggleGroup()
		}
	case "[":
		m.collapseGroup()
	case "]":
		m.expandGroup()
	case "w":
		m.attention = !m.attention
		m.filter()
	case "A":
		m.archived = !m.archived
		return m.refresh()
	case "z":
		m.archived = false
		m.ended = !m.ended
		return m.refresh()
	case "o":
		return m.attachSelectedTo(false, true)
	case "b":
		return m.toggleBatchMode()
	case "enter", "a":
		if section == "approvals" {
			if k == "enter" {
				return m.attachApprovalSession()
			}
			return nil
		}
		if m.batchOpen && m.section == 0 {
			return m.openSelectedBatch()
		}
		if m.selectedGroup() != nil {
			m.toggleGroup()
			return nil
		}
		if m.controlOnly {
			return m.nativeDisabled()
		}
		return m.attachSelected(false)
	case "s":
		if m.controlOnly {
			return m.nativeDisabled()
		}
		return m.attachSelected(true)
	case "n":
		return m.newForm()
	case "e":
		return m.renameForm()
	case "h":
		return m.readDetail("History")
	case "v":
		return m.readDetail("Diff")
	case "u":
		return m.uploadForm()
	case "f":
		return m.discover()
	// Silent aliases: the palette lists all of these by name.
	case "G":
		return m.groupForm()
	case "F":
		return m.nativeSearchForm()
	case "H":
		return m.savedConversations()
	case "U":
		return m.undoLastClose()
	case "O":
		return m.olderNative()
	case "P":
		return m.manageProfilesForm()
	case "Q":
		return m.manageAgentsForm()
	case "S":
		if m.controlOnly {
			return m.nativeDisabled()
		}
		return m.newShellForm()
	case "7":
		return m.settingsForm()
	case "8":
		return m.readResource("Usage", "/stats")
	case "9":
		return m.apiForm()
	}
	return nil
}

func (m *dashboard) leaveDetail() {
	m.detailKey = ""
	m.detail = ""
	m.detailTitle = ""
	m.previewFocus = false
	m.updatePreview()
}

// nextPane cycles through the numbered panes; from Routines or Machines it
// returns to the numbered ones.
func (m *dashboard) nextPane(delta int) int {
	if m.section >= numberedPanes {
		if delta > 0 {
			return 0
		}
		return numberedPanes - 1
	}
	return (m.section + delta + numberedPanes) % numberedPanes
}

func (m *dashboard) cycleGrouping() {
	modes := 3
	if m.section == 0 {
		modes = 4
	}
	m.grouping = (m.grouping + 1) % modes
	m.filter()
	m.savePreferences()
}

func (m *dashboard) toggleBatchMode() tea.Cmd {
	if m.controlOnly {
		return m.nativeDisabled()
	}
	if m.section != 0 {
		m.notice = "Select sessions in the Sessions pane (1)"
		return nil
	}
	m.batchOpen = !m.batchOpen
	m.batchSelected = map[string]bool{}
	if m.batchOpen {
		m.notice = "Batch select · click/Space selects · Enter opens selected terminals · b cancels"
	} else {
		m.notice = "Batch open OFF · Enter/click attaches here"
	}
	return nil
}

// restoreKey is r on Sessions: bring back whatever the selection is. An
// agent that exited starts again, an ended record is tracked again, and
// otherwise the Restore list opens.
func (m *dashboard) restoreKey() tea.Cmd {
	r := m.current()
	switch {
	case r != nil && agentExited(r):
		return m.choose(reviveAction(r))
	case r != nil && r["ended_at"] != nil && r["can_restore"] == true && r["archived_at"] == nil:
		return m.request("Track again", "POST", "/sessions/"+id(r)+"/restore", map[string]any{}, false)
	}
	return m.loadRecentSessions()
}

// endSelected is x / d / Delete: the selection's own End (or Delete, or Stop
// tracking for an adopted session), always behind a confirmation.
func (m *dashboard) endSelected() tea.Cmd {
	r := m.current()
	if r == nil || sections[m.section] == "approvals" {
		if sections[m.section] == "approvals" {
			m.notice = "n denies an approval; nothing is deleted here."
		}
		return nil
	}
	if r["ended_at"] != nil {
		m.notice = "This session has already ended. r brings it back."
		return nil
	}
	for _, a := range m.rowActions() {
		if a.Key == "x" && a.Method == "DELETE" {
			if a.Label == "End session" {
				a.Label = "End session " + quoteName(r)
			}
			return m.choose(a)
		}
	}
	return nil
}

func quoteName(r row) string { return "“" + oneLine(name(r)) + "”" }

func (m *dashboard) updateHelp(v tea.KeyMsg) tea.Cmd {
	if m.helpSearching {
		switch v.Type {
		case tea.KeyEsc:
			m.helpSearching = false
			m.helpQuery = ""
		case tea.KeyEnter:
			m.helpSearching = false
		case tea.KeyBackspace:
			if r := []rune(m.helpQuery); len(r) > 0 {
				m.helpQuery = string(r[:len(r)-1])
			}
		case tea.KeySpace:
			m.helpQuery += " "
		case tea.KeyRunes:
			m.helpQuery += string(v.Runes)
		}
		m.helpOffset = 0
		return nil
	}
	page := max(1, m.bodyHeight()-2)
	switch v.String() {
	case "esc", "q", "?":
		if v.String() == "esc" && m.helpQuery != "" {
			m.helpQuery = ""
			return nil
		}
		m.help = false
		if m.reviewPaused != nil && m.form == nil {
			m.review, m.reviewPaused = m.reviewPaused, nil
		}
	case "/":
		m.helpSearching = true
	case "up", "k":
		m.helpOffset--
	case "down", "j":
		m.helpOffset++
	case "pgup", "ctrl+u", "b":
		m.helpOffset -= page
	case "pgdown", "ctrl+d", " ", "f":
		m.helpOffset += page
	case "home", "g":
		m.helpOffset = 0
	case "end", "G":
		m.helpOffset = 1 << 20
	}
	m.helpOffset = max(0, m.helpOffset)
	return nil
}
