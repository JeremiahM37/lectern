package tui

// /editor settings: the eleven editor integration toggles in a picker of their
// own. Each toggle saves its editor_integration key to the global config as it
// flips; the picker stays open. The same rows also sit on the Tools tab.

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// openEditorSettings is the /editor settings picker.
func (m *Model) openEditorSettings() {
	p := NewPicker(m.th, "Editor integrations", "gear")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Enter or Space toggle · Esc close"
	rebuild := func() {
		c := fields7(m.cur().be)
		var items []PickItem
		for _, name := range config.EditorIntegrationKeys {
			lab := editorIntegrationLabels[name]
			items = append(items, PickItem{Value: name, Label: lab[0], Desc: lab[1], Kind: PickToggle, On: c.EditorIntegrationOn(name)})
		}
		p.SetItems(items)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		// The picker has already flipped it.On; save that state.
		if err := m.saveGlobalFlat("editor_integration."+it.Value, it.On); err != nil {
			return cmdErr(err)
		}
		rebuild()
		return flashCmd("info", it.Label+" "+onOff(it.On)+" (saved)")
	}
	rebuild()
	m.pushModal(p)
}
