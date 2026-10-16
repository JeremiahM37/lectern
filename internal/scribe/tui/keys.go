package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleGlobalKey runs the keys that work from anywhere (spec 8.1, Scribe's
// set). It reports handled=false for keys the input box should see.
func (m *Model) handleGlobalKey(km tea.KeyMsg) (tea.Cmd, bool) {
	t := m.cur()
	if t.chat.RailFocused() {
		return m.railKey(km)
	}
	switch km.String() {
	case "ctrl+k":
		m.openPalette()
	case "ctrl+l":
		return m.openModelPicker(), true
	case "ctrl+d", "shift+tab":
		return m.setMode(nextMode(t.mode)), true
	case "ctrl+g":
		m.openGitMenu()
	case "ctrl+n":
		return m.newSession(), true
	case "ctrl+p":
		m.openSessionPicker()
	case "ctrl+s":
		m.openSkillBrowser()
	case "ctrl+o":
		t.chat.ToggleExpand()
	case "esc":
		if t.in.search != nil || t.in.popupKind() != "" {
			// the input box owns Esc while its popup or history search is open
			return nil, false
		}
		if t.busy {
			t.be.Abort()
			m.setFlash("info", "Stopping…")
		}
	case "ctrl+x":
		if t.busy {
			t.be.Abort()
			m.setFlash("info", "Stopping…")
		}
	case "ctrl+t":
		m.openNewTab()
	case "ctrl+w":
		m.requestCloseTab()
	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5":
		m.switchTab(int(km.Runes[0] - '1'))
	case "alt+[", "ctrl+[":
		m.switchTab((m.active - 1 + len(m.tabs)) % len(m.tabs))
	case "alt+]", "ctrl+]":
		m.switchTab((m.active + 1) % len(m.tabs))
	case "alt+r":
		m.openErrorLog()
	case "alt+c":
		return m.toggleRail(), true
	case "ctrl+b":
		return m.browseCheckpoint(-1), true
	case "ctrl+f":
		return m.browseCheckpoint(1), true
	case "ctrl+z":
		m.confirmUndo()
	case "pgup":
		t.chat.PageUp()
	case "pgdown":
		t.chat.PageDown()
	case "home":
		if t.in.empty() {
			t.chat.Top()
			return nil, true
		}
		return nil, false
	case "end":
		if t.in.empty() {
			t.chat.Bottom()
			return nil, true
		}
		return nil, false
	default:
		return nil, false
	}
	return nil, true
}

// setMode changes the mode on the active tab and tells the backend.
func (m *Model) setMode(mode string) tea.Cmd {
	t := m.cur()
	t.be.SetMode(mode)
	t.mode = t.be.Mode()
	mi := modeStyle(t.mode, m.th)
	m.setFlash("ok", "Mode: "+mi.label+" — "+mi.desc)
	return nil
}

// switchTab activates tab i.
func (m *Model) switchTab(i int) {
	if i < 0 || i >= len(m.tabs) || i == m.active {
		return
	}
	m.active = i
	t := m.cur()
	t.unread = false
	t.errFlag = false
}

// newSession starts a fresh session on the active tab.
func (m *Model) newSession() tea.Cmd {
	t := m.cur()
	if err := t.be.NewSession(); err != nil {
		m.logError(t, err.Error())
		m.setFlash("err", "Could not start a session: "+err.Error())
		return nil
	}
	m.resetTranscript(t)
	m.setFlash("ok", "New session")
	return nil
}

// resetTranscript clears the view and the pending queue for t.
func (m *Model) resetTranscript(t *tab) {
	t.chat.Clear()
	t.steerQ = nil
	t.cps = nil
	t.browse = -1
	t.in.history = nil
	t.in.hidx = -1
}

// replayTranscript rebuilds the view from the backend's saved events (after a resume).
func (m *Model) replayTranscript(t *tab) {
	m.resetTranscript(t)
	replayEvents(t.chat, t.be.Transcript())
	t.in.history = historyFromTranscript(t.be.Transcript())
	t.model = t.be.Model()
	t.local = isLocalModel(t.model)
	t.mode = t.be.Mode()
	if m.phase == phaseLanding && len(t.be.Transcript()) > 0 {
		m.phase = phaseChat
	}
}

// browseCheckpoint steps through checkpoints. dir<0 goes back, dir>0 forward.
// Going forward past the last one returns to the live view.
func (m *Model) browseCheckpoint(dir int) tea.Cmd {
	t := m.cur()
	t.cps = t.be.Checkpoints()
	var usable []int
	for i, cp := range t.cps {
		if !cp.Undone {
			usable = append(usable, i)
		}
	}
	if len(usable) == 0 {
		m.setFlash("info", "No checkpoints yet")
		return nil
	}
	pos := -1
	for p, idx := range usable {
		if idx == t.browse {
			pos = p
		}
	}
	if dir < 0 {
		if pos < 0 {
			pos = len(usable) - 2 // from live, the second-to-last
		} else {
			pos--
		}
		pos = clamp(pos, 0, len(usable)-1)
	} else {
		if t.browse < 0 {
			return nil
		}
		pos++
		if pos >= len(usable) {
			t.browse = -1
			t.chat.Bottom()
			m.setFlash("info", "Live view")
			return nil
		}
	}
	t.browse = usable[pos]
	t.chat.JumpToTurn(t.cps[t.browse].Turn)
	return nil
}

// requestCloseTab implements Ctrl+W: closes the active tab, asking first when it is busy.
func (m *Model) requestCloseTab() {
	if len(m.tabs) <= 1 {
		m.setFlash("info", "Only one tab is open")
		return
	}
	t := m.cur()
	if t.busy {
		m.pushModal(NewConfirm(m.th, "Tab is busy — close anyway?",
			"The running turn will be stopped.", true, func() tea.Cmd {
				m.closeTab(t)
				return nil
			}).WithLabels("Yes, close it", "Cancel"))
		return
	}
	m.closeTab(t)
}

// closeTab removes t and activates the neighbour at the same index.
func (m *Model) closeTab(t *tab) {
	idx := -1
	for i, x := range m.tabs {
		if x == t {
			idx = i
		}
	}
	if idx < 0 {
		return
	}
	if t.busy {
		t.be.Abort()
	}
	m.tabs = append(m.tabs[:idx], m.tabs[idx+1:]...)
	if m.active >= len(m.tabs) {
		m.active = len(m.tabs) - 1
	}
	go func() { _ = t.be.Close() }()
	m.setFlash("info", "Closed "+t.name)
}

// openNewTab asks for a name, then opens a tab with its own backend.
func (m *Model) openNewTab() {
	if len(m.tabs) >= MaxTabs {
		m.setFlash("err", fmt.Sprintf("Up to %d tabs", MaxTabs))
		return
	}
	if m.newBackend == nil {
		m.setFlash("err", "New tabs are not available here")
		return
	}
	name := fmt.Sprintf("TAB-%d", m.nextID+1)
	p := NewTextPrompt(m.th, "New Tab", "Give this tab a name", false, func(v string) tea.Cmd {
		be, err := m.newBackend()
		if err != nil {
			m.setFlash("err", "Could not open a tab: "+err.Error())
			return nil
		}
		if strings.TrimSpace(v) == "" {
			v = name
		}
		t := m.addTab(be, v)
		t.ctxWin = 0
		m.phase = phaseChat
		return waitBoth(t)
	})
	p.AllowEmpty()
	p.icon = "tabs"
	p.WithHint("Enter creates it. Leave it empty for " + name + ".")
	m.pushModal(p)
}

// waitBoth starts the event, prompt and git pumps for a new tab.
func waitBoth(t *tab) tea.Cmd {
	return tea.Batch(waitEvent(t.id, t.be), waitPrompt(t.id, t.be), gitCmd(t.id, t.be))
}
