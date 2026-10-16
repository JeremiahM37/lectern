package anthropic

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// thinkingBudgets maps llm reasoning effort to extended-thinking budgets.
var thinkingBudgets = map[string]int{
	"low":    2048,
	"medium": 8192,
	"high":   24576,
}

// minThinkingBudget is the smallest budget_tokens the API accepts.
const minThinkingBudget = 1024

// content blocks are built as maps so that optional fields are omitted
// exactly when unset, without omitempty tripping on legitimately empty
// values (an empty thinking string, for example).
type block = map[string]any

type wireMessage struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// wireThinking is either {"type":"enabled","budget_tokens":N} (budget
// models) or {"type":"adaptive"} (adaptive models, which take no budget).
type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// wireOutputConfig carries the effort level for adaptive thinking.
type wireOutputConfig struct {
	Effort string `json:"effort"`
}

type wireRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       []block           `json:"system,omitempty"`
	Messages     []wireMessage     `json:"messages"`
	Tools        []block           `json:"tools,omitempty"`
	ToolChoice   block             `json:"tool_choice,omitempty"`
	Temperature  *float64          `json:"temperature,omitempty"`
	Thinking     *wireThinking     `json:"thinking,omitempty"`
	OutputConfig *wireOutputConfig `json:"output_config,omitempty"`
	ServiceTier  string            `json:"service_tier,omitempty"`
	Stream       bool              `json:"stream"`
}

func ephemeral() block { return block{"type": "ephemeral"} }

// buildBody translates an llm.Request into a Messages API request body.
func buildBody(req llm.Request) ([]byte, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	body := wireRequest{Model: req.Model, Stream: true}

	if req.System != "" {
		sys := block{"type": "text", "text": req.System}
		if req.CacheSystem {
			sys["cache_control"] = ephemeral()
		}
		body.System = []block{sys}
	}

	body.Messages = convertMessages(req.Messages)
	if req.CacheSystem {
		markCacheBreakpoint(body.Messages)
	}

	for _, t := range req.Tools {
		schema := t.Schema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !json.Valid(schema) {
			return nil, fmt.Errorf("anthropic: tool %q has an invalid input schema", t.Name)
		}
		tb := block{"name": t.Name, "input_schema": schema}
		if t.Description != "" {
			tb["description"] = t.Description
		}
		body.Tools = append(body.Tools, tb)
	}
	if len(body.Tools) > 0 {
		switch req.ToolChoice {
		case "required":
			body.ToolChoice = block{"type": "any"}
		case "none":
			body.ToolChoice = block{"type": "none"}
		default:
			body.ToolChoice = block{"type": "auto"}
		}
	}

	style := ReasoningStyle(req.Model)
	effort := effortLevel(req.Reasoning)
	switch style {
	case llm.ReasoningAdaptive:
		// Adaptive models reject budget_tokens. A budget with no effort
		// picks the nearest effort level.
		if effort == "" && req.ThinkingBudget > 0 {
			effort = effortForBudget(req.ThinkingBudget)
		}
		if effort != "" {
			body.Thinking = &wireThinking{Type: "adaptive"}
			body.OutputConfig = &wireOutputConfig{Effort: effort}
		}
	case llm.ReasoningBudget:
		budget := req.ThinkingBudget
		if budget <= 0 {
			budget = thinkingBudgets[effort]
		}
		if budget > 0 {
			if budget < minThinkingBudget {
				budget = minThinkingBudget
			}
			// max_tokens must exceed the thinking budget.
			if maxTokens <= budget {
				maxTokens = budget + defaultMaxTokens
			}
			body.Thinking = &wireThinking{Type: "enabled", BudgetTokens: budget}
		}
	}

	// Temperature is rejected alongside any thinking, and by adaptive models
	// even without it.
	if req.Temperature != nil && body.Thinking == nil && style != llm.ReasoningAdaptive {
		body.Temperature = req.Temperature
	}
	body.ServiceTier = req.ServiceTier
	body.MaxTokens = maxTokens

	return json.Marshal(body)
}

// effortLevel returns r when it is a level the request type defines, else "".
func effortLevel(r string) string {
	switch r {
	case "low", "medium", "high":
		return r
	}
	return ""
}

// effortForBudget maps a thinking budget onto the nearest effort level.
func effortForBudget(budget int) string {
	switch {
	case budget <= thinkingBudgets["low"]:
		return "low"
	case budget <= thinkingBudgets["medium"]:
		return "medium"
	}
	return "high"
}

// convertMessages maps llm messages onto the API's user/assistant
// alternation. Tool results fold into user messages, and consecutive
// same-role messages are merged because the API rejects them.
func convertMessages(msgs []llm.Message) []wireMessage {
	var out []wireMessage
	for _, m := range msgs {
		role := "user"
		if m.Role == llm.RoleAssistant {
			role = "assistant"
		}
		blocks := convertParts(m.Parts)
		if len(blocks) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
		} else {
			out = append(out, wireMessage{Role: role, Content: blocks})
		}
	}
	for i := range out {
		if out[i].Role == "user" {
			out[i].Content = resultsFirst(out[i].Content)
		}
	}
	return out
}

func convertParts(parts []llm.Part) []block {
	var out []block
	for _, p := range parts {
		switch p.Type {
		case llm.PartText:
			if p.Text == "" {
				continue // the API rejects empty text blocks
			}
			out = append(out, block{"type": "text", "text": p.Text})
		case llm.PartReasoning:
			// Thinking must be echoed back with its signature. Unsigned
			// reasoning (for example from another provider) cannot be sent.
			if p.Signature == "" {
				continue
			}
			out = append(out, block{"type": "thinking", "thinking": p.Text, "signature": p.Signature})
		case llm.PartToolCall:
			out = append(out, block{"type": "tool_use", "id": p.ToolCallID, "name": p.ToolName, "input": toolInput(p.Args)})
		case llm.PartToolResult:
			tr := block{"type": "tool_result", "tool_use_id": p.ToolCallID}
			if p.Result != "" {
				tr["content"] = p.Result
			}
			if p.IsError {
				tr["is_error"] = true
			}
			out = append(out, tr)
		case llm.PartImage:
			if len(p.Data) == 0 {
				continue
			}
			mt := p.MediaType
			if mt == "" {
				mt = "image/png"
			}
			out = append(out, block{"type": "image", "source": block{
				"type":       "base64",
				"media_type": mt,
				"data":       base64.StdEncoding.EncodeToString(p.Data),
			}})
		}
	}
	return out
}

// toolInput returns args as a JSON object for the request history. The API
// requires tool_use input to be an object, so empty, malformed or non-object
// args become {}. Malformed calls never reach history as runnable input: the
// agent has already rejected them with an error the model saw.
func toolInput(args json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return json.RawMessage(`{}`)
	}
	return trimmed
}

// resultsFirst moves tool_result blocks ahead of other blocks, which the API
// expects when a user message answers a tool_use. The order within each
// group is kept.
func resultsFirst(blocks []block) []block {
	out := make([]block, 0, len(blocks))
	for _, b := range blocks {
		if b["type"] == "tool_result" {
			out = append(out, b)
		}
	}
	for _, b := range blocks {
		if b["type"] != "tool_result" {
			out = append(out, b)
		}
	}
	return out
}

// markCacheBreakpoint puts cache_control on the last cacheable block of the
// last message. Thinking blocks cannot carry cache_control, so the search
// walks back past them.
func markCacheBreakpoint(msgs []wireMessage) {
	if len(msgs) == 0 {
		return
	}
	content := msgs[len(msgs)-1].Content
	for i := len(content) - 1; i >= 0; i-- {
		if t := content[i]["type"]; t == "thinking" || t == "redacted_thinking" {
			continue
		}
		content[i]["cache_control"] = ephemeral()
		return
	}
}
