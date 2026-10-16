package tui

// Floating terminals: up to maxFloats shells in the workdir, shown one at a
// time as a centred box over the screen. Esc hides a terminal; its process
// keeps running until it exits or the TUI quits.

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/term"
)

const maxFloats = 5

// floatTerm is one shell. It stays in the list after its shell exits.
type floatTerm struct {
	pane *term.Pane
}

// shellCommand is the user's shell: $SHELL, else a platform default.
func shellCommand() []string {
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" {
		return []string{sh}
	}
	if runtime.GOOS == "windows" {
		if c := strings.TrimSpace(os.Getenv("ComSpec")); c != "" {
			return []string{c}
		}
		return []string{"cmd.exe"}
	}
	return []string{"/bin/sh"}
}

// floatBox is the outer size of the floating terminal for the current screen.
func (m *Model) floatBox() (int, int) {
	w := min(100, max(50, m.width-10))
	w = min(w, m.width-2)
	h := min(max(12, m.height/2), m.height-2)
	return max(w, 4), max(h, 4)
}

// floatInner is the size of the shell's screen inside the box.
func (m *Model) floatInner() (int, int) {
	w, h := m.floatBox()
	return max(1, w-4), max(1, h-2)
}

// floatRect is where the box is drawn: centred, as overlay places it.
func (m *Model) floatRect() (x, y, w, h int) {
	w, h = m.floatBox()
	return (m.width - w) / 2, (m.height - h) / 2, w, h
}

// newFloat starts another terminal and shows it.
func (m *Model) newFloat() tea.Cmd {
	ps := m.ps()
	if len(ps.floats) >= maxFloats {
		m.setFlash("err", "Up to "+itoa(maxFloats)+" terminals; close one with /terminals close")
		return nil
	}
	w, h := m.floatInner()
	p, err := term.New(shellCommand(), paneDir(m.cur().be.Workdir()), w, h)
	if err != nil {
		m.setFlash("err", "Could not start a terminal: "+err.Error())
		return nil
	}
	ps.floats = append(ps.floats, &floatTerm{pane: p})
	return m.showFloat(len(ps.floats) - 1)
}

// closeFloat ends terminal i: a shell still running is killed, and the
// terminal leaves the list. An exited one stays listed (so its last output can
// be read) until it is closed here, and it counts toward the cap until then.
func (m *Model) closeFloat(i int) {
	ps := m.ps()
	if i < 0 || i >= len(ps.floats) {
		return
	}
	f := ps.floats[i]
	_ = f.pane.Close()
	delete(ps.armed, f.pane)
	ps.floats = append(ps.floats[:i], ps.floats[i+1:]...)
	switch {
	case ps.shown == i:
		ps.shown = -1
		if ps.focus == focusFloat {
			ps.focus = focusChat
		}
	case ps.shown > i:
		ps.shown--
	}
	switch {
	case ps.last == i:
		ps.last = -1
	case ps.last > i:
		ps.last--
	}
	m.syncFocus()
	m.setFlash("info", "Closed terminal "+itoa(i+1))
}

// closeFloatCmd is "/terminals close [n]": n is the terminal's number as the
// list shows it; with no number it closes the one on screen, else the last one used.
func (m *Model) closeFloatCmd(arg string) {
	ps := m.ps()
	if len(ps.floats) == 0 {
		m.setFlash("info", "No terminals open.")
		return
	}
	i := ps.shown
	if i < 0 {
		i = ps.last
	}
	if arg = strings.TrimSpace(arg); arg != "" {
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(ps.floats) {
			m.setFlash("err", "Usage: /terminals close [1-"+itoa(len(ps.floats))+"]")
			return
		}
		i = n - 1
	}
	if i < 0 || i >= len(ps.floats) {
		m.setFlash("err", "Name the terminal to close: /terminals close [1-"+itoa(len(ps.floats))+"]")
		return
	}
	m.closeFloat(i)
}

// showFloatOrNew shows the terminal used last, or starts one when none exists.
func (m *Model) showFloatOrNew() tea.Cmd {
	ps := m.ps()
	if len(ps.floats) == 0 {
		return m.newFloat()
	}
	i := ps.last
	if i < 0 || i >= len(ps.floats) {
		i = len(ps.floats) - 1
	}
	return m.showFloat(i)
}

// showFloat shows terminal i and gives it the keys.
func (m *Model) showFloat(i int) tea.Cmd {
	ps := m.ps()
	if i < 0 || i >= len(ps.floats) {
		return nil
	}
	ps.shown = i
	ps.last = i
	ps.focus = focusFloat
	m.syncFocus()
	f := ps.floats[i]
	if exited, _ := f.pane.Exited(); exited {
		return nil
	}
	return m.arm(f.pane)
}

// hideFloat hides the visible terminal. Its process keeps running.
func (m *Model) hideFloat() {
	ps := m.ps()
	if ps.shown < 0 {
		return
	}
	ps.shown = -1
	if ps.focus == focusFloat {
		ps.focus = focusChat
	}
	m.syncFocus()
}

// focusFloat moves the keys to the visible terminal.
func (m *Model) focusFloat() {
	ps := m.ps()
	if ps.shown >= 0 {
		ps.focus = focusFloat
		m.syncFocus()
	}
}

// openTerminalList lists the terminals; Enter shows the chosen one.
func (m *Model) openTerminalList() {
	ps := m.ps()
	if len(ps.floats) == 0 {
		m.setFlash("info", "No terminals open. Use /terminal new.")
		return
	}
	p := NewPicker(m.th, "Terminals", "terminal")
	p.Hint = "Enter show · Esc close"
	for i, f := range ps.floats {
		dot, desc := "●", "pid "+strconv.Itoa(f.pane.Pid())
		if exited, code := f.pane.Exited(); exited {
			dot, desc = "○", "exited "+strconv.Itoa(code)
		}
		p.Items = append(p.Items, PickItem{
			Value: strconv.Itoa(i),
			Label: fmt.Sprintf("%s Terminal %d", dot, i+1),
			Desc:  desc,
		})
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		i, err := strconv.Atoi(it.Value)
		if err != nil {
			return nil
		}
		return m.showFloat(i)
	}
	m.pushModal(p)
}

// overlayFloat draws the visible terminal over base.
func (m *Model) overlayFloat(base []string) []string {
	ps := m.ps()
	if ps.shown < 0 || ps.shown >= len(ps.floats) {
		return base
	}
	f := ps.floats[ps.shown]
	w, _ := m.floatBox()
	iw, ih := m.floatInner()
	body := fitLines(strings.Split(f.pane.View(), "\n"), iw, ih)
	for i := range body {
		body[i] = " " + body[i] + " "
	}
	title := fmt.Sprintf("Terminal %d · %s", ps.shown+1, f.pane.Title())
	footer := "Esc hide · /terminals list"
	border := m.th.BorderFocused
	if exited, code := f.pane.Exited(); exited {
		footer = fmt.Sprintf("exited %d · Esc hide", code)
		border = m.th.Border
	}
	box := frame(title, icons.Icon("terminal"), body, footer, w, border, m.th)
	return overlay(base, box, m.width, m.height)
}
