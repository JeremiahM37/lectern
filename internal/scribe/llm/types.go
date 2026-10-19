// Package llm is Scribe's provider-neutral model interface: one message
// format, one tool-spec format and one streaming event type that every
// provider adapter (Anthropic Messages, OpenAI-compatible chat completions,
// Gemini) translates to and from.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Role of a message in the conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool carries tool results. Anthropic adapters fold these into a
	// user message with tool_result blocks; OpenAI adapters send one
	// role=tool message per result.
	RoleTool Role = "tool"
)

// PartType discriminates Part.
type PartType string

const (
	PartText       PartType = "text"
	PartReasoning  PartType = "reasoning"
	PartToolCall   PartType = "tool_call"
	PartToolResult PartType = "tool_result"
	PartImage      PartType = "image"
)

// Part is one block of a message. Only the fields for its Type are set.
type Part struct {
	Type PartType `json:"type"`

	// PartText, PartReasoning
	Text string `json:"text,omitempty"`
	// Signature is the provider's opaque reasoning signature (Anthropic
	// thinking blocks must be echoed back with it).
	Signature string `json:"signature,omitempty"`

	// PartToolCall
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`

	// PartToolResult (ToolCallID and ToolName set as well)
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"is_error,omitempty"`

	// PartImage
	MediaType string `json:"media_type,omitempty"` // e.g. image/png
	Data      []byte `json:"data,omitempty"`       // raw bytes (base64 in JSON)
}

// Message is one turn.
type Message struct {
	Role  Role   `json:"role"`
	Parts []Part `json:"parts"`
}

// Text returns the concatenated text parts.
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ToolCalls returns the tool-call parts.
func (m Message) ToolCalls() []Part {
	var out []Part
	for _, p := range m.Parts {
		if p.Type == PartToolCall {
			out = append(out, p)
		}
	}
	return out
}

// UserText builds a plain user message.
func UserText(s string) Message {
	return Message{Role: RoleUser, Parts: []Part{{Type: PartText, Text: s}}}
}

// AssistantText builds a plain assistant message.
func AssistantText(s string) Message {
	return Message{Role: RoleAssistant, Parts: []Part{{Type: PartText, Text: s}}}
}

// ToolSpec describes a tool to the model. Schema is a JSON Schema object.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"`
}

// Request is one model call.
type Request struct {
	Model    string    // provider-local model id (no "provider/" prefix)
	System   string    // system prompt; adapters place it per provider rules
	Messages []Message // conversation, oldest first
	Tools    []ToolSpec
	// ToolChoice: "" or "auto" (default), "none", "required".
	ToolChoice  string
	MaxTokens   int
	Temperature *float64
	// Reasoning effort: "" (provider default/off), "low", "medium", "high".
	Reasoning string
	// ThinkingBudget is an explicit reasoning token budget for providers that
	// take one (Anthropic budget-style models). 0 means the provider default,
	// which is the budget for Reasoning. Adapters ignore it where a budget is
	// not part of the wire format.
	ThinkingBudget int
	// ReasoningExclude asks OpenRouter to leave the reasoning text out of the
	// response (reasoning.exclude). Other adapters ignore it.
	ReasoningExclude bool
	// DeepSeekThinking is the DeepSeek thinking switch: "on" sends
	// thinking.type enabled, "off" sends disabled, "" leaves the field out so
	// the provider default (on) applies. Other adapters ignore it.
	DeepSeekThinking string
	// Verbosity is the answer length control (chat completions "verbosity":
	// low, medium or high). The OpenAI adapter sends it only for GPT-5 models.
	Verbosity string
	// ServiceTier selects a provider processing tier (Anthropic service_tier,
	// OpenAI service_tier). "" leaves the provider default; the value is sent
	// verbatim when set.
	ServiceTier string
	// CacheSystem asks adapters that support prompt caching (Anthropic) to
	// mark the system prompt and the last message as cache breakpoints.
	CacheSystem bool
	// CacheTTL is the lifetime of those breakpoints: "" or "5m" for the
	// provider default, "1h" for the one-hour cache (Anthropic). Ignored
	// when CacheSystem is false.
	CacheTTL string
	// DisableParallelToolUse asks the provider to make at most one tool call
	// per turn. Anthropic sends tool_choice.disable_parallel_tool_use; the
	// OpenAI-compatible adapter sends parallel_tool_calls false.
	DisableParallelToolUse bool
	// Headers are extra HTTP headers (e.g. session affinity for gateways).
	Headers map[string]string
	// SessionID identifies the chat this call belongs to. Anthropic sends it
	// as metadata.user_id, which lets the provider route a session's calls
	// consistently. "" omits it.
	SessionID string
	// Speed asks for a speed tier. Anthropic takes "fast" (fast mode), and
	// only on the models that offer it; any other model ignores the field.
	// "" or "standard" leaves the request unchanged.
	Speed string
	// ToolStreaming, when true, asks adapters that support it to stream tool
	// arguments as they are generated (Anthropic eager_input_streaming on
	// client tools). nil and false leave the request unchanged.
	ToolStreaming *bool
	// SendReasoning, when false, drops prior reasoning from the history
	// before sending it. nil and true keep it, which is the default.
	SendReasoning *bool
	// ClearThinking asks Anthropic to keep every earlier thinking block
	// (context management clear_thinking, keep all). It is sent only when
	// thinking is on for the request. false leaves the request unchanged.
	ClearThinking bool
	// ClearToolUses asks Anthropic to clear old tool results once the input
	// passes a share of the context window (context management
	// clear_tool_uses). false leaves the request unchanged.
	ClearToolUses bool
	// ContextWindow is the model's context window in tokens. The clear-tool-uses
	// thresholds scale with it. 0 uses the fixed floors alone.
	ContextWindow int
	// ServerCompaction asks Anthropic to summarise the conversation on its
	// side once the input passes a trigger (context_management compact). The
	// adapter sends it only for windows of 200 000 tokens or more. false
	// leaves the request unchanged. The summary is not yet carried back in
	// the history, so this stays opt-in.
	ServerCompaction bool
	// ReasoningFormat is Groq's reasoning_format (parsed, raw or hidden).
	// "" sends nothing. Only the Groq adapter id reads it.
	ReasoningFormat string
	// DashScopeThinking is the DashScope thinking switch: "on" sends
	// enable_thinking true, "" leaves it out. ThinkingBudget becomes
	// thinking_budget. Only a provider id "dashscope" reads it.
	DashScopeThinking string
	// ExtraParams are provider-specific body fields. The OpenAI-compatible
	// adapter merges them into the request body verbatim, and they win over
	// the fields it sets itself, except the core fields model, messages,
	// tools and stream, which it never takes from here. Other adapters ignore
	// the field.
	ExtraParams map[string]json.RawMessage
}

// EventType discriminates Event.
type EventType string

const (
	EvText          EventType = "text"            // Text delta
	EvReasoning     EventType = "reasoning"       // Text delta of reasoning
	EvReasoningSig  EventType = "reasoning_sig"   // Signature for the current reasoning block
	EvToolCallStart EventType = "tool_call_start" // ToolCallID, ToolName known
	EvToolCallDelta EventType = "tool_call_delta" // ToolCallID, ArgsDelta (partial JSON)
	EvToolCallEnd   EventType = "tool_call_end"   // ToolCallID, ToolName, Args complete
	EvFinish        EventType = "finish"          // FinishReason, Usage
	EvError         EventType = "error"           // Err
)

// FinishReason normalised across providers.
type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
	FinishFilter    FinishReason = "content_filter"
	FinishError     FinishReason = "error"
)

// Usage is token accounting for one model call.
type Usage struct {
	InputTokens      int `json:"input"`
	OutputTokens     int `json:"output"`
	CacheReadTokens  int `json:"cache_read"`
	CacheWriteTokens int `json:"cache_write"`
	ReasoningTokens  int `json:"reasoning,omitempty"`
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.CacheReadTokens += u2.CacheReadTokens
	u.CacheWriteTokens += u2.CacheWriteTokens
	u.ReasoningTokens += u2.ReasoningTokens
}

// Event is one streamed item. A stream always ends with exactly one
// EvFinish or EvError, after which the channel is closed.
type Event struct {
	Type         EventType
	Text         string
	ToolCallID   string
	ToolName     string
	ArgsDelta    string
	Args         json.RawMessage
	FinishReason FinishReason
	Usage        Usage
	Err          error
}

// Provider is a model backend.
type Provider interface {
	// ID is the registry id, e.g. "anthropic", "openai", "deepseek", "ollama".
	ID() string
	// Stream starts a model call. The returned channel is closed after the
	// terminal EvFinish/EvError event. Cancelling ctx aborts the HTTP call
	// and yields EvError with ctx.Err().
	Stream(ctx context.Context, req Request) (<-chan Event, error)
	// Models lists models the provider offers (may hit the network).
	Models(ctx context.Context) ([]ModelInfo, error)
}

// ModelInfo is metadata about one model.
type ModelInfo struct {
	ID                string  `json:"id"`   // provider-local id
	Name              string  `json:"name"` // display name
	ContextWindow     int     `json:"context_window,omitempty"`
	MaxOutput         int     `json:"max_output,omitempty"`
	InputPerMTok      float64 `json:"input_per_mtok,omitempty"`  // USD
	OutputPerMTok     float64 `json:"output_per_mtok,omitempty"` // USD
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok,omitempty"`
	Reasoning         bool    `json:"reasoning,omitempty"`
	// ReasoningStyle says how the model takes reasoning controls. See the
	// ReasoningStyle constants. "" means none (or not known to take any).
	ReasoningStyle string `json:"reasoning_style,omitempty"`
	// Estimated is true when the prices are a default for an unknown model
	// id, not a known price. The UI should show the cost as an estimate.
	Estimated bool `json:"estimated,omitempty"`
	Vision    bool `json:"vision,omitempty"`
	Tools     bool `json:"tools,omitempty"`
	// CatalogPriced is true when InputPerMTok and OutputPerMTok came from a
	// provider's own price catalog, so a zero price means free. The registry
	// does not replace such prices with its static table.
	CatalogPriced bool `json:"catalog_priced,omitempty"`
}

// Reasoning styles for ModelInfo.ReasoningStyle.
const (
	// ReasoningNone: no reasoning controls, or none known.
	ReasoningNone = ""
	// ReasoningBudget: thinking with an explicit token budget
	// ({"type":"enabled","budget_tokens":N} on Anthropic).
	ReasoningBudget = "budget"
	// ReasoningAdaptive: the model decides how much to think; only the effort
	// level is chosen (Anthropic thinking.type "adaptive" plus output_config).
	// Budget-style thinking is rejected by these models.
	ReasoningAdaptive = "adaptive"
	// ReasoningEffort: a reasoning_effort level on chat completions
	// (OpenAI-compatible o-series and GPT-5 models).
	ReasoningEffort = "effort"
)

// Cost returns the USD cost of u at m's prices.
func (m ModelInfo) Cost(u Usage) float64 {
	return (float64(u.InputTokens)*m.InputPerMTok +
		float64(u.OutputTokens)*m.OutputPerMTok +
		float64(u.CacheReadTokens)*m.CacheReadPerMTok +
		float64(u.CacheWriteTokens)*m.CacheWritePerMTok) / 1e6
}

// ToolArgs turns the accumulated argument text of one tool call into the
// Args of its EvToolCallEnd. Empty text (and "null") is {}. Text that is a
// JSON object is returned as-is. Anything else is malformed: it is returned
// as a JSON string holding the raw text, so the bytes are kept and the value
// stays valid JSON. Use UnparsedArgs to detect that case. The agent rejects
// malformed calls with an error the model can read; it never runs them.
func ToolArgs(raw string) json.RawMessage {
	t := strings.TrimSpace(raw)
	if t == "" || t == "null" {
		return json.RawMessage(`{}`)
	}
	if t[0] == '{' && json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	b, _ := json.Marshal(raw)
	return b
}

// UnparsedArgs reports whether args came from ToolArgs with text that did not
// parse as a JSON object, and returns that raw text.
func UnparsedArgs(args json.RawMessage) (string, bool) {
	if len(args) == 0 || args[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(args, &s); err != nil {
		return "", false
	}
	return s, true
}

// SplitModelRef splits "provider/model" into its parts. A ref with no slash
// returns provider "".
func SplitModelRef(ref string) (provider, model string) {
	if i := strings.IndexByte(ref, '/'); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// APIError is a non-2xx response from a provider.
type APIError struct {
	Provider   string
	StatusCode int
	Type       string // provider error type, e.g. "overloaded_error"
	Message    string
	RetryAfter time.Duration // from Retry-After header when present
	// RetryAfterSet is true when the response carried a parseable Retry-After,
	// so that an explicit zero means "retry now" rather than "no hint".
	RetryAfterSet bool
}

// StreamErrorType marks an APIError that arrived inside a successful stream
// rather than as an HTTP status. Retrying the request may help.
const StreamErrorType = "stream_error"

func (e *APIError) Error() string {
	if e.StatusCode == 0 {
		// Inside a stream there is no status to show.
		s := e.Provider + ": model stream error"
		if e.Message != "" {
			s += ": " + e.Message
		}
		return s
	}
	s := fmt.Sprintf("%s: HTTP %d", e.Provider, e.StatusCode)
	if e.Type != "" {
		s += " " + e.Type
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// ErrorCategory is the user-facing class of a model error.
type ErrorCategory string

const (
	CatRateLimited ErrorCategory = "Rate Limited"
	CatOverloaded  ErrorCategory = "Overloaded"
	CatAuth        ErrorCategory = "Auth Error"
	CatConfig      ErrorCategory = "Config Error"
	CatNetwork     ErrorCategory = "Network Error"
	CatStream      ErrorCategory = "Stream Error"
	CatOther       ErrorCategory = "Error"
)

// transientNetwork matches network failures that the list above misses:
// DNS lookups that failed to resolve (ENOTFOUND, EAI_AGAIN), a stream closed
// early, a socket hang-up, a connection that was reset, closed or aborted,
// and a request that was terminated.
var transientNetwork = regexp.MustCompile(`\benotfound\b|\beai_again\b|premature close|socket hang up|\bconnection (?:reset|closed|aborted|error|failed)\b|\bterminated\b`)

// Classify returns the category of err and whether retrying may help.
func Classify(err error) (ErrorCategory, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return CatOther, false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Type == StreamErrorType:
			return CatStream, true
		case ae.StatusCode == 429:
			return CatRateLimited, true
		case ae.StatusCode == 529 || ae.Type == "overloaded_error":
			return CatOverloaded, true
		case ae.StatusCode == 403 && strings.Contains(strings.ToLower(ae.Message), "rate"):
			// Some gateways answer a rate limit with 403 and say so in the body.
			return CatRateLimited, true
		case ae.StatusCode == 403 && strings.Contains(strings.ToLower(ae.Message), "overloaded"):
			return CatOverloaded, true
		case ae.StatusCode == 401 || ae.StatusCode == 403:
			return CatAuth, false
		case ae.StatusCode == 400 || ae.StatusCode == 404 || ae.StatusCode == 422:
			return CatConfig, false
		case ae.StatusCode >= 500:
			return CatOverloaded, true
		}
		return CatOther, false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"connection refused", "connection reset", "no such host", "timeout", "eof", "broken pipe", "tls handshake", "i/o timeout", "deadline exceeded"} {
		if strings.Contains(s, k) {
			return CatNetwork, true
		}
	}
	if transientNetwork.MatchString(s) {
		return CatNetwork, true
	}
	if strings.Contains(s, "stream") {
		return CatStream, true
	}
	return CatOther, false
}
