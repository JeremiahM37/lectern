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
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
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
	sess     *nvim.Session // the editor's session; its RPC link when it is Neovim
	noEditor bool          // error state: nothing to run
	errMsg   string        // error state: why it did not start
}

// editorCommand picks the editor and returns its argv with file appended.
func editorCommand(file string) ([]string, error) { return editorCommandAt(file, 0) }

// editorCommandAt is editorCommand with the cursor put on line (1-based, 0
// for the start). "+N file" opens at a line in Neovim, Vim, nano and vi.
func editorCommandAt(file string, line int) ([]string, error) {
	var argv []string
	// SCRIBE_EDITOR, then the editor setting (editor.command and args), then
	// VISUAL and EDITOR, then the first installed of nvim, vim, nano, vi.
	for _, name := range []string{"SCRIBE_EDITOR", "setting", "VISUAL", "EDITOR"} {
		v := ""
		if name == "setting" {
			editorSettingMu.Lock()
			v = editorSetting
			editorSettingMu.Unlock()
			name = "the editor setting"
		} else {
			v = strings.TrimSpace(os.Getenv(name))
		}
		if v == "" {
			continue
		}
		// An explicit choice is honoured or reported, never swapped out. It is
		// split as a shell would, so "code --wait" and quoted arguments work.
		argv = shellWords(v)
		if len(argv) == 0 {
			continue
		}
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
	if line > 0 {
		argv = append(argv, "+"+itoa(line))
	}
	if file != "" {
		argv = append(argv, file)
	}
	return argv, nil
}

// splitFileLine reads "path", "path:12" and "path:12:3" (the form compilers
// print). The line is 0 when there is none.
func splitFileLine(arg string) (string, int) {
	arg = strings.TrimSpace(arg)
	i := strings.LastIndex(arg, ":")
	if i <= 0 {
		return arg, 0
	}
	n, ok := parseLine(arg[i+1:])
	if !ok {
		return arg, 0
	}
	rest := arg[:i]
	if j := strings.LastIndex(rest, ":"); j > 0 {
		if l, ok := parseLine(rest[j+1:]); ok {
			return rest[:j], l // path:line:col, the column is dropped
		}
	}
	return rest, n
}

// parseLine reads a positive line number.
func parseLine(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	return n, n > 0
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

// fitEditorName shortens a file name to at most max cells by dropping its
// start and marking that with "…". The end is what names the file, so
// "…/sample.txt" is kept over "/tmp/claude-…".
func fitEditorName(name string, max int) string {
	if ansi.StringWidth(name) <= max {
		return name
	}
	if max <= 1 {
		return "…"
	}
	r := []rune(name)
	for i := range r {
		if tail := string(r[i:]); ansi.StringWidth(tail)+1 <= max {
			return "…" + tail
		}
	}
	return "…"
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
func (m *Model) openEditor(file string) tea.Cmd { return m.openEditorAt(file, 0) }

// openEditorAt is openEditor with the cursor on line (0 for the start).
func (m *Model) openEditorAt(file string, line int) tea.Cmd {
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
	// Read the editor settings again: they may have changed in /settings.
	applyEditorSettings(t.be.Workdir())
	argv, err := editorCommandAt(file, line)
	if err != nil {
		ed.noEditor = errors.Is(err, errNoEditor)
		ed.errMsg = err.Error()
	} else {
		w, h := m.editorInner()
		sess, err := nvim.Start(argv, paneDir(t.be.Workdir()), w, h)
		if err != nil {
			ed.errMsg = err.Error()
		} else {
			ed.sess = sess
			ed.pane = sess.Pane
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
	if ed := ps.editor; ed.sess != nil {
		ed.sess.Close()
	} else if ed.pane != nil {
		_ = ed.pane.Close()
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

	// The title is " ✎ " then " name ". A long path keeps its end, where the
	// file name is, rather than being cut after the directory.
	const icon = " ✎ "
	name := fitEditorName(ed.name, max(1, w-4-ansi.StringWidth(icon)-2))
	seg := lipgloss.NewStyle().Background(th.Brand).Foreground(lipgloss.Color("#ffffff")).Bold(true).Render(icon) +
		fg(th.TextPrimary).Render(" "+name+" ")
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

// applyEditorSettings sets the editor's start config, agent sync and agent
// access from the config for root. A missing or unreadable config keeps the
// defaults.
func applyEditorSettings(root string) {
	c, err := config.Load(root)
	if err != nil {
		return
	}
	nvim.SetStartConfig(c.EditorConfig)
	setEditorCommand(c.Editor.Command, c.Editor.Args)
	nvim.SetSyncOnEdit(c.EditorSync == nil || *c.EditorSync)
	if c.EditorAccess != "" {
		if a, err := nvim.ParseAccess(c.EditorAccess); err == nil {
			nvim.SetAccess(a)
		}
	}
}

// saveEditorSetting writes one editor setting to the global config, keeping the
// rest of the file as it is. The change applies now and is read again at the
// next start.
func saveEditorSetting(change func(c *config.Config)) error {
	c, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		return err
	}
	change(&c)
	return config.SaveGlobal(c)
}

// parseOnOff reads an on/off setting.
func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "yes", "true":
		return true, nil
	case "off", "no", "false":
		return false, nil
	}
	return false, fmt.Errorf("want on or off, got %q", s)
}

// syncLabel is the sync setting as the flash names it.
func syncLabel(on bool) string {
	if on {
		return "Editor sync: agent edits follow into the open buffer"
	}
	return "Editor sync: off; agent edits do not move the editor"
}

// editorSetting is the editor command line from the config (editor.command
// plus editor.args), or "" when unset.
var (
	editorSettingMu sync.Mutex
	editorSetting   string
)

func setEditorCommand(cmd string, args []string) {
	v := ""
	if c := strings.TrimSpace(cmd); c != "" {
		v = strings.Join(append([]string{c}, args...), " ")
	}
	editorSettingMu.Lock()
	editorSetting = v
	editorSettingMu.Unlock()
}

// shellWords splits a command line the way a POSIX shell splits words:
// whitespace separates words, single quotes keep everything literal, double
// quotes keep spaces and allow \" and \\, and a backslash outside quotes
// escapes the next character. It does no expansion. An unclosed quote takes
// the rest of the line.
func shellWords(s string) []string {
	var (
		words []string
		cur   strings.Builder
		inTok bool
	)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\'':
			inTok = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
		case r == '"':
			inTok = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\') {
					i++
				}
				cur.WriteRune(rs[i])
			}
		case r == '\\' && i+1 < len(rs):
			inTok = true
			i++
			cur.WriteRune(rs[i])
		case r == ' ' || r == '\t' || r == '\n':
			if inTok {
				words = append(words, cur.String())
				cur.Reset()
				inTok = false
			}
		default:
			inTok = true
			cur.WriteRune(r)
		}
	}
	if inTok {
		words = append(words, cur.String())
	}
	return words
}
