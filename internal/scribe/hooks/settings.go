package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// claudeSettings is the part of a Claude Code settings.json we read.
type claudeSettings struct {
	Hooks           map[string][]claudeRule `json:"hooks"`
	DisableAllHooks bool                    `json:"disableAllHooks"`
}

type claudeRule struct {
	Matcher string       `json:"matcher"`
	Hooks   []claudeHook `json:"hooks"`
}

type claudeHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// readClaudeSettings parses one settings file into hook entries. A missing
// file yields nothing. disableAllHooks in the file yields nothing either.
func readClaudeSettings(path, source string) ([]entry, []string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(b) == 0 {
		return nil, nil, nil
	}
	var s claudeSettings
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, nil, err
	}
	if s.DisableAllHooks {
		return nil, nil, nil
	}
	var out []entry
	var warns []string
	for event, rules := range s.Hooks {
		for _, rule := range rules {
			for _, h := range rule.Hooks {
				if h.Type != "command" || h.Command == "" {
					continue
				}
				e, err := newEntry(event, rule.Matcher, h.Command, h.Timeout, source)
				if err != nil {
					warns = append(warns, err.Error())
					continue
				}
				out = append(out, e)
			}
		}
	}
	return out, warns, nil
}

// fromConfig converts config.Hook entries.
func fromConfig(hs []config.Hook, source string) ([]entry, []string) {
	var out []entry
	var warns []string
	for _, h := range hs {
		if h.Command == "" || h.Event == "" {
			continue
		}
		e, err := newEntry(h.Event, h.Matcher, h.Command, h.Timeout, source)
		if err != nil {
			warns = append(warns, err.Error())
			continue
		}
		out = append(out, e)
	}
	return out, warns
}

// LoadConfigHooks reads the hooks arrays of the global and project config
// files separately, so the caller can gate the project one on trust.
func LoadConfigHooks(root string) (global, project []config.Hook, err error) {
	g, err := readConfigFile(config.GlobalPath())
	if err != nil {
		return nil, nil, err
	}
	if root == "" {
		return g.Hooks, nil, nil
	}
	p, err := readConfigFile(config.ProjectPath(root))
	if err != nil {
		return nil, nil, err
	}
	return g.Hooks, p.Hooks, nil
}

func readConfigFile(path string) (config.Config, error) {
	var c config.Config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(b) == 0) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// userSettingsPath is ~/.claude/settings.json.
func userSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// timeoutOf returns the configured timeout in seconds, defaulting to 30.
func timeoutOf(secs int) time.Duration {
	if secs <= 0 {
		return defaultTimeout
	}
	return time.Duration(secs) * time.Second
}
