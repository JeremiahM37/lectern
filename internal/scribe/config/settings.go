package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DefaultHookTimeout is the seconds a hook may run when its Timeout is zero.
// It matches the hooks package.
const DefaultHookTimeout = 10

// DefaultToolTimeout is the minutes a shell or project command may run when
// ToolTimeout is unset.
const DefaultToolTimeout = 2.0

// HookTimeout returns the run limit for a hook whose Timeout is secs.
func HookTimeout(secs int) time.Duration {
	if secs <= 0 {
		secs = DefaultHookTimeout
	}
	return time.Duration(secs) * time.Second
}

// ClampToolTimeout turns Config.ToolTimeout into the limit for one shell or
// project command. nil (unset) gives DefaultToolTimeout. A set value is
// clamped to 0.5-30 minutes. Zero or less gives 0, which means no limit.
func ClampToolTimeout(minutes *float64) time.Duration {
	m := DefaultToolTimeout
	if minutes != nil {
		m = *minutes
	}
	if m <= 0 {
		return 0
	}
	m = min(max(m, 0.5), 30)
	return time.Duration(m * float64(time.Minute))
}

// ModelsAPI is where a provider's model list is read from. In JSON it is
// either a URL string or the boolean false, which stops listing models for
// that provider. The zero value is not a setting: leave the field nil for the
// default (baseURL + "/models").
type ModelsAPI struct {
	URL string // explicit list URL
	Off bool   // true for "modelsAPI": false
}

// MarshalJSON writes the URL, or false when listing is off.
func (m ModelsAPI) MarshalJSON() ([]byte, error) {
	if m.Off {
		return []byte("false"), nil
	}
	return json.Marshal(m.URL)
}

// UnmarshalJSON accepts a URL string or false.
func (m *ModelsAPI) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "false" {
		*m = ModelsAPI{Off: true}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf(`models_api must be a URL or false: %w`, err)
	}
	*m = ModelsAPI{URL: strings.TrimSpace(s)}
	return nil
}

// AgentFeatures switches optional agent behaviour. A nil field keeps its
// default, shown in the comment. Use Enabled to read a flag by its JSON key.
type AgentFeatures struct {
	Desloppify           *bool `json:"desloppify,omitempty"`             // default false: cleanup pass after edits
	TierRouting          *bool `json:"tier_routing,omitempty"`           // default true: cheaper models for simple steps
	DispatchCache        *bool `json:"dispatch_cache,omitempty"`         // default true: reuse subagent results
	TargetFileValidation *bool `json:"target_file_validation,omitempty"` // default true: check edit targets exist
	VerifyEdits          *bool `json:"verify_edits,omitempty"`           // default false: LLM review of edits
	AgentSkills          *bool `json:"agent_skills,omitempty"`           // default true: skills are offered to the agent
	OnDemandTools        *bool `json:"on_demand_tools,omitempty"`        // default false: load tools when asked
}

// agentFeatureDefaults lists every AgentFeatures key with its default, in the
// order the settings screen shows them.
var agentFeatureDefaults = []struct {
	key string
	def bool
}{
	{"desloppify", false},
	{"tier_routing", true},
	{"dispatch_cache", true},
	{"target_file_validation", true},
	{"verify_edits", false},
	{"agent_skills", true},
	{"on_demand_tools", false},
}

// AgentFeatureKeys returns every key Enabled and Set accept, in display order.
func AgentFeatureKeys() []string {
	out := make([]string, 0, len(agentFeatureDefaults))
	for _, f := range agentFeatureDefaults {
		out = append(out, f.key)
	}
	return out
}

// field returns the pointer for key, or nil for an unknown key.
func (f *AgentFeatures) field(key string) **bool {
	switch key {
	case "desloppify":
		return &f.Desloppify
	case "tier_routing":
		return &f.TierRouting
	case "dispatch_cache":
		return &f.DispatchCache
	case "target_file_validation":
		return &f.TargetFileValidation
	case "verify_edits":
		return &f.VerifyEdits
	case "agent_skills":
		return &f.AgentSkills
	case "on_demand_tools":
		return &f.OnDemandTools
	}
	return nil
}

// Enabled reports whether a feature is on. A nil receiver, a nil field or an
// unknown key gives the default.
func (f *AgentFeatures) Enabled(key string) bool {
	for _, d := range agentFeatureDefaults {
		if d.key != key {
			continue
		}
		if f == nil {
			return d.def
		}
		return Bool(*f.field(key), d.def)
	}
	return false
}

// Set turns a feature on or off. It errors on an unknown key.
func (f *AgentFeatures) Set(key string, on bool) error {
	if f == nil {
		return fmt.Errorf("agent features are not set up")
	}
	p := f.field(key)
	if p == nil {
		return fmt.Errorf("unknown agent feature %q", key)
	}
	*p = &on
	return nil
}

// mergeAgentFeatures overlays o onto base field by field.
func mergeAgentFeatures(base, o *AgentFeatures) *AgentFeatures {
	if base == nil && o == nil {
		return nil
	}
	out := AgentFeatures{}
	if base != nil {
		out = *base
	}
	if o != nil {
		for _, k := range AgentFeatureKeys() {
			if v := *o.field(k); v != nil {
				_ = out.Set(k, *v)
			}
		}
	}
	return &out
}

// EditorSettings is the editor the in-app editor starts. Empty Command means
// the default ("nvim"). Args are passed before the file name.
type EditorSettings struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// DefaultEditorCommand is the editor used when Command is empty.
const DefaultEditorCommand = "nvim"

// CommandOrDefault returns the command and arguments to start the editor.
func (e EditorSettings) CommandOrDefault() (string, []string) {
	cmd := strings.TrimSpace(e.Command)
	if cmd == "" {
		cmd = DefaultEditorCommand
	}
	return cmd, append([]string(nil), e.Args...)
}

func mergeEditor(base, o EditorSettings) EditorSettings {
	if strings.TrimSpace(o.Command) != "" {
		base.Command = o.Command
	}
	if o.Args != nil {
		base.Args = o.Args
	}
	return base
}

// SearchSettings holds the keys and options of the web tools. The keys are
// never stored here: each names an environment variable, and a key saved with
// `lectern code --set-key` under the same id (brave-api-key, jina-api-key)
// is the fallback. See the registry's Secret method.
type SearchSettings struct {
	// BraveKeyEnv names the variable holding the Brave Search key. Default
	// BRAVE_SEARCH_API_KEY.
	BraveKeyEnv string `json:"brave_key_env,omitempty"`
	// JinaKeyEnv names the variable holding the Jina Reader key. Default
	// JINA_API_KEY. The key is optional: the free tier works without one.
	JinaKeyEnv string `json:"jina_key_env,omitempty"`
	// JinaReader sends fetched URLs to r.jina.ai before a local fetch. It is
	// off by default because it shares every URL with a third party.
	JinaReader *bool `json:"jina_reader,omitempty"`
}

// BraveEnv is the variable that holds the Brave key.
func (s SearchSettings) BraveEnv() string {
	if s.BraveKeyEnv == "" {
		return "BRAVE_SEARCH_API_KEY"
	}
	return s.BraveKeyEnv
}

// JinaEnv is the variable that holds the Jina Reader key.
func (s SearchSettings) JinaEnv() string {
	if s.JinaKeyEnv == "" {
		return "JINA_API_KEY"
	}
	return s.JinaKeyEnv
}

// UseJinaReader reports whether URLs go to Jina first.
func (s SearchSettings) UseJinaReader() bool { return Bool(s.JinaReader, false) }

func mergeSearch(base, o SearchSettings) SearchSettings {
	if o.BraveKeyEnv != "" {
		base.BraveKeyEnv = o.BraveKeyEnv
	}
	if o.JinaKeyEnv != "" {
		base.JinaKeyEnv = o.JinaKeyEnv
	}
	if o.JinaReader != nil {
		base.JinaReader = o.JinaReader
	}
	return base
}

// Performance holds request options that change the wire format. Empty or
// nil fields keep the adapter's default.
type Performance struct {
	// Thinking is off, adaptive, auto or enabled (Anthropic).
	Thinking string `json:"thinking,omitempty"`
	// Effort is the reasoning effort for Anthropic and OpenAI-style models.
	Effort string `json:"effort,omitempty"`
	// Speed is the speed tier some providers accept, e.g. "fast".
	Speed string `json:"speed,omitempty"`
	// CacheTTL is the prompt cache lifetime: 5m (default) or 1h.
	CacheTTL string `json:"cache_ttl,omitempty"`
	// ToolStreaming streams tool arguments as they are generated. Default on;
	// an explicit false turns it off.
	ToolStreaming *bool `json:"tool_streaming,omitempty"`
	// SendReasoning sends prior reasoning back to the provider. Default off.
	SendReasoning *bool `json:"send_reasoning,omitempty"`
	// DisableParallelToolUse makes the model call one tool at a time.
	DisableParallelToolUse *bool `json:"disable_parallel_tool_use,omitempty"`
	// SpeedByProvider overrides Speed for one provider id, so fast mode can
	// be on for Claude and off for another provider. A provider with no
	// entry uses Speed; an entry of "" or "standard" turns fast mode off for
	// that provider only. Without this map every request keeps the old
	// single global value.
	SpeedByProvider map[string]string `json:"speed_by_provider,omitempty"`
	// GroqReasoningFormat is Groq's reasoning_format: parsed, raw or hidden.
	// "" and "off" send nothing, so Groq's own default applies.
	GroqReasoningFormat string `json:"groq_reasoning_format,omitempty"`
}

// SpeedFor returns the speed tier for one provider: its own entry in
// SpeedByProvider when it has one, else the global Speed.
func (p Performance) SpeedFor(provider string) string {
	if v, ok := p.SpeedByProvider[provider]; ok {
		return v
	}
	return p.Speed
}

// mergeSpeedByProvider overlays o's per-provider speeds on base's. Entries
// are merged one provider at a time, so a project file can change one
// provider without dropping the global file's others.
func mergeSpeedByProvider(base, o map[string]string) map[string]string {
	if len(o) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(o))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range o {
		out[k] = v
	}
	return out
}

func mergePerformance(base, o Performance) Performance {
	base.SpeedByProvider = mergeSpeedByProvider(base.SpeedByProvider, o.SpeedByProvider)
	if o.GroqReasoningFormat != "" {
		base.GroqReasoningFormat = o.GroqReasoningFormat
	}
	if o.Thinking != "" {
		base.Thinking = o.Thinking
	}
	if o.Effort != "" {
		base.Effort = o.Effort
	}
	if o.Speed != "" {
		base.Speed = o.Speed
	}
	if o.CacheTTL != "" {
		base.CacheTTL = o.CacheTTL
	}
	if o.ToolStreaming != nil {
		base.ToolStreaming = o.ToolStreaming
	}
	if o.SendReasoning != nil {
		base.SendReasoning = o.SendReasoning
	}
	if o.DisableParallelToolUse != nil {
		base.DisableParallelToolUse = o.DisableParallelToolUse
	}
	return base
}

// Retry sets how many times a failed model call is tried again. Zero keeps
// the agent's default.
type Retry struct {
	// Transient is retries for rate limits, overload and network errors.
	Transient int `json:"transient,omitempty"`
	// Stall is retries when a stream stops making progress.
	Stall int `json:"stall,omitempty"`
	// BaseDelayMs is the base of the transient backoff in milliseconds: the
	// wait before retry n is base×2^n plus jitter. Zero keeps the agent's
	// default (1 s for chat, 2 s for subagents). The agent clamps it to
	// 250 ms–60 s.
	BaseDelayMs int `json:"base_delay_ms,omitempty"`
	// MaxAttempts is the legacy count for both kinds of retry. It applies
	// only where Transient or Stall is unset.
	MaxAttempts int `json:"max_attempts,omitempty"`
}

// BaseDelay is the transient backoff base as a duration. It is zero when
// unset, so the agent applies its own default.
func (r Retry) BaseDelay() time.Duration {
	return time.Duration(r.BaseDelayMs) * time.Millisecond
}

func mergeRetry(base, o Retry) Retry {
	if o.Transient != 0 {
		base.Transient = o.Transient
	}
	if o.Stall != 0 {
		base.Stall = o.Stall
	}
	if o.BaseDelayMs != 0 {
		base.BaseDelayMs = o.BaseDelayMs
	}
	if o.MaxAttempts != 0 {
		base.MaxAttempts = o.MaxAttempts
	}
	return base
}

// mergeMap overlays o onto base key by key. o wins for a key both hold.
func mergeMap[V any](base, o map[string]V) map[string]V {
	if len(base) == 0 && len(o) == 0 {
		return nil
	}
	out := make(map[string]V, len(base)+len(o))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range o {
		out[k] = v
	}
	return out
}

// mergeContextWindows overlays context window overrides. A value of zero in
// o removes an override inherited from the global file.
func mergeContextWindows(base, o map[string]int) map[string]int {
	if len(base) == 0 && len(o) == 0 {
		return nil
	}
	out := make(map[string]int, len(base)+len(o))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range o {
		if v == 0 {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// unionStrings returns a followed by the entries of b it lacks, trimmed and
// without blanks.
func unionStrings(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// diffStyles, keyStyles and so on are the accepted values of the string
// settings. Validate reports anything else.
var (
	diffStyles       = []string{"", "default", "sidebyside", "compact"}
	compactStrategy  = []string{"", "v2", "v1", "disabled"}
	keyPriorities    = []string{"", "env", "app"}
	thinkingModes    = []string{"", "off", "adaptive", "auto", "enabled"}
	cacheTTLs        = []string{"", "5m", "1h"}
	reasoningEfforts = []string{"", "low", "medium", "high", "xhigh", "max", "minimal", "none"}
	groqFormats      = []string{"", "off", "parsed", "raw", "hidden"}
	speedTiers       = []string{"", "standard", "fast"}
)

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Validate reports every setting whose value this build does not accept. It
// does not change the config, and Load does not call it, so a bad value is
// reported where the user edits settings instead of stopping the agent at
// start.
func (c Config) Validate() error {
	var bad []string
	check := func(name, v string, allowed []string) {
		if !oneOf(v, allowed) {
			bad = append(bad, fmt.Sprintf("%s %q is not one of %s", name, v, strings.Join(allowed[1:], ", ")))
		}
	}
	check("key_priority", c.KeyPriority, keyPriorities)
	check("diff_style", c.DiffStyle, diffStyles)
	check("compact_strategy", c.CompactStrategy, compactStrategy)
	check("performance.thinking", c.Performance.Thinking, thinkingModes)
	check("performance.cache_ttl", c.Performance.CacheTTL, cacheTTLs)
	check("performance.effort", c.Performance.Effort, reasoningEfforts)
	check("performance.groq_reasoning_format", c.Performance.GroqReasoningFormat, groqFormats)
	for k, v := range c.Performance.SpeedByProvider {
		if k == "" {
			bad = append(bad, "performance.speed_by_provider has an empty provider id")
			continue
		}
		check(fmt.Sprintf("performance.speed_by_provider[%q]", k), v, speedTiers)
	}
	if c.CompactKeepRecent < 0 {
		bad = append(bad, "compact_keep_recent cannot be negative; use 0 for the default")
	}
	if c.DailyTokenBudget != nil && *c.DailyTokenBudget < 0 {
		bad = append(bad, "daily_token_budget cannot be negative; use 0 for no limit")
	}
	if c.Retry.Transient < 0 || c.Retry.Stall < 0 || c.Retry.MaxAttempts < 0 {
		bad = append(bad, "retry counts cannot be negative")
	}
	if c.Retry.BaseDelayMs < 0 {
		bad = append(bad, "retry.base_delay_ms cannot be negative; use 0 for the default")
	}
	for k, v := range c.ContextWindowOverrides {
		if v < 0 {
			bad = append(bad, fmt.Sprintf("context_window_overrides[%q] cannot be negative", k))
		}
	}
	for k := range c.Router.FallbackByModel {
		if !strings.Contains(k, "/") {
			bad = append(bad, fmt.Sprintf("fallback_by_model key %q must be provider/model", k))
		}
	}
	for _, p := range c.Providers {
		if p.ModelsAPI != nil && !p.ModelsAPI.Off && p.ModelsAPI.URL == "" {
			bad = append(bad, fmt.Sprintf("provider %q: models_api is empty; use a URL or false", p.ID))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("invalid settings: %s", strings.Join(bad, "; "))
}
