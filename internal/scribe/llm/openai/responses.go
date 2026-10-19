package openai

// Responses API path. OpenAI returns reasoning summaries only on /responses,
// not on chat completions, so a request with llm.Request.ReasoningSummary set
// goes here. Every other request keeps the chat-completions path in openai.go.

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
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// summaryOn reports whether this request takes the Responses path for a
// reasoning summary. Only the openai provider, only reasoning models (the
// same ids that take reasoning_effort), and only the three documented values.
func summaryOn(providerID string, req llm.Request) bool {
	switch req.ReasoningSummary {
	case "auto", "concise", "detailed":
	default:
		return false
	}
	return providerID == "openai" && ReasoningStyle(req.Model) == llm.ReasoningEffort
}

// ---- request ---------------------------------------------------------------

type rRequest struct {
	Model             string      `json:"model"`
	Input             []any       `json:"input"`
	Instructions      string      `json:"instructions,omitempty"`
	Tools             []rTool     `json:"tools,omitempty"`
	ToolChoice        any         `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool       `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   int         `json:"max_output_tokens,omitempty"`
	Reasoning         *rReasoning `json:"reasoning,omitempty"`
	ServiceTier       string      `json:"service_tier,omitempty"`
	Store             bool        `json:"store"`
	Stream            bool        `json:"stream"`
}

type rReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type rTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Input items. Each has its own struct so no item carries a field the API
// does not know for that type.
type rMsg struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type rText struct {
	Type string `json:"type"` // input_text or output_text
	Text string `json:"text"`
}

type rImage struct {
	Type     string `json:"type"` // input_image
	ImageURL string `json:"image_url"`
	Detail   string `json:"detail"`
}

type rCall struct {
	Type      string `json:"type"` // function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type rOutput struct {
	Type   string `json:"type"` // function_call_output
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// buildResponsesBody translates a request into a /responses body. Reasoning
// parts are not echoed back: the request asks for store=false, so the server
// keeps nothing to refer to, and the model reasons afresh on each call.
func (p *Provider) buildResponsesBody(req llm.Request) ([]byte, error) {
	w := rRequest{
		Model:        req.Model,
		Input:        responsesInput(req.Messages),
		Instructions: req.System,
		Store:        false,
		Stream:       true,
		ServiceTier:  req.ServiceTier,
		Reasoning:    &rReasoning{Summary: req.ReasoningSummary},
	}
	if req.Reasoning != "" && sendsReasoningEffort(p.opts.ID, req.Model) {
		w.Reasoning.Effort = req.Reasoning
	}
	if req.MaxTokens > 0 {
		w.MaxOutputTokens = req.MaxTokens
	}
	for _, t := range req.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		w.Tools = append(w.Tools, rTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}
	if len(w.Tools) > 0 && (req.ToolChoice == "none" || req.ToolChoice == "required") {
		w.ToolChoice = req.ToolChoice
	}
	if len(w.Tools) > 0 && req.DisableParallelToolUse {
		off := false
		w.ParallelToolCalls = &off
	}
	raw, err := json.Marshal(w)
	if err != nil || len(req.ExtraParams) == 0 {
		return raw, err
	}
	return mergeExtraParams(raw, req.ExtraParams)
}

// responsesInput maps llm messages to Responses input items, oldest first.
func responsesInput(msgs []llm.Message) []any {
	var out []any
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			out = append(out, assistantItems(m)...)
		} else {
			out = append(out, userItems(m)...)
		}
	}
	return out
}

func assistantItems(m llm.Message) []any {
	var out []any
	var text strings.Builder
	var calls []any
	for _, part := range m.Parts {
		switch part.Type {
		case llm.PartText:
			text.WriteString(part.Text)
		case llm.PartToolCall:
			args := "{}"
			if raw, bad := llm.UnparsedArgs(part.Args); bad {
				args = raw
			} else if len(part.Args) > 0 && string(part.Args) != "null" {
				args = string(part.Args)
			}
			calls = append(calls, rCall{Type: "function_call", CallID: part.ToolCallID, Name: part.ToolName, Arguments: args})
		}
	}
	if text.Len() > 0 {
		out = append(out, rMsg{Type: "message", Role: "assistant", Content: []any{rText{Type: "output_text", Text: text.String()}}})
	}
	return append(out, calls...)
}

func userItems(m llm.Message) []any {
	var out []any
	var content []any
	for _, part := range m.Parts {
		switch part.Type {
		case llm.PartToolResult:
			res := part.Result
			if part.IsError {
				res = "error: " + res
			}
			out = append(out, rOutput{Type: "function_call_output", CallID: part.ToolCallID, Output: res})
		case llm.PartText:
			content = append(content, rText{Type: "input_text", Text: part.Text})
		case llm.PartImage:
			mt := part.MediaType
			if mt == "" {
				mt = defaultImgType
			}
			uri := "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
			content = append(content, rImage{Type: "input_image", ImageURL: uri, Detail: "auto"})
		}
	}
	if len(content) > 0 {
		out = append(out, rMsg{Type: "message", Role: "user", Content: content})
	}
	return out
}

// ---- stream ----------------------------------------------------------------

// streamResponses starts a /responses call and returns its event channel.
func (p *Provider) streamResponses(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := p.buildResponsesBody(req)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", p.opts.ID, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url("/responses"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	token, err := p.bearer(ctx)
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq.Header, req.Headers, token)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.unreachable(ctx, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}
	ch := make(chan llm.Event, 32)
	go p.readResponses(ctx, resp.Body, ch)
	return ch, nil
}

// rEvent is the subset of a Responses stream event this adapter reads.
type rEvent struct {
	Type     string     `json:"type"`
	Delta    string     `json:"delta"`
	ItemID   string     `json:"item_id"`
	Item     *rItem     `json:"item"`
	Args     string     `json:"arguments"`
	Message  string     `json:"message"`
	Response *rResponse `json:"response"`
}

type rItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type rResponse struct {
	Usage *rUsage `json:"usage"`
	// IncompleteDetails is set on response.incomplete.
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type rUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Input        *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	Output *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *rUsage) toLLM() llm.Usage {
	var cached, reasoning int
	if u.Input != nil {
		cached = u.Input.CachedTokens
	}
	if u.Output != nil {
		reasoning = u.Output.ReasoningTokens
	}
	in := u.InputTokens - cached
	if in < 0 {
		in = 0
	}
	return llm.Usage{InputTokens: in, OutputTokens: u.OutputTokens, CacheReadTokens: cached, ReasoningTokens: reasoning}
}

// rCallAcc is one function call in progress, keyed by its output item id.
type rCallAcc struct {
	callID string
	name   string
	args   strings.Builder
	ended  bool
}

// rStream holds the state of one /responses stream.
type rStream struct {
	ctx     context.Context
	ch      chan<- llm.Event
	stopped bool
	done    bool // a terminal event has been sent
	calls   map[string]*rCallAcc
	order   []*rCallAcc
	parts   int // reasoning summary parts seen, to separate them
	usage   llm.Usage
}

func (p *Provider) readResponses(ctx context.Context, body io.ReadCloser, ch chan<- llm.Event) {
	defer close(ch)
	defer body.Close()
	s := &rStream{ctx: ctx, ch: ch, calls: map[string]*rCallAcc{}}

	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:"); ok {
			payload = strings.TrimSpace(payload)
			if payload != "" && payload != "[DONE]" {
				if err := s.chunk(p.opts.ID, payload); err != nil {
					s.fail(err)
					return
				}
				if s.stopped || s.done {
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
			s.fail(fmt.Errorf("%s: stream read: %w", p.opts.ID, err))
			return
		}
	}
	if !s.done && !s.stopped {
		// Unlike chat completions, the Responses stream has an explicit end.
		// Without it the answer may be cut short, so it is an error.
		s.fail(fmt.Errorf("%s: stream ended before the response completed", p.opts.ID))
	}
}

func (s *rStream) fail(err error) {
	if s.done {
		return
	}
	s.done = true
	select {
	case s.ch <- llm.Event{Type: llm.EvError, Err: err}:
	case <-s.ctx.Done():
	case <-time.After(terminalGrace):
	}
}

func (s *rStream) emit(ev llm.Event) bool {
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

// chunk decodes one data payload and emits what it means. It returns an
// error only for a payload that is not JSON; API errors become EvError.
func (s *rStream) chunk(id, payload string) error {
	var ev rEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return fmt.Errorf("%s: bad stream event: %w", id, err)
	}
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta != "" {
			s.emit(llm.Event{Type: llm.EvText, Text: ev.Delta})
		}
	case "response.reasoning_summary_part.added":
		// Summary parts are separate paragraphs; keep them apart.
		if s.parts > 0 {
			s.emit(llm.Event{Type: llm.EvReasoning, Text: "\n\n"})
		}
		s.parts++
	case "response.reasoning_summary_text.delta":
		if ev.Delta != "" {
			s.emit(llm.Event{Type: llm.EvReasoning, Text: ev.Delta})
		}
	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			s.startCall(ev.Item)
		}
	case "response.function_call_arguments.delta":
		if a := s.calls[ev.ItemID]; a != nil && ev.Delta != "" {
			a.args.WriteString(ev.Delta)
			s.emit(llm.Event{Type: llm.EvToolCallDelta, ToolCallID: a.callID, ArgsDelta: ev.Delta})
		}
	case "response.function_call_arguments.done":
		if a := s.calls[ev.ItemID]; a != nil {
			s.endCall(a, ev.Args)
		}
	case "response.output_item.done":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			if a := s.calls[ev.Item.ID]; a != nil {
				s.endCall(a, ev.Item.Arguments)
			}
		}
	case "response.completed":
		if ev.Response != nil && ev.Response.Usage != nil {
			s.usage = ev.Response.Usage.toLLM()
		}
		s.finish(llm.FinishStop)
	case "response.incomplete":
		reason := llm.FinishLength
		if ev.Response != nil && ev.Response.IncompleteDetails != nil {
			switch ev.Response.IncompleteDetails.Reason {
			case "content_filter":
				reason = llm.FinishFilter
			case "max_output_tokens", "":
				reason = llm.FinishLength
			}
		}
		if ev.Response != nil && ev.Response.Usage != nil {
			s.usage = ev.Response.Usage.toLLM()
		}
		s.finish(reason)
	case "response.failed":
		msg := "response failed"
		if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
			msg = ev.Response.Error.Message
		}
		s.fail(fmt.Errorf("%s: %s", id, msg))
	case "error":
		msg := ev.Message
		if msg == "" {
			msg = "stream error"
		}
		s.fail(fmt.Errorf("%s: %s", id, msg))
	}
	return nil
}

func (s *rStream) startCall(item *rItem) {
	callID := item.CallID
	if callID == "" {
		callID = item.ID
	}
	a := &rCallAcc{callID: callID, name: item.Name}
	s.calls[item.ID] = a
	s.order = append(s.order, a)
	s.emit(llm.Event{Type: llm.EvToolCallStart, ToolCallID: callID, ToolName: item.Name})
}

// endCall emits a call's final arguments once. final is the server's complete
// text when it sent one; otherwise the accumulated deltas are used.
func (s *rStream) endCall(a *rCallAcc, final string) {
	if a.ended {
		return
	}
	a.ended = true
	raw := final
	if raw == "" {
		raw = a.args.String()
	}
	s.emit(llm.Event{Type: llm.EvToolCallEnd, ToolCallID: a.callID, ToolName: a.name, Args: llm.ToolArgs(raw)})
}

// finish closes every open call and sends the terminal EvFinish.
func (s *rStream) finish(reason llm.FinishReason) {
	if s.done {
		return
	}
	for _, a := range s.order {
		s.endCall(a, "")
	}
	if len(s.order) > 0 && reason == llm.FinishStop {
		reason = llm.FinishToolCalls
	}
	s.done = true
	s.emit(llm.Event{Type: llm.EvFinish, FinishReason: reason, Usage: s.usage})
}
