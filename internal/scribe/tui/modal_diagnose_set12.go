package tui

// Wave 12: /diagnose also runs the parser and semantic-index probes. The
// backend answers them through IndexDiagnoser; the popup itself and its
// Diagnoser contract stay in modal_diagnose.go. This file wraps that
// Diagnoser so the one popup shows both sets of checks.

import (
	tea "github.com/charmbracelet/bubbletea"
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
