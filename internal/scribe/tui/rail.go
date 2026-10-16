package tui

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

// railMouse handles a left click on a checkpoint marker. It browses to that
// checkpoint, the same as Ctrl+B or Ctrl+F landing on it, and leaves the
// restore decision to Enter on the rail or /checkpoints, so a stray click
// cannot rewind a session. Undone markers are skipped, as Ctrl+B and Ctrl+F
// skip them. Clicks elsewhere go on to the chat (handled=false).
func (m *Model) railMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil, false
	}
	t := m.cur()
	idx, cp, ok := t.chat.RailHit(msg.X, msg.Y)
	if !ok {
		return nil, false
	}
	if cp.Undone {
		return nil, true
	}
	t.cps = t.be.Checkpoints()
	if idx >= len(t.cps) {
		return nil, true
	}
	t.browse = idx
	t.chat.SelectRail(idx)
	t.chat.JumpToTurn(cp.Turn)
	m.syncRail(t)
	return nil, true
}

// chatTop is the screen row where the transcript starts: the header, then the
// tab bar when it is shown. Mouse rows are screen rows; the chat takes rows
// relative to its own top.
func (m *Model) chatTop() int {
	top := 1 // header
	if m.tabBarRows() == 1 {
		top++
	}
	return top
}

// chatMouse is msg in the transcript's own coordinates. A framed transcript
// starts one row and one column inside its border.
func (m *Model) chatMouse(msg tea.MouseMsg) tea.MouseMsg {
	msg.Y -= m.chatTop()
	if m.chatFramed() {
		msg.X--
		msg.Y--
	}
	return msg
}

// inSidePanel reports whether a mouse event lands in the right-hand panels,
// which take no clicks or wheel: the transcript never sees them.
func (m *Model) inSidePanel(msg tea.MouseMsg) bool {
	side := m.sideWidth()
	return side > 0 && msg.X >= m.width-side
}

// sideMinWidth is the narrowest screen that gets the side panels. Below it the
// transcript keeps every column for its text and checkpoint rail.
const sideMinWidth = 110

// sideWidth is the width of the right-hand panel column: a fifth of the screen
// when it is shown, else 0. The landing page and the quit screen have none,
// and neither does a session with no changed files and no terminals: two
// empty panels would only take columns from the transcript.
func (m *Model) sideWidth() int {
	if m.width < sideMinWidth || m.phase == phaseLanding || m.phase == phaseShutdown {
		return 0
	}
	if !m.sideHasContent() {
		return 0
	}
	return m.width * 20 / 100
}

// sideHasContent reports whether the current tab has changed files or any
// terminal is open.
func (m *Model) sideHasContent() bool {
	if len(m.ps().floats) > 0 {
		return true
	}
	if len(m.tabs) == 0 {
		return false
	}
	created, modified := m.cur().changedNow()
	return len(created)+len(modified) > 0
}

// sideColumn draws the changes panel over the terminals panel, h rows by
// sideWidth cells. The transcript's border is the divider, so the column
// needs no rule of its own.
func (m *Model) sideColumn(t *tab, h int) []string {
	w := m.sideWidth()
	if w <= 0 {
		return nil
	}
	top := h / 2
	return append(fitLines(m.changesPanel(t, w, top), w, top), fitLines(m.terminalsPanel(w, h-top), w, h-top)...)
}

// changesPanel is the header and file tree of the changes panel, rows lines
// at most. Created files show A in success, modified files M in amber.
func (m *Model) changesPanel(t *tab, w, rows int) []string {
	if rows <= 0 {
		return nil
	}
	th := m.th
	created, modified := t.changedNow()
	head := " " + fgb(th.TextPrimary).Render("Changes")
	if len(created) > 0 {
		head += " " + fg(th.Success).Render("+"+itoa(len(created)))
	}
	if len(modified) > 0 {
		head += " " + fg(th.Amber).Render("~"+itoa(len(modified)))
	}
	out := []string{head}
	if len(created)+len(modified) == 0 {
		return append(out, " "+fg(th.TextDim).Render("No changes yet"))
	}
	type entry struct {
		status string
		col    lipgloss.TerminalColor
		path   string
	}
	var files []entry
	for _, f := range created {
		files = append(files, entry{"A", th.Success, f})
	}
	for _, f := range modified {
		files = append(files, entry{"M", th.Amber, f})
	}
	// one row per file, the last one may be the overflow count
	room := rows - 1
	show := len(files)
	if show > room {
		show = max(room-1, 0)
	}
	for i, f := range files[:show] {
		branch := "├─ "
		if i == len(files)-1 {
			branch = "└─ "
		}
		name := tailTruncate(f.path, w-6)
		out = append(out, " "+fg(th.TextFaint).Render(branch)+fg(f.col).Render(f.status)+" "+fg(th.TextSecondary).Render(name))
	}
	if rest := len(files) - show; rest > 0 && len(out) < rows {
		out = append(out, " "+fg(th.TextDim).Render("… "+itoa(rest)+" more"))
	}
	return out
}

// terminalsPanel is the header and rows of the floating terminals, rows lines
// at most: a dot (● live, ○ exited), the title and pid, then the shortened
// working directory on the row below.
func (m *Model) terminalsPanel(w, rows int) []string {
	if rows <= 0 {
		return nil
	}
	th := m.th
	floats := m.ps().floats
	active := 0
	for _, f := range floats {
		if done, _ := f.pane.Exited(); !done {
			active++
		}
	}
	head := " " + fgb(th.TextPrimary).Render("Terminals") + " " +
		fg(th.Success).Render(itoa(active)) + fg(th.TextDim).Render("/"+itoa(len(floats)-active)) +
		" " + fg(th.TextDim).Render("["+itoa(len(floats))+"/"+itoa(maxFloats)+"]")
	out := []string{head}
	if len(floats) == 0 {
		return append(out, " "+fg(th.TextDim).Render("No terminals"), " "+fg(th.TextDim).Render("/terminal opens one"))
	}
	for _, f := range floats {
		if len(out) >= rows {
			break
		}
		done, _ := f.pane.Exited()
		dot := fg(th.Success).Render("●")
		if done {
			dot = fg(th.TextDim).Render("○")
		}
		pid := f.pane.Pid()
		label := tailTruncate(f.pane.Title(), w-8-len(itoa(pid)))
		out = append(out, " "+dot+" "+fg(th.TextPrimary).Render(label)+" "+fg(th.TextDim).Render("["+itoa(pid)+"]"))
		if len(out) >= rows {
			break
		}
		if cwd := shortPath(paneCwd(pid)); cwd != "" {
			out = append(out, "   "+fg(th.TextDim).Render(tailTruncate(cwd, w-4)))
		}
	}
	return out
}

// paneCwd is the working directory of a process. Only Linux exposes it
// cheaply; elsewhere it is empty and the row is left out.
func paneCwd(pid int) string {
	if pid <= 0 || runtime.GOOS != "linux" {
		return ""
	}
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil {
		return ""
	}
	return p
}

// shortPath writes p with the home directory as ~, and keeps only the last
// two components once it runs deep: ~/…/parent/leaf.
func shortPath(p string) string {
	if p == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	parts := strings.Split(p, "/")
	if len(parts) > 4 {
		p = strings.Join(append([]string{parts[0], "…"}, parts[len(parts)-2:]...), "/")
	}
	return p
}

// tailTruncate keeps the end of s, which is the part of a path that names the
// file, and marks the cut with an ellipsis at the front.
func tailTruncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if ansi.StringWidth(s) <= n {
		return s
	}
	for len(r) > 0 && ansi.StringWidth("…"+string(r)) > n {
		r = r[1:]
	}
	return "…" + string(r)
}
