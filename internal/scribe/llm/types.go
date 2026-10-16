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
	// CacheSystem asks adapters that support prompt caching (Anthropic) to
	// mark the system prompt and the last message as cache breakpoints.
	CacheSystem bool
	// Headers are extra HTTP headers (e.g. session affinity for gateways).
	Headers map[string]string
}

// EventType discriminates Event.
type EventType string

const (
	EvText          EventType = "text"           // Text delta
	EvReasoning     EventType = "reasoning"      // Text delta of reasoning
	EvReasoningSig  EventType = "reasoning_sig"  // Signature for the current reasoning block
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
	ID            string  `json:"id"`   // provider-local id
	Name          string  `json:"name"` // display name
	ContextWindow int     `json:"context_window,omitempty"`
	MaxOutput     int     `json:"max_output,omitempty"`
	InputPerMTok  float64 `json:"input_per_mtok,omitempty"`  // USD
	OutputPerMTok float64 `json:"output_per_mtok,omitempty"` // USD
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok,omitempty"`
	Reasoning     bool    `json:"reasoning,omitempty"`
	Vision        bool    `json:"vision,omitempty"`
	Tools         bool    `json:"tools,omitempty"`
}

// Cost returns the USD cost of u at m's prices.
func (m ModelInfo) Cost(u Usage) float64 {
	return (float64(u.InputTokens)*m.InputPerMTok +
		float64(u.OutputTokens)*m.OutputPerMTok +
		float64(u.CacheReadTokens)*m.CacheReadPerMTok +
		float64(u.CacheWriteTokens)*m.CacheWritePerMTok) / 1e6
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
}

func (e *APIError) Error() string {
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
		case ae.StatusCode == 429:
			return CatRateLimited, true
		case ae.StatusCode == 529 || ae.Type == "overloaded_error":
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
	if strings.Contains(s, "stream") {
		return CatStream, true
	}
	return CatOther, false
}
