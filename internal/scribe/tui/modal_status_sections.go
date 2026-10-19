package tui

// The sectioned status dashboard. A left menu lists the sections, each with a
// short status mark; the right pane shows the selected one. Up/Down or j/k
// move, 1-7 jump, Tab/Shift+Tab step, PgUp/PgDn scroll the pane, Esc or q
// closes. Every fact the old tabs showed is in one of the sections below.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Section indexes, in menu order. Keys 1-7 jump to them.
const (
	secOverview = iota
	secModel
	secContext
	secServers
	secJobs
	secUsage
	secGit
)

// statusSections are the section names, in menu order.
var statusSections = []string{"Overview", "Model", "Context", "Servers", "Jobs", "Usage", "Git"}

const (
	statusMenuW  = 22 // menu cell, including its separator
	statusLabelW = 14 // label column in the pane
	statusPage   = 8  // lines PgUp and PgDn move the pane
)

// statusMark is the short status glyph beside a section name in the menu.
type statusMark int

const (
	markNone statusMark = iota // nothing to report
	markOK                     // healthy
	markWarn                   // needs attention
	markBusy                   // work is happening
)

// glyph is the character a mark draws as.
func (m statusMark) glyph() string {
	switch m {
	case markOK:
		return "✓"
	case markWarn:
		return "!"
	case markBusy:
		return "●"
	}
	return " "
}

// statusView is the /status dashboard. It is a read-only modal.
type statusView struct {
	th   theme.Theme
	be   Backend
	sec  int // the selected section
	off  int // the pane's first line
	snap statusSnap
}

func newStatusView(th theme.Theme, be Backend) *statusView {
	return &statusView{th: th, be: be, snap: takeStatusSnap(be)}
}

// fresh re-reads the backend once the reading is older than statusRefresh.
// It runs while the view is drawn, so the dashboard stays live with no timer
// of its own, and it does nothing once the view is closed.
func (v *statusView) fresh() {
	if time.Since(v.snap.at) >= statusRefresh {
		v.snap = takeStatusSnap(v.be)
	}
}

// Title implements Modal.
func (v *statusView) Title() string { return "Status" }

// Icon implements Iconed.
func (v *statusView) Icon() string { return "gauge" }

// Footer implements Footered.
func (v *statusView) Footer() string {
	return "↑↓/jk section · 1-7 jump · Tab next · PgUp/PgDn scroll · Esc/q close"
}

// Size implements Sized. It uses the same frame as the other panels, so the
// dashboard fits an 80 column terminal.
func (v *statusView) Size(cols, rows int) (int, int) {
	return panelFrame(cols, rows)
}

// Update implements Modal.
func (v *statusView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	key := km.String()
	switch key {
	case "esc", "q":
		return v, nil, true
	case "up", "k":
		v.move(-1)
	case "down", "j", "tab":
		v.move(1)
	case "shift+tab":
		v.move(-1)
	case "pgup":
		v.scroll(-statusPage)
	case "pgdown":
		v.scroll(statusPage)
	default:
		if len(key) == 1 && key[0] >= '1' && int(key[0]-'1') < len(statusSections) {
			v.jump(int(key[0] - '1'))
		}
	}
	return v, nil, false
}

// move steps to the next (d = 1) or previous (d = -1) section, wrapping, and
// starts it at the top.
func (v *statusView) move(d int) {
	v.jump((v.sec + d + len(statusSections)) % len(statusSections))
}

// jump selects section i and starts it at the top.
func (v *statusView) jump(i int) {
	v.sec = i
	v.off = 0
}

// scroll moves the pane by d lines. The bottom is clamped when it is drawn,
// because it depends on the frame size.
func (v *statusView) scroll(d int) {
	v.off = max(0, v.off+d)
}

// View implements Modal.
func (v *statusView) View(w, h int) string {
	v.fresh()
	paneW := max(1, w-statusMenuW)
	pane := v.paneLines(paneW - 2)
	if top := len(pane) - h; v.off > top {
		v.off = max(0, top)
	}
	end := min(len(pane), v.off+h)
	shown := pane[v.off:end]

	menu := v.menuLines(statusMenuW-1, h)
	sep := fg(v.th.TextDim).Render("│")
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		line := menu[i] + sep
		if i < len(shown) {
			line += " " + shown[i]
		}
		out = append(out, line)
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

// menuLines draws the section menu, one row per section, each cw cells wide.
func (v *statusView) menuLines(cw, h int) []string {
	out := make([]string, 0, h)
	for i, name := range statusSections {
		if i >= h {
			break
		}
		cursor := " "
		nameStyle := fg(v.th.TextPrimary)
		if i == v.sec {
			cursor = fgb(v.th.Brand).Render("▸")
			nameStyle = fgb(v.th.TextPrimary)
		}
		m := v.mark(i)
		mark := v.markStyle(m).Render(m.glyph())
		line := cursor + fg(v.th.TextMuted).Render(fmt.Sprintf("%d", i+1)) + " " + mark + " " + nameStyle.Render(name)
		out = append(out, padRight(line, cw))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", cw))
	}
	return out
}

// markStyle colours a mark.
func (v *statusView) markStyle(m statusMark) lipgloss.Style {
	switch m {
	case markOK:
		return fg(v.th.Success)
	case markWarn:
		return fgb(v.th.Warning)
	case markBusy:
		return fg(v.th.Info)
	}
	return fg(v.th.TextDim)
}

// mark is the status mark of section i, from the current reading.
func (v *statusView) mark(i int) statusMark {
	s := v.snap
	switch i {
	case secOverview:
		if s.running {
			return markBusy
		}
		return markOK
	case secModel:
		if s.model == "" || !providerOK(s.keyState) {
			return markWarn
		}
		return markOK
	case secContext:
		if s.window > 0 && s.used*10 >= s.window*9 {
			return markWarn
		}
		return markNone
	case secServers:
		for _, r := range append(append([]statusRow{}, s.mcp...), s.lsp...) {
			if rowFailed(r.state) {
				return markWarn
			}
		}
		return markOK
	case secJobs:
		for _, j := range s.jobs {
			if j.status == "running" {
				return markBusy
			}
		}
		return markNone
	case secUsage:
		if s.detail.DailyBudget > 0 && s.detail.Today >= s.detail.DailyBudget {
			return markWarn
		}
		return markNone
	case secGit:
		if s.git.Behind > 0 {
			return markWarn
		}
		if s.git.Dirty {
			return markBusy
		}
		if s.git.Branch != "" {
			return markOK
		}
	}
	return markNone
}

// providerOK says whether a key state from providerState means the model can
// be called.
func providerOK(state string) bool {
	return strings.HasPrefix(state, "local") || strings.HasPrefix(state, "key")
}

// rowFailed says whether a server or language server state needs attention.
func rowFailed(state string) bool {
	return state == "failed" || state == "degraded"
}

// paneLines is the selected section, cw cells wide, starting with its name.
func (v *statusView) paneLines(cw int) []string {
	var body []string
	switch v.sec {
	case secOverview:
		body = v.overviewLines(cw)
	case secModel:
		body = v.modelLines(cw)
	case secContext:
		body = v.contextLines(cw)
	case secServers:
		body = v.serverLines(cw)
	case secJobs:
		body = v.jobLines(cw)
	case secUsage:
		body = v.usageLines(cw)
	case secGit:
		body = v.gitLines(cw)
	}
	out := []string{v.section(statusSections[v.sec]), ""}
	out = append(out, body...)
	for i := range out {
		out[i] = render.Truncate(out[i], cw)
	}
	return out
}

// section is a heading inside the pane.
func (v *statusView) section(title string) string {
	return fgb(v.th.BrandSecondary).Render(title)
}

// sub is a sub-heading inside a section.
func (v *statusView) sub(title string) string {
	return fg(v.th.BrandSecondary).Render(title)
}

// row is one label and value line, cut to the pane width.
func (v *statusView) row(label, value string, w int) string {
	lab := fg(v.th.TextMuted).Render(padRight(label, statusLabelW))
	val := fg(v.th.TextPrimary).Render(value)
	return render.Truncate(lab+val, w)
}

// overviewLines are the session, the working directory, the version and
// whether a turn is running.
func (v *statusView) overviewLines(w int) []string {
	s := v.snap
	session := s.sessionID
	if s.sessionTitle != "" {
		session += " " + s.sessionTitle
	}
	state := "idle"
	if s.running {
		state = "running a turn"
	}
	if n := len(s.jobs); n > 0 {
		state += fmt.Sprintf(", %d background%s", n, plural(n, " job", " jobs"))
	}
	return []string{
		v.row("Session", session, w),
		v.row("Workdir", s.workdir, w),
		v.row("Version", s.version, w),
		v.row("Reasoning", orWord(s.reasoning, "default"), w),
		v.row("State", state, w),
	}
}

// modelLines are the model, its provider and key state, the reasoning setting
// and the mode.
func (v *statusView) modelLines(w int) []string {
	s := v.snap
	provider := s.keyState
	if s.provider != "" {
		provider = s.provider + " · " + s.keyState
	}
	return []string{
		v.row("Model", s.model, w),
		v.row("Provider", provider, w),
		v.row("Reasoning", orWord(s.reasoning, "default"), w),
		v.row("Mode", s.mode, w),
	}
}

// contextLines are the context window and its estimated parts, the code map
// and the memory cleanup count.
func (v *statusView) contextLines(w int) []string {
	s := v.snap
	ctx := "no window reported"
	if s.window > 0 {
		frac := float64(s.used) / float64(s.window)
		ctx = fmt.Sprintf("%s of %s (%d%%) %s", render.HumanTokens(s.used), render.HumanTokens(s.window), pct(s.used, s.window), bar(frac, 20))
	}
	d := s.detail
	return []string{
		v.row("Window", ctx, w),
		v.row("System", render.HumanTokens(d.System)+" (estimate)", w),
		v.row("Tools", render.HumanTokens(d.Tools)+" (estimate)", w),
		v.row("Conversation", render.HumanTokens(d.Conversation)+" (estimate)", w),
		"",
		v.sub("Code map"),
		v.row("Map", mapText(s.mapStats), w),
		"",
		v.sub("Memory"),
		v.row("To review", fmt.Sprintf("%d (not used in 90 days, not pinned)", s.stale), w),
	}
}

// serverLines are the MCP servers and the language servers. A row that has
// failed carries a warning mark.
func (v *statusView) serverLines(w int) []string {
	s := v.snap
	out := []string{v.sub("MCP servers")}
	out = append(out, v.serverList(s.mcp, "none configured", w)...)
	out = append(out, "", v.sub("Language servers"))
	out = append(out, v.serverList(s.lsp, "none", w)...)
	return out
}

// serverList draws one line per server, or the empty text as one line when
// there are none.
func (v *statusView) serverList(rows []statusRow, empty string, w int) []string {
	if len(rows) == 0 {
		return []string{v.row("", empty, w)}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		flag := "  "
		if rowFailed(r.state) {
			flag = fgb(v.th.Warning).Render("! ")
		}
		head := flag +
			fg(v.th.TextPrimary).Render(padRight(r.name, 20)) +
			fg(v.th.TextMuted).Render(padRight(r.state, 10))
		const headW = 32 // flag, name and state columns
		if ansi.StringWidth(r.detail) <= w-headW {
			out = append(out, render.Truncate(head+fg(v.th.TextDim).Render(r.detail), w))
			continue
		}
		// A long detail, such as an error, goes on lines of its own so no
		// part of it is cut off.
		out = append(out, render.Truncate(head, w))
		for _, l := range wrapLines(r.detail, w-4) {
			out = append(out, "    "+fg(v.th.TextDim).Render(l))
		}
	}
	return out
}

// jobLines are the background jobs and the subagents of the newest dispatch.
func (v *statusView) jobLines(w int) []string {
	s := v.snap
	out := []string{v.sub("Background")}
	if len(s.jobs) == 0 {
		out = append(out, v.row("", "none", w))
	}
	for _, j := range s.jobs {
		out = append(out, v.row(j.status, j.command, w))
	}
	return append(out, "", v.sub("Subagents"), v.row("Dispatch", subagentText(s.subagents), w))
}

// usageLines are the token spend, the cost and the spend against the daily
// budget.
func (v *statusView) usageLines(w int) []string {
	s := v.snap
	d := s.detail
	budget := fmt.Sprintf("%s spent today, no daily cap", render.HumanTokens(d.Today))
	if d.DailyBudget > 0 {
		budget = fmt.Sprintf("%s of %s today (%d%%)", render.HumanTokens(d.Today), render.HumanTokens(d.DailyBudget), pct(d.Today, d.DailyBudget))
	}
	return []string{
		v.row("Tokens", tokenText(s.usage), w),
		v.row("Cost", fmt.Sprintf("$%.4f", s.cost), w),
		v.row("Budget", budget, w),
	}
}

// gitLines are the branch with its state, and the changed files.
func (v *statusView) gitLines(w int) []string {
	g := v.snap.git
	out := []string{v.row("Git", gitText(g), w)}
	if !g.Dirty || len(g.Changed) == 0 {
		return out
	}
	out = append(out, "", v.sub("Changed"))
	for _, f := range g.Changed {
		out = append(out, v.row("", f, w))
	}
	return out
}
