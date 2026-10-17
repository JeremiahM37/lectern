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
	"reflect"
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
	// ModelsAPI sets the URL the model list is read from. The value false
	// stops listing models for this provider and uses Models only. Unset
	// means baseURL + "/models". See ModelsAPI.
	ModelsAPI *ModelsAPI `json:"models_api,omitempty"`
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
	// Default is the model for any slot that is not set above; it is used
	// before Model. Empty means Model.
	Default string `json:"default,omitempty"`
	// Desloppify, Verify and Semantic are the model slots for the cleanup
	// pass, the LLM review of edits and semantic code-map summaries. Empty
	// means Default, then Model.
	Desloppify string `json:"desloppify,omitempty"`
	Verify     string `json:"verify,omitempty"`
	Semantic   string `json:"semantic,omitempty"`
	// FallbackByModel gives a fallback chain for one "provider/model" id. It
	// is checked before Fallback, which stays the list for every other model.
	FallbackByModel map[string][]string `json:"fallback_by_model,omitempty"`
}

// Hook runs a shell command on an agent event. The fields If, Once, Async
// and StatusMessage mirror Claude Code's hook settings, so a hook can be
// copied between the two.
type Hook struct {
	Event   string `json:"event"`             // PreToolUse | PostToolUse | UserPromptSubmit | Stop | SessionStart | SessionEnd | PreCompact | Notification
	Matcher string `json:"matcher,omitempty"` // tool-name regex for tool events
	Command string `json:"command"`
	// Timeout is in seconds. Zero or less means DefaultHookTimeout (10).
	Timeout int `json:"timeout,omitempty"`
	// If is a condition on the tool call, e.g. "Bash(git commit*)". The hook
	// runs only when it matches. Empty means always.
	If string `json:"if,omitempty"`
	// Once runs the hook a single time per session, then never again.
	Once bool `json:"once,omitempty"`
	// Async starts the hook and does not wait for it, so it cannot block or
	// change the tool call.
	Async bool `json:"async,omitempty"`
	// StatusMessage is shown while the hook runs.
	StatusMessage string `json:"status_message,omitempty"`
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
	// Timeout is the per-call limit in seconds. Zero means the MCP default
	// (60 s).
	Timeout int `json:"timeout,omitempty"`
}

// Config is the merged configuration. Pointer and zero values mean "unset"
// so merging can tell them apart.
type Config struct {
	Model     string `json:"model,omitempty"`      // provider/model
	Mode      string `json:"mode,omitempty"`       // default mode
	Theme     string `json:"theme,omitempty"`      // theme name
	Reasoning string `json:"reasoning,omitempty"`  // "", low, medium, high
	MaxTokens int    `json:"max_tokens,omitempty"` // per response

	NerdFont      *bool  `json:"nerd_font,omitempty"`
	ShowReasoning *bool  `json:"show_reasoning,omitempty"`
	Verbose       *bool  `json:"verbose,omitempty"`    // show every tool row in order
	ChatStyle     string `json:"chat_style,omitempty"` // accent | bubble
	Vim           *bool  `json:"vim,omitempty"`        // vim keys in the input box
	RepoMap       *bool  `json:"repo_map,omitempty"`   // build the code map (default on)
	// RepoMapTokenBudget sizes the rendered code map in tokens. Zero is auto.
	RepoMapTokenBudget int `json:"repo_map_token_budget,omitempty"`
	// SemanticSummaries picks what the code map shows after a symbol: off,
	// ast, synthetic, llm or full. Empty means ast. SemanticSummaryLimit is
	// how many files one model pass covers (llm and full only); zero means
	// 300. SemanticAutoRegen asks the model again after every map build.
	SemanticSummaries    string  `json:"semantic_summaries,omitempty"`
	SemanticSummaryLimit int     `json:"semantic_summary_limit,omitempty"`
	SemanticAutoRegen    *bool   `json:"semantic_auto_regen,omitempty"`
	AutoCompact          *bool   `json:"auto_compact,omitempty"` // default on
	CompactAt            float64 `json:"compact_at,omitempty"`   // fraction of window, default 0.8
	Memory               *bool   `json:"memory,omitempty"`       // recall + memory tool (default on)
	// MemoryEmbeddingModel is a provider/model id (e.g. ollama/nomic-embed-text)
	// for memory recall. Empty means the local hash embedding, and nothing
	// calls a network for memory.
	MemoryEmbeddingModel string `json:"memory_embedding_model,omitempty"`
	Checkpoints          *bool  `json:"checkpoints,omitempty"` // default on
	// DeferredTools sends only core tools each step; other tools load on
	// request_tools (default on). AutoCheck runs the project's typecheck or
	// build after a turn that edited files and ended without a check
	// (default on).
	DeferredTools *bool `json:"deferred_tools,omitempty"`
	AutoCheck     *bool `json:"auto_check,omitempty"`
	// FormatOnEdit runs the project formatter on files an edit changed and
	// reports new language-server errors after the edit (default on).
	FormatOnEdit *bool `json:"format_on_edit,omitempty"`

	// The editor (Neovim, Ctrl+E). EditorConfig is the Neovim config it starts
	// with: "user" (default, the user's own config) or "none" (-u NONE).
	// EditorSync makes the agent's edits follow into the open buffer (default
	// on). EditorAccess is the agent's access to the editor: "on" (default),
	// "when-open" or "off". The /editor commands write the last two.
	EditorConfig string `json:"editor_config,omitempty"`
	EditorSync   *bool  `json:"editor_sync,omitempty"`
	EditorAccess string `json:"editor_access,omitempty"`

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
	// DisabledLSPServers names language servers (by language key, such as
	// "go" or "typescript") that Scribe never starts.
	DisabledLSPServers []string `json:"disabled_lsp_servers,omitempty"`
	// SearchURL is a SearXNG instance for web_search (else DuckDuckGo HTML).
	SearchURL string `json:"search_url,omitempty"`

	// ReasoningByProvider overrides the reasoning controls for one provider
	// id, e.g. {"anthropic": {"effort": "medium", "budget": 8000}}. Empty
	// fields leave the provider's own default in place.
	ReasoningByProvider map[string]Reasoning `json:"reasoning_by_provider,omitempty"`

	// KeyPriority decides which key wins when both the environment and the
	// key store hold one: "env" (default) or "app" (stored key first).
	KeyPriority string `json:"key_priority,omitempty"`
	// ContextWindowOverrides sets a context window, in tokens, for one
	// "provider/model" id. A user value wins over every other source.
	ContextWindowOverrides map[string]int `json:"context_window_overrides,omitempty"`
	// Search holds the web search and reader keys and options.
	Search SearchSettings `json:"search,omitempty"`
	// Forbidden lists path patterns no file or shell tool may touch. They
	// are merged with the forbidden.json files; see ForbiddenPatterns.
	Forbidden []string `json:"forbidden,omitempty"`
	// AgentFeatures switches optional agent behaviour on or off. Unset
	// fields keep their default (see AgentFeatures).
	AgentFeatures *AgentFeatures `json:"agent_features,omitempty"`
	// Editor is the command the in-app editor starts and its arguments.
	Editor EditorSettings `json:"editor,omitempty"`
	// DiffStyle is how diffs render: default, sidebyside or compact.
	// CollapseDiffs folds long diffs in the chat until opened.
	DiffStyle     string `json:"diff_style,omitempty"`
	CollapseDiffs *bool  `json:"collapse_diffs,omitempty"`
	// CoAuthorTrailer adds a Co-Authored-By trailer to commits made by the
	// agent. Off by default: it is opt-in.
	CoAuthorTrailer *bool `json:"co_author_trailer,omitempty"`
	// ToolTimeout is the default limit for shell and project commands, in
	// minutes. Unset means DefaultToolTimeout (2); set values are clamped to
	// 0.5-30 by ClampToolTimeout, and 0 or less means no limit.
	ToolTimeout *float64 `json:"tool_timeout,omitempty"`
	// CompactStrategy picks how context is compacted: v2 (default), v1 or
	// disabled.
	CompactStrategy string `json:"compact_strategy,omitempty"`
	// CompactKeepRecent is how many of the newest messages compaction keeps
	// verbatim. Zero keeps the token-sized tail (about 20 000 tokens).
	CompactKeepRecent int `json:"compact_keep_recent,omitempty"`
	// DailyTokenBudget caps the tokens Scribe may spend in one local day.
	// Nil or zero means no limit. A pointer so a project file can switch
	// off a global budget with an explicit 0. See DailyBudget.
	DailyTokenBudget *int `json:"daily_token_budget,omitempty"`
	// ChatDailyTokenBudget caps the tokens one chat (one session) may spend
	// in one local day, on top of the global cap. Nil or zero means no
	// per-chat limit. See ChatDailyBudget.
	ChatDailyTokenBudget *int `json:"chat_daily_token_budget,omitempty"`
	// MaxToolResults is how many recent tool-result summaries compaction
	// keeps in the working state. Zero means the default (30), which is also
	// the most the working state holds. See ToolResultsKept.
	MaxToolResults int `json:"max_tool_results,omitempty"`
	// Theme style (wave 10): transparency, message and diff background
	// opacity (percent, 0 to 100) and border strength ("", default, strong or
	// op). Applied at startup and by the settings screen.
	ThemeTransparent    *bool  `json:"theme_transparent,omitempty"`
	ThemeMessageOpacity *int   `json:"theme_message_opacity,omitempty"`
	ThemeDiffOpacity    *int   `json:"theme_diff_opacity,omitempty"`
	ThemeBorder         string `json:"theme_border,omitempty"`
	// Performance holds per-provider request options that change the wire
	// format. Unset fields keep the adapter's default.
	Performance Performance `json:"performance,omitempty"`
	// Retry sets how many times a failed model call is retried. Zero keeps
	// the agent's default.
	Retry Retry `json:"retry,omitempty"`
	// WatchdogTimeouts are the model stream thresholds (/watchdog). They are
	// separate from ToolTimeout, which limits shell and project commands.
	WatchdogTimeouts Watchdog `json:"watchdog_timeouts,omitempty"`
	// EditorIntegrations are the per-feature editor toggles (/editor
	// settings). A missing name takes its default; see EditorIntegrationOn.
	EditorIntegrations map[string]bool `json:"editor_integrations,omitempty"`
}

// Reasoning is one provider's reasoning controls. Which fields apply depends
// on the model's reasoning style (llm.ModelInfo.ReasoningStyle): Effort for
// "effort" and "adaptive", Budget for "budget". ServiceTier is sent wherever
// the provider accepts it.
type Reasoning struct {
	Effort      string `json:"effort,omitempty"`       // low | medium | high | ...
	Budget      int    `json:"budget,omitempty"`       // thinking tokens; 0 = provider default
	ServiceTier string `json:"service_tier,omitempty"` // e.g. auto, priority, flex
	Verbosity   string `json:"verbosity,omitempty"`    // OpenAI GPT-5: low, medium, high
	Exclude     bool   `json:"exclude,omitempty"`      // OpenRouter: hide reasoning text
	Thinking    string `json:"thinking,omitempty"`     // DeepSeek V4: on | off; "" = provider default (on)
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
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
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
	if o.CompactKeepRecent != 0 {
		r.CompactKeepRecent = o.CompactKeepRecent
	}
	if o.DailyTokenBudget != nil {
		r.DailyTokenBudget = o.DailyTokenBudget
	}
	if o.ChatDailyTokenBudget != nil {
		r.ChatDailyTokenBudget = o.ChatDailyTokenBudget
	}
	if o.MaxToolResults != 0 {
		r.MaxToolResults = o.MaxToolResults
	}
	bp(&r.ThemeTransparent, o.ThemeTransparent)
	if o.ThemeMessageOpacity != nil {
		r.ThemeMessageOpacity = o.ThemeMessageOpacity
	}
	if o.ThemeDiffOpacity != nil {
		r.ThemeDiffOpacity = o.ThemeDiffOpacity
	}
	str(&r.ThemeBorder, o.ThemeBorder)
	bp(&r.NerdFont, o.NerdFont)
	bp(&r.ShowReasoning, o.ShowReasoning)
	bp(&r.Verbose, o.Verbose)
	bp(&r.Vim, o.Vim)
	bp(&r.RepoMap, o.RepoMap)
	if o.RepoMapTokenBudget != 0 {
		r.RepoMapTokenBudget = o.RepoMapTokenBudget
	}
	str(&r.SemanticSummaries, o.SemanticSummaries)
	if o.SemanticSummaryLimit != 0 {
		r.SemanticSummaryLimit = o.SemanticSummaryLimit
	}
	bp(&r.SemanticAutoRegen, o.SemanticAutoRegen)
	bp(&r.AutoCompact, o.AutoCompact)
	bp(&r.Memory, o.Memory)
	str(&r.MemoryEmbeddingModel, o.MemoryEmbeddingModel)
	bp(&r.Checkpoints, o.Checkpoints)
	bp(&r.DeferredTools, o.DeferredTools)
	bp(&r.AutoCheck, o.AutoCheck)
	bp(&r.FormatOnEdit, o.FormatOnEdit)
	str(&r.EditorConfig, o.EditorConfig)
	str(&r.EditorAccess, o.EditorAccess)
	bp(&r.EditorSync, o.EditorSync)
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
	r.DisabledLSPServers = append(append([]string(nil), base.DisabledLSPServers...), o.DisabledLSPServers...)
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
	r.ReasoningByProvider = mergeReasoning(base.ReasoningByProvider, o.ReasoningByProvider)
	str(&r.KeyPriority, o.KeyPriority)
	str(&r.DiffStyle, o.DiffStyle)
	str(&r.CompactStrategy, o.CompactStrategy)
	bp(&r.CollapseDiffs, o.CollapseDiffs)
	bp(&r.CoAuthorTrailer, o.CoAuthorTrailer)
	if o.ToolTimeout != nil {
		r.ToolTimeout = o.ToolTimeout
	}
	r.ContextWindowOverrides = mergeContextWindows(base.ContextWindowOverrides, o.ContextWindowOverrides)
	r.Search = mergeSearch(base.Search, o.Search)
	r.Forbidden = unionStrings(base.Forbidden, o.Forbidden)
	r.AgentFeatures = mergeAgentFeatures(base.AgentFeatures, o.AgentFeatures)
	r.Editor = mergeEditor(base.Editor, o.Editor)
	r.Performance = mergePerformance(base.Performance, o.Performance)
	r.Retry = mergeRetry(base.Retry, o.Retry)
	r.WatchdogTimeouts = mergeWatchdog(base.WatchdogTimeouts, o.WatchdogTimeouts)
	r.EditorIntegrations = mergeEditorIntegrations(base.EditorIntegrations, o.EditorIntegrations)
	str(&rt.Default, o.Router.Default)
	str(&rt.Desloppify, o.Router.Desloppify)
	str(&rt.Verify, o.Router.Verify)
	str(&rt.Semantic, o.Router.Semantic)
	rt.FallbackByModel = mergeMap(base.Router.FallbackByModel, o.Router.FallbackByModel)
	r.Router = rt
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
		if v.Verbosity != "" {
			m.Verbosity = v.Verbosity
		}
		if v.Exclude {
			m.Exclude = true
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

// save writes c to path. Keys this package does not model (the "remote"
// section, or anything a user added by hand) are kept, so a settings change
// never deletes them. A file that is not a JSON object is refused rather than
// overwritten. The write goes to a temporary file in the same directory and
// is renamed into place, so a crash leaves either the old or the new file.
func save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	fresh, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(fresh, &next); err != nil {
		return err
	}
	kept, err := readObject(path)
	if err != nil {
		return err
	}
	for k := range configKeys() {
		delete(kept, k)
	}
	for k, v := range next {
		kept[k] = v
	}
	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(out, '\n'))
}

// readObject returns the top-level keys of the JSON object at path. A missing
// or empty file is an empty object.
func readObject(path string) (map[string]json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return obj, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return obj, nil
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object, not overwriting it: %w", path, err)
	}
	return obj, nil
}

// configKeys is the set of top-level JSON keys that Config owns.
func configKeys() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// writeAtomic replaces path with data via a synced temporary file (0600).
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
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
	case "verbosity":
		r.Verbosity = strings.TrimSpace(fmt.Sprint(v))
	case "thinking":
		s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		if s != "" && s != "on" && s != "off" {
			return true, fmt.Errorf("%s must be on, off or default", key)
		}
		r.Thinking = s
	case "exclude":
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" {
			s = "false"
		}
		b, err := strconv.ParseBool(s)
		if err != nil {
			return true, fmt.Errorf("%s must be true or false", key)
		}
		r.Exclude = b
	case "budget":
		n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
		if err != nil || n < 0 {
			return true, fmt.Errorf("%s must be a whole number of tokens, 0 for the default", key)
		}
		r.Budget = n
	default:
		return true, fmt.Errorf("unknown reasoning field %q (use effort, budget, service_tier, verbosity or exclude)", field)
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

// DailyBudget is the daily token limit, or 0 when there is none.
func (c Config) DailyBudget() int {
	if c.DailyTokenBudget == nil || *c.DailyTokenBudget < 0 {
		return 0
	}
	return *c.DailyTokenBudget
}

// ChatDailyBudget is the per-chat daily token limit, or 0 when there is none.
func (c Config) ChatDailyBudget() int {
	if c.ChatDailyTokenBudget == nil || *c.ChatDailyTokenBudget < 0 {
		return 0
	}
	return *c.ChatDailyTokenBudget
}

// ToolResultsKept is how many tool-result summaries compaction keeps. Unset
// or negative means the default of 30; the working state never holds more
// than 30, so larger values are not honoured (see agent.SummaryCompactor).
func (c Config) ToolResultsKept() int {
	if c.MaxToolResults <= 0 {
		return 0
	}
	return c.MaxToolResults
}
