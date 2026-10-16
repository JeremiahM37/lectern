package config

// The stream watchdog thresholds (/watchdog) and the editor integration
// toggles (/editor settings). Both are plain config objects with flat keys
// for the settings screen and the slash commands.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Watchdog holds the model stream thresholds in seconds. Zero keeps the
// agent's default. They are separate from the command timeout (/timeouts),
// which limits shell and project commands.
type Watchdog struct {
	// FirstToken is how long a model call may wait for its first text or
	// tool call. Default 180 s, allowed 30 to 600.
	FirstToken int `json:"first_token,omitempty"`
	// InterToken is the longest gap between two chunks once output has
	// started. Default 120 s, allowed 30 to 600.
	InterToken int `json:"inter_token,omitempty"`
	// Step caps one whole model call. Default 900 s, allowed 120 to 1800.
	Step int `json:"step,omitempty"`
}

// Limits for each watchdog field, in seconds.
const (
	watchdogFirstMin, watchdogFirstMax = 30, 600
	watchdogInterMin, watchdogInterMax = 30, 600
	watchdogStepMin, watchdogStepMax   = 120, 1800
)

func mergeWatchdog(base, o Watchdog) Watchdog {
	if o.FirstToken != 0 {
		base.FirstToken = o.FirstToken
	}
	if o.InterToken != 0 {
		base.InterToken = o.InterToken
	}
	if o.Step != 0 {
		base.Step = o.Step
	}
	return base
}

// parseSeconds reads a watchdog value. A bare number is seconds; "3m" and
// "180s" are accepted too, since people type both. An empty value or
// "default" means the default (0).
func parseSeconds(v any) (int, error) {
	s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
	if s == "" || s == "default" || s == "<nil>" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d%time.Second != 0 || d < 0 {
			return 0, fmt.Errorf("use whole seconds, for example 180 or 3m")
		}
		return int(d / time.Second), nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "s"))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("use whole seconds, for example 180 or 3m; 0 or default for the default")
	}
	return n, nil
}

// setWatchdog handles the "watchdog.<field>" flat keys.
func (c *Config) setWatchdog(field string, v any) error {
	n, err := parseSeconds(v)
	if err != nil {
		return fmt.Errorf("watchdog.%s: %v", field, err)
	}
	check := func(lo, hi int) error {
		if n != 0 && (n < lo || n > hi) {
			return fmt.Errorf("watchdog.%s must be %d to %d seconds, or 0 for the default", field, lo, hi)
		}
		return nil
	}
	switch field {
	case "first_token":
		if err := check(watchdogFirstMin, watchdogFirstMax); err != nil {
			return err
		}
		c.WatchdogTimeouts.FirstToken = n
	case "inter_token":
		if err := check(watchdogInterMin, watchdogInterMax); err != nil {
			return err
		}
		c.WatchdogTimeouts.InterToken = n
	case "step":
		if err := check(watchdogStepMin, watchdogStepMax); err != nil {
			return err
		}
		c.WatchdogTimeouts.Step = n
	default:
		return fmt.Errorf("unknown watchdog setting %q (use first_token, inter_token or step)", field)
	}
	return nil
}

// EditorIntegrationKeys are the eleven editor integration toggles, in the
// order the settings show them.
var EditorIntegrationKeys = []string{
	"diagnostics", "symbols", "hover", "references", "definition",
	"code_actions", "rename", "lsp_status", "format", "editor_context",
	"sync_on_edit",
}

// editorIntegrationDefault is the value of each toggle when unset. Every
// integration is on except sync_on_edit, which jumps the editor to edited
// files and is off by default.
func editorIntegrationDefault(name string) bool {
	return name != "sync_on_edit"
}

// EditorIntegrationOn reports whether one editor integration is enabled.
// Unset toggles take their default.
func (c Config) EditorIntegrationOn(name string) bool {
	if v, ok := c.EditorIntegrations[name]; ok {
		return v
	}
	return editorIntegrationDefault(name)
}

func mergeEditorIntegrations(base, o map[string]bool) map[string]bool {
	if len(base) == 0 && len(o) == 0 {
		return nil
	}
	out := make(map[string]bool, len(base)+len(o))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range o {
		out[k] = v
	}
	return out
}

// setEditorIntegration handles the "editor_integration.<name>" flat keys.
// A nil or empty value clears the toggle back to its default.
func (c *Config) setEditorIntegration(name string, v any) error {
	if !isEditorIntegration(name) {
		return fmt.Errorf("unknown editor integration %q (use one of: %s)", name, strings.Join(EditorIntegrationKeys, ", "))
	}
	s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
	if v == nil || s == "" || s == "<nil>" || s == "default" {
		delete(c.EditorIntegrations, name)
		return nil
	}
	on, err := parseBool(s)
	if err != nil {
		return fmt.Errorf("editor_integration.%s takes on or off", name)
	}
	if c.EditorIntegrations == nil {
		c.EditorIntegrations = map[string]bool{}
	}
	c.EditorIntegrations[name] = on
	return nil
}

func isEditorIntegration(name string) bool {
	for _, k := range EditorIntegrationKeys {
		if k == name {
			return true
		}
	}
	return false
}

// parseBool accepts the words people type for a switch.
func parseBool(s string) (bool, error) {
	switch s {
	case "on", "true", "yes", "1", "enabled":
		return true, nil
	case "off", "false", "no", "0", "disabled":
		return false, nil
	}
	return false, fmt.Errorf("not a switch value: %q", s)
}
