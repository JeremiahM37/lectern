package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSetFieldScalarsValidateAndStore(t *testing.T) {
	var c Config
	must := func(key string, v any) {
		t.Helper()
		if ok, err := c.SetField(key, v); !ok || err != nil {
			t.Fatalf("SetField(%s, %v) = %v, %v", key, v, ok, err)
		}
	}
	must("diff_style", "sidebyside")
	must("collapse_diffs", "on")
	must("compact_strategy", "disabled")
	must("tool_timeout", "0.5")
	must("router.verify", "openai/gpt-6")
	must("router.semantic", "ollama/qwen")
	must("key_priority", "app")
	if c.DiffStyle != "sidebyside" || c.CollapseDiffs == nil || !*c.CollapseDiffs {
		t.Fatalf("diff settings = %q %v", c.DiffStyle, c.CollapseDiffs)
	}
	if c.ToolTimeout == nil || *c.ToolTimeout != 0.5 {
		t.Fatalf("tool timeout = %v", c.ToolTimeout)
	}
	if c.Router.Verify != "openai/gpt-6" || c.Router.Semantic != "ollama/qwen" || c.KeyPriority != "app" {
		t.Fatalf("router/key = %+v %q", c.Router, c.KeyPriority)
	}
	for key, bad := range map[string]any{
		"diff_style":       "wide",
		"compact_strategy": "v9",
		"tool_timeout":     "soon",
		"key_priority":     "keychain",
		"collapse_diffs":   "maybe",
	} {
		if ok, err := c.SetField(key, bad); !ok || err == nil {
			t.Errorf("SetField(%s, %v) should be refused, got handled=%v err=%v", key, bad, ok, err)
		}
	}
}

func TestSetFieldToolTimeoutDefaultAndZero(t *testing.T) {
	var c Config
	c.SetField("tool_timeout", 5.0)
	c.SetField("tool_timeout", "default")
	if c.ToolTimeout != nil {
		t.Fatal("'default' must clear the tool timeout")
	}
	c.SetField("tool_timeout", 0)
	if c.ToolTimeout == nil || ClampToolTimeout(c.ToolTimeout) != 0 {
		t.Fatal("0 must mean no limit")
	}
}

func TestSetFieldMapKeysKeepDottedModelRefs(t *testing.T) {
	var c Config
	ref := "ollama/qwen3.6:35b-a3b"
	if ok, err := c.SetField(keyFallbackByModel+ref, "openai/gpt-6, anthropic/haiku"); !ok || err != nil {
		t.Fatalf("fallback: %v %v", ok, err)
	}
	if got := c.Router.FallbackByModel[ref]; len(got) != 2 || got[1] != "anthropic/haiku" {
		t.Fatalf("fallback chain = %v", got)
	}
	if _, err := c.SetField(keyFallbackByModel+"noslash", "x"); err == nil {
		t.Fatal("a model without a provider must be refused")
	}
	c.SetField(keyFallbackByModel+ref, []string{})
	if _, ok := c.Router.FallbackByModel[ref]; ok {
		t.Fatal("an empty chain removes the entry")
	}
	c.SetField(keyContextWindow+ref, 32768)
	if c.ContextWindowOverrides[ref] != 32768 {
		t.Fatalf("context window = %v", c.ContextWindowOverrides)
	}
	c.SetField(keyContextWindow+ref, 0)
	if _, ok := c.ContextWindowOverrides[ref]; ok {
		t.Fatal("0 removes the context override")
	}
}

func TestSetFieldAgentFeaturesToggleAndReset(t *testing.T) {
	var c Config
	if ok, err := c.SetField(keyAgentFeature+"desloppify", true); !ok || err != nil {
		t.Fatal(err)
	}
	if !c.AgentFeatures.Enabled("desloppify") {
		t.Fatal("desloppify should be on")
	}
	c.SetField(keyAgentFeature+"desloppify", nil)
	if c.AgentFeatures.Desloppify != nil || c.AgentFeatures.Enabled("desloppify") {
		t.Fatal("nil resets the feature to its default")
	}
	if _, err := c.SetField(keyAgentFeature+"nope", true); err == nil {
		t.Fatal("unknown feature must be refused")
	}
}

func TestSetFieldEditorSearchRetryAndPerformance(t *testing.T) {
	var c Config
	c.SetField("editor.command", "hx")
	c.SetField("editor.args", "--clean, -v")
	if cmd, args := c.Editor.CommandOrDefault(); cmd != "hx" || len(args) != 2 || args[0] != "--clean" {
		t.Fatalf("editor = %q %v", cmd, args)
	}
	if _, err := c.SetField("editor.command", "nvim -u NONE"); err == nil {
		t.Fatal("a command with spaces must be refused")
	}
	c.SetField("search.jina_reader", true)
	if !c.Search.UseJinaReader() {
		t.Fatal("jina reader should be on")
	}
	if _, err := c.SetField("search.brave_key_env", "BAD NAME"); err == nil {
		t.Fatal("env names cannot hold spaces")
	}
	c.SetField("retry.transient", 5)
	c.SetField("retry.stall", "2")
	if c.Retry.Transient != 5 || c.Retry.Stall != 2 {
		t.Fatalf("retry = %+v", c.Retry)
	}
	if _, err := c.SetField("retry.transient", -1); err == nil {
		t.Fatal("negative retries must be refused")
	}
	c.SetField("performance.cache_ttl", "1h")
	c.SetField("performance.tool_streaming", false)
	if c.Performance.CacheTTL != "1h" || c.Performance.ToolStreaming == nil || *c.Performance.ToolStreaming {
		t.Fatalf("performance = %+v", c.Performance)
	}
	if _, err := c.SetField("performance.cache_ttl", "2h"); err == nil {
		t.Fatal("cache TTL must be 5m or 1h")
	}
}

func TestSetFieldUnownedKeysAreNotHandled(t *testing.T) {
	var c Config
	for _, key := range []string{"verbose", "model", "co_author_trailer", "instructions", "router.explore"} {
		if ok, _ := c.SetField(key, "x"); ok {
			t.Errorf("%s belongs to another setter and must not be handled here", key)
		}
	}
}

func TestClearFieldResetsAndReportsOwnership(t *testing.T) {
	c := Config{DiffStyle: "compact", ToolTimeout: new(float64), Retry: Retry{Transient: 2}}
	c.Router.FallbackByModel = map[string][]string{"a/b": {"c/d"}}
	if !c.ClearField("diff_style") || c.DiffStyle != "" {
		t.Fatal("diff_style not cleared")
	}
	if !c.ClearField("tool_timeout") || c.ToolTimeout != nil {
		t.Fatal("tool_timeout not cleared")
	}
	if !c.ClearField("retry.transient") || c.Retry.Transient != 0 {
		t.Fatal("retry not cleared")
	}
	if !c.ClearField(keyFallbackByModel+"a/b") || len(c.Router.FallbackByModel) != 0 {
		t.Fatal("fallback entry not cleared")
	}
	if c.ClearField("verbose") {
		t.Fatal("verbose is not this file's key")
	}
}

func TestNewSettingsSurviveSaveAndLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	var c Config
	c.SetField("diff_style", "compact")
	c.SetField("tool_timeout", 3)
	c.SetField(keyAgentFeature+"verify_edits", true)
	c.SetField(keyFallbackByModel+"ollama/qwen3.6:35b-a3b", "openai/gpt-6")
	c.SetField("forbidden", "secrets/**, .env")
	c.SetField("search.brave_key_env", "MY_BRAVE")
	if err := SaveGlobal(c); err != nil {
		t.Fatal(err)
	}
	back, err := LoadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if back.DiffStyle != "compact" || back.ToolTimeout == nil || *back.ToolTimeout != 3 {
		t.Fatalf("round trip lost scalars: %+v", back)
	}
	if !back.AgentFeatures.Enabled("verify_edits") || back.Router.FallbackByModel["ollama/qwen3.6:35b-a3b"][0] != "openai/gpt-6" {
		t.Fatal("round trip lost feature or fallback")
	}
	if strings.Join(back.Forbidden, "|") != "secrets/**|.env" || back.Search.BraveEnv() != "MY_BRAVE" {
		t.Fatalf("round trip lost forbidden or search env: %v %q", back.Forbidden, back.Search.BraveEnv())
	}
	// No secret value ever lands in config.json: it only names env variables.
	raw, _ := os.ReadFile(GlobalPath())
	if strings.Contains(string(raw), "api_key") || !json.Valid(raw) {
		t.Fatalf("config.json unexpected: %s", raw)
	}
}

func TestSettings7KeysAreListed(t *testing.T) {
	keys := Settings7Keys()
	if len(keys) < 20 {
		t.Fatalf("only %d keys listed", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	for _, want := range []string{"diff_style", "tool_timeout", "retry.stall", "router.desloppify", "editor.args", "search.jina_reader"} {
		if !seen[want] {
			t.Errorf("%s not listed", want)
		}
	}
}
