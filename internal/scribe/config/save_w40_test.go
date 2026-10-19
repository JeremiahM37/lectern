package config

import (
	"encoding/json"
	"os"
	"testing"
)

// readJSONFile decodes the global config file the test wrote.
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("saved file is not JSON: %v\n%s", err, b)
	}
	return m
}

// A save must keep keys hand-added under a known object, and must still
// clear a modelled key that the settings screen emptied.
func TestSaveKeepsUnknownKeysUnderKnownObjects(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	path := GlobalPath()
	seed := `{
  "theme": "dark",
  "mystery": {"kept": true},
  "performance": {"thinking": "adaptive", "effort": "high", "future_knob": 3},
  "retry": {"max_attempts": 4, "backoff_hint": "exp"}
}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	c := Config{Theme: "light", Performance: Performance{Thinking: "off"}}
	if err := SaveGlobal(c); err != nil {
		t.Fatal(err)
	}
	got := readJSONFile(t, path)

	if got["theme"] != "light" {
		t.Errorf("theme = %v, want light", got["theme"])
	}
	if _, ok := got["mystery"]; !ok {
		t.Error("unknown top-level key was dropped")
	}
	perf, _ := got["performance"].(map[string]any)
	if perf["thinking"] != "off" {
		t.Errorf("performance.thinking = %v, want off", perf["thinking"])
	}
	if perf["future_knob"] != float64(3) {
		t.Errorf("performance.future_knob = %v, want 3 (unknown nested key dropped)", perf["future_knob"])
	}
	if _, ok := perf["effort"]; ok {
		t.Error("performance.effort was cleared in the struct but survived the save")
	}
	// retry is a known object too, and the struct's zero value is written as
	// an empty object, so its unknown key must survive as well.
	retry, _ := got["retry"].(map[string]any)
	if retry["backoff_hint"] != "exp" {
		t.Errorf("retry.backoff_hint = %v, want exp", retry["backoff_hint"])
	}
}

// A non-object value under a known key is replaced, not merged into.
func TestSaveReplacesNonObjectUnderKnownKey(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	path := GlobalPath()
	if err := os.WriteFile(path, []byte(`{"performance": null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(Config{Performance: Performance{Speed: "fast"}}); err != nil {
		t.Fatal(err)
	}
	perf, _ := readJSONFile(t, path)["performance"].(map[string]any)
	if perf["speed"] != "fast" {
		t.Errorf("performance.speed = %v, want fast", perf["speed"])
	}
}

// The MCP start timeout round-trips through the config file in seconds.
func TestSaveKeepsMCPStartTimeout(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	c := Config{MCP: []MCPServer{{Name: "slow", Command: "x", StartTimeout: 45}}}
	if err := SaveGlobal(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MCP) != 1 || loaded.MCP[0].StartTimeout != 45 {
		t.Fatalf("MCP = %+v, want start_timeout 45", loaded.MCP)
	}
}
