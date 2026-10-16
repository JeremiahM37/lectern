package tui

// Row controls for the Picker: a toggle row (an on/off switch Enter or Space
// flips), a selector row (Enter steps through Options), and the "Save to"
// scope bar that picks project or global for the settings the picker writes.
// The picker owns the state of these rows, so a caller reads it back from
// the item it is handed in OnSelect.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// PickKind is how a Picker row behaves when it is activated.
type PickKind int

const (
	// PickPlain is an ordinary row: Enter hands it to OnSelect unchanged.
	PickPlain PickKind = iota
	// PickToggle is an on/off row. Enter or Space flips On first.
	PickToggle
	// PickSelect is a selector row. Enter steps Choice through Options first.
	PickSelect
)

// scopeKey cycles the "Save to" scope bar.
const scopeKey = "ctrl+o"

// activate advances the highlighted row's control and stores the new state in
// the item list. It reports false when the cursor is not on a row.
func (p *Picker) activate() (PickItem, bool) {
	it, ok := p.Selected()
	if !ok {
		return it, false
	}
	idx := p.visible[p.cursor]
	switch it.Kind {
	case PickToggle:
		it.On = !it.On
	case PickSelect:
		if n := len(it.Options); n > 0 {
			it.Choice = (it.Choice + 1) % n
		}
	default:
		return it, true
	}
	p.Items[idx] = it
	return it, true
}

// controlText is the control at the start of a row: a checkbox for a toggle, the chosen option between arrows for a selector.
func controlText(th theme.Theme, it PickItem) string {
	switch it.Kind {
	case PickToggle:
		return Checkbox(th, it.On)
	case PickSelect:
		val := ""
		if it.Choice >= 0 && it.Choice < len(it.Options) {
			val = it.Options[it.Choice]
		}
		return fgb(th.BrandSecondary).Render("‹ " + val + " ›")
	}
	return ""
}

// Scope is the name the "Save to" bar selects, or "" when the picker has no bar.
func (p *Picker) Scope() string {
	if len(p.ScopeOptions) == 0 {
		return ""
	}
	return p.ScopeOptions[p.scopeIdx]
}

// SetScope selects the scope option with the given name, if there is one.
func (p *Picker) SetScope(name string) {
	for i, opt := range p.ScopeOptions {
		if opt == name {
			p.scopeIdx = i
		}
	}
}

// cycleScope moves the scope bar to its next option and tells OnScope.
func (p *Picker) cycleScope() tea.Cmd {
	if len(p.ScopeOptions) == 0 {
		return nil
	}
	p.scopeIdx = (p.scopeIdx + 1) % len(p.ScopeOptions)
	if p.OnScope != nil {
		return p.OnScope(p.Scope())
	}
	return nil
}

// scopeBar draws the "Save to" line with the selected scope bracketed. It is
// empty when the picker has no scope options.
func (p *Picker) scopeBar() string {
	if len(p.ScopeOptions) == 0 {
		return ""
	}
	th := p.th
	parts := []string{fg(th.TextSecondary).Render("Save to")}
	for i, opt := range p.ScopeOptions {
		if i == p.scopeIdx {
			parts = append(parts, fgb(th.BrandSecondary).Render("["+opt+"]"))
		} else {
			parts = append(parts, fg(th.TextDim).Render(opt))
		}
	}
	return strings.Join(parts, "  ") + fg(th.TextFaint).Render("  "+scopeKey+" change")
}

// controlActive reports whether the highlighted row is a live toggle or selector.
func (p *Picker) controlActive() bool {
	it, ok := p.Selected()
	return ok && it.Kind != PickPlain && !it.Disabled
}
