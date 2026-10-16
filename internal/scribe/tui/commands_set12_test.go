package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pressSpace(t *testing.T, m *Model) *Picker {
	t.Helper()
	p, ok := m.topModal().(*Picker)
	if !ok {
		t.Fatalf("expected a picker, top modal is %T", m.topModal())
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	return p
}

func TestVimHintsIsAToggleRow(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	m.vimCommand("")
	p := pressSpace(t, m)
	if len(p.Items) != 1 || p.Items[0].Kind != PickToggle {
		t.Fatalf("Vim Hints should be one toggle row, got %+v", p.Items)
	}
	last := fb.writes[len(fb.writes)-1]
	if last.Key != "vim" || last.Value != true {
		t.Fatalf("space should save vim on, last write %+v", last)
	}
}

func TestAgentFeaturesAreToggleRows(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	m.openAgentFeatures()
	p, ok := m.topModal().(*Picker)
	if !ok {
		t.Fatalf("expected a picker, top modal is %T", m.topModal())
	}
	for _, it := range p.Items {
		if it.Kind != PickToggle {
			t.Fatalf("feature row %q should be a toggle", it.Value)
		}
	}
	first := p.Items[0]
	before := fb.MergedConfig7().AgentFeatures.Enabled(first.Value)
	pressSpace(t, m)
	if after := fb.MergedConfig7().AgentFeatures.Enabled(first.Value); after == before {
		t.Fatalf("space should flip feature %q (stayed %v)", first.Value, before)
	}
}

func TestWebsearchTogglesBothWebTools(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	websearchCommand(m, "off")
	for _, n := range webToolNames {
		if fb.toolOn[n] {
			t.Fatalf("%s should be off", n)
		}
	}
	websearchCommand(m, "on")
	for _, n := range webToolNames {
		if !fb.toolOn[n] {
			t.Fatalf("%s should be on", n)
		}
	}
	if cmd := websearchCommand(m, "sideways"); cmd == nil {
		t.Fatal("a bad argument should say what to type")
	}
}

func TestBudgetSetsAndClears(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	budgetCommand(m, "2m")
	last := fb.writes[len(fb.writes)-1]
	if last.Key != "daily_token_budget" || last.Value != 2000000 {
		t.Fatalf("/budget 2m should write 2000000, got %+v", last)
	}
	budgetCommand(m, "off")
	last = fb.writes[len(fb.writes)-1]
	if last.Key != "daily_token_budget" || last.Value != 0 {
		t.Fatalf("/budget off should write 0, got %+v", last)
	}
	if cmd := budgetCommand(m, "lots"); cmd == nil {
		t.Fatal("a non-number should be reported")
	}
}
