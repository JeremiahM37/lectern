package tui

// Panes beside and over the chat: the editor (termpane_editor.go) and the
// floating terminals (termpane_float.go). This file holds what they share:
// the state, focus, keys, mouse, sizes and the redraw signal from a pane.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/term"
)

// focusTarget says which pane takes the keys.
type focusTarget int

const (
	focusChat focusTarget = iota
	focusEditor
	focusFloat
)

// paneState is everything the panes need. It is created on first use, so
// a Model built without it still works.
type paneState struct {
	focus  focusTarget
	split  int // index into editorSplits
	editor *editorPane
	floats []*floatTerm
	shown  int // index into floats of the visible terminal, -1 when hidden
	last   int // index of the terminal shown most recently, -1 when none
	armed  map[*term.Pane]bool
}

// paneMsg says a pane has output or has exited; the Model redraws and
// decides whether to wait for the next one.
type paneMsg struct{ p *term.Pane }

// ps returns the pane state, creating it on first use.
func (m *Model) ps() *paneState {
	if m.panes == nil {
		m.panes = &paneState{shown: -1, last: -1, split: defaultSplit, armed: map[*term.Pane]bool{}}
	}
	return m.panes
}

// waitPane blocks until p has output or exits, then sends paneMsg. Output
// is throttled to term.FrameGap so a fast program does not flood the loop.
func waitPane(p *term.Pane) tea.Cmd {
	return func() tea.Msg {
		<-p.Updates()
		time.Sleep(term.FrameGap())
		return paneMsg{p: p}
	}
}

// arm starts a waiter for p unless one is already running.
func (m *Model) arm(p *term.Pane) tea.Cmd {
	ps := m.ps()
	if ps.armed[p] {
		return nil
	}
	ps.armed[p] = true
	return waitPane(p)
}

// onPaneMsg handles a pane wakeup: it closes panes that exited and keeps
// waiting on the ones that are on screen.
func (m *Model) onPaneMsg(msg paneMsg) tea.Cmd {
	ps := m.ps()
	p := msg.p
	delete(ps.armed, p)
	if ed := ps.editor; ed != nil && ed.pane == p {
		if exited, code := p.Exited(); exited {
			m.closeEditor()
			if code > 0 {
				m.failAlert("Editor exited with status " + itoa(code))
			} else {
				m.setFlash("info", "Editor closed")
			}
			return nil
		}
		return m.arm(p)
	}
	for i, f := range ps.floats {
		if f.pane != p {
			continue
		}
		if exited, code := p.Exited(); exited {
			if ps.shown == i {
				m.hideFloat()
			}
			m.setFlash("info", "Terminal "+itoa(i+1)+" exited ("+itoa(code)+")")
			return nil
		}
		if ps.shown == i {
			return m.arm(p)
		}
	}
	return nil
}

// syncFocus tells each pane whether it has the focus, so it draws its cursor.
func (m *Model) syncFocus() {
	ps := m.ps()
	if ps.editor != nil && ps.editor.pane != nil {
		ps.editor.pane.SetFocus(ps.focus == focusEditor)
	}
	for i, f := range ps.floats {
		f.pane.SetFocus(ps.focus == focusFloat && i == ps.shown)
	}
}

// handlePaneKey routes a key to the focused pane. It reports handled=false
// for keys the rest of the Model should see.
func (m *Model) handlePaneKey(km tea.KeyMsg) (tea.Cmd, bool) {
	ps := m.ps()
	if m.topModal() != nil {
		return nil, false
	}
	if km.String() == "ctrl+e" {
		return m.toggleEditor(), true
	}
	switch ps.focus {
	case focusFloat:
		if ps.shown >= 0 && ps.shown < len(ps.floats) {
			if km.Type == tea.KeyEsc && !km.Alt {
				m.hideFloat()
				return nil, true
			}
			_ = ps.floats[ps.shown].pane.SendKey(km)
			return nil, true
		}
	case focusEditor:
		if ed := ps.editor; ed != nil {
			if ed.pane == nil {
				// the error state has nothing to type into; Esc closes it
				if km.Type == tea.KeyEsc {
					m.closeEditor()
				}
				return nil, true
			}
			_ = ed.pane.SendKey(km)
			return nil, true
		}
	}
	return nil, false
}

// handlePaneMouse routes wheel and click events to the pane under the
// pointer. The chat keeps every event the panes do not take.
func (m *Model) handlePaneMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	ps := m.ps()
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	up := msg.Button == tea.MouseButtonWheelUp
	press := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft

	if ps.shown >= 0 && ps.shown < len(ps.floats) {
		x0, y0, w, h := m.floatRect()
		if msg.X >= x0 && msg.X < x0+w && msg.Y >= y0 && msg.Y < y0+h {
			if wheel {
				ps.floats[ps.shown].pane.Wheel(up, 3)
			}
			if press {
				m.focusFloat()
			}
			return nil, true
		}
	}
	if ed := ps.editor; ed != nil {
		top := 1 + m.tabBarRows()
		if msg.X < m.editorWidth() && msg.Y >= top && msg.Y < top+m.bodyRows() {
			if wheel && ed.pane != nil {
				ed.pane.Wheel(up, 3)
			}
			if press {
				m.focusEditor()
			}
			return nil, true
		}
	}
	if press && ps.focus != focusChat {
		ps.focus = focusChat
		m.syncFocus()
	}
	return nil, false
}

// resizePanes gives every pane its current size. It runs after each update,
// so it must be cheap: a pane whose size did not change returns at once.
func (m *Model) resizePanes() {
	ps := m.ps()
	if ps.editor != nil && ps.editor.pane != nil {
		w, h := m.editorInner()
		_ = ps.editor.pane.Resize(w, h)
	}
	w, h := m.floatInner()
	for _, f := range ps.floats {
		_ = f.pane.Resize(w, h)
	}
}

// closePanes ends every pane's program. The Model calls it on exit.
func (m *Model) closePanes() {
	ps := m.ps()
	if ps.editor != nil {
		m.closeEditor()
	}
	for _, f := range ps.floats {
		_ = f.pane.Close()
	}
	ps.floats = nil
	ps.shown = -1
	ps.focus = focusChat
}

// bodyRows is the height of the main area, between the header and footer.
func (m *Model) bodyRows() int {
	return max(1, m.height-2-m.tabBarRows())
}

// bodyWidth is the width left for the chat column beside the editor.
func (m *Model) bodyWidth() int {
	if m.panes == nil || m.panes.editor == nil {
		return m.width
	}
	return max(1, m.width-m.editorWidth())
}

// joinCols puts two blocks of lines side by side. Each block is padded to
// its own height.
func joinCols(left, right []string) []string {
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := range out {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = l + r
	}
	return out
}
