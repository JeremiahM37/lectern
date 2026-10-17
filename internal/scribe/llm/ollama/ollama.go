// Package ollama is Scribe's adapter for Ollama's native chat API
// (POST /api/chat). It exists because the OpenAI-compatible endpoint ignores
// options.num_ctx, so the server falls back to its own default window and
// cuts the start of long prompts (system prompt and tools) without a word.
// The native endpoint honours num_ctx. The adapter translates to and from
// package llm.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Options configures one provider instance.
type Options struct {
	ID         string            // registry id, returned by ID(); default "ollama"
	BaseURL    string            // server root without /v1, e.g. http://127.0.0.1:11434
	HTTPClient *http.Client      // nil uses a client with no overall timeout
	Headers    map[string]string // extra headers on every request
	// NumCtx returns the context window sent as options.num_ctx for a model.
	// Zero or nil omits num_ctx, and the server uses its own default.
	NumCtx func(model string) int
}

const (
	maxErrorBody   = 64 << 10
	modelsTimeout  = 15 * time.Second
	showTimeout    = 2 * time.Second
	terminalGrace  = time.Second
	defaultImgType = "image/png"
)

// Provider is an llm.Provider for Ollama's native API.
type Provider struct {
	opts   Options
	client *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider with defaults applied.
func New(opts Options) *Provider {
	if opts.ID == "" {
		opts.ID = "ollama"
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

func (p *Provider) url(path string) string { return p.opts.BaseURL + path }

func (p *Provider) setHeaders(h http.Header, extra map[string]string) {
	h.Set("Content-Type", "application/json")
	for k, v := range p.opts.Headers {
		h.Set(k, v)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
}

// ---- request ---------------------------------------------------------------

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Think    *bool         `json:"think,omitempty"`
	Stream   bool          `json:"stream"`
	Options  wireOptions   `json:"options,omitempty"`
}

type wireOptions struct {
	NumCtx      int      `json:"num_ctx,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
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
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Images    []string       `json:"images,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
}

type wireToolCall struct {
	Function wireCallFunction `json:"function"`
}

type wireCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// thinkOn reports whether a reasoning level asks the model to think.
func thinkOn(reasoning string) bool {
	switch strings.ToLower(strings.TrimSpace(reasoning)) {
	case "", "off", "none":
		return false
	}
	return true
}

// buildBody translates a provider-neutral request into the wire body.
func (p *Provider) buildBody(req llm.Request) ([]byte, error) {
	w := wireRequest{
		Model:    req.Model,
		Messages: translate(req),
		Stream:   true,
	}
	if p.opts.NumCtx != nil {
		if n := p.opts.NumCtx(req.Model); n > 0 {
			w.Options.NumCtx = n
		}
	}
	w.Options.Temperature = req.Temperature
	if req.MaxTokens > 0 {
		w.Options.NumPredict = req.MaxTokens
	}
	if thinkOn(req.Reasoning) {
		on := true
		w.Think = &on
	}
	if req.ToolChoice != "none" {
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
	}
	return json.Marshal(w)
}

// translate maps llm messages to native chat messages. Reasoning parts are
// dropped: Ollama does not take reasoning back on assistant turns.
func translate(req llm.Request) []wireMessage {
	var out []wireMessage
	if req.System != "" {
		out = append(out, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleAssistant:
			out = append(out, assistantMessage(m))
		default: // user and tool
			out = append(out, userAndToolMessages(m)...)
		}
	}
	return out
}

func assistantMessage(m llm.Message) wireMessage {
	var text strings.Builder
	var calls []wireToolCall
	for _, part := range m.Parts {
		switch part.Type {
		case llm.PartText:
			text.WriteString(part.Text)
		case llm.PartToolCall:
			calls = append(calls, wireToolCall{Function: wireCallFunction{
				Name:      part.ToolName,
				Arguments: callArgs(part.Args),
			}})
		}
	}
	return wireMessage{Role: "assistant", Content: text.String(), ToolCalls: calls}
}

// callArgs returns a tool call's arguments as the JSON object Ollama requires.
// Malformed argument text from an earlier turn cannot be sent as an object,
// so it goes back as an empty object instead of failing the whole request.
func callArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	if _, bad := llm.UnparsedArgs(raw); bad {
		return json.RawMessage(`{}`)
	}
	return raw
}

// userAndToolMessages emits one role=tool message per tool result, then one
// user message for any text and images.
func userAndToolMessages(m llm.Message) []wireMessage {
	var out []wireMessage
	var text strings.Builder
	var images []string
	hasText := false
	for _, part := range m.Parts {
		switch part.Type {
		case llm.PartToolResult:
			res := part.Result
			if part.IsError {
				res = "error: " + res
			}
			out = append(out, wireMessage{Role: "tool", Content: res, ToolName: part.ToolName})
		case llm.PartText:
			hasText = true
			text.WriteString(part.Text)
		case llm.PartImage:
			images = append(images, base64.StdEncoding.EncodeToString(part.Data))
		}
	}
	if !hasText && len(images) == 0 {
		return out
	}
	return append(out, wireMessage{Role: "user", Content: text.String(), Images: images})
}

// ---- errors ----------------------------------------------------------------

// unreachable names the server that did not answer and why, in place of the
// raw URL error. A cancelled or timed-out request keeps its own error.
func (p *Provider) unreachable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	cause := err
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		cause = ue.Err
	}
	return fmt.Errorf("%s: cannot reach %s (%v); check that ollama is running and that OLLAMA_HOST points at it", p.opts.ID, p.opts.BaseURL, cause)
}

func (p *Provider) httpError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	ae := &llm.APIError{Provider: p.opts.ID, StatusCode: resp.StatusCode}
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Error) > 0 {
		ae.Message = errorText(env.Error)
	}
	if ae.Message == "" {
		ae.Message = truncate(strings.TrimSpace(string(raw)), 300)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := time.ParseDuration(ra + "s"); err == nil {
			ae.RetryAfter = n
			ae.RetryAfterSet = true
		}
	}
	// A missing model is the usual 404. Say what to do about it.
	if lower := strings.ToLower(ae.Message); resp.StatusCode == http.StatusNotFound && strings.Contains(lower, "model") && strings.Contains(lower, "not found") {
		ae.Message += ". Pick a model the server has: lectern code --list-models " + p.opts.ID
	}
	return ae
}

// errorText reads Ollama's error field, a bare string in practice.
func errorText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Message
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---- stream ----------------------------------------------------------------

// Stream starts a chat call and returns its event channel. Non-2xx responses
// are returned as *llm.APIError before any event is produced.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", p.opts.ID, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url("/api/chat"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	p.setHeaders(httpReq.Header, req.Headers)
	httpReq.Header.Set("Accept", "application/x-ndjson")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.unreachable(ctx, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}
	ch := make(chan llm.Event, 32)
	go p.read(ctx, resp.Body, ch)
	return ch, nil
}

// wireChunk is one NDJSON line of /api/chat.
type wireChunk struct {
	Message struct {
		Content   string         `json:"content"`
		Thinking  string         `json:"thinking"`
		ToolCalls []wireCallLine `json:"tool_calls"`
	} `json:"message"`
	Done            bool            `json:"done"`
	DoneReason      string          `json:"done_reason"`
	PromptEvalCount int             `json:"prompt_eval_count"`
	EvalCount       int             `json:"eval_count"`
	Error           json.RawMessage `json:"error"`
}

type wireCallLine struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// streamer holds the state of one response while it is being read.
type streamer struct {
	ctx        context.Context
	ch         chan<- llm.Event
	id         string
	stopped    bool
	calls      int
	callPrefix string // random per response, so ids never repeat across steps
	sawCalls   bool
	usage      llm.Usage
	doneReason string
}

func (p *Provider) read(ctx context.Context, body io.ReadCloser, ch chan<- llm.Event) {
	defer close(ch)
	defer body.Close()
	s := &streamer{ctx: ctx, ch: ch, id: p.opts.ID, callPrefix: newCallPrefix()}

	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if payload := bytes.TrimSpace(line); len(payload) > 0 {
			done, herr := s.chunk(payload)
			if herr != nil {
				if !s.stopped {
					s.fail(herr)
				}
				return
			}
			if s.stopped {
				return
			}
			if done {
				s.finishStream()
				return
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
	s.fail(fmt.Errorf("%s: stream ended before the response finished", s.id))
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

// chunk handles one NDJSON line. done reports the final line. A returned
// error is terminal.
func (s *streamer) chunk(payload []byte) (bool, error) {
	var c wireChunk
	if err := json.Unmarshal(payload, &c); err != nil {
		return false, fmt.Errorf("%s: bad stream chunk: %w", s.id, err)
	}
	if len(c.Error) > 0 && string(c.Error) != "null" {
		return false, &llm.APIError{Provider: s.id, Message: errorText(c.Error)}
	}
	if c.Message.Thinking != "" {
		if !s.emit(llm.Event{Type: llm.EvReasoning, Text: c.Message.Thinking}) {
			return false, nil
		}
	}
	if c.Message.Content != "" {
		if !s.emit(llm.Event{Type: llm.EvText, Text: c.Message.Content}) {
			return false, nil
		}
	}
	for _, tc := range c.Message.ToolCalls {
		if tc.Function.Name == "" {
			continue
		}
		if !s.toolCall(tc) {
			return false, nil
		}
	}
	if !c.Done {
		return false, nil
	}
	if s.stopped {
		return true, nil
	}
	// The final line carries the usage; it is emitted by finishStream.
	s.usage = llm.Usage{InputTokens: c.PromptEvalCount, OutputTokens: c.EvalCount}
	s.doneReason = c.DoneReason
	return true, nil
}

// newCallPrefix returns a short random tag for this response's call ids.
// Ollama sends no ids, and the agent, the transcript and the TUI key tool
// rows by id, so a counter that restarts each response made later calls
// overwrite earlier ones.
func newCallPrefix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// toolCall emits one whole tool call as start, argument text and end. Ollama
// sends each call complete, so the id is generated per response.
func (s *streamer) toolCall(tc wireCallLine) bool {
	id := fmt.Sprintf("call_%s_%d", s.callPrefix, s.calls)
	s.calls++
	s.sawCalls = true
	args := argsObject(tc.Function.Arguments)
	if !s.emit(llm.Event{Type: llm.EvToolCallStart, ToolCallID: id, ToolName: tc.Function.Name}) {
		return false
	}
	if !s.emit(llm.Event{Type: llm.EvToolCallDelta, ToolCallID: id, ArgsDelta: string(args)}) {
		return false
	}
	return s.emit(llm.Event{Type: llm.EvToolCallEnd, ToolCallID: id, ToolName: tc.Function.Name, Args: args})
}

// argsObject returns tool arguments as a JSON object. Ollama sends an object;
// a string holding JSON text is unwrapped, and anything else is kept raw
// through llm.ToolArgs so the bytes survive.
func argsObject(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return json.RawMessage(`{}`)
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(t, &s) == nil {
			return llm.ToolArgs(s)
		}
	}
	return llm.ToolArgs(string(t))
}

// finishStream emits the terminal EvFinish after the done line.
func (s *streamer) finishStream() {
	reason := llm.FinishStop
	switch s.doneReason {
	case "length":
		reason = llm.FinishLength
	}
	if s.sawCalls {
		reason = llm.FinishToolCalls
	}
	s.emit(llm.Event{Type: llm.EvFinish, FinishReason: reason, Usage: s.usage})
}

// ---- models ----------------------------------------------------------------

// Models lists the models the server has (GET /api/tags). Each model's
// context window and capabilities come from POST /api/show, best effort: a
// model whose details fail is still listed, with a zero window.
func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	c, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, p.url("/api/tags"), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.opts.ID, err)
	}
	p.setHeaders(req.Header, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, p.unreachable(c, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, p.httpError(resp)
	}
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&tags); err != nil {
		return nil, fmt.Errorf("%s: bad model list: %w", p.opts.ID, err)
	}
	out := make([]llm.ModelInfo, 0, len(tags.Models))
	for _, m := range tags.Models {
		id := m.Model
		if id == "" {
			id = m.Name
		}
		if id == "" {
			continue
		}
		out = append(out, llm.ModelInfo{ID: id, Name: id})
	}
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.describe(ctx, &out[i])
		}(i)
	}
	wg.Wait()
	return out, nil
}

// describe fills the context window and capabilities of one model from
// /api/show. Errors leave the model as it was.
func (p *Provider) describe(ctx context.Context, m *llm.ModelInfo) {
	c, cancel := context.WithTimeout(ctx, showTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": m.ID})
	req, err := http.NewRequestWithContext(c, http.MethodPost, p.url("/api/show"), bytes.NewReader(body))
	if err != nil {
		return
	}
	p.setHeaders(req.Header, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return
	}
	var show struct {
		ModelInfo    map[string]json.RawMessage `json:"model_info"`
		Capabilities []string                   `json:"capabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&show); err != nil {
		return
	}
	for k, v := range show.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			var n int
			if json.Unmarshal(v, &n) == nil && n > 0 {
				m.ContextWindow = n
			}
		}
	}
	for _, capab := range show.Capabilities {
		switch capab {
		case "tools":
			m.Tools = true
		case "thinking":
			m.Reasoning = true
		case "vision":
			m.Vision = true
		}
	}
}
