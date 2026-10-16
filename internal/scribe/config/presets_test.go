package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture points Home at a temp dir and returns the project root.
func fixture(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	root = t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	return home, root
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMergeReasoningByProviderFieldwise(t *testing.T) {
	base := Config{ReasoningByProvider: map[string]Reasoning{
		"anthropic": {Effort: "low", Budget: 4000, ServiceTier: "auto"},
		"openai":    {Effort: "high"},
	}}
	over := Config{ReasoningByProvider: map[string]Reasoning{
		"anthropic": {Budget: 8000},
		"google":    {Effort: "medium"},
	}}
	got := Merge(base, over).ReasoningByProvider
	want := map[string]Reasoning{
		"anthropic": {Effort: "low", Budget: 8000, ServiceTier: "auto"},
		"openai":    {Effort: "high"},
		"google":    {Effort: "medium"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge:\n got %+v\nwant %+v", got, want)
	}
	if base.ReasoningByProvider["anthropic"].Budget != 4000 {
		t.Fatal("merge must not write into the base map")
	}
	if Merge(Config{}, Config{}).ReasoningByProvider != nil {
		t.Fatal("two empty maps should merge to nil")
	}
}

func TestReasoningJSONRoundTrip(t *testing.T) {
	in := `{"reasoning_by_provider":{"anthropic":{"effort":"medium","budget":8000,"service_tier":"auto"}}}`
	var c Config
	if err := jsonUnmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	want := Reasoning{Effort: "medium", Budget: 8000, ServiceTier: "auto"}
	if c.ReasoningByProvider["anthropic"] != want {
		t.Fatalf("parsed %+v want %+v", c.ReasoningByProvider["anthropic"], want)
	}
}

func TestListPresetsProjectOverridesGlobal(t *testing.T) {
	home, root := fixture(t)
	writeFile(t, filepath.Join(home, "presets", "fast.json"), `{"model":"openai/gpt-6"}`)
	writeFile(t, filepath.Join(home, "presets", "careful.json"), `{"mode":"plan"}`)
	writeFile(t, filepath.Join(ProjectPresetDir(root), "fast.json"), `{"model":"anthropic/claude-sonnet-5"}`)

	ps, errs := ListPresets(root)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	names := []string{}
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"careful", "fast"}) {
		t.Fatalf("names %v", names)
	}
	fast, err := FindPreset(root, "fast")
	if err != nil {
		t.Fatal(err)
	}
	if !fast.Project || fast.Config.Model != "anthropic/claude-sonnet-5" {
		t.Fatalf("project preset should win: %+v", fast)
	}
}

func TestListPresetsSkipsBadFilesAndReportsThem(t *testing.T) {
	home, root := fixture(t)
	writeFile(t, filepath.Join(home, "presets", "good.json"), `{"mode":"auto"}`)
	writeFile(t, filepath.Join(home, "presets", "Bad_Name.json"), `{}`)
	writeFile(t, filepath.Join(home, "presets", "broken.json"), `{not json`)
	writeFile(t, filepath.Join(home, "presets", "notes.txt"), `ignored`)

	ps, errs := ListPresets(root)
	if len(ps) != 1 || ps[0].Name != "good" {
		t.Fatalf("only good should load, got %+v", ps)
	}
	if len(errs) != 2 {
		t.Fatalf("want 2 reported errors (bad name, bad json), got %v", errs)
	}
}

func TestPresetSizeCapAndSymlinkRefused(t *testing.T) {
	home, root := fixture(t)
	big := strings.Repeat(" ", MaxPresetBytes+1)
	writeFile(t, filepath.Join(home, "presets", "huge.json"), big)
	writeFile(t, filepath.Join(home, "presets", "real.json"), `{"mode":"auto"}`)
	if err := os.Symlink(filepath.Join(home, "presets", "real.json"), filepath.Join(home, "presets", "link.json")); err != nil {
		t.Fatal(err)
	}
	ps, errs := ListPresets(root)
	if len(ps) != 1 || ps[0].Name != "real" {
		t.Fatalf("only real should load, got %+v", ps)
	}
	if len(errs) != 2 {
		t.Fatalf("huge and link should be reported, got %v", errs)
	}
}

func TestFindPresetErrorListsNames(t *testing.T) {
	home, root := fixture(t)
	writeFile(t, filepath.Join(home, "presets", "fast.json"), `{}`)
	_, err := FindPreset(root, "slow")
	if err == nil || !strings.Contains(err.Error(), "have: fast") {
		t.Fatalf("error should list existing presets, got %v", err)
	}
	_, err = FindPreset(t.TempDir(), "x")
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadWithPresetOverlaysLoadedConfig(t *testing.T) {
	home, root := fixture(t)
	writeFile(t, filepath.Join(home, "config.json"), `{"model":"openai/gpt-6","mode":"default","max_tokens":2000}`)
	writeFile(t, filepath.Join(home, "presets", "plan.json"),
		`{"mode":"plan","reasoning_by_provider":{"anthropic":{"effort":"high"}}}`)

	c, err := LoadWithPreset(root, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "plan" {
		t.Fatalf("preset mode should win, got %q", c.Mode)
	}
	if c.Model != "openai/gpt-6" || c.MaxTokens != 2000 {
		t.Fatalf("preset must not clear keys it does not set: %+v", c)
	}
	if c.ReasoningByProvider["anthropic"].Effort != "high" {
		t.Fatalf("reasoning from preset missing: %+v", c.ReasoningByProvider)
	}

	plain, err := LoadWithPreset(root, "")
	if err != nil || plain.Mode != "default" {
		t.Fatalf("empty name must be plain Load, got %+v %v", plain, err)
	}
	if _, err := LoadWithPreset(root, "nope"); err == nil {
		t.Fatal("unknown preset should be an error, not a silent no-op")
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
