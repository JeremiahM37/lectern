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

	// NoStreamUsage omits stream_options for servers that reject it.
	NoStreamUsage bool
	// ReasoningField names a delta field that carries reasoning text. It is
	// checked before the built-in names reasoning_content and reasoning.
	ReasoningField string
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
	ServiceTier         string          `json:"service_tier,omitempty"`
	Stream              bool            `json:"stream"`
	StreamOptions       *wireStreamOpts `json:"stream_options,omitempty"`
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
	}
	return llm.ReasoningNone
}

// sendsReasoningEffort decides whether reasoning_effort goes on the wire. On
// api.openai.com it is sent only to effort-style models, because the others
// reject it. Other OpenAI-compatible endpoints host model families this table
// does not list, so they keep the send-when-set behaviour.
func sendsReasoningEffort(provider, model string) bool {
	if ReasoningStyle(model) == llm.ReasoningEffort {
		return true
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
	if req.Reasoning != "" && sendsReasoningEffort(p.opts.ID, req.Model) {
		w.ReasoningEffort = req.Reasoning
	}
	w.ServiceTier = req.ServiceTier
	if !p.opts.NoStreamUsage {
		w.StreamOptions = &wireStreamOpts{IncludeUsage: true}
	}
	return json.Marshal(w)
}

// translate maps llm messages to chat-completions messages. Reasoning parts
// are dropped, except that DeepSeek needs reasoning_content echoed on
// assistant turns that made tool calls.
func (p *Provider) translate(req llm.Request) []wireMessage {
	var out []wireMessage
	if req.System != "" {
		out = append(out, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleAssistant:
			out = append(out, p.assistantMessage(m))
		default: // user and tool
			out = append(out, userAndToolMessages(m)...)
		}
	}
	return out
}

func (p *Provider) assistantMessage(m llm.Message) wireMessage {
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
	if p.opts.ID == "deepseek" && len(calls) > 0 {
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

func (p *Provider) setHeaders(h http.Header, extra map[string]string) {
	h.Set("Content-Type", "application/json")
	for k, v := range p.opts.Headers {
		h.Set(k, v)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	if p.opts.APIKey != "" {
		h.Set("Authorization", "Bearer "+p.opts.APIKey)
	}
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
	p.setHeaders(httpReq.Header, req.Headers)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}
	ch := make(chan llm.Event, 32)
	go p.read(ctx, resp.Body, ch)
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
	return ae
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

func (p *Provider) read(ctx context.Context, body io.ReadCloser, ch chan<- llm.Event) {
	defer close(ch)
	defer body.Close()
	s := &streamer{ctx: ctx, ch: ch, id: p.opts.ID, reasoningField: p.opts.ReasoningField, byIndex: map[int]*toolAcc{}}

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
		for _, pc := range s.think.push(t) {
			if !s.emitPiece(pc) {
				return nil
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

// ---- inline <think> splitting -----------------------------------------------

const (
	openTag  = "<think>"
	closeTag = "</think>"
)

type piece struct {
	reasoning bool
	text      string
}

// splitter routes text inside <think>...</think> to reasoning. Tags may be
// split across chunks, so a trailing partial tag is held back.
type splitter struct {
	buf     string
	inThink bool
}

func (sp *splitter) push(s string) []piece {
	sp.buf += s
	var out []piece
	for {
		tag := openTag
		if sp.inThink {
			tag = closeTag
		}
		if i := strings.Index(sp.buf, tag); i >= 0 {
			if i > 0 {
				out = append(out, piece{reasoning: sp.inThink, text: sp.buf[:i]})
			}
			sp.buf = sp.buf[i+len(tag):]
			sp.inThink = !sp.inThink
			continue
		}
		keep := partialSuffix(sp.buf, tag)
		if n := len(sp.buf) - keep; n > 0 {
			out = append(out, piece{reasoning: sp.inThink, text: sp.buf[:n]})
		}
		sp.buf = sp.buf[len(sp.buf)-keep:]
		return out
	}
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

// Models lists models from GET {base}/models. When that fails or returns
// nothing, the static Options.Models list is returned instead.
func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	ids, err := p.fetchModelIDs(ctx)
	if err != nil || len(ids) == 0 {
		if len(p.opts.Models) > 0 {
			return staticModels(p.opts.Models), nil
		}
		if err == nil {
			err = fmt.Errorf("%s: server listed no models", p.opts.ID)
		}
		return nil, err
	}
	out := make([]llm.ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, llm.ModelInfo{ID: id, Name: id})
	}
	return out, nil
}

func (p *Provider) fetchModelIDs(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url("/models"), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	p.setHeaders(req.Header, nil)
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
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s: decode models: %w", p.opts.ID, err)
	}
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

func staticModels(ids []string) []llm.ModelInfo {
	out := make([]llm.ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, llm.ModelInfo{ID: id, Name: id})
	}
	return out
}
