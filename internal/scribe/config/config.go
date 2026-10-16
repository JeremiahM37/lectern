// Package config loads Scribe's settings: a global file at
// $LECTERN_HOME/scribe/config.json (default ~/.lectern/scribe/config.json)
// merged with an optional project file at <repo>/.scribe/config.json.
// Project values override global ones field by field.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Provider is a user-defined OpenAI-compatible provider.
type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"base_url"`
	// APIKeyEnv names the environment variable holding the key. Keys may
	// also be stored with `lectern code --set-key`.
	APIKeyEnv string            `json:"api_key_env,omitempty"`
	Models    []string          `json:"models,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// Router assigns models to task slots. Empty slots use Model.
type Router struct {
	Explore   string   `json:"explore,omitempty"`    // read-only subagents
	Code      string   `json:"code,omitempty"`       // editing subagents
	WebSearch string   `json:"web_search,omitempty"` // web research subagent
	Compact   string   `json:"compact,omitempty"`    // summarisation for compaction
	Title     string   `json:"title,omitempty"`      // session titles / commit messages
	Fallback  []string `json:"fallback,omitempty"`   // tried in order when Model keeps failing
	MaxAgents int      `json:"max_agents,omitempty"` // concurrent subagents (2-8, default 3)
}

// Hook runs a shell command on an agent event.
type Hook struct {
	Event   string `json:"event"`             // PreToolUse | PostToolUse | UserPromptSubmit | Stop | SessionStart | SessionEnd | PreCompact | Notification
	Matcher string `json:"matcher,omitempty"` // tool-name regex for tool events
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds, default 30
}

// MCPServer is an MCP server the agent can use.
type MCPServer struct {
	Name     string            `json:"name"`
	Command  string            `json:"command,omitempty"` // stdio transport
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"` // streamable HTTP transport
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// Config is the merged configuration. Pointer and zero values mean "unset"
// so merging can tell them apart.
type Config struct {
	Model     string `json:"model,omitempty"`      // provider/model
	Mode      string `json:"mode,omitempty"`       // default mode
	Theme     string `json:"theme,omitempty"`      // theme name
	Reasoning string `json:"reasoning,omitempty"`  // "", low, medium, high
	MaxTokens int    `json:"max_tokens,omitempty"` // per response

	NerdFont      *bool   `json:"nerd_font,omitempty"`
	ShowReasoning *bool   `json:"show_reasoning,omitempty"`
	Verbose       *bool   `json:"verbose,omitempty"`      // show every tool row in order
	ChatStyle     string  `json:"chat_style,omitempty"`   // accent | bubble
	Vim           *bool   `json:"vim,omitempty"`          // vim keys in the input box
	RepoMap       *bool   `json:"repo_map,omitempty"`     // build the code map (default on)
	AutoCompact   *bool   `json:"auto_compact,omitempty"` // default on
	CompactAt     float64 `json:"compact_at,omitempty"`   // fraction of window, default 0.8
	Memory        *bool   `json:"memory,omitempty"`       // recall + memory tool (default on)
	Checkpoints   *bool   `json:"checkpoints,omitempty"`  // default on
	// DeferredTools sends only core tools each step; other tools load on
	// request_tools (default on). AutoCheck runs the project's typecheck or
	// build after a turn that edited files and ended without a check
	// (default on).
	DeferredTools *bool `json:"deferred_tools,omitempty"`
	AutoCheck     *bool `json:"auto_check,omitempty"`
	// FormatOnEdit runs the project formatter on files an edit changed and
	// reports new language-server errors after the edit (default on).
	FormatOnEdit *bool `json:"format_on_edit,omitempty"`

	// Instructions lists instruction files loaded into the system prompt,
	// relative to the project root or ~ (default: SCRIBE.md, AGENTS.md,
	// CLAUDE.md).
	Instructions []string `json:"instructions,omitempty"`

	Providers []Provider  `json:"providers,omitempty"`
	Router    Router      `json:"router,omitempty"`
	Hooks     []Hook      `json:"hooks,omitempty"`
	MCP       []MCPServer `json:"mcp,omitempty"`

	// Permissions: shell command prefixes always allowed / denied.
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`

	DisabledTools []string `json:"disabled_tools,omitempty"`
	// SearchURL is a SearXNG instance for web_search (else DuckDuckGo HTML).
	SearchURL string `json:"search_url,omitempty"`

	// ReasoningByProvider overrides the reasoning controls for one provider
	// id, e.g. {"anthropic": {"effort": "medium", "budget": 8000}}. Empty
	// fields leave the provider's own default in place.
	ReasoningByProvider map[string]Reasoning `json:"reasoning_by_provider,omitempty"`
}

// Reasoning is one provider's reasoning controls. Which fields apply depends
// on the model's reasoning style (llm.ModelInfo.ReasoningStyle): Effort for
// "effort" and "adaptive", Budget for "budget". ServiceTier is sent wherever
// the provider accepts it.
type Reasoning struct {
	Effort      string `json:"effort,omitempty"`       // low | medium | high | ...
	Budget      int    `json:"budget,omitempty"`       // thinking tokens; 0 = provider default
	ServiceTier string `json:"service_tier,omitempty"` // e.g. auto, priority, flex
}

// IsZero reports whether no reasoning control is set.
func (r Reasoning) IsZero() bool { return r == Reasoning{} }

// Bool returns *p or def when unset.
func Bool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Home is Scribe's global directory.
func Home() string {
	if h := os.Getenv("SCRIBE_HOME"); h != "" {
		return h
	}
	if h := os.Getenv("LECTERN_HOME"); h != "" {
		return filepath.Join(h, "scribe")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".lectern", "scribe")
}

// ProjectDir is the per-project directory (<root>/.scribe).
func ProjectDir(root string) string { return filepath.Join(root, ".scribe") }

// GlobalPath is the global config file.
func GlobalPath() string { return filepath.Join(Home(), "config.json") }

// ProjectPath is the project config file.
func ProjectPath(root string) string { return filepath.Join(ProjectDir(root), "config.json") }

// Load reads and merges global and project config. Missing files are fine.
func Load(root string) (Config, error) {
	var g, p Config
	if err := readJSON(GlobalPath(), &g); err != nil {
		return Config{}, err
	}
	if root != "" {
		if err := readJSON(ProjectPath(root), &p); err != nil {
			return Config{}, err
		}
	}
	return Merge(g, p), nil
}

// LoadFile reads one config file without merging. A missing file gives the
// zero Config. Settings edits the raw file so it never writes defaults back.
func LoadFile(path string) (Config, error) {
	var c Config
	if err := readJSON(path, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Merge overlays o onto base.
func Merge(base, o Config) Config {
	r := base
	str := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	bp := func(dst **bool, v *bool) {
		if v != nil {
			*dst = v
		}
	}
	str(&r.Model, o.Model)
	str(&r.Mode, o.Mode)
	str(&r.Theme, o.Theme)
	str(&r.Reasoning, o.Reasoning)
	str(&r.ChatStyle, o.ChatStyle)
	str(&r.SearchURL, o.SearchURL)
	if o.MaxTokens != 0 {
		r.MaxTokens = o.MaxTokens
	}
	if o.CompactAt != 0 {
		r.CompactAt = o.CompactAt
	}
	bp(&r.NerdFont, o.NerdFont)
	bp(&r.ShowReasoning, o.ShowReasoning)
	bp(&r.Verbose, o.Verbose)
	bp(&r.Vim, o.Vim)
	bp(&r.RepoMap, o.RepoMap)
	bp(&r.AutoCompact, o.AutoCompact)
	bp(&r.Memory, o.Memory)
	bp(&r.Checkpoints, o.Checkpoints)
	bp(&r.DeferredTools, o.DeferredTools)
	bp(&r.AutoCheck, o.AutoCheck)
	bp(&r.FormatOnEdit, o.FormatOnEdit)
	if o.Instructions != nil {
		r.Instructions = o.Instructions
	}
	// Providers and MCP servers merge by id/name, project wins.
	r.Providers = mergeBy(base.Providers, o.Providers, func(p Provider) string { return p.ID })
	r.MCP = mergeBy(base.MCP, o.MCP, func(m MCPServer) string { return m.Name })
	r.Hooks = append(append([]Hook(nil), base.Hooks...), o.Hooks...)
	r.Allow = append(append([]string(nil), base.Allow...), o.Allow...)
	r.Deny = append(append([]string(nil), base.Deny...), o.Deny...)
	r.DisabledTools = append(append([]string(nil), base.DisabledTools...), o.DisabledTools...)
	rt := base.Router
	str(&rt.Explore, o.Router.Explore)
	str(&rt.Code, o.Router.Code)
	str(&rt.WebSearch, o.Router.WebSearch)
	str(&rt.Compact, o.Router.Compact)
	str(&rt.Title, o.Router.Title)
	if o.Router.Fallback != nil {
		rt.Fallback = o.Router.Fallback
	}
	if o.Router.MaxAgents != 0 {
		rt.MaxAgents = o.Router.MaxAgents
	}
	r.Router = rt
	r.ReasoningByProvider = mergeReasoning(base.ReasoningByProvider, o.ReasoningByProvider)
	return r
}

// mergeReasoning overlays o onto base one provider at a time. A field set in
// o wins; a field o leaves empty keeps the base value.
func mergeReasoning(base, o map[string]Reasoning) map[string]Reasoning {
	if len(base) == 0 && len(o) == 0 {
		return nil
	}
	out := make(map[string]Reasoning, len(base)+len(o))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range o {
		m := out[k]
		if v.Effort != "" {
			m.Effort = v.Effort
		}
		if v.Budget != 0 {
			m.Budget = v.Budget
		}
		if v.ServiceTier != "" {
			m.ServiceTier = v.ServiceTier
		}
		out[k] = m
	}
	return out
}

func mergeBy[T any](a, b []T, key func(T) string) []T {
	idx := map[string]int{}
	out := append([]T(nil), a...)
	for i, v := range out {
		idx[key(v)] = i
	}
	for _, v := range b {
		if i, ok := idx[key(v)]; ok {
			out[i] = v
		} else {
			idx[key(v)] = len(out)
			out = append(out, v)
		}
	}
	return out
}

// SaveGlobal writes c as the global config (0600).
func SaveGlobal(c Config) error { return save(GlobalPath(), c) }

// SaveProject writes c as the project config.
func SaveProject(root string, c Config) error { return save(ProjectPath(root), c) }

func save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SetReasoningField sets one field of ReasoningByProvider from a flat settings
// key, "reasoning_by_provider.<provider>.<effort|budget|service_tier>". It
// reports false, and changes nothing, when key is not one of those keys. An
// empty value clears the field.
func (c *Config) SetReasoningField(key string, v any) (bool, error) {
	rest, ok := strings.CutPrefix(key, "reasoning_by_provider.")
	if !ok {
		return false, nil
	}
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 {
		return true, fmt.Errorf("setting %q needs a provider and a field", key)
	}
	provider, field := rest[:i], rest[i+1:]
	r := c.ReasoningByProvider[provider]
	switch field {
	case "effort":
		r.Effort = strings.TrimSpace(fmt.Sprint(v))
	case "service_tier":
		r.ServiceTier = strings.TrimSpace(fmt.Sprint(v))
	case "budget":
		n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
		if err != nil || n < 0 {
			return true, fmt.Errorf("%s must be a whole number of tokens, 0 for the default", key)
		}
		r.Budget = n
	default:
		return true, fmt.Errorf("unknown reasoning field %q (use effort, budget or service_tier)", field)
	}
	if c.ReasoningByProvider == nil {
		c.ReasoningByProvider = map[string]Reasoning{}
	}
	if r.IsZero() {
		delete(c.ReasoningByProvider, provider)
	} else {
		c.ReasoningByProvider[provider] = r
	}
	return true, nil
}
