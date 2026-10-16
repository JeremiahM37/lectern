package tui

// The editor pane: $SCRIBE_EDITOR (or $VISUAL, $EDITOR, else the first of
// nvim, vim, nano, vi on PATH) runs beside the chat, on a file.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/term"
)

// editorSplits are the widths the editor cycles through, as percent of the
// screen. defaultSplit indexes 60.
var editorSplits = []int{40, 50, 60, 70}

const defaultSplit = 2

var errNoEditor = errors.New("no editor found on PATH")

// editorPane is one open editor. pane is nil in the error state, where the
// pane shows why it could not start.
type editorPane struct {
	name     string
	pane     *term.Pane
	noEditor bool   // error state: nothing to run
	errMsg   string // error state: why it did not start
}

// editorCommand picks the editor and returns its argv with file appended.
func editorCommand(file string) ([]string, error) {
	var argv []string
	for _, name := range []string{"SCRIBE_EDITOR", "VISUAL", "EDITOR"} {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			continue
		}
		// An explicit choice is honoured or reported, never swapped out.
		argv = strings.Fields(v)
		if _, err := exec.LookPath(argv[0]); err != nil {
			return nil, fmt.Errorf("%s is set to %q, which was not found", name, argv[0])
		}
		break
	}
	if argv == nil {
		for _, c := range []string{"nvim", "vim", "nano", "vi"} {
			if _, err := exec.LookPath(c); err == nil {
				argv = []string{c}
				break
			}
		}
		if argv == nil {
			return nil, errNoEditor
		}
	}
	if file != "" {
		argv = append(argv, file)
	}
	return argv, nil
}

// installHints are the install commands for this platform.
func installHints() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"brew install neovim"}
	case "windows":
		return []string{"winget install Neovim.Neovim"}
	default:
		return []string{"sudo apt install neovim", "sudo dnf install neovim", "sudo pacman -S neovim"}
	}
}

// editorWidth is the editor column's width for the current split.
func (m *Model) editorWidth() int {
	w := m.width * editorSplits[m.ps().split] / 100
	return clamp(w, 10, max(10, m.width-20))
}

// editorInner is the size of the editor's program: the box less its border.
func (m *Model) editorInner() (int, int) {
	return max(1, m.editorWidth()-2), max(1, m.bodyRows()-2)
}

// paneDir is where a pane starts: the workdir, or the current directory when
// the workdir is not a directory on disk (a removed checkout, the demo).
func paneDir(workdir string) string {
	if fi, err := os.Stat(workdir); err == nil && fi.IsDir() {
		return workdir
	}
	return ""
}

// displayName is the file as the user sees it: relative to the workdir.
func displayName(file, workdir string) string {
	if file == "" {
		return "(no file)"
	}
	if filepath.IsAbs(file) && workdir != "" {
		if rel, err := filepath.Rel(workdir, file); err == nil && !strings.HasPrefix(rel, "..") {
			file = rel
		}
	}
	return filepath.ToSlash(file)
}

// recentFile is the file the agent changed last, or "".
func (m *Model) recentFile() string {
	files := changedFiles(m.cur().chat)
	if len(files) == 0 {
		return ""
	}
	return files[len(files)-1]
}

// openEditor starts the editor on file (workdir-relative, or "" for none).
// An editor that is already open is closed first when a file is given.
func (m *Model) openEditor(file string) tea.Cmd {
	ps := m.ps()
	if ps.editor != nil {
		if file == "" {
			m.focusEditor()
			return nil
		}
		m.closeEditor()
	}
	// Show the pane at once: the landing transition would hide it for 600ms.
	if m.phase == phaseLanding || m.phase == phaseTransition {
		m.phase = phaseChat
	}
	t := m.cur()
	ed := &editorPane{name: displayName(file, t.be.Workdir())}
	argv, err := editorCommand(file)
	if err != nil {
		ed.noEditor = errors.Is(err, errNoEditor)
		ed.errMsg = err.Error()
	} else {
		w, h := m.editorInner()
		p, err := term.New(argv, paneDir(t.be.Workdir()), w, h)
		if err != nil {
			ed.errMsg = err.Error()
		} else {
			ed.pane = p
		}
	}
	ps.editor = ed
	ps.focus = focusEditor
	ps.shown = -1
	m.resizePanes()
	m.syncFocus()
	if ed.pane != nil {
		return m.arm(ed.pane)
	}
	return nil
}

// closeEditor ends the editor and returns the focus to the chat.
func (m *Model) closeEditor() {
	ps := m.ps()
	if ps.editor == nil {
		return
	}
	if ps.editor.pane != nil {
		_ = ps.editor.pane.Close()
	}
	ps.editor = nil
	if ps.focus == focusEditor {
		ps.focus = focusChat
	}
	m.syncFocus()
	m.resizePanes()
}

// focusEditor gives the editor the keys. A visible floating terminal is hidden
// first, so one pane is focused and on screen at a time.
func (m *Model) focusEditor() {
	ps := m.ps()
	if ps.editor == nil {
		return
	}
	m.hideFloat()
	ps.focus = focusEditor
	m.syncFocus()
}

// toggleEditor is Ctrl+E: opens the editor when none is open, otherwise
// moves the focus between the editor and the chat.
func (m *Model) toggleEditor() tea.Cmd {
	ps := m.ps()
	if ps.editor == nil {
		return m.openEditor(m.recentFile())
	}
	if ps.focus == focusEditor {
		ps.focus = focusChat
		m.syncFocus()
		return nil
	}
	m.focusEditor()
	return nil
}

// cycleSplit moves to the next editor width (40, 50, 60, 70, 40, ...).
func (m *Model) cycleSplit() {
	ps := m.ps()
	ps.split = (ps.split + 1) % len(editorSplits)
	m.setFlash("info", "Editor width "+itoa(editorSplits[ps.split])+"%")
}

// viewBody is the main area: the editor column beside the chat, or the chat
// alone when no editor is open.
func (m *Model) viewBody(rows int) []string {
	ps := m.ps()
	if ps.editor == nil {
		return m.viewChatBody(rows)
	}
	ew := m.editorWidth()
	cw := max(1, m.width-ew)
	saved := m.width
	m.width = cw
	chat := m.viewChatBody(rows)
	m.width = saved
	h := m.bodyRows()
	return joinCols(m.viewEditor(ew, h), fitLines(chat, cw, h))
}

// viewEditor draws the editor box, w x h cells: a rounded border with the
// " ✎ file " title, and the program or the error state inside.
func (m *Model) viewEditor(w, h int) []string {
	ed := m.ps().editor
	th := m.th
	col := th.Border
	if m.ps().focus == focusEditor {
		col = th.BorderFocused
	}
	bs := lipgloss.NewStyle().Foreground(col)

	var inner []string
	if ed.pane != nil {
		inner = strings.Split(ed.pane.View(), "\n")
	} else {
		inner = m.editorErrorLines()
	}
	inner = fitLines(inner, w-2, h-2)

	seg := lipgloss.NewStyle().Background(th.Brand).Foreground(lipgloss.Color("#ffffff")).Bold(true).Render(" ✎ ") +
		fg(th.TextPrimary).Render(" "+ed.name+" ")
	if ansi.StringWidth(seg) > w-4 {
		seg = ansi.Truncate(seg, max(1, w-4), "…")
	}
	fill := max(0, w-2-ansi.StringWidth(seg))
	out := []string{bs.Render("╭") + seg + bs.Render(strings.Repeat("─", fill)) + bs.Render("╮")}
	for _, l := range inner {
		out = append(out, bs.Render("│")+l+bs.Render("│"))
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

// editorErrorLines is the error state: why no editor could run here.
func (m *Model) editorErrorLines() []string {
	ed := m.ps().editor
	th := m.th
	var out []string
	if ed.noEditor {
		out = append(out, fgb(th.Error).Render("Neovim Not Found"), "",
			"No editor was found on PATH: nvim, vim, nano or vi.", "Install one:")
		for _, h := range installHints() {
			out = append(out, "  "+fg(th.TextSecondary).Render(h))
		}
		out = append(out, "", "Or set SCRIBE_EDITOR to the editor to run.")
	} else {
		out = append(out, fgb(th.Error).Render("Editor did not start"), "", ed.errMsg)
	}
	return append(out, "", fg(th.TextDim).Render("Esc closes this pane"))
}
