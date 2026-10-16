// Package anthropic implements llm.Provider over the Anthropic Messages API
// (POST /v1/messages with stream=true). It speaks the wire format directly
// with net/http and encoding/json; no SDK is used.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
	// defaultMaxTokens applies when the request leaves MaxTokens unset.
	defaultMaxTokens = 8192
	// maxErrorBody bounds how much of a non-2xx body is read for the error.
	maxErrorBody = 1 << 20
)

// Options configures a Provider.
type Options struct {
	// APIKey is sent as x-api-key. Ignored when AuthToken is set.
	APIKey string
	// BaseURL defaults to https://api.anthropic.com. A trailing slash is trimmed.
	BaseURL string
	// HTTPClient defaults to a client with no overall timeout (streams are long).
	HTTPClient *http.Client
	// ID is the registry id, default "anthropic".
	ID string
	// ExtraHeaders are added to every request (e.g. anthropic-beta).
	ExtraHeaders map[string]string
	// AuthToken, when set, is sent as "Authorization: Bearer" instead of
	// x-api-key. Used for OAuth tokens and proxies.
	AuthToken string
}

// Provider is an llm.Provider for the Anthropic Messages API.
type Provider struct {
	opts Options
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider with defaults applied.
func New(opts Options) *Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	if opts.ID == "" {
		opts.ID = "anthropic"
	}
	return &Provider{opts: opts}
}

// ID returns the registry id.
func (p *Provider) ID() string { return p.opts.ID }

// Stream starts a streamed Messages call. Failures that happen after the
// request is built (transport errors, non-2xx responses, mid-stream error
// events) are reported as an EvError on the returned channel, so a caller
// has one place to look. Only failures to build the request return an error.
//
// The channel is closed after exactly one terminal event. Consumers must
// drain it until it closes.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.opts.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	p.setAuth(httpReq.Header, req.Headers)

	resp, err := p.opts.HTTPClient.Do(httpReq)
	ch := make(chan llm.Event, 16)
	if err != nil {
		terminal(ch, llm.Event{Type: llm.EvError, Err: err})
		return ch, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := p.readAPIError(resp)
		_ = resp.Body.Close()
		terminal(ch, llm.Event{Type: llm.EvError, Err: apiErr})
		return ch, nil
	}

	go func() {
		defer close(ch)
		defer resp.Body.Close()
		ch <- p.run(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// setAuth applies auth, version and extra headers. Per-request headers win
// over provider-level ones so a caller can always override.
func (p *Provider) setAuth(h http.Header, perRequest map[string]string) {
	h.Set("anthropic-version", apiVersion)
	switch {
	case p.opts.AuthToken != "":
		h.Set("Authorization", "Bearer "+p.opts.AuthToken)
	case p.opts.APIKey != "":
		h.Set("x-api-key", p.opts.APIKey)
	}
	for k, v := range p.opts.ExtraHeaders {
		h.Set(k, v)
	}
	for k, v := range perRequest {
		h.Set(k, v)
	}
}

// run consumes the SSE body, emitting non-terminal events on ch, and returns
// the single terminal event.
func (p *Provider) run(ctx context.Context, body io.Reader, ch chan<- llm.Event) llm.Event {
	st := newStreamState(p.opts.ID)
	var final *llm.Event

	err := readSSE(body, func(data []byte) error {
		evs, done, err := st.apply(data)
		if err != nil {
			return err
		}
		if done {
			last := evs[len(evs)-1]
			final = &last
			evs = evs[:len(evs)-1]
		}
		for _, ev := range evs {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if done {
			return errStreamDone
		}
		return nil
	})

	switch {
	case final != nil:
		return *final
	case ctx.Err() != nil:
		return llm.Event{Type: llm.EvError, Err: ctx.Err()}
	case err != nil:
		return llm.Event{Type: llm.EvError, Err: fmt.Errorf("anthropic: read stream: %w", err)}
	default:
		return llm.Event{Type: llm.EvError, Err: fmt.Errorf("anthropic: stream ended before message_stop: %w", io.ErrUnexpectedEOF)}
	}
}

// readAPIError builds an *llm.APIError from a non-2xx response.
func (p *Provider) readAPIError(resp *http.Response) *llm.APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	ae := &llm.APIError{
		Provider:   p.opts.ID,
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && (env.Error.Type != "" || env.Error.Message != "") {
		ae.Type = env.Error.Type
		ae.Message = env.Error.Message
	} else {
		ae.Message = strings.TrimSpace(string(raw))
		if len(ae.Message) > 500 {
			ae.Message = ae.Message[:500]
		}
	}
	if ae.Message == "" {
		ae.Message = http.StatusText(resp.StatusCode)
	}
	return ae
}

// parseRetryAfter accepts both delta-seconds and an HTTP date.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// terminal delivers one terminal event and closes ch.
func terminal(ch chan llm.Event, ev llm.Event) {
	defer close(ch)
	ch <- ev
}
