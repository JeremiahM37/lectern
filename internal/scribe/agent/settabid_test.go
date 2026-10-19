package agent

import "testing"

// The TUI sets the tab id after the agent exists; entries logged afterwards
// must carry it, and entries with no tab set must not gain one.
func TestSetTabIDTagsModelLogEntries(t *testing.T) {
	a := &Agent{}
	if got := a.withTab(map[string]any{"k": 1}); got[metaTab] != nil {
		t.Fatalf("no tab set, got %v", got[metaTab])
	}
	a.SetTabID("tab-3")
	got := a.withTab(map[string]any{"k": 1})
	if got[metaTab] != "tab-3" || got["k"] != 1 {
		t.Fatalf("tab not tagged: %v", got)
	}
}
