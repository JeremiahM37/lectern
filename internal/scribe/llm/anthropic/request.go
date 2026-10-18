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
	Speed        string            `json:"speed,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	// ContextManagement carries server-side context edits. It is nil unless a
	// setting asked for one, so the body is unchanged by default.
	ContextManagement *wireContextManagement `json:"context_management,omitempty"`
	Stream            bool                   `json:"stream"`
}

// wireContextManagement is the request's context_management object.
type wireContextManagement struct {
	Edits []block `json:"edits"`
}

// fastModeBeta is the beta flag fast mode needs on every request that sets
// speed. Fast mode is a research preview on a short list of models.
const fastModeBeta = "fast-mode-2026-02-01"

// contextManagementBeta is the beta flag the context_management edits need.
const contextManagementBeta = "context-management-2025-06-27"

// clearToolUsesKeep is how many of the latest tool uses survive a clear.
const clearToolUsesKeep = 5

// contextEdits returns the context management edits req asks for, in a fixed
// order so the body stays the same from one request to the next. Thinking
// preservation is sent only when thinking is on for this request, since the
// edit has nothing to keep otherwise.
func contextEdits(req llm.Request, thinking bool) []block {
	var edits []block
	if req.ClearThinking && thinking {
		edits = append(edits, block{"type": "clear_thinking_20251015", "keep": "all"})
	}
	if req.ClearToolUses {
		edits = append(edits, clearToolUsesEdit(req.ContextWindow))
	}
	return edits
}

// clearToolUsesEdit is the clear_tool_uses edit. It triggers at the larger of
// 120 000 input tokens and 65% of the window, keeps the last five tool uses,
// and clears at least the larger of 40 000 tokens and 20% of the window.
func clearToolUsesEdit(window int) block {
	return block{
		"type":           "clear_tool_uses_20250919",
		"trigger":        block{"type": "input_tokens", "value": max(120000, window*65/100)},
		"keep":           block{"type": "tool_uses", "value": clearToolUsesKeep},
		"clear_at_least": block{"type": "input_tokens", "value": max(40000, window/5)},
	}
}

// fastModes are the models that accept speed "fast". Opus 4.7 dropped it and
// returns an error, so it is left out.
var fastModes = map[string]bool{
	"claude-opus-5":   true,
	"claude-opus-5-5": true,
	"claude-opus-4-8": true,
}

// fastSpeed reports whether req asks for fast mode and the model takes it.
// A request for fast mode on any other model is sent unchanged rather than
// rejected, so one speed setting can stay on while the model changes.
func fastSpeed(req llm.Request) bool {
	if req.Speed != "fast" {
		return false
	}
	return fastModes[dateSuffix.ReplaceAllString(req.Model, "")]
}

// betaFeatures lists the anthropic-beta flags this request needs.
func betaFeatures(req llm.Request) []string {
	var out []string
	if fastSpeed(req) {
		out = append(out, fastModeBeta)
	}
	// The flag is needed only when the body carries an edit. A setting that
	// is on but has nothing to act on (thinking off) adds neither.
	if req.ClearThinking || req.ClearToolUses {
		if w, err := buildWire(req); err == nil && w.ContextManagement != nil {
			out = append(out, contextManagementBeta)
		}
	}
	return out
}

// ephemeral is a cache breakpoint. ttl "1h" asks for the one-hour cache;
// anything else gets the provider default (5 minutes).
func ephemeral(ttl string) block {
	if ttl == "1h" {
		return block{"type": "ephemeral", "ttl": "1h"}
	}
	return block{"type": "ephemeral"}
}

// clampOutput lowers an output cap to the model's documented maximum when the
// model is in the known table. Unknown ids are left alone: the provider
// decides, as it always has.
func clampOutput(model string, n int) int {
	if s, ok := lookupKnown(model); ok && s.maxOut > 0 && n > s.maxOut {
		return s.maxOut
	}
	return n
}

// buildBody translates an llm.Request into a Messages API request body.
func buildBody(req llm.Request) ([]byte, error) {
	body, err := buildWire(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// buildWire builds the request as a struct. Stream reads it to decide which
// beta flags the request needs, which depends on what the body ended up with.
func buildWire(req llm.Request) (wireRequest, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	maxTokens = clampOutput(req.Model, maxTokens)

	body := wireRequest{Model: req.Model, Stream: true}
	if req.SessionID != "" {
		body.Metadata = map[string]string{"user_id": req.SessionID}
	}
	if fastSpeed(req) {
		body.Speed = "fast"
	}

	if req.System != "" {
		sys := block{"type": "text", "text": req.System}
		if req.CacheSystem {
			sys["cache_control"] = ephemeral(req.CacheTTL)
		}
		body.System = []block{sys}
	}

	body.Messages = convertMessages(req.Messages)
	if req.SendReasoning != nil && !*req.SendReasoning {
		body.Messages = stripReasoning(body.Messages)
	}
	if req.CacheSystem {
		markCacheBreakpoint(body.Messages, req.CacheTTL)
	}

	for _, t := range req.Tools {
		schema := t.Schema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !json.Valid(schema) {
			return wireRequest{}, fmt.Errorf("anthropic: tool %q has an invalid input schema", t.Name)
		}
		tb := block{"name": t.Name, "input_schema": schema}
		if t.Description != "" {
			tb["description"] = t.Description
		}
		// Only an explicit true changes the wire. Without the flag the API
		// buffers each tool input and validates it whole.
		if req.ToolStreaming != nil && *req.ToolStreaming {
			tb["eager_input_streaming"] = true
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
		if req.DisableParallelToolUse && req.ToolChoice != "none" {
			body.ToolChoice["disable_parallel_tool_use"] = true
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
		// The clamp applies to the wire level only. Budget models map effort
		// to a token budget below, so they keep the mapping they always had.
		effort = clampEffort(req.Model, effort)
		if effort != "" {
			body.Thinking = &wireThinking{Type: "adaptive"}
			body.OutputConfig = &wireOutputConfig{Effort: effort}
		}
	case llm.ReasoningBudget:
		budget := req.ThinkingBudget
		if budget <= 0 {
			budget = thinkingBudgets[budgetEffort(effort)]
		}
		if budget > 0 {
			if budget < minThinkingBudget {
				budget = minThinkingBudget
			}
			// max_tokens must exceed the thinking budget.
			if maxTokens <= budget {
				maxTokens = clampOutput(req.Model, budget+defaultMaxTokens)
			}
			body.Thinking = &wireThinking{Type: "enabled", BudgetTokens: budget}
		}
	}

	// Temperature is rejected alongside any thinking, and by adaptive models
	// even without it. Where it is accepted and the caller left it unset, the
	// wire gets 0 (spec 4.8: temperature 0 when the model supports it).
	if body.Thinking == nil && style != llm.ReasoningAdaptive {
		if req.Temperature != nil {
			body.Temperature = req.Temperature
		} else if knownTemperatureModel(req.Model) {
			// Only for ids known to accept it: an unknown (newer) model may
			// reject temperature, and an unset value is safe to leave out.
			zero := 0.0
			body.Temperature = &zero
		}
	}
	body.ServiceTier = req.ServiceTier
	body.MaxTokens = maxTokens
	if edits := contextEdits(req, body.Thinking != nil); len(edits) > 0 {
		body.ContextManagement = &wireContextManagement{Edits: edits}
	}

	return body, nil
}

// effortLevels lists the effort levels a model accepts, lowest first. nil
// means the model takes no effort control at all. Ids this table does not
// know get every level, so a new model is never clamped by mistake.
func effortLevels(model string) []string {
	id := dateSuffix.ReplaceAllString(model, "")
	m := familyFirst.FindStringSubmatch(id)
	if m == nil {
		return allEffortLevels
	}
	major, minor := atoi(m[2]), atoi(m[3])
	switch {
	case major >= 5, m[1] == "opus" && (major > 4 || (major == 4 && minor >= 7)):
		return allEffortLevels
	case (m[1] == "opus" || m[1] == "sonnet") && major == 4 && minor == 6:
		return []string{"low", "medium", "high", "max"}
	case m[1] == "opus" && major == 4 && minor == 5:
		return []string{"low", "medium", "high"}
	}
	return nil
}

var allEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// clampEffort maps effort to a level the model accepts. An unsupported level
// goes to the nearest supported one below it, and only when nothing below is
// supported to the lowest one above. Going down keeps cost where the caller
// asked for less than the model offers. "" stays "".
func clampEffort(model, effort string) string {
	if effort == "" {
		return ""
	}
	levels := effortLevels(model)
	idx := -1
	for i, l := range allEffortLevels {
		if l == effort {
			idx = i
		}
	}
	if idx < 0 {
		return ""
	}
	supported := func(l string) bool {
		for _, s := range levels {
			if s == l {
				return true
			}
		}
		return false
	}
	for i := idx; i >= 0; i-- {
		if supported(allEffortLevels[i]) {
			return allEffortLevels[i]
		}
	}
	for i := idx + 1; i < len(allEffortLevels); i++ {
		if supported(allEffortLevels[i]) {
			return allEffortLevels[i]
		}
	}
	return ""
}

// stripReasoning removes thinking blocks from the history. Messages that
// held only thinking are dropped, and the user turns that become adjacent
// are merged, because the API rejects same-role neighbours.
func stripReasoning(msgs []wireMessage) []wireMessage {
	// The turn in progress keeps its thinking: with thinking on, the API
	// wants the thinking that preceded a tool call sent back with that
	// call's result, and a request without it can be refused. The current
	// turn starts at the last user message the user wrote (one that is not
	// only tool results).
	current := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && !onlyToolResults(msgs[i]) {
			current = i
			break
		}
	}
	var out []wireMessage
	for i, m := range msgs {
		var kept []block
		for _, b := range m.Content {
			if t := b["type"]; i < current && (t == "thinking" || t == "redacted_thinking") {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = append(out[n-1].Content, kept...)
			continue
		}
		out = append(out, wireMessage{Role: m.Role, Content: kept})
	}
	return out
}

// onlyToolResults reports whether a user message carries only tool results,
// so it continues a tool loop rather than starting a turn.
func onlyToolResults(m wireMessage) bool {
	if len(m.Content) == 0 {
		return false
	}
	for _, b := range m.Content {
		if b["type"] != "tool_result" {
			return false
		}
	}
	return true
}

// effortLevel returns r when it is an effort level the Messages API takes,
// else "". xhigh and max are adaptive-only levels (see budgetEffort for the
// budget-style models, which take none of them).
func effortLevel(r string) string {
	switch r {
	case "low", "medium", "high", "xhigh", "max":
		return r
	}
	return ""
}

// budgetEffort maps an effort level onto the budget-style table. Levels the
// table does not have (xhigh, max) use the largest budget rather than none.
func budgetEffort(effort string) string {
	if _, ok := thinkingBudgets[effort]; ok {
		return effort
	}
	if effort == "xhigh" || effort == "max" {
		return "high"
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
func markCacheBreakpoint(msgs []wireMessage, ttl string) {
	if len(msgs) == 0 {
		return
	}
	content := msgs[len(msgs)-1].Content
	for i := len(content) - 1; i >= 0; i-- {
		if t := content[i]["type"]; t == "thinking" || t == "redacted_thinking" {
			continue
		}
		content[i]["cache_control"] = ephemeral(ttl)
		return
	}
}
