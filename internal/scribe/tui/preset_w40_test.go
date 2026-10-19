package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// The three settings /preset used to skip now become writes: deferred tools,
// auto-check and the disabled tools. Disabled tools add to the list already in
// place, as allow and deny do.
func TestPresetWritesCarryToolAndCheckSettings(t *testing.T) {
	off, on := false, true
	p := config.Config{DeferredTools: &off, AutoCheck: &on, DisabledTools: []string{"web_search"}}
	cur := map[string]any{"disabled_tools": []string{"fetch"}}

	got := map[string]any{}
	for _, w := range presetWrites(p, cur) {
		got[w.key] = w.value
	}
	want := map[string]any{
		"deferred_tools": false,
		"auto_check":     true,
		"disabled_tools": []string{"fetch", "web_search"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writes:\n got %v\nwant %v", got, want)
	}
}

// Applying a preset that sets deferred tools writes the flag to the scope the
// preset lives in, and no "Not applied here" line is printed for it.
func TestPresetCommandAppliesDeferredToolsAndNotesNothingSkipped(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "lean", `{"deferred_tools": false}`)
	m, fb := newTestModel(t, 100, 40)

	m.runCommand("/preset lean")
	advanceChat(m)

	out := screen(m)
	if strings.Contains(out, "Not applied here") {
		t.Fatalf("nothing should be reported as skipped:\n%s", out)
	}
	if !strings.Contains(out, "Applied preset lean") {
		t.Fatalf("the result should be reported:\n%s", out)
	}
	if fb.MergedConfig7().DeferredTools == nil || *fb.MergedConfig7().DeferredTools {
		t.Fatalf("deferred_tools should now be off in the settings")
	}
}

// Removing a preset says which of its settings stay, so a setting such as
// deferred_tools is not left behind without a word (QA wave 40, item 6).
func TestPresetRemoveNamesTheSettingsThatStay(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "lean", `{"deferred_tools": true}`)
	m, _ := newTestModel(t, 140, 40)

	m.runCommand("/preset lean")
	advanceChat(m)
	m.runCommand("/preset remove lean")
	advanceChat(m)

	var out string
	for _, r := range m.cur().chat.Export() {
		if strings.Contains(r.Text, "Removed preset lean") {
			out = r.Text
		}
	}
	if !strings.Contains(out, "deferred_tools") || !strings.Contains(out, "/settings") {
		t.Fatalf("the removal should name the setting that stays: %q", out)
	}
}
