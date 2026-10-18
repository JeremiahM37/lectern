package tui

// The status dashboard behind /status (spec 01 §8.3 StatusDashboard, spec 02
// §2.1). It has three tabs, all read from the backend the command was run in:
// Overview (session, model, mode, work), Usage (context, spend, budget,
// subagents) and Servers (MCP and language servers, code map, memory). Tab and
// the arrow keys switch tabs; ↑↓ and PgUp/PgDn scroll a tab that is taller
// than the frame.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// statusTabs are the tabs of the status dashboard, in order.
var statusTabs = []string{"Overview", "Usage", "Servers"}

// statusRefresh is the oldest a reading may be while the dashboard is drawn.
// Git status costs a process and the server list takes a lock, and the view is
// drawn on every 50 ms tick, so the backend is read at most this often.
const statusRefresh = 2 * time.Second

// statusPage is how many lines PgUp and PgDn move.
const statusPage = 8

// statusLabelW is the width of the label column in the rows.
const statusLabelW = 14

// statusRow is one line of a server list: a name, its state and a short detail.
type statusRow struct {
	name, state, detail string
}

// statusSnap is one reading of the backend. The tabs draw from it, so a frame
// does not run git or walk the server list again.
type statusSnap struct {
	at                                        time.Time
	workdir, version, sessionID, sessionTitle string
	model, reasoning, mode                    string
	provider, keyState                        string
	running                                   bool
	jobs                                      int
	git                                       GitStatus
	used, window                              int
	usage                                     llm.Usage
	cost                                      float64
	detail                                    ContextDetail
	subagents                                 []string // status of each subagent of the newest dispatch
	mcp, lsp                                  []statusRow
	mapStats                                  MapStats
	stale                                     int
}

// takeStatusSnap reads everything the dashboard shows.
func takeStatusSnap(be Backend) statusSnap {
	s := statusSnap{
		at:           time.Now(),
		workdir:      be.Workdir(),
		version:      be.Version(),
		sessionID:    be.SessionID(),
		sessionTitle: be.SessionTitle(),
		model:        be.Model(),
		reasoning:    be.Reasoning(),
		mode:         be.Mode(),
		running:      be.Running(),
		jobs:         len(be.Jobs()),
		git:          be.Git(),
		detail:       be.ContextDetail(),
		mapStats:     be.MapStats(),
		stale:        be.StaleMemories(),
	}
	s.used, s.window = be.Context()
	s.usage, s.cost = be.Usage()
	s.provider, s.keyState = providerState(be, s.model)
	for _, a := range be.Subagents() {
		s.subagents = append(s.subagents, a.Status)
	}
	s.mcp, s.lsp = serverRows(be)
	return s
}

// providerState names the provider of a "provider/model" id and says whether
// it can be called. A provider the backend does not list is shown as unknown
// rather than guessed at.
func providerState(be Backend, model string) (name, state string) {
	if model == "" {
		return "", "no model selected"
	}
	id, _, _ := strings.Cut(model, "/")
	for _, p := range be.Providers() {
		if p.ID != id {
			continue
		}
		switch {
		case p.Local:
			return p.Name, "local, no key needed"
		case p.HasKey && p.KeySource != "":
			return p.Name, "key from " + p.KeySource
		case p.HasKey:
			return p.Name, "key set"
		}
		return p.Name, "no key set"
	}
	return id, "not a known provider"
}

// serverRows lists the MCP servers and the language servers. Language servers
// come from LSPSessions when the backend has it, because that carries the
// process id and open documents; otherwise from the shared server list.
func serverRows(be Backend) (mcp, lsp []statusRow) {
	servers := be.Servers()
	for _, s := range servers {
		if s.Kind == "mcp" {
			mcp = append(mcp, statusRow{name: s.Name, state: s.State, detail: serverDetail(s)})
		}
	}
	if ls, ok := be.(LSPSessioner); ok {
		for _, s := range ls.LSPSessions() {
			lsp = append(lsp, statusRow{name: lspName(s), state: lspState(s.State), detail: lspSessionDetail(s)})
		}
		return mcp, lsp
	}
	for _, s := range servers {
		if s.Kind == "lsp" {
			lsp = append(lsp, statusRow{name: s.Name, state: s.State, detail: serverDetail(s)})
		}
	}
	return mcp, lsp
}

// serverDetail is the tail of a server row: its error, or its tool count.
func serverDetail(s ServerStatus) string {
	if s.Error != "" {
		return oneLine(s.Error)
	}
	return fmt.Sprintf("%d tools", s.Tools)
}

// lspSessionDetail is the tail of a language server row: its error, or its
// process id and open document count.
func lspSessionDetail(s LSPSession) string {
	if s.LastError != "" {
		return oneLine(s.LastError)
	}
	detail := fmt.Sprintf("%d open", s.OpenDocs)
	if s.PID > 0 {
		detail = fmt.Sprintf("pid %d · %s", s.PID, detail)
	}
	return detail
}

// statusView is the /status dashboard. It is a read-only modal.
type statusView struct {
	th   theme.Theme
	be   Backend
	tab  int
	off  int
	snap statusSnap
}

// openStatusModal opens the status dashboard for the active tab's backend.
func (m *Model) openStatusModal() {
	m.pushModal(newStatusView(m.th, m.cur().be))
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
	return "Tab/←→ tabs · ↑↓ PgUp/PgDn scroll · Esc close"
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
	switch km.String() {
	case "esc":
		return v, nil, true
	case "tab", "right":
		v.switchTab(1)
	case "shift+tab", "left":
		v.switchTab(-1)
	case "up":
		v.scroll(-1)
	case "down":
		v.scroll(1)
	case "pgup":
		v.scroll(-statusPage)
	case "pgdown":
		v.scroll(statusPage)
	}
	return v, nil, false
}

// switchTab moves to the next (d = 1) or previous (d = -1) tab, wrapping, and
// starts it at the top.
func (v *statusView) switchTab(d int) {
	v.tab = (v.tab + d + len(statusTabs)) % len(statusTabs)
	v.off = 0
}

// scroll moves the tab's view by d lines. The upper bound is clamped when the
// tab is drawn, because it depends on the frame size.
func (v *statusView) scroll(d int) {
	v.off = max(0, v.off+d)
}

// View implements Modal.
func (v *statusView) View(w, h int) string {
	v.fresh()
	head := []string{modalTabs(v.th, statusTabs, v.tab), ""}
	body := v.lines(w)
	return scrollList(head, body, nil, w, h, &v.off, v.off)
}

// lines is the body of the active tab.
func (v *statusView) lines(w int) []string {
	switch v.tab {
	case 1:
		return v.usageLines(w)
	case 2:
		return v.serverLines(w)
	}
	return v.overviewLines(w)
}

// section is a heading inside a tab.
func (v *statusView) section(title string) string {
	return fgb(v.th.BrandSecondary).Render(title)
}

// row is one label and value line, cut to the frame width.
func (v *statusView) row(label, value string, w int) string {
	lab := fg(v.th.TextMuted).Render(padRight(label, statusLabelW))
	val := fg(v.th.TextPrimary).Render(value)
	return render.Truncate(lab+val, w)
}

// overviewLines are the session, the model, the mode and the work state.
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
	if s.jobs > 0 {
		state += fmt.Sprintf(", %d background%s", s.jobs, plural(s.jobs, " job", " jobs"))
	}
	provider := s.keyState
	if s.provider != "" {
		provider = s.provider + " · " + s.keyState
	}
	return []string{
		v.section("Session"),
		v.row("Session", session, w),
		v.row("Workdir", s.workdir, w),
		v.row("Version", s.version, w),
		"",
		v.section("Model"),
		v.row("Model", s.model, w),
		v.row("Provider", provider, w),
		v.row("Reasoning", orWord(s.reasoning, "default"), w),
		v.row("Mode", s.mode, w),
		"",
		v.section("Work"),
		v.row("State", state, w),
		v.row("Git", gitText(s.git), w),
	}
}

// gitText is the branch, with the changed-file count and any ahead or behind.
func gitText(g GitStatus) string {
	branch := g.Branch
	if branch == "" {
		branch = "(no branch)"
	}
	if g.Dirty {
		branch += fmt.Sprintf(" · %d changed", len(g.Changed))
	} else if g.Branch != "" {
		branch += " · clean"
	}
	if g.Ahead > 0 {
		branch += fmt.Sprintf(" · %d ahead", g.Ahead)
	}
	if g.Behind > 0 {
		branch += fmt.Sprintf(" · %d behind", g.Behind)
	}
	return branch
}

// usageLines are the context fill, the spend against the daily budget, and the
// subagents of the newest dispatch.
func (v *statusView) usageLines(w int) []string {
	s := v.snap
	ctx := "no window reported"
	if s.window > 0 {
		frac := float64(s.used) / float64(s.window)
		ctx = fmt.Sprintf("%s of %s (%d%%) %s", render.HumanTokens(s.used), render.HumanTokens(s.window), pct(s.used, s.window), bar(frac, 20))
	}
	d := s.detail
	u := s.usage
	budget := fmt.Sprintf("%s spent today, no daily cap", render.HumanTokens(d.Today))
	if d.DailyBudget > 0 {
		budget = fmt.Sprintf("%s of %s today (%d%%)", render.HumanTokens(d.Today), render.HumanTokens(d.DailyBudget), pct(d.Today, d.DailyBudget))
	}
	return []string{
		v.section("Context"),
		v.row("Window", ctx, w),
		v.row("System", render.HumanTokens(d.System)+" (estimate)", w),
		v.row("Tools", render.HumanTokens(d.Tools)+" (estimate)", w),
		v.row("Conversation", render.HumanTokens(d.Conversation)+" (estimate)", w),
		"",
		v.section("Spend"),
		v.row("Tokens", fmt.Sprintf("%s in · %s out · %s cache read · %s cache write",
			render.HumanTokens(u.InputTokens), render.HumanTokens(u.OutputTokens),
			render.HumanTokens(u.CacheReadTokens), render.HumanTokens(u.CacheWriteTokens)), w),
		v.row("Cost", fmt.Sprintf("$%.4f", s.cost), w),
		v.row("Budget", budget, w),
		"",
		v.section("Subagents"),
		v.row("Dispatch", subagentText(s.subagents), w),
	}
}

// subagentText counts subagents by status, or says there are none.
func subagentText(statuses []string) string {
	if len(statuses) == 0 {
		return "none"
	}
	counts := map[string]int{}
	for _, st := range statuses {
		counts[st]++
	}
	var parts []string
	for _, st := range []string{"running", "pending", "done", "error"} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	return fmt.Sprintf("%d total: %s", len(statuses), strings.Join(parts, ", "))
}

// serverLines are the MCP servers, the language servers, the code map and the
// memory cleanup count.
func (v *statusView) serverLines(w int) []string {
	s := v.snap
	out := []string{v.section("MCP servers")}
	out = append(out, v.serverList(s.mcp, "none configured", w)...)
	out = append(out, "", v.section("Language servers"))
	out = append(out, v.serverList(s.lsp, "none", w)...)
	out = append(out, "", v.section("Code map"))
	out = append(out, v.row("Map", v.mapText(), w))
	out = append(out, "", v.section("Memory"))
	out = append(out, v.row("To review", fmt.Sprintf("%d (not used in 90 days, not pinned)", s.stale), w))
	return out
}

// serverRows draws one line per server, or empty as one line when there are none.
func (v *statusView) serverList(rows []statusRow, empty string, w int) []string {
	if len(rows) == 0 {
		return []string{v.row("", empty, w)}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		line := fg(v.th.TextPrimary).Render(padRight(r.name, 22)) +
			fg(v.th.TextMuted).Render(padRight(r.state, 10)) +
			fg(v.th.TextDim).Render(r.detail)
		out = append(out, render.Truncate(line, w))
	}
	return out
}

// mapText is the code map size, or its build progress while it builds.
func (v *statusView) mapText() string {
	m := v.snap.mapStats
	if m.Building {
		return fmt.Sprintf("building, %d%%", int(m.Progress*100))
	}
	return fmt.Sprintf("%s files · %s symbols · %s edges", thousands(m.Files), thousands(m.Symbols), thousands(m.Edges))
}
