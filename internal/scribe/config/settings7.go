package config

// Flat keys for the settings written by the wave 5 options: the settings
// screen and the slash commands write them by name, and the backend saves
// the result. Keys that hold a map (a per-model fallback, a context window)
// take the model reference after a prefix, so "provider/model" may contain
// dots. Keys that hold a nested object take the field after the prefix.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	keyFallbackByModel = "router.fallback_by_model."
	keyContextWindow   = "context_window_overrides."
	keyAgentFeature    = "agent_features."
	keyEditorPrefix    = "editor."
	keySearchPrefix    = "search."
	keyRetryPrefix     = "retry."
	keyPerfPrefix      = "performance."
	keyWatchdogPrefix  = "watchdog."
	keyEditorIntPrefix = "editor_integration."
)

// settings7Keys lists the flat keys SetField accepts, for the settings
// screen to show what is editable. Prefixed keys are listed with their prefix.
var settings7Keys = []string{
	"diff_style", "collapse_diffs", "compact_strategy", "tool_timeout",
	"format_on_edit", "deferred_tools", "router.max_agents",
	"retry.transient", "retry.stall",
	"router.default", "router.desloppify", "router.verify", "router.semantic",
	keyFallbackByModel, keyContextWindow, keyAgentFeature,
	"editor.command", "editor.args",
	"search.brave_key_env", "search.jina_key_env", "search.jina_reader",
	"key_priority", "forbidden",
	"performance.thinking", "performance.effort", "performance.speed",
	"performance.cache_ttl", "performance.tool_streaming",
	"performance.send_reasoning", "performance.disable_parallel_tool_use",
	themeTransparentKey, themeMessageKey, themeDiffKey, themeBorderKey,
	"compact_keep_recent", "watchdog.first_token", "watchdog.inter_token", "watchdog.step",
	keyEditorIntPrefix,
}

// SettingKeyPrefixes are the prefixes of the map-style and nested keys.
func SettingKeyPrefixes() []string {
	return []string{keyFallbackByModel, keyContextWindow, keyAgentFeature, keyEditorPrefix, keySearchPrefix, keyRetryPrefix, keyPerfPrefix, keyWatchdogPrefix, keyEditorIntPrefix}
}

// SetField sets one flat key from a settings value. It reports handled=false
// (and changes nothing) for a key this file does not own, so a caller can try
// its own keys next. A nil or empty value clears the field to its default.
func (c *Config) SetField(key string, v any) (bool, error) {
	if ok, err := c.setScalar(key, v); ok {
		return true, err
	}
	switch {
	case strings.HasPrefix(key, "context_management."):
		return true, c.ContextManagement.set(strings.TrimPrefix(key, "context_management."), v)
	case strings.HasPrefix(key, keyFallbackByModel):
		ref := strings.TrimPrefix(key, keyFallbackByModel)
		if !strings.Contains(ref, "/") {
			return true, fmt.Errorf("fallback for %q: the model must be provider/model", ref)
		}
		chain := toList(v)
		if len(chain) == 0 {
			delete(c.Router.FallbackByModel, ref)
			return true, nil
		}
		if c.Router.FallbackByModel == nil {
			c.Router.FallbackByModel = map[string][]string{}
		}
		c.Router.FallbackByModel[ref] = chain
		return true, nil
	case strings.HasPrefix(key, keyContextWindow):
		ref := strings.TrimPrefix(key, keyContextWindow)
		if !strings.Contains(ref, "/") {
			return true, fmt.Errorf("context window for %q: the model must be provider/model", ref)
		}
		n, err := toInt(v)
		if err != nil || n < 0 {
			return true, fmt.Errorf("context window must be a whole number of tokens, 0 to remove")
		}
		if n == 0 {
			delete(c.ContextWindowOverrides, ref)
			return true, nil
		}
		if c.ContextWindowOverrides == nil {
			c.ContextWindowOverrides = map[string]int{}
		}
		c.ContextWindowOverrides[ref] = n
		return true, nil
	case strings.HasPrefix(key, keyAgentFeature):
		name := strings.TrimPrefix(key, keyAgentFeature)
		if c.AgentFeatures == nil {
			c.AgentFeatures = &AgentFeatures{}
		}
		p := c.AgentFeatures.field(name)
		if p == nil {
			return true, fmt.Errorf("unknown agent feature %q", name)
		}
		if v == nil || toString(v) == "" {
			*p = nil
			return true, nil
		}
		on, err := toBool(v)
		if err != nil {
			return true, err
		}
		*p = &on
		return true, nil
	case strings.HasPrefix(key, keyEditorPrefix):
		return true, c.setEditor(strings.TrimPrefix(key, keyEditorPrefix), v)
	case strings.HasPrefix(key, keySearchPrefix):
		return true, c.setSearch(strings.TrimPrefix(key, keySearchPrefix), v)
	case strings.HasPrefix(key, keyRetryPrefix):
		n, err := toInt(v)
		if err != nil || n < 0 {
			return true, fmt.Errorf("%s must be a whole number, 0 for the default", key)
		}
		switch strings.TrimPrefix(key, keyRetryPrefix) {
		case "transient":
			c.Retry.Transient = n
		case "stall":
			c.Retry.Stall = n
		default:
			return true, fmt.Errorf("unknown retry setting %q", key)
		}
		return true, nil
	case strings.HasPrefix(key, keyPerfPrefix):
		return true, c.setPerformance(strings.TrimPrefix(key, keyPerfPrefix), v)
	case strings.HasPrefix(key, keyWatchdogPrefix):
		return true, c.setWatchdog(strings.TrimPrefix(key, keyWatchdogPrefix), v)
	case strings.HasPrefix(key, keyEditorIntPrefix):
		return true, c.setEditorIntegration(strings.TrimPrefix(key, keyEditorIntPrefix), v)
	}
	return false, nil
}

// setScalar handles the flat keys. It reports handled=false for keys that
// are not scalar settings.
func (c *Config) setScalar(key string, v any) (bool, error) {
	switch key {
	case "diff_style":
		s := toString(v)
		if !oneOf(s, diffStyles) {
			return true, fmt.Errorf("diff_style must be default, sidebyside or compact")
		}
		c.DiffStyle = s
	case "collapse_diffs":
		b, err := toBool(v)
		if err != nil {
			return true, err
		}
		c.CollapseDiffs = &b
	case "compact_strategy":
		s := toString(v)
		if !oneOf(s, compactStrategy) {
			return true, fmt.Errorf("compact_strategy must be v2, v1 or disabled")
		}
		c.CompactStrategy = s
	case "compact_keep_recent":
		n, err := toInt(v)
		if err != nil || n < 0 {
			return true, fmt.Errorf("compact_keep_recent must be a whole number of messages, 0 for the default")
		}
		c.CompactKeepRecent = n
	case "daily_token_budget":
		n, err := toInt(v)
		if err != nil || n < 0 {
			return true, fmt.Errorf("daily_token_budget must be a whole number of tokens, 0 for no limit")
		}
		c.DailyTokenBudget = &n
	case "tool_timeout":
		s := toString(v)
		if s == "" || s == "default" {
			c.ToolTimeout = nil
			return true, nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return true, fmt.Errorf("tool_timeout is in minutes, for example 2 or 0.5; 0 means no limit")
		}
		c.ToolTimeout = &f
	case "router.default":
		c.Router.Default = toString(v)
	case "router.desloppify":
		c.Router.Desloppify = toString(v)
	case "router.verify":
		c.Router.Verify = toString(v)
	case "router.semantic":
		c.Router.Semantic = toString(v)
	case "router.max_agents":
		n, err := toInt(v)
		if err != nil || (n != 0 && (n < 2 || n > 8)) {
			return true, fmt.Errorf("router.max_agents must be a whole number from 2 to 8, 0 for the default (3)")
		}
		c.Router.MaxAgents = n
	case "format_on_edit":
		b, err := toBool(v)
		if err != nil {
			return true, err
		}
		c.FormatOnEdit = &b
	case "deferred_tools":
		b, err := toBool(v)
		if err != nil {
			return true, err
		}
		c.DeferredTools = &b
	case "auto_check":
		b, err := toBool(v)
		if err != nil {
			return true, err
		}
		c.AutoCheck = &b
	case "disabled_tools":
		c.DisabledTools = toList(v)
	case "key_priority":
		s := toString(v)
		if !oneOf(s, keyPriorities) {
			return true, fmt.Errorf("key_priority must be env or app")
		}
		c.KeyPriority = s
	case "forbidden":
		c.Forbidden = toList(v)
	case themeTransparentKey, themeMessageKey, themeDiffKey, themeBorderKey:
		return true, c.setThemeStyle(key, v)
	default:
		return false, nil
	}
	return true, nil
}

func (c *Config) setEditor(field string, v any) error {
	switch field {
	case "command":
		cmd := toString(v)
		if strings.ContainsAny(cmd, " \t\n") {
			return fmt.Errorf("editor command is one program name or path; put options in editor args")
		}
		c.Editor.Command = cmd
	case "args":
		c.Editor.Args = toList(v)
	default:
		return fmt.Errorf("unknown editor setting %q", field)
	}
	return nil
}

func (c *Config) setSearch(field string, v any) error {
	switch field {
	case "brave_key_env", "jina_key_env":
		name := toString(v)
		if strings.ContainsAny(name, " =\t\n") {
			return fmt.Errorf("%s is the name of an environment variable", field)
		}
		if field == "brave_key_env" {
			c.Search.BraveKeyEnv = name
		} else {
			c.Search.JinaKeyEnv = name
		}
	case "jina_reader":
		b, err := toBool(v)
		if err != nil {
			return err
		}
		c.Search.JinaReader = &b
	default:
		return fmt.Errorf("unknown search setting %q", field)
	}
	return nil
}

func (c *Config) setPerformance(field string, v any) error {
	str := func(dst *string, allowed []string) error {
		s := toString(v)
		if !oneOf(s, allowed) {
			return fmt.Errorf("performance.%s %q is not one of %s", field, s, strings.Join(allowed[1:], ", "))
		}
		*dst = s
		return nil
	}
	boolp := func(dst **bool) error {
		if v == nil || toString(v) == "" {
			*dst = nil
			return nil
		}
		b, err := toBool(v)
		if err != nil {
			return err
		}
		*dst = &b
		return nil
	}
	p := &c.Performance
	switch field {
	case "thinking":
		return str(&p.Thinking, thinkingModes)
	case "effort":
		return str(&p.Effort, reasoningEfforts)
	case "cache_ttl":
		return str(&p.CacheTTL, cacheTTLs)
	case "speed":
		p.Speed = toString(v)
	case "tool_streaming":
		return boolp(&p.ToolStreaming)
	case "send_reasoning":
		return boolp(&p.SendReasoning)
	case "disable_parallel_tool_use":
		return boolp(&p.DisableParallelToolUse)
	default:
		return fmt.Errorf("unknown performance setting %q", field)
	}
	return nil
}

// ClearField resets one flat key to its default, so the omitempty tag drops
// it from the file. It reports handled=false for a key this file does not own.
func (c *Config) ClearField(key string) bool {
	switch {
	case key == "context_management.clear_thinking":
		c.ContextManagement.ClearThinking = nil
		return true
	case key == "context_management.clear_tool_uses":
		c.ContextManagement.ClearToolUses = nil
		return true
	case strings.HasPrefix(key, keyFallbackByModel):
		delete(c.Router.FallbackByModel, strings.TrimPrefix(key, keyFallbackByModel))
		return true
	case strings.HasPrefix(key, keyContextWindow):
		delete(c.ContextWindowOverrides, strings.TrimPrefix(key, keyContextWindow))
		return true
	case strings.HasPrefix(key, keyWatchdogPrefix):
		switch strings.TrimPrefix(key, keyWatchdogPrefix) {
		case "first_token":
			c.WatchdogTimeouts.FirstToken = 0
		case "inter_token":
			c.WatchdogTimeouts.InterToken = 0
		case "step":
			c.WatchdogTimeouts.Step = 0
		}
		return true
	case strings.HasPrefix(key, keyEditorIntPrefix):
		delete(c.EditorIntegrations, strings.TrimPrefix(key, keyEditorIntPrefix))
		return true
	case strings.HasPrefix(key, keyAgentFeature):
		if c.AgentFeatures == nil {
			return true
		}
		if p := c.AgentFeatures.field(strings.TrimPrefix(key, keyAgentFeature)); p != nil {
			*p = nil
		}
		return true
	}
	switch key {
	case "diff_style":
		c.DiffStyle = ""
	case "collapse_diffs":
		c.CollapseDiffs = nil
	case "compact_strategy":
		c.CompactStrategy = ""
	case "tool_timeout":
		c.ToolTimeout = nil
	case "router.default":
		c.Router.Default = ""
	case "router.desloppify":
		c.Router.Desloppify = ""
	case "router.verify":
		c.Router.Verify = ""
	case "router.semantic":
		c.Router.Semantic = ""
	case "router.max_agents":
		c.Router.MaxAgents = 0
	case "format_on_edit":
		c.FormatOnEdit = nil
	case "deferred_tools":
		c.DeferredTools = nil
	case "auto_check":
		c.AutoCheck = nil
	case "disabled_tools":
		c.DisabledTools = nil
	case "key_priority":
		c.KeyPriority = ""
	case "forbidden":
		c.Forbidden = nil
	case "editor.command":
		c.Editor.Command = ""
	case "editor.args":
		c.Editor.Args = nil
	case "search.brave_key_env":
		c.Search.BraveKeyEnv = ""
	case "search.jina_key_env":
		c.Search.JinaKeyEnv = ""
	case "search.jina_reader":
		c.Search.JinaReader = nil
	case "compact_keep_recent":
		c.CompactKeepRecent = 0
	case "retry.transient":
		c.Retry.Transient = 0
	case "retry.stall":
		c.Retry.Stall = 0
	case "performance.thinking":
		c.Performance.Thinking = ""
	case "performance.effort":
		c.Performance.Effort = ""
	case "performance.speed":
		c.Performance.Speed = ""
	case "performance.cache_ttl":
		c.Performance.CacheTTL = ""
	case "performance.tool_streaming":
		c.Performance.ToolStreaming = nil
	case "performance.send_reasoning":
		c.Performance.SendReasoning = nil
	case "performance.disable_parallel_tool_use":
		c.Performance.DisableParallelToolUse = nil
	default:
		return false
	}
	return true
}

// Settings7Keys returns the flat keys SetField takes, for listing. The prefixed
// keys appear once with their prefix.
func Settings7Keys() []string { return append([]string(nil), settings7Keys...) }

// toString is the trimmed text of a settings value; nil is "".
func toString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// toBool reads a bool or an on/off, true/false, yes/no text.
func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "on", "true", "yes", "1":
			return true, nil
		case "off", "false", "no", "0":
			return false, nil
		}
	}
	return false, fmt.Errorf("expected on or off, got %v", v)
}

// toInt reads a whole number from an int, a float with no fraction, or text.
func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case float64:
		if x == float64(int(x)) {
			return int(x), nil
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("expected a whole number, got %v", v)
}

// toList reads a list from a []string, a []any, or comma-separated text. Blank
// entries are dropped. nil and an empty text both give nil.
func toList(v any) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		for _, s := range x {
			add(s)
		}
	case []any:
		for _, s := range x {
			add(fmt.Sprint(s))
		}
	case string:
		for _, s := range strings.Split(x, ",") {
			add(s)
		}
	default:
		add(fmt.Sprint(x))
	}
	return out
}
