// Package openai is Scribe's adapter for OpenAI-compatible Chat Completions
// servers (OpenAI, DeepSeek, OpenRouter, Groq, Ollama, LM Studio, ...). It
// speaks the wire format over net/http and translates to and from package llm.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Options configures one provider instance.
type Options struct {
	ID         string            // registry id, returned by ID()
	BaseURL    string            // e.g. https://api.openai.com/v1
	APIKey     string            // bearer token; empty sends no Authorization header
	HTTPClient *http.Client      // nil uses a client with no overall timeout
	Headers    map[string]string // extra headers on every request
	Models     []string          // static model list, used when /models fails
	// ContextWindows gives the context window, in tokens, for static model
	// ids (a custom provider's {id, contextWindow} entries). Ids not listed
	// here get no window from this package; the registry fills it in later.
	ContextWindows map[string]int

	// NoStreamUsage omits stream_options for servers that reject it.
	NoStreamUsage bool
	// ReasoningField names a delta field that carries reasoning text. It is
	// checked before the built-in names reasoning_content and reasoning.
	ReasoningField string

	// TokenSource, when set, supplies the bearer token for each call instead
	// of APIKey. Used by providers that exchange a long-lived credential for
	// a short-lived token (GitHub Copilot). It is called once per request.
	TokenSource func(ctx context.Context) (string, error)
	// ModelsTimeout bounds GET /models. Zero means the 15 s default.
	ModelsTimeout time.Duration
	// ModelsURL replaces BaseURL + "/models" as the model list address.
	// Empty keeps the default.
	ModelsURL string
	// NoModelList never asks the server for its models. Models is used as
	// the list, and with no Models the listing is an error.
	NoModelList bool
}

// Provider is an llm.Provider for OpenAI-compatible chat completions.
type Provider struct {
	opts   Options
	client *http.Client
}

var _ llm.Provider = (*Provider)(nil)

const (
	maxErrorBody   = 64 << 10
	modelsTimeout  = 15 * time.Second
	terminalGrace  = time.Second
	defaultImgType = "image/png"
)

// New returns a Provider with defaults applied.
func New(opts Options) *Provider {
	if opts.ID == "" {
		opts.ID = "openai"
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{opts: opts, client: client}
}

// ID returns the registry id.
func (p *Provider) ID() string { return p.opts.ID }

// bearer returns the token for one call: TokenSource when set, else APIKey.
func (p *Provider) bearer(ctx context.Context) (string, error) {
	if p.opts.TokenSource != nil {
		tok, err := p.opts.TokenSource(ctx)
		if err != nil {
			return "", fmt.Errorf("%s: %w", p.opts.ID, err)
		}
		return tok, nil
	}
	return p.opts.APIKey, nil
}

func (p *Provider) modelsTimeout() time.Duration {
	if p.opts.ModelsTimeout > 0 {
		return p.opts.ModelsTimeout
	}
	return modelsTimeout
}

// ---- request ---------------------------------------------------------------

type wireRequest struct {
	Model               string          `json:"model"`
	Messages            []wireMessage   `json:"messages"`
	Tools               []wireTool      `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	Reasoning           *wireReasoning  `json:"reasoning,omitempty"`
	Thinking            *wireThinking   `json:"thinking,omitempty"`
	Verbosity           string          `json:"verbosity,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ServiceTier         string          `json:"service_tier,omitempty"`
	Stream              bool            `json:"stream"`
	StreamOptions       *wireStreamOpts `json:"stream_options,omitempty"`
}

// wireReasoning is OpenRouter's unified reasoning object. Effort and
// max_tokens are alternatives; the adapter sends effort when both are set.
type wireReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Exclude   bool   `json:"exclude,omitempty"`
}

// wireThinking is DeepSeek's thinking switch: {"type": "enabled"} or
// {"type": "disabled"}. DeepSeek thinks by default, so the field is sent only
// when the user has chosen one of the two.
type wireThinking struct {
	Type string `json:"type"`
}

type wireStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireMessage struct {
	Role             string         `json:"role"`
	Content          any            `json:"content"` // string, []wireContent or nil
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireCallFunction `json:"function"`
}

type wireCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireContent struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

// ReasoningStyle says how an OpenAI model takes reasoning controls: "effort"
// for reasoning_effort on o1, o3, o4-mini and GPT-5 chat models, "" for the
// rest (GPT-4.1 and GPT-5 chat reject the parameter). Gateway ids are matched
// on their last path segment.
func ReasoningStyle(model string) string {
	m := strings.ToLower(path.Base(model))
	switch {
	case m == "o1", m == "o3", strings.HasPrefix(m, "o3-"), strings.HasPrefix(m, "o4-mini"):
		return llm.ReasoningEffort
	case strings.HasPrefix(m, "gpt-5") && !strings.HasPrefix(m, "gpt-5-chat"):
		return llm.ReasoningEffort
	case strings.HasPrefix(m, "grok-3-mini"):
		// xAI: reasoning_effort on grok-3-mini (low or high).
		return llm.ReasoningEffort
	case xaiEffortModel(m):
		return llm.ReasoningEffort
	case strings.HasPrefix(strings.ToLower(model), "deepseek-v4"):
		// DeepSeek V4 takes reasoning_effort (high, max). Only bare ids match:
		// a gateway id such as "deepseek/deepseek-v4-pro" keeps its own reasoning
		// object, so the Think tab's DeepSeek rows never reach other providers.
		return llm.ReasoningEffort
	case strings.HasPrefix(strings.ToLower(model), "gemini-"):
		// Gemini's OpenAI-compatible endpoint takes reasoning_effort (minimal,
		// low, medium, high). Bare ids only, for the same reason as DeepSeek:
		// a gateway id such as "google/gemini-2.5-pro" is not matched here.
		return llm.ReasoningEffort
	}
	return llm.ReasoningNone
}

// geminiEffort maps a reasoning level to the values Gemini's compatible
// endpoint takes: minimal, low, medium and high. xhigh, max and ultra have no
// Gemini level, so they map to high, the top level. none and anything else
// return "", so the field is left out. Gemini 2.5 Pro and Gemini 3 cannot turn
// reasoning off, so none is not sent to them.
func geminiEffort(v string) string {
	switch v {
	case "minimal", "low", "medium", "high":
		return v
	case "xhigh", "max", "ultra":
		return "high"
	}
	return ""
}

// deepSeekThinkingType maps the Think tab's on/off choice to DeepSeek's wire
// value. Unset returns "", so the thinking field is left out and the request
// keeps DeepSeek's default, which is on.
func deepSeekThinkingType(v string) string {
	switch v {
	case "on":
		return "enabled"
	case "off":
		return "disabled"
	}
	return ""
}

// sendsDeepSeekThinking reports whether the thinking field goes on the wire.
// DeepSeek documents it for the V4 family only, so other DeepSeek ids (and
// gateway ids) never get it, whatever the caller sets.
func sendsDeepSeekThinking(provider, model string) bool {
	return provider == "deepseek" && strings.HasPrefix(strings.ToLower(model), "deepseek-v4")
}

// xaiEffortModel reports the grok ids that take reasoning_effort, per xAI's
// reasoning guide (checked 2026-10-17): grok-4.5, grok-4.6 and grok-4.7 accept
// low and high. grok-4.20-multi-agent also takes reasoning.effort, but there it
// sets how many agents collaborate, not reasoning depth, so it is left out.
// Other grok-4 ids are not listed as taking the field and get no row.
func xaiEffortModel(m string) bool {
	for _, p := range []string{"grok-4.5", "grok-4.6", "grok-4.7"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// deepSeekEffort maps a reasoning level to the two values DeepSeek takes,
// following its documented mapping: minimal and low go to low, medium, high
// and xhigh to high, max and ultra to max. Anything else returns "", so the
// field is left out rather than sent as an error.
func deepSeekEffort(v string) string {
	switch v {
	case "minimal", "low":
		return "low"
	case "medium", "high", "xhigh":
		return "high"
	case "max", "ultra":
		return "max"
	}
	return ""
}

// sendsVerbosity decides whether verbosity goes on the wire. Only GPT-5 models
// take it on api.openai.com; the other families reject the parameter.
func sendsVerbosity(provider, model string) bool {
	return provider == "openai" && ReasoningStyle(model) == llm.ReasoningEffort &&
		strings.HasPrefix(strings.ToLower(path.Base(model)), "gpt-5")
}

// sendsReasoningEffort decides whether reasoning_effort goes on the wire. On
// api.openai.com it is sent only to effort-style models, because the others
// reject it. Other OpenAI-compatible endpoints host model families this table
// does not list, so they keep the send-when-set behaviour.
func sendsReasoningEffort(provider, model string) bool {
	if ReasoningStyle(model) == llm.ReasoningEffort {
		return true
	}
	// xAI's own API is gated the same way: grok ids outside xaiEffortModel
	// reject the field, so they never get it, whatever the caller sets.
	if provider == "xai" {
		return false
	}
	return provider != "openai"
}

// buildBody translates a provider-neutral request into the wire body.
func (p *Provider) buildBody(req llm.Request) ([]byte, error) {
	w := wireRequest{
		Model:       req.Model,
		Messages:    p.translate(req),
		Temperature: req.Temperature,
		Stream:      true,
	}
	if req.MaxTokens > 0 {
		// OpenAI's reasoning models reject max_tokens; everyone else takes it.
		if p.opts.ID == "openai" {
			w.MaxCompletionTokens = req.MaxTokens
		} else {
			w.MaxTokens = req.MaxTokens
		}
	}
	for _, t := range req.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		w.Tools = append(w.Tools, wireTool{
			Type:     "function",
			Function: wireFunction{Name: t.Name, Description: t.Description, Parameters: params},
		})
	}
	if len(w.Tools) > 0 && (req.ToolChoice == "none" || req.ToolChoice == "required") {
		w.ToolChoice = req.ToolChoice
	}
	if len(w.Tools) > 0 && req.DisableParallelToolUse {
		off := false
		w.ParallelToolCalls = &off
	}
	if p.opts.ID == "openrouter" {
		// OpenRouter takes one reasoning object for every model family.
		if rr := openRouterReasoning(req); rr != nil {
			w.Reasoning = rr
		}
	} else if p.opts.ID == "deepseek" && ReasoningStyle(req.Model) == llm.ReasoningEffort {
		// DeepSeek V4 takes only high or max, so other levels are mapped.
		w.ReasoningEffort = deepSeekEffort(req.Reasoning)
	} else if p.opts.ID == "gemini" && ReasoningStyle(req.Model) == llm.ReasoningEffort {
		// Gemini's compatible endpoint takes four levels; the rest are mapped.
		w.ReasoningEffort = geminiEffort(req.Reasoning)
	} else if req.Reasoning != "" && sendsReasoningEffort(p.opts.ID, req.Model) {
		w.ReasoningEffort = req.Reasoning
	}
	if req.Verbosity != "" && sendsVerbosity(p.opts.ID, req.Model) {
		w.Verbosity = req.Verbosity
	}
	if t := deepSeekThinkingType(req.DeepSeekThinking); t != "" && sendsDeepSeekThinking(p.opts.ID, req.Model) {
		w.Thinking = &wireThinking{Type: t}
	}
	w.ServiceTier = req.ServiceTier
	if !p.opts.NoStreamUsage {
		w.StreamOptions = &wireStreamOpts{IncludeUsage: true}
	}
	raw, err := json.Marshal(w)
	if err != nil || len(req.ExtraParams) == 0 {
		return raw, err
	}
	return mergeExtraParams(raw, req.ExtraParams)
}

// protectedBodyKeys are the request fields that extra params may never set.
// Overriding them would change the model, the conversation, the tool list or
// whether the reply streams, which the agent relies on. The config layer
// warns about these keys; this list is what keeps them off the wire.
var protectedBodyKeys = map[string]bool{"model": true, "messages": true, "tools": true, "stream": true}

// mergeExtraParams sets the extra fields on a built request body. An extra
// field replaces a field of the same name that the adapter set, so a
// provider-specific switch always reaches the wire. Protected keys are
// skipped.
func mergeExtraParams(body []byte, extra map[string]json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if protectedBodyKeys[k] {
			continue
		}
		fields[k] = v
	}
	return json.Marshal(fields)
}

// openRouterReasoning builds the reasoning object from the request, or nil
// when there is nothing to say. Effort wins over a token budget.
func openRouterReasoning(req llm.Request) *wireReasoning {
	rr := &wireReasoning{Exclude: req.ReasoningExclude}
	switch {
	case req.Reasoning != "":
		rr.Effort = req.Reasoning
	case req.ThinkingBudget > 0:
		rr.MaxTokens = req.ThinkingBudget
	}
	if rr.Effort == "" && rr.MaxTokens == 0 && !rr.Exclude {
		return nil
	}
	return rr
}

// translate maps llm messages to chat-completions messages. Reasoning parts
// are dropped, except that DeepSeek needs reasoning_content echoed on
// assistant turns that made tool calls.
func (p *Provider) translate(req llm.Request) []wireMessage {
	var out []wireMessage
	if req.System != "" {
		out = append(out, wireMessage{Role: "system", Content: req.System})
	}
	// SendReasoning=false keeps only the turn in progress, as the Anthropic
	// adapter does. Before that turn, reasoning is left off the wire.
	turn := 0
	if req.SendReasoning != nil && !*req.SendReasoning {
		turn = currentTurnStart(req.Messages)
	}
	echo := p.echoesReasoning(req.Model)
	for i, m := range req.Messages {
		switch m.Role {
		case llm.RoleAssistant:
			out = append(out, p.assistantMessage(m, echo && i >= turn))
		default: // user and tool
			out = append(out, userAndToolMessages(m)...)
		}
	}
	return out
}

// currentTurnStart returns the index of the user message that opens the turn
// in progress. A user message that carries only tool results continues the
// turn, so it does not count. Without a user turn, every message is before it.
func currentTurnStart(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser && !onlyToolResults(msgs[i]) {
			return i
		}
	}
	return len(msgs)
}

// onlyToolResults reports whether every part of m is a tool result.
func onlyToolResults(m llm.Message) bool {
	if len(m.Parts) == 0 {
		return false
	}
	for _, part := range m.Parts {
		if part.Type != llm.PartToolResult {
			return false
		}
	}
	return true
}

// echoesReasoning reports whether assistant turns for this model carry their
// reasoning back to the server. Only DeepSeek asks for it, and not on the
// reasoner models, which must never receive it (see deepSeekDropsReasoning).
func (p *Provider) echoesReasoning(model string) bool {
	return p.opts.ID == "deepseek" && !deepSeekDropsReasoning(model)
}

// deepSeekDropsReasoning reports the DeepSeek reasoner ids. Spec 7.2 says
// callers strip reasoning_content from history for deepseek-reasoner (R1 and
// similar). The V3.1-and-later thinking models are the other side of the rule:
// with tools they need it passed back, so they are not listed here.
func deepSeekDropsReasoning(model string) bool {
	m := strings.ToLower(path.Base(model))
	return m == "deepseek-reasoner" || strings.HasPrefix(m, "deepseek-r1")
}

// assistantMessage builds one assistant turn. Reasoning is echoed only where
// the provider needs it (DeepSeek, on tool-call turns), and only when keep is
// true. The caller sets keep from the provider, SendReasoning and the turn
// position.
func (p *Provider) assistantMessage(m llm.Message, keep bool) wireMessage {
	var text, reasoning strings.Builder
	var calls []wireToolCall
	for _, part := range m.Parts {
		switch part.Type {
		case llm.PartText:
			text.WriteString(part.Text)
		case llm.PartReasoning:
			reasoning.WriteString(part.Text)
		case llm.PartToolCall:
			args := "{}"
			if raw, bad := llm.UnparsedArgs(part.Args); bad {
				// A malformed call from an earlier turn goes back as the
				// text the model actually wrote.
				args = raw
			} else if len(part.Args) > 0 && string(part.Args) != "null" {
				args = string(part.Args)
			}
			calls = append(calls, wireToolCall{
				ID:       part.ToolCallID,
				Type:     "function",
				Function: wireCallFunction{Name: part.ToolName, Arguments: args},
			})
		}
	}
	wm := wireMessage{Role: "assistant", ToolCalls: calls}
	switch {
	case len(calls) > 0 && text.Len() == 0:
		wm.Content = nil
	default:
		wm.Content = text.String()
	}
	if len(calls) > 0 && keep {
		rc := reasoning.String()
		wm.ReasoningContent = &rc
	}
	return wm
}

// userAndToolMessages emits one role=tool message per tool result, then one
// user message for any text and images. Images from tool results cannot ride
// in a tool message, so they land in that trailing user message.
func userAndToolMessages(m llm.Message) []wireMessage {
	var out []wireMessage
	var content []wireContent
	var plain strings.Builder
	hasImage := false
	for _, part := range m.Parts {
		switch part.Type {
		case llm.PartToolResult:
			res := part.Result
			if part.IsError {
				res = "error: " + res
			}
			out = append(out, wireMessage{Role: "tool", ToolCallID: part.ToolCallID, Content: res})
		case llm.PartText:
			plain.WriteString(part.Text)
			content = append(content, wireContent{Type: "text", Text: part.Text})
		case llm.PartImage:
			hasImage = true
			mt := part.MediaType
			if mt == "" {
				mt = defaultImgType
			}
			uri := "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
			content = append(content, wireContent{Type: "image_url", ImageURL: &wireImageURL{URL: uri}})
		}
	}
	if len(content) == 0 {
		return out
	}
	wm := wireMessage{Role: "user"}
	if hasImage {
		wm.Content = content
	} else {
		wm.Content = plain.String()
	}
	return append(out, wm)
}

func (p *Provider) url(path string) string { return p.opts.BaseURL + path }

// modelsURL is the model list address: ModelsURL when set, else /models
// under the base URL.
func (p *Provider) modelsURL() string {
	if p.opts.ModelsURL != "" {
		return p.opts.ModelsURL
	}
	return p.url("/models")
}

func (p *Provider) setHeaders(h http.Header, extra map[string]string, token string) {
	h.Set("Content-Type", "application/json")
	for k, v := range p.opts.Headers {
		h.Set(k, v)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
}

// sessionHeader is the request header carrying the session id.
const sessionHeader = "X-Session-Id"

// promptCacheKey returns the session id to send as x-prompt-cache-key. It is
// only set on the openai provider, which uses it to route requests with the
// same prefix to the same cache backend.
func (p *Provider) promptCacheKey(extra map[string]string) string {
	if p.opts.ID != "openai" {
		return ""
	}
	for k, v := range extra {
		if strings.EqualFold(k, sessionHeader) {
			return v
		}
	}
	return ""
}

// ---- stream ----------------------------------------------------------------

// Stream starts a chat completion and returns its event channel. Non-2xx
// responses are returned as *llm.APIError before any event is produced.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", p.opts.ID, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	token, err := p.bearer(ctx)
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq.Header, req.Headers, token)
	httpReq.Header.Set("Accept", "text/event-stream")
	if key := p.promptCacheKey(req.Headers); key != "" {
		httpReq.Header.Set("x-prompt-cache-key", key)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.unreachable(ctx, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}
	ch := make(chan llm.Event, 32)
	go p.read(ctx, resp.Body, ch, extractsThinkTags(req.Model))
	return ch, nil
}

func (p *Provider) httpError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	ae := &llm.APIError{Provider: p.opts.ID, StatusCode: resp.StatusCode}
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(raw, &env) == nil {
		if len(env.Error) > 0 {
			ae.Type, ae.Message = parseErrorObject(env.Error)
		} else {
			ae.Type, ae.Message = env.Type, env.Message
		}
	}
	if ae.Message == "" {
		ae.Message = truncate(strings.TrimSpace(string(raw)), 300)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil {
			ae.RetryAfter = time.Duration(n) * time.Second
			ae.RetryAfterSet = true
		}
	}
	// A missing model is the usual 404 here. Say what to do about it. A
	// wrong base URL gives a different 404 and gets no model advice.
	if lower := strings.ToLower(ae.Message); resp.StatusCode == http.StatusNotFound && strings.Contains(lower, "model") && strings.Contains(lower, "not found") {
		ae.Message += ". Pick a model the server has: lectern code --list-models " + p.opts.ID
	}
	return ae
}

// unreachable names the server that did not answer and why, in place of the
// raw URL error. A cancelled or timed-out request keeps its own error, so an
// aborted turn is not reported as an unreachable server.
func (p *Provider) unreachable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	cause := err
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		cause = ue.Err
	}
	hint := "check that the server is running and that its address is right"
	if p.opts.ID == "ollama" {
		hint = "check that ollama is running and that OLLAMA_HOST points at it"
	}
	return fmt.Errorf("%s: cannot reach %s (%v); %s", p.opts.ID, p.opts.BaseURL, cause, hint)
}

// parseErrorObject reads either {"message","type","code"} or a bare string.
func parseErrorObject(raw json.RawMessage) (typ, msg string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "", s
	}
	var o struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return "", ""
	}
	typ = o.Type
	if typ == "" && len(o.Code) > 0 {
		var cs string
		if json.Unmarshal(o.Code, &cs) == nil {
			typ = cs
		}
	}
	return typ, o.Message
}

// streamer holds the state of one response while it is being read.
type streamer struct {
	ctx            context.Context
	ch             chan<- llm.Event
	id             string
	reasoningField string
	stopped        bool

	think     splitter
	thinkTags bool // inline <think>/<thinking> extraction is on for this model
	byIndex   map[int]*toolAcc
	order     []*toolAcc
	finishRaw string
	hasFinish bool
	usage     llm.Usage
}

// toolAcc accumulates one tool call across chunks.
type toolAcc struct {
	index   int
	id      string
	name    string
	args    strings.Builder
	started bool
}

func (p *Provider) read(ctx context.Context, body io.ReadCloser, ch chan<- llm.Event, thinkTags bool) {
	defer close(ch)
	defer body.Close()
	s := &streamer{ctx: ctx, ch: ch, id: p.opts.ID, reasoningField: p.opts.ReasoningField, byIndex: map[int]*toolAcc{}, thinkTags: thinkTags}

	br := bufio.NewReaderSize(body, 64<<10)
	sawDone := false
loop:
	for {
		line, err := br.ReadString('\n')
		if payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:"); ok {
			payload = strings.TrimSpace(payload)
			switch {
			case payload == "[DONE]":
				sawDone = true
				break loop
			case payload != "":
				if err := s.chunk(payload); err != nil {
					if !s.stopped {
						s.fail(err)
					}
					return
				}
				if s.stopped {
					return
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				s.fail(ctx.Err())
				return
			}
			if errors.Is(err, io.EOF) {
				break
			}
			s.fail(fmt.Errorf("%s: stream read: %w", s.id, err))
			return
		}
	}
	if ctx.Err() != nil {
		s.fail(ctx.Err())
		return
	}
	if !sawDone && !s.hasFinish {
		s.fail(fmt.Errorf("%s: stream ended before the response finished", s.id))
		return
	}
	s.finish()
}

// fail sends the terminal error. It waits briefly so a consumer that is still
// reading receives it even when the context was cancelled.
func (s *streamer) fail(err error) {
	select {
	case s.ch <- llm.Event{Type: llm.EvError, Err: err}:
	case <-time.After(terminalGrace):
	}
}

func (s *streamer) emit(ev llm.Event) bool {
	if s.stopped {
		return false
	}
	select {
	case s.ch <- ev:
		return true
	case <-s.ctx.Done():
		s.stopped = true
		return false
	}
}

type wireChunk struct {
	Choices []struct {
		Delta        json.RawMessage `json:"delta"`
		FinishReason *string         `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	PromptCacheHit   int `json:"prompt_cache_hit_tokens"`
	PromptDetails    *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type wireToolDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// chunk handles one "data:" payload. A returned error is terminal.
func (s *streamer) chunk(payload string) error {
	var c wireChunk
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return fmt.Errorf("%s: bad stream chunk: %w", s.id, err)
	}
	if len(c.Error) > 0 && string(c.Error) != "null" {
		typ, msg := parseErrorObject(c.Error)
		return &llm.APIError{Provider: s.id, Type: typ, Message: msg}
	}
	for _, ch := range c.Choices {
		if len(ch.Delta) > 0 {
			if err := s.delta(ch.Delta); err != nil {
				return err
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.finishRaw = *ch.FinishReason
			s.hasFinish = true
		}
	}
	if c.Usage != nil {
		s.usage = c.Usage.toLLM()
	}
	return nil
}

func (s *streamer) delta(raw json.RawMessage) error {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("%s: bad stream delta: %w", s.id, err)
	}
	if r := s.reasoningText(d); r != "" {
		if !s.emit(llm.Event{Type: llm.EvReasoning, Text: r}) {
			return nil
		}
	}
	if t := rawString(d["content"]); t != "" {
		if !s.thinkTags {
			if !s.emitPiece(piece{text: t}) {
				return nil
			}
		} else {
			for _, pc := range s.think.push(t) {
				if !s.emitPiece(pc) {
					return nil
				}
			}
		}
	}
	if tc := d["tool_calls"]; len(tc) > 0 && string(tc) != "null" {
		var deltas []wireToolDelta
		if err := json.Unmarshal(tc, &deltas); err != nil {
			return fmt.Errorf("%s: bad tool_calls delta: %w", s.id, err)
		}
		for _, td := range deltas {
			if !s.toolFragment(td) {
				return nil
			}
		}
	}
	return nil
}

func (s *streamer) reasoningText(d map[string]json.RawMessage) string {
	names := make([]string, 0, 3)
	if s.reasoningField != "" {
		names = append(names, s.reasoningField)
	}
	names = append(names, "reasoning_content", "reasoning")
	for _, n := range names {
		if r := rawString(d[n]); r != "" {
			return r
		}
	}
	return ""
}

func (s *streamer) emitPiece(pc piece) bool {
	if pc.text == "" {
		return true
	}
	if pc.reasoning {
		return s.emit(llm.Event{Type: llm.EvReasoning, Text: pc.text})
	}
	return s.emit(llm.Event{Type: llm.EvText, Text: pc.text})
}

// lookup finds or creates the accumulator for a tool-call delta. Servers that
// omit index are matched by id, or else treated as continuing the last call.
func (s *streamer) lookup(td wireToolDelta) *toolAcc {
	var idx int
	switch {
	case td.Index != nil:
		idx = *td.Index
	case td.ID != "":
		for _, a := range s.order {
			if a.id == td.ID {
				return a
			}
		}
		idx = len(s.order)
	case len(s.order) > 0:
		return s.order[len(s.order)-1]
	}
	if a, ok := s.byIndex[idx]; ok {
		return a
	}
	a := &toolAcc{index: idx}
	s.byIndex[idx] = a
	s.order = append(s.order, a)
	return a
}

func (s *streamer) toolFragment(td wireToolDelta) bool {
	a := s.lookup(td)
	if td.ID != "" && a.id == "" {
		a.id = td.ID
	}
	if td.Function.Name != "" && a.name == "" {
		a.name = td.Function.Name
	}
	frag := argsText(td.Function.Arguments)
	a.args.WriteString(frag)
	if !a.started {
		if a.id != "" && a.name != "" {
			return s.start(a)
		}
		return true
	}
	if frag == "" {
		return true
	}
	return s.emit(llm.Event{Type: llm.EvToolCallDelta, ToolCallID: a.id, ArgsDelta: frag})
}

// start emits EvToolCallStart and any argument text that arrived before the
// id and name did.
func (s *streamer) start(a *toolAcc) bool {
	a.started = true
	if !s.emit(llm.Event{Type: llm.EvToolCallStart, ToolCallID: a.id, ToolName: a.name}) {
		return false
	}
	if a.args.Len() > 0 {
		return s.emit(llm.Event{Type: llm.EvToolCallDelta, ToolCallID: a.id, ArgsDelta: a.args.String()})
	}
	return true
}

// finish flushes buffered text, closes every tool call and emits EvFinish.
func (s *streamer) finish() {
	for _, pc := range s.think.flush() {
		if !s.emitPiece(pc) {
			return
		}
	}
	for _, a := range s.order {
		if !a.started {
			if a.name == "" {
				continue // nameless fragments cannot become a call
			}
			if a.id == "" {
				a.id = fmt.Sprintf("call_%d", a.index)
			}
			if !s.start(a) {
				return
			}
		}
		// Malformed argument text is kept raw (llm.ToolArgs), never {}.
		ev := llm.Event{Type: llm.EvToolCallEnd, ToolCallID: a.id, ToolName: a.name, Args: llm.ToolArgs(a.args.String())}
		if !s.emit(ev) {
			return
		}
	}
	reason := mapFinish(s.finishRaw)
	if len(s.order) > 0 && reason == llm.FinishStop {
		reason = llm.FinishToolCalls
	}
	s.emit(llm.Event{Type: llm.EvFinish, FinishReason: reason, Usage: s.usage})
}

func mapFinish(raw string) llm.FinishReason {
	switch raw {
	case "tool_calls", "function_call":
		return llm.FinishToolCalls
	case "length":
		return llm.FinishLength
	case "content_filter", "refusal":
		return llm.FinishFilter
	case "error":
		return llm.FinishError
	default: // "stop", "", and anything unknown
		return llm.FinishStop
	}
}

func (u *wireUsage) toLLM() llm.Usage {
	cached := u.PromptCacheHit
	if u.PromptDetails != nil && u.PromptDetails.CachedTokens > 0 {
		cached = u.PromptDetails.CachedTokens
	}
	in := u.PromptTokens - cached
	if in < 0 {
		in = 0
	}
	out := llm.Usage{InputTokens: in, OutputTokens: u.CompletionTokens, CacheReadTokens: cached}
	if u.CompletionDetails != nil {
		out.ReasoningTokens = u.CompletionDetails.ReasoningTokens
	}
	return out
}

// argsText returns tool-argument text whether the server sent a JSON string
// (the usual case, a fragment) or an already-decoded object.
func argsText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	return string(raw)
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---- inline think-tag splitting -----------------------------------------------

// thinkPairs are the inline reasoning tag pairs: text between an open tag and
// its close tag is reasoning, not answer text.
var thinkPairs = [][2]string{
	{"<think>", "</think>"},
	{"<thinking>", "</thinking>"},
}

type piece struct {
	reasoning bool
	text      string
}

// extractsThinkTags reports whether inline think tags are routed to reasoning
// for this model. Families that report reasoning natively (Claude, OpenAI
// o-series and GPT, Gemini, Grok, deepseek-reasoner) keep their text as is:
// a literal tag in their answer is not reasoning. Every other model is
// wrapped.
func extractsThinkTags(model string) bool {
	m := strings.ToLower(path.Base(model))
	switch {
	case strings.HasPrefix(m, "claude"),
		strings.HasPrefix(m, "gpt-"),
		m == "o1", m == "o3", strings.HasPrefix(m, "o3-"), strings.HasPrefix(m, "o4-"),
		strings.HasPrefix(m, "gemini"),
		strings.HasPrefix(m, "grok"),
		m == "deepseek-reasoner":
		return false
	}
	return true
}

// splitter routes text inside a think block to reasoning. Tags may be split
// across chunks, so a trailing partial tag is held back. The zero value is
// ready to use.
type splitter struct {
	buf     string
	inThink bool
	close   string // closing tag of the open block, set while inThink
}

// push consumes one chunk and returns the pieces that are now known.
func (sp *splitter) push(s string) []piece {
	sp.buf += s
	var out []piece
	for {
		idx, tag := -1, ""
		if sp.inThink {
			if i := strings.Index(sp.buf, sp.close); i >= 0 {
				idx, tag = i, sp.close
			}
		} else {
			for _, pr := range thinkPairs {
				if i := strings.Index(sp.buf, pr[0]); i >= 0 && (idx < 0 || i < idx) {
					idx, tag = i, pr[0]
				}
			}
		}
		if idx >= 0 {
			if idx > 0 {
				out = append(out, piece{reasoning: sp.inThink, text: sp.buf[:idx]})
			}
			sp.buf = sp.buf[idx+len(tag):]
			if sp.inThink {
				sp.inThink, sp.close = false, ""
			} else {
				sp.inThink, sp.close = true, closeTagFor(tag)
			}
			continue
		}
		keep := sp.partial()
		if n := len(sp.buf) - keep; n > 0 {
			out = append(out, piece{reasoning: sp.inThink, text: sp.buf[:n]})
		}
		sp.buf = sp.buf[len(sp.buf)-keep:]
		return out
	}
}

// partial is the length of the longest buffer suffix that could be the start
// of a tag the splitter is waiting for.
func (sp *splitter) partial() int {
	best := 0
	tags := []string{sp.close}
	if !sp.inThink {
		tags = tags[:0]
		for _, pr := range thinkPairs {
			tags = append(tags, pr[0])
		}
	}
	for _, tag := range tags {
		if k := partialSuffix(sp.buf, tag); k > best {
			best = k
		}
	}
	return best
}

// closeTagFor returns the closing tag for an open think tag.
func closeTagFor(open string) string {
	for _, pr := range thinkPairs {
		if pr[0] == open {
			return pr[1]
		}
	}
	return ""
}

func (sp *splitter) flush() []piece {
	var out []piece
	if sp.buf != "" {
		out = append(out, piece{reasoning: sp.inThink, text: sp.buf})
	}
	sp.buf = ""
	return out
}

// partialSuffix returns the length of the longest suffix of s that is a
// proper prefix of tag.
func partialSuffix(s, tag string) int {
	for k := len(tag) - 1; k > 0; k-- {
		if k <= len(s) && strings.HasSuffix(s, tag[:k]) {
			return k
		}
	}
	return 0
}

// ---- models ----------------------------------------------------------------

// remoteModel is one entry of GET /models with the metadata some gateways
// add to it. Zero fields mean the server did not say.
type remoteModel struct {
	ID            string
	ContextWindow int
	// InputPerMTok and OutputPerMTok come from a "pricing" object (OpenRouter
	// style, USD per token as strings). Priced is true when it was present,
	// even if the price is zero: zero then means free, not unknown.
	InputPerMTok  float64
	OutputPerMTok float64
	Priced        bool
	// Reasoning is true when the server lists reasoning among the parameters
	// the model takes (OpenRouter supported_parameters).
	Reasoning bool
}

// Models lists models from GET {base}/models. When that fails or returns
// nothing, the static Options.Models list is returned instead.
func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	if p.opts.NoModelList {
		if len(p.opts.Models) == 0 {
			return nil, fmt.Errorf("%s: model listing is off and no models are configured", p.opts.ID)
		}
		return p.static(), nil
	}
	remote, err := p.fetchModels(ctx)
	if err != nil || len(remote) == 0 {
		if len(p.opts.Models) > 0 {
			return p.static(), nil
		}
		if err == nil {
			err = fmt.Errorf("%s: server listed no models", p.opts.ID)
		}
		return nil, err
	}
	out := make([]llm.ModelInfo, 0, len(remote))
	for _, m := range remote {
		out = append(out, llm.ModelInfo{
			ID:            m.ID,
			Name:          m.ID,
			ContextWindow: m.ContextWindow,
			InputPerMTok:  m.InputPerMTok,
			OutputPerMTok: m.OutputPerMTok,
			CatalogPriced: m.Priced,
			Reasoning:     m.Reasoning,
		})
	}
	return out, nil
}

func (p *Provider) fetchModels(ctx context.Context) ([]remoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, p.modelsTimeout())
	defer cancel()
	token, err := p.bearer(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.modelsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	p.setHeaders(req.Header, nil, token)
	req.Header.Del("Content-Type")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, p.httpError(resp)
	}
	var body struct {
		Data []wireModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s: decode models: %w", p.opts.ID, err)
	}
	out := make([]remoteModel, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID == "" {
			continue
		}
		out = append(out, d.remote())
	}
	return out, nil
}

// wireModel is the subset of a model-list entry Scribe reads. The context
// fields are the names used by OpenAI-compatible servers (LM Studio and
// others use context_length, llama.cpp style servers n_ctx_train).
type wireModel struct {
	ID                  string   `json:"id"`
	SupportedParameters []string `json:"supported_parameters"`
	ContextLength       int      `json:"context_length"`
	MaxContextLength    int      `json:"max_context_length"`
	ModelInfo           struct {
		MaxInputTokens int `json:"max_input_tokens"`
	} `json:"model_info"`
	Meta struct {
		NCtxTrain int `json:"n_ctx_train"`
	} `json:"meta"`
	Pricing *struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

func (w wireModel) remote() remoteModel {
	m := remoteModel{ID: w.ID}
	for _, p := range w.SupportedParameters {
		if p == "reasoning" {
			m.Reasoning = true
		}
	}
	for _, n := range []int{w.ContextLength, w.MaxContextLength, w.ModelInfo.MaxInputTokens, w.Meta.NCtxTrain} {
		if n > 0 {
			m.ContextWindow = n
			break
		}
	}
	if w.Pricing != nil {
		in, inOK := perTokenToMTok(w.Pricing.Prompt)
		out, outOK := perTokenToMTok(w.Pricing.Completion)
		if inOK && outOK {
			m.InputPerMTok, m.OutputPerMTok, m.Priced = in, out, true
		}
	}
	return m
}

// perTokenToMTok converts a USD-per-token price string to USD per million.
// Negative or unparseable prices (OpenRouter uses -1 for "varies") are not
// usable.
func perTokenToMTok(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v * 1e6, true
}

// static builds the ModelInfo list from Options.Models, with the configured
// context window where one was given.
func (p *Provider) static() []llm.ModelInfo {
	out := make([]llm.ModelInfo, 0, len(p.opts.Models))
	for _, id := range p.opts.Models {
		out = append(out, llm.ModelInfo{ID: id, Name: id, ContextWindow: p.opts.ContextWindows[id]})
	}
	return out
}
