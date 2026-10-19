package tui

// The status dashboard behind /status (spec 01 §8.3 StatusDashboard, spec 02
// §2.1). This file reads the backend the command was run in into one snapshot
// and holds the small formatters the sections draw from. The drawing itself,
// a left menu of sections and a right pane, is in modal_status_sections.go.

import (
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// statusRefresh is the oldest a reading may be while the dashboard is drawn.
// Git status costs a process and the server list takes a lock, and the view is
// drawn on every 50 ms tick, so the backend is read at most this often.
const statusRefresh = 2 * time.Second

// statusRow is one line of a server list: a name, its state and a short detail.
type statusRow struct {
	name, state, detail string
}

// statusJob is one background job: its command and its status.
type statusJob struct {
	command, status string
}

// statusSnap is one reading of the backend. The sections draw from it, so a
// frame does not run git or walk the server list again.
type statusSnap struct {
	at                                        time.Time
	workdir, version, sessionID, sessionTitle string
	model, reasoning, mode                    string
	provider, keyState                        string
	running                                   bool
	jobs                                      []statusJob
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
		git:          be.Git(),
		detail:       be.ContextDetail(),
		mapStats:     be.MapStats(),
		stale:        be.StaleMemories(),
	}
	for _, j := range be.Jobs() {
		s.jobs = append(s.jobs, statusJob{command: j.Command, status: j.Status})
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

// openStatusModal opens the status dashboard for the active tab's backend.
func (m *Model) openStatusModal() {
	m.pushModal(newStatusView(m.th, m.cur().be))
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

// mapText is the code map size, or its build progress while it builds.
func mapText(m MapStats) string {
	if m.Building {
		return fmt.Sprintf("building, %d%%", int(m.Progress*100))
	}
	return fmt.Sprintf("%s files · %s symbols · %s edges", thousands(m.Files), thousands(m.Symbols), thousands(m.Edges))
}

// tokenText is the token spend, one clause per kind.
func tokenText(u llm.Usage) string {
	return fmt.Sprintf("%s in · %s out · %s cache read · %s cache write",
		render.HumanTokens(u.InputTokens), render.HumanTokens(u.OutputTokens),
		render.HumanTokens(u.CacheReadTokens), render.HumanTokens(u.CacheWriteTokens))
}
