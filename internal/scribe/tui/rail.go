package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
)

// syncRail reads the tab's checkpoints from the backend and hands them to its
// transcript. It runs on every frame: Undo and Redo do not emit events, so a
// copy taken at turn end would leave the rail showing a stale state. The
// backend list is in memory and small, so this is cheap.
func (m *Model) syncRail(t *tab) {
	t.cps = t.be.Checkpoints()
	cps := make([]chat.Checkpoint, len(t.cps))
	for i, cp := range t.cps {
		cps[i] = chat.Checkpoint{Turn: cp.Turn, Undone: cp.Undone}
	}
	viewed := -1
	if t.browse >= 0 && t.browse < len(t.cps) {
		viewed = t.browse
	}
	t.chat.SetCheckpoints(cps, viewed)
}

// toggleRail is Alt+C: it gives the checkpoint rail the keyboard, or hands
// it back when it already has it.
func (m *Model) toggleRail() tea.Cmd {
	t := m.cur()
	m.syncRail(t)
	if t.chat.RailFocused() {
		t.chat.BlurRail()
		return nil
	}
	if !t.chat.FocusRail() {
		m.setFlash("info", "No checkpoints to show. The rail appears when a turn changes files, in 80 columns or wider.")
	}
	return nil
}

// railKey handles keys while the rail has focus. Up and down move, Enter
// opens the checkpoint under the cursor, Esc gives the keyboard back. Any
// other key gives the rail up and goes to the input as usual. handled=false
// passes the key on.
func (m *Model) railKey(km tea.KeyMsg) (tea.Cmd, bool) {
	t := m.cur()
	switch km.String() {
	case "up", "k":
		t.chat.RailMove(-1)
		return nil, true
	case "down", "j":
		t.chat.RailMove(1)
		return nil, true
	case "enter":
		_, cp, ok := t.chat.RailSelected()
		t.chat.BlurRail()
		if !ok {
			return nil, true
		}
		return m.openRailCheckpoint(t, cp.Turn), true
	case "esc":
		t.chat.BlurRail()
		return nil, true
	}
	t.chat.BlurRail()
	return nil, false
}

// openRailCheckpoint browses to the checkpoint for turn, scrolling the chat
// to it, then offers the same restore confirm as /checkpoints.
func (m *Model) openRailCheckpoint(t *tab, turn int) tea.Cmd {
	t.cps = t.be.Checkpoints()
	idx := -1
	for i, cp := range t.cps {
		if cp.Turn == turn {
			idx = i
		}
	}
	if idx < 0 {
		m.setFlash("err", fmt.Sprintf("Turn %d is no longer checkpointed", turn))
		return nil
	}
	t.browse = idx
	t.chat.JumpToTurn(turn)
	m.syncRail(t)
	b := &cpBrowser{m: m, be: t.be, th: m.th, cps: t.cps, cur: idx, stats: map[int][2]int{}}
	b.restore()
	return nil
}
