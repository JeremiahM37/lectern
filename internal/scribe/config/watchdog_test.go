package config

import (
	"strings"
	"testing"
)

func TestWatchdogSetFieldParsesSecondsAndDurations(t *testing.T) {
	cases := map[string]int{"180": 180, "180s": 180, "3m": 180, " 4m ": 240, "default": 0, "": 0}
	for in, want := range cases {
		var c Config
		if _, err := c.SetField("watchdog.first_token", in); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if c.WatchdogTimeouts.FirstToken != want {
			t.Errorf("%q: got %d, want %d", in, c.WatchdogTimeouts.FirstToken, want)
		}
	}
}

func TestWatchdogRangesAreEnforced(t *testing.T) {
	var c Config
	if _, err := c.SetField("watchdog.step", "60"); err == nil || !strings.Contains(err.Error(), "120 to 1800") {
		t.Fatalf("step 60s should say the allowed range, got %v", err)
	}
	if _, err := c.SetField("watchdog.inter_token", "5"); err == nil {
		t.Fatal("inter_token 5s must be refused")
	}
	if _, err := c.SetField("watchdog.first_token", "1.25s"); err == nil {
		t.Fatal("fractional seconds must be refused with a hint")
	}
	if _, err := c.SetField("watchdog.nope", "60"); err == nil {
		t.Fatal("unknown watchdog field must be refused")
	}
}

func TestWatchdogMergeAndClear(t *testing.T) {
	base := Config{WatchdogTimeouts: Watchdog{FirstToken: 200, InterToken: 90}}
	over := Config{WatchdogTimeouts: Watchdog{InterToken: 150}}
	m := Merge(base, over)
	if m.WatchdogTimeouts.FirstToken != 200 || m.WatchdogTimeouts.InterToken != 150 {
		t.Fatalf("merge: %+v", m.WatchdogTimeouts)
	}
	if !m.ClearField("watchdog.first_token") {
		t.Fatal("ClearField should own watchdog keys")
	}
	if m.WatchdogTimeouts.FirstToken != 0 {
		t.Fatal("ClearField did not reset first_token")
	}
}

func TestEditorIntegrationDefaultsAndToggle(t *testing.T) {
	var c Config
	if !c.EditorIntegrationOn("diagnostics") {
		t.Fatal("diagnostics should default on")
	}
	if c.EditorIntegrationOn("sync_on_edit") {
		t.Fatal("sync_on_edit should default off")
	}
	if _, err := c.SetField("editor_integration.hover", "off"); err != nil {
		t.Fatal(err)
	}
	if c.EditorIntegrationOn("hover") {
		t.Fatal("hover should be off")
	}
	if _, err := c.SetField("editor_integration.sync_on_edit", "on"); err != nil {
		t.Fatal(err)
	}
	if !c.EditorIntegrationOn("sync_on_edit") {
		t.Fatal("sync_on_edit should be on")
	}
	if _, err := c.SetField("editor_integration.hover", "default"); err != nil {
		t.Fatal(err)
	}
	if !c.EditorIntegrationOn("hover") {
		t.Fatal("default should restore hover")
	}
	if _, err := c.SetField("editor_integration.bogus", "on"); err == nil {
		t.Fatal("unknown integration must be refused")
	}
	if _, err := c.SetField("editor_integration.hover", "maybe"); err == nil {
		t.Fatal("a non-switch value must be refused")
	}
}

func TestEditorIntegrationMergePerKey(t *testing.T) {
	base := Config{EditorIntegrations: map[string]bool{"hover": false, "rename": false}}
	over := Config{EditorIntegrations: map[string]bool{"rename": true}}
	m := Merge(base, over)
	if m.EditorIntegrations["hover"] || !m.EditorIntegrations["rename"] {
		t.Fatalf("merge: %v", m.EditorIntegrations)
	}
}

func TestCompactKeepRecentFlatKey(t *testing.T) {
	var c Config
	if _, err := c.SetField("compact_keep_recent", "6"); err != nil || c.CompactKeepRecent != 6 {
		t.Fatalf("keep recent: %v %d", err, c.CompactKeepRecent)
	}
	c.ClearField("compact_keep_recent")
	if c.CompactKeepRecent != 0 {
		t.Fatal("ClearField did not reset keep recent")
	}
}
