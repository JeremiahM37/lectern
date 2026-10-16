package tui

// The Diagnose popup (/diagnose): one pass over the parts of Scribe that can
// be checked from the backend. The checks run in the background, so the
// popup opens at once and fills in. r runs them again.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "diagnose", Desc: "Health check: providers, language and MCP servers, git, disk", Group: "Intelligence",
		Run: func(m *Model, _ string) tea.Cmd {
			d, ok := m.cur().be.(Diagnoser)
			if !ok {
				m.openInfo("Diagnose", "bug", m.diagnoseLines())
				return nil
			}
			p := &diagnosePopup{th: m.th, be: d, loading: true}
			m.pushModal(p)
			return p.run()
		}})
}

// DiagnoseCheck is one line of the Diagnose popup.
type DiagnoseCheck struct {
	Area   string // Providers, Language servers, MCP servers, Git, Disk
	Name   string
	State  string // ok, warn, fail or info
	Detail string
}

// Diagnoser is optionally implemented by a Backend that can run the checks.
// Each check must return promptly; a check that cannot answer says so.
type Diagnoser interface {
	Diagnose() []DiagnoseCheck
}

// diagnoseDone carries the results of one run back to the popup.
type diagnoseDone struct{ checks []DiagnoseCheck }

type diagnosePopup struct {
	th      theme.Theme
	be      Diagnoser
	checks  []DiagnoseCheck
	loading bool
	off     int
}

// run starts the checks in the background.
func (p *diagnosePopup) run() tea.Cmd {
	be := p.be
	return func() tea.Msg { return diagnoseDone{checks: be.Diagnose()} }
}

// Title implements Modal.
func (p *diagnosePopup) Title() string { return "Diagnose" }

// Icon implements Iconed.
func (p *diagnosePopup) Icon() string { return "bug" }

// Footer implements Footered.
func (p *diagnosePopup) Footer() string { return "↑↓ scroll · r run again · Esc close" }

// Update implements Modal.
func (p *diagnosePopup) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case diagnoseDone:
		p.checks, p.loading, p.off = msg.checks, false, 0
		return p, nil, false
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return p, nil, true
		case "r":
			if !p.loading {
				p.loading = true
				return p, p.run(), false
			}
		case "up", "k":
			p.off = max(0, p.off-1)
		case "down", "j":
			p.off++
		case "pgup":
			p.off = max(0, p.off-8)
		case "pgdown":
			p.off += 8
		}
	}
	return p, nil, false
}

// View implements Modal.
func (p *diagnosePopup) View(w, h int) string {
	th := p.th
	if p.loading && len(p.checks) == 0 {
		return strings.Join(fitLines([]string{fg(th.TextFaint).Render("  Checking providers, servers, git and disk…")}, w, h), "\n")
	}
	var rows []string
	area := ""
	for _, c := range p.checks {
		if c.Area != area {
			if area != "" {
				rows = append(rows, "")
			}
			area = c.Area
			rows = append(rows, fgb(th.TextSecondary).Render(area))
		}
		glyph, col := diagnoseGlyph(th, c.State)
		line := "  " + fg(col).Render(glyph) + " " + fg(th.TextPrimary).Render(padRight(c.Name, 18))
		if c.Detail != "" {
			line += " " + fg(th.TextDim).Render(render.Truncate(oneLine(c.Detail), max(8, w-26)))
		}
		rows = append(rows, render.Truncate(line, w))
	}
	if len(rows) == 0 {
		rows = []string{fg(th.TextFaint).Render("  Nothing to check yet.")}
	}
	if p.loading {
		rows = append(rows, "", fg(th.TextFaint).Render("  Checking again…"))
	}
	p.off = min(p.off, max(0, len(rows)-h))
	end := min(len(rows), p.off+h)
	return strings.Join(fitLines(rows[p.off:end], w, h), "\n")
}

// diagnoseGlyph is the marker and colour for a check's state: ✓ ok, ! warn,
// ✗ fail, and · for information that is not a problem.
func diagnoseGlyph(th theme.Theme, state string) (string, lipgloss.TerminalColor) {
	switch state {
	case "ok":
		return "✓", th.Success
	case "warn":
		return "!", th.Warning
	case "fail":
		return "✗", th.Error
	}
	return "·", th.TextDim
}
