package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookTimeoutDefaultsToTenSeconds(t *testing.T) {
	if got := HookTimeout(0); got != 10*time.Second {
		t.Fatalf("zero timeout = %v, want 10s", got)
	}
	if got := HookTimeout(-3); got != 10*time.Second {
		t.Fatalf("negative timeout = %v, want 10s", got)
	}
	if got := HookTimeout(4); got != 4*time.Second {
		t.Fatalf("explicit timeout = %v, want 4s", got)
	}
}

func TestHookFieldsParseFromConfigFile(t *testing.T) {
	var c Config
	body := `{"hooks":[{"event":"PreToolUse","matcher":"Bash","command":"x",
		"if":"Bash(git commit*)","once":true,"async":true,"status_message":"checking"}]}`
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	h := c.Hooks[0]
	if h.If != "Bash(git commit*)" || !h.Once || !h.Async || h.StatusMessage != "checking" {
		t.Fatalf("hook fields not read: %+v", h)
	}
}

func TestClampToolTimeout(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		in   *float64
		want time.Duration
	}{
		{"unset is the 2 minute default", nil, 2 * time.Minute},
		{"zero means no limit", f(0), 0},
		{"negative means no limit", f(-1), 0},
		{"below the floor is 30 seconds", f(0.1), 30 * time.Second},
		{"above the ceiling is 30 minutes", f(60), 30 * time.Minute},
		{"in range is kept", f(5), 5 * time.Minute},
	}
	for _, tc := range cases {
		if got := ClampToolTimeout(tc.in); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestModelsAPIJSON(t *testing.T) {
	var p Provider
	if err := json.Unmarshal([]byte(`{"id":"x","base_url":"http://h/v1","models_api":"http://h/models"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ModelsAPI == nil || p.ModelsAPI.URL != "http://h/models" || p.ModelsAPI.Off {
		t.Fatalf("URL form: %+v", p.ModelsAPI)
	}
	if err := json.Unmarshal([]byte(`{"id":"x","base_url":"http://h/v1","models_api":false}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ModelsAPI == nil || !p.ModelsAPI.Off {
		t.Fatalf("false form must turn listing off: %+v", p.ModelsAPI)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"models_api":false`) {
		t.Fatalf("false not written back: %s", out)
	}
	var bad Provider
	if err := json.Unmarshal([]byte(`{"models_api":5}`), &bad); err == nil {
		t.Fatal("a number must be refused")
	}
	var unset Provider
	if err := json.Unmarshal([]byte(`{"id":"x"}`), &unset); err != nil {
		t.Fatal(err)
	}
	if unset.ModelsAPI != nil {
		t.Fatal("absent models_api must stay nil (default listing)")
	}
}

func TestAgentFeatureDefaultsAndDeepMerge(t *testing.T) {
	var nilF *AgentFeatures
	if nilF.Enabled("desloppify") || !nilF.Enabled("tier_routing") {
		t.Fatal("nil features must give the documented defaults")
	}
	if nilF.Enabled("no_such_key") {
		t.Fatal("unknown key must be off")
	}
	base := Config{AgentFeatures: &AgentFeatures{}}
	if err := base.AgentFeatures.Set("desloppify", true); err != nil {
		t.Fatal(err)
	}
	over := Config{AgentFeatures: &AgentFeatures{}}
	if err := over.AgentFeatures.Set("tier_routing", false); err != nil {
		t.Fatal(err)
	}
	m := Merge(base, over).AgentFeatures
	if !m.Enabled("desloppify") {
		t.Error("base's desloppify lost in merge")
	}
	if m.Enabled("tier_routing") {
		t.Error("override's tier_routing not applied")
	}
	if !m.Enabled("dispatch_cache") || m.Enabled("verify_edits") {
		t.Error("untouched fields must keep their defaults")
	}
	if err := m.Set("bogus", true); err == nil {
		t.Error("unknown feature key must be refused")
	}
	if len(AgentFeatureKeys()) != 7 {
		t.Errorf("want 7 feature keys, got %d", len(AgentFeatureKeys()))
	}
}

func TestEditorCommandDefaultAndMerge(t *testing.T) {
	cmd, args := EditorSettings{}.CommandOrDefault()
	if cmd != "nvim" || len(args) != 0 {
		t.Fatalf("default editor = %q %v", cmd, args)
	}
	m := Merge(Config{Editor: EditorSettings{Command: "hx", Args: []string{"-v"}}},
		Config{Editor: EditorSettings{Args: []string{}}})
	cmd, args = m.Editor.CommandOrDefault()
	if cmd != "hx" {
		t.Errorf("command should survive a merge that sets only args: %q", cmd)
	}
	if len(args) != 0 {
		t.Errorf("an explicit empty args list must replace the base args: %v", args)
	}
}

func TestSearchKeyEnvDefaultsAndMerge(t *testing.T) {
	var s SearchSettings
	if s.BraveEnv() != "BRAVE_SEARCH_API_KEY" || s.JinaEnv() != "JINA_API_KEY" {
		t.Fatalf("default env names: %s %s", s.BraveEnv(), s.JinaEnv())
	}
	if s.UseJinaReader() {
		t.Fatal("the Jina reader must be off by default: it shares every URL")
	}
	off, on := false, true
	m := Merge(Config{Search: SearchSettings{JinaReader: &on, BraveKeyEnv: "B1"}},
		Config{Search: SearchSettings{JinaReader: &off}})
	if m.Search.UseJinaReader() || m.Search.BraveEnv() != "B1" {
		t.Fatalf("merge: %+v", m.Search)
	}
}

func TestPerformanceRetryAndRouterSlotsMerge(t *testing.T) {
	no := false
	base := Config{
		Performance: Performance{Thinking: "adaptive", CacheTTL: "5m", ToolStreaming: &no},
		Retry:       Retry{Transient: 3, Stall: 1},
		Router:      Router{Default: "a/x", Verify: "a/v", FallbackByModel: map[string][]string{"a/x": {"b/y"}}},
	}
	over := Config{
		Performance: Performance{CacheTTL: "1h"},
		Retry:       Retry{Stall: 2},
		Router:      Router{Desloppify: "a/d", FallbackByModel: map[string][]string{"c/z": {"d/w"}}},
	}
	m := Merge(base, over)
	if m.Performance.Thinking != "adaptive" || m.Performance.CacheTTL != "1h" {
		t.Errorf("performance: %+v", m.Performance)
	}
	if m.Performance.ToolStreaming == nil || *m.Performance.ToolStreaming {
		t.Error("unset tool_streaming in the override must keep the base value")
	}
	if m.Retry.Transient != 3 || m.Retry.Stall != 2 {
		t.Errorf("retry: %+v", m.Retry)
	}
	if m.Router.Default != "a/x" || m.Router.Verify != "a/v" || m.Router.Desloppify != "a/d" {
		t.Errorf("router slots: %+v", m.Router)
	}
	if len(m.Router.FallbackByModel) != 2 || m.Router.FallbackByModel["a/x"][0] != "b/y" {
		t.Errorf("per-model fallbacks must merge by key: %v", m.Router.FallbackByModel)
	}
}

func TestContextWindowOverridesMergeAndZeroRemoves(t *testing.T) {
	base := Config{ContextWindowOverrides: map[string]int{"p/a": 100000, "p/b": 50000}}
	over := Config{ContextWindowOverrides: map[string]int{"p/a": 0, "p/c": 8000}}
	m := Merge(base, over).ContextWindowOverrides
	if _, ok := m["p/a"]; ok {
		t.Error("a zero in the project file must remove the inherited override")
	}
	if m["p/b"] != 50000 || m["p/c"] != 8000 {
		t.Errorf("overrides: %v", m)
	}
}

func TestMergeNeverMutatesBase(t *testing.T) {
	base := Config{Forbidden: []string{"*.log"}, ContextWindowOverrides: map[string]int{"p/a": 1}}
	Merge(base, Config{Forbidden: []string{"x"}, ContextWindowOverrides: map[string]int{"p/a": 2}})
	if base.ContextWindowOverrides["p/a"] != 1 || len(base.Forbidden) != 1 {
		t.Fatal("Merge changed its base argument")
	}
}

func TestValidateReportsBadValuesOnly(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("empty config must be valid: %v", err)
	}
	good := Config{KeyPriority: "app", DiffStyle: "sidebyside", CompactStrategy: "v1",
		Performance:            Performance{Thinking: "enabled", CacheTTL: "1h", Effort: "high"},
		ContextWindowOverrides: map[string]int{"p/a": 8000},
		Router:                 Router{FallbackByModel: map[string][]string{"p/a": {"q/b"}}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid values refused: %v", err)
	}
	bad := Config{KeyPriority: "keychain", DiffStyle: "wide", CompactStrategy: "v9",
		Performance:            Performance{Thinking: "maybe", CacheTTL: "2h"},
		Retry:                  Retry{Transient: -1},
		ContextWindowOverrides: map[string]int{"p/a": -5},
		Router:                 Router{FallbackByModel: map[string][]string{"nomodel": {"q/b"}}},
		Providers:              []Provider{{ID: "x", BaseURL: "http://h", ModelsAPI: &ModelsAPI{}}}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("bad values accepted")
	}
	for _, want := range []string{"key_priority", "diff_style", "compact_strategy", "thinking", "cache_ttl",
		"retry", "context_window_overrides", "fallback_by_model", "models_api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestSettingsSurviveSaveAndLoad(t *testing.T) {
	home, root := fixture(t)
	on := true
	c := Config{
		KeyPriority:            "app",
		ContextWindowOverrides: map[string]int{"openai/gpt-x": 200000},
		Forbidden:              []string{"secrets/**"},
		Editor:                 EditorSettings{Command: "hx", Args: []string{"--clean"}},
		DiffStyle:              "compact",
		CoAuthorTrailer:        &on,
		Performance:            Performance{CacheTTL: "1h"},
		Retry:                  Retry{Stall: 2},
	}
	if err := SaveProject(root, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyPriority != "app" || got.ContextWindowOverrides["openai/gpt-x"] != 200000 ||
		len(got.Forbidden) != 1 || got.Editor.Command != "hx" || got.DiffStyle != "compact" ||
		!Bool(got.CoAuthorTrailer, false) || got.Performance.CacheTTL != "1h" || got.Retry.Stall != 2 {
		t.Fatalf("round trip lost a field: %+v", got)
	}
	_ = home
}

func TestForbiddenPatternsMergesEveryPlace(t *testing.T) {
	home, root := fixture(t)
	writeFile(t, GlobalPath(), `{"forbidden":["  *.pem ","*.log"]}`)
	writeFile(t, ProjectPath(root), `{"forbidden":["*.log","private/**"]}`)
	writeFile(t, filepath.Join(home, ForbiddenFile), `{"patterns":["keys/*"]}`)
	writeFile(t, filepath.Join(ProjectDir(root), ForbiddenFile), `{"patterns":["keys/*","tmp/**", ""]}`)

	got, err := ForbiddenPatterns(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.pem", "*.log", "private/**", "keys/*", "tmp/**"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestForbiddenPatternsFailsClosedOnBadFile(t *testing.T) {
	_, root := fixture(t)
	writeFile(t, ProjectDir(root)+"/"+ForbiddenFile, `{"patterns": [`)
	if _, err := ForbiddenPatterns(root); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("a broken forbidden.json must be an error, got %v", err)
	}
}

func TestForbiddenPatternsMissingFilesAreFine(t *testing.T) {
	_, root := fixture(t)
	got, err := ForbiddenPatterns(root)
	if err != nil || len(got) != 0 {
		t.Fatalf("no files: got %q, %v", got, err)
	}
	if got, err := ForbiddenPatterns(""); err != nil || len(got) != 0 {
		t.Fatalf("empty root: got %q, %v", got, err)
	}
}
