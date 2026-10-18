package tui

// Wave 12: /diagnose also runs the parser and semantic-index probes. The
// backend answers them through IndexDiagnoser; the popup itself and its
// Diagnoser contract stay in modal_diagnose.go. This file wraps that
// Diagnoser so the one popup shows both sets of checks.
//
// Wave 25 (REAUDIT4 #4): /diagnose opens the Health Check popup when the
// backend implements HealthChecker. That popup shows one block per backend,
// each probe with a pass, warn or fail state, its latency and a one-line
// reason. The older checklist stays as the fallback for backends without it.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// IndexDiagnoser is optionally implemented by a Backend that can probe the
// code map. Each check must return promptly; a probe that cannot run says so
// in its Detail. The areas are "Parsers" and "Semantic index".
type IndexDiagnoser interface {
	DiagnoseIndex() []DiagnoseCheck
}

// RoutingDiagnoser is optionally implemented by a Backend that can say which
// model each task-router slot uses and whether its provider is reachable. The
// area is "Model routing". It sends no model request.
type RoutingDiagnoser interface {
	DiagnoseRouting() []DiagnoseCheck
}

// HealthProbe is one probe under a backend in the Health Check popup.
type HealthProbe struct {
	Name   string
	State  string // pass, warn, fail or info (a check that does not apply)
	Reason string // one line: what the probe found
	Fix    string // one line: what to do about a warn or fail
	Ms     int    // how long the probe took; -1 when it was not timed
}

// HealthBackend is one block in the Health Check popup: a provider, the
// router, the language servers, the code map and so on.
type HealthBackend struct {
	Name   string
	Probes []HealthProbe
}

// HealthChecker is optionally implemented by a Backend that runs the Health
// Check. It must return within a few seconds and must not send a model
// request.
type HealthChecker interface {
	HealthChecks() []HealthBackend
}

// combinedDiagnoser runs the base checks, then the index probes, then the
// routing probe when the backend has one.
type combinedDiagnoser struct {
	base  Diagnoser
	index IndexDiagnoser
}

// Diagnose implements Diagnoser.
func (c combinedDiagnoser) Diagnose() []DiagnoseCheck {
	out := c.base.Diagnose()
	out = append(out, c.index.DiagnoseIndex()...)
	if r, ok := c.index.(RoutingDiagnoser); ok {
		out = append(out, r.DiagnoseRouting()...)
	}
	return out
}

func init() {
	overrideCommands(&command{Name: "diagnose", Desc: "Health check: providers, language and MCP servers, git, disk, parsers, semantic index", Group: "Intelligence",
		Run: func(m *Model, _ string) tea.Cmd {
			be := m.cur().be
			if hc, ok := be.(HealthChecker); ok {
				p := &healthPopup{th: m.th, be: hc, loading: true}
				m.pushModal(p)
				return p.run()
			}
			d, ok := be.(Diagnoser)
			if !ok {
				m.openInfo("Diagnose", "bug", m.diagnoseLines())
				return nil
			}
			if ix, ok := be.(IndexDiagnoser); ok {
				d = combinedDiagnoser{base: d, index: ix}
			}
			p := &diagnosePopup{th: m.th, be: d, loading: true}
			m.pushModal(p)
			return p.run()
		}})
}

// healthDone carries one run's results back to the popup. gen says which run
// produced them, so a slow earlier run cannot overwrite a newer one.
type healthDone struct {
	gen      int
	backends []HealthBackend
}

// healthPopup is the Health Check modal. It opens at once, runs the probes in
// the background and re-runs on r. Keys stay live while it runs, so Esc and r
// always work.
type healthPopup struct {
	th       theme.Theme
	be       HealthChecker
	backends []HealthBackend
	loading  bool
	gen      int
	off      int
}

// run starts the probes in the background.
func (p *healthPopup) run() tea.Cmd {
	be, gen := p.be, p.gen
	return func() tea.Msg { return healthDone{gen: gen, backends: be.HealthChecks()} }
}

// Title implements Modal.
func (p *healthPopup) Title() string { return "Health Check" }

// Icon implements Iconed.
func (p *healthPopup) Icon() string { return "brain" }

// Footer implements Footered.
func (p *healthPopup) Footer() string { return "↑↓ scroll · r re-run · Esc close" }

// Size implements Sized: the spec's frame, 65% of the width and at most 30 rows.
func (p *healthPopup) Size(cols, rows int) (int, int) {
	return min(80, max(56, cols*65/100)), min(30, max(16, rows-4))
}

// Update implements Modal. Only Esc closes; keys the popup does not use are
// swallowed.
func (p *healthPopup) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case healthDone:
		if msg.gen != p.gen {
			return p, nil, false
		}
		p.backends, p.loading, p.off = msg.backends, false, 0
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return p, nil, true
		case "r":
			p.gen++
			p.loading = true
			return p, p.run(), false
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
func (p *healthPopup) View(w, h int) string {
	th := p.th
	if p.loading && p.backends == nil {
		return strings.Join(fitLines([]string{fg(th.TextFaint).Render("  Running probes…")}, w, h), "\n")
	}
	var rows []string
	for _, b := range p.backends {
		rows = append(rows, "")
		glyph, col := healthBadge(th, b)
		rows = append(rows, render.Truncate(fg(col).Render(glyph)+" "+fgb(th.TextSecondary).Render(b.Name), w))
		if len(b.Probes) == 0 {
			rows = append(rows, "    "+fg(th.TextFaint).Render("no probes ran"))
		}
		for _, pr := range b.Probes {
			glyph, col := healthGlyph(th, pr.State)
			head := "    " + fg(col).Render(glyph) + " " + fg(th.TextPrimary).Render(padRight(pr.Name, 14)) +
				" " + fg(col).Render(padRight(pr.State, 4)) + " " + fg(th.TextDim).Render(padRight(healthMs(pr.Ms), 7))
			rows = append(rows, healthProbeLines(th, head, pr.Reason, w)...)
			if pr.Fix != "" && (pr.State == "warn" || pr.State == "fail") {
				for _, l := range wrapPrefixed(oneLine(pr.Fix), "      → ", "        ", w) {
					rows = append(rows, fg(th.TextFaint).Render(l))
				}
			}
		}
	}
	if p.loading {
		rows = append(rows, "", fg(th.TextFaint).Render("  Running probes again…"))
	}
	// The summary stays on the first line while the rows scroll under it, so
	// the counts can always be checked against the rows that are shown. Before,
	// it scrolled away with them and a counted warn row could not be matched.
	bodyH := max(0, h-1)
	p.off = min(p.off, max(0, len(rows)-bodyH))
	end := min(len(rows), p.off+bodyH)
	out := append([]string{healthSummary(th, p.backends)}, rows[p.off:end]...)
	return strings.Join(fitLines(out, w, h), "\n")
}

// healthReasonIndent is the indent of a reason that wraps onto its own lines,
// under the probe name it belongs to.
const healthReasonIndent = "      "

// healthProbeLines draws one probe row. The reason stays on the row when it
// fits to the right of the state and latency columns. Otherwise it starts on
// an indented line of its own and wraps within the popup width, so a long
// reason is read in full instead of being cut.
func healthProbeLines(th theme.Theme, head, reason string, w int) []string {
	reason = oneLine(reason)
	if reason == "" {
		return []string{render.Truncate(head, w)}
	}
	if ansi.StringWidth(head)+1+ansi.StringWidth(reason) <= w {
		return []string{head + " " + fg(th.TextDim).Render(reason)}
	}
	out := []string{head}
	for _, l := range wrapPrefixed(reason, healthReasonIndent, healthReasonIndent, w) {
		out = append(out, fg(th.TextDim).Render(l))
	}
	return out
}

// wrapPrefixed breaks text at spaces into lines at most w cells wide. The first
// line starts with first and every later line with rest, so an indent or a
// marker stays on the lines it belongs to. A word wider than a line is broken
// across lines rather than dropped.
func wrapPrefixed(text, first, rest string, w int) []string {
	var out []string
	prefix, body := first, ""
	flush := func() {
		out = append(out, prefix+body)
		prefix, body = rest, ""
	}
	for _, word := range strings.Fields(text) {
		sep := " "
		if body == "" {
			sep = ""
		}
		if ansi.StringWidth(prefix+body+sep+word) <= w {
			body += sep + word
			continue
		}
		if body != "" {
			flush()
		}
		for ansi.StringWidth(prefix+word) > w {
			cut := max(1, w-ansi.StringWidth(prefix))
			rs := []rune(word)
			cut = min(cut, len(rs))
			body = string(rs[:cut])
			word = string(rs[cut:])
			flush()
		}
		body = word
	}
	if body != "" || len(out) == 0 {
		out = append(out, prefix+body)
	}
	return out
}

// healthSummary counts the probes by state, so the top line says how the
// machine stands before the detail below it is read.
func healthSummary(th theme.Theme, bs []HealthBackend) string {
	var pass, warn, fail int
	for _, b := range bs {
		for _, pr := range b.Probes {
			switch pr.State {
			case "pass":
				pass++
			case "warn":
				warn++
			case "fail":
				fail++
			}
		}
	}
	return "  " + fg(th.Success).Render(itoa(pass)+" pass") + fg(th.TextDim).Render(" · ") +
		fg(th.Warning).Render(itoa(warn)+" warn") + fg(th.TextDim).Render(" · ") +
		fg(th.Error).Render(itoa(fail)+" fail")
}

// healthBadge is the marker for a backend: ✗ when nothing passes and something
// fails, ◐ when some probes are not passing, ● when all pass, ○ when nothing
// applies, and ◌ when no probe has reported yet.
func healthBadge(th theme.Theme, b HealthBackend) (string, lipgloss.TerminalColor) {
	if len(b.Probes) == 0 {
		return "◌", th.TextDim
	}
	var pass, notPass, fail int
	for _, pr := range b.Probes {
		switch pr.State {
		case "pass":
			pass++
		case "fail":
			fail++
			notPass++
		case "warn":
			notPass++
		}
	}
	switch {
	case fail > 0 && pass == 0:
		return "✗", th.Error
	case notPass > 0:
		return "◐", th.Warning
	case pass > 0:
		return "●", th.Success
	}
	return "○", th.TextDim
}

// healthGlyph is the marker and colour for one probe's state.
func healthGlyph(th theme.Theme, state string) (string, lipgloss.TerminalColor) {
	switch state {
	case "pass":
		return "✓", th.Success
	case "warn":
		return "!", th.Warning
	case "fail":
		return "✗", th.Error
	}
	return "·", th.TextDim
}

// healthMs is the latency column: "12ms", or blank when the probe was not timed.
func healthMs(ms int) string {
	if ms < 0 {
		return ""
	}
	return itoa(ms) + "ms"
}
