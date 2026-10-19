package tui

import "testing"

// Closing a tab disposes that tab's last subagent bus, and only that tab's.
func TestClosingATabDisposesItsLastBus(t *testing.T) {
	m, first := newTestModel(t, 120, 30)
	second := NewFakeBackend()
	second.Delay = 0
	tb := m.addTab(second, "second")

	m.closeTab(tb)

	if got := second.Disposed(); got != 1 {
		t.Fatalf("closed tab disposed its bus %d times, want 1", got)
	}
	if got := first.Disposed(); got != 0 {
		t.Fatalf("the tab still open had its bus disposed %d times", got)
	}
	for _, x := range m.tabs {
		if x == tb {
			t.Fatal("the closed tab is still in the tab list")
		}
	}
}
