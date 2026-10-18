// Package proxy is the "proxy" provider: a local CLIProxyAPI sidecar that
// relays existing chat subscriptions as an API on 127.0.0.1:8317. Claude
// models go through the Anthropic Messages API the proxy serves. Every other
// model goes through OpenAI-compatible chat completions.
//
// This package covers routing, the system-prompt workaround, key validation,
// health probing, the bounce after a connection error (bounce.go) and the
// sidecar process itself: starting, adopting and stopping it (sidecar.go),
// and the proxy's own login flows (login.go).
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/anthropic"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

// probeTimeout bounds the health check.
const probeTimeout = 2 * time.Second

// Options configures the provider.
type Options struct {
	// BaseURL is the OpenAI-compatible root, e.g. http://127.0.0.1:8317/v1.
	// The Anthropic route uses the same host without the /v1 suffix.
	BaseURL    string
	APIKey     string // optional; sent as a bearer (and x-api-key on Claude)
	HTTPClient *http.Client
	Headers    map[string]string

	// Bounce restarts the sidecar after a connection error and reports the
	// endpoint to use from then on (spec 03 section 10: proxy bounce). It is
	// only called when the health probe finds nothing listening. The sidecar
	// lifecycle belongs to the caller. Nil means no restart is possible, and
	// the provider reports the error instead.
	Bounce func(ctx context.Context) (Endpoint, error)
}

// Endpoint is where the proxy listens and the key to send to it.
type Endpoint struct {
	// BaseURL is the OpenAI-compatible root, as in Options.BaseURL.
	BaseURL string
	APIKey  string
}

// route is the pair of adapters for one endpoint. It is replaced when a
// bounce finds the proxy somewhere else or with another key.
type route struct {
	base   string
	key    string
	claude llm.Provider
	chat   llm.Provider
}

// Provider routes each request to the adapter for its model.
type Provider struct {
	opts Options

	mu sync.RWMutex
	rt route
	// bounced is true from a bounce until a stream from the proxy delivers
	// its first event. It keeps one failing run from restarting the proxy on
	// every retry.
	bounced bool
}

var _ llm.Provider = (*Provider)(nil)

// New returns the routing provider.
func New(opts Options) *Provider {
	return &Provider{opts: opts, rt: newRoute(opts, opts.BaseURL, opts.APIKey)}
}

func newRoute(opts Options, base, key string) route {
	root := strings.TrimRight(base, "/")
	root = strings.TrimSuffix(root, "/v1")
	return route{
		base: base,
		key:  key,
		claude: anthropic.New(anthropic.Options{
			ID:           "proxy",
			BaseURL:      root,
			APIKey:       key,
			HTTPClient:   opts.HTTPClient,
			ExtraHeaders: opts.Headers,
		}),
		chat: openai.New(openai.Options{
			ID:         "proxy",
			BaseURL:    strings.TrimRight(base, "/"),
			APIKey:     key,
			HTTPClient: opts.HTTPClient,
			Headers:    opts.Headers,
		}),
	}
}

// current returns the route in use now.
func (p *Provider) current() route {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rt
}

// ID returns the registry id.
func (p *Provider) ID() string { return "proxy" }

// Stream sends a Claude model through the Anthropic route, with the system
// prompt moved into the first user message, and every other model through
// chat completions. A connection error before any output is handled by
// bounceAndRetry (bounce.go).
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	ch, err := p.open(ctx, req)
	if err != nil {
		if !bounceable(ctx, err) {
			return nil, err
		}
		return p.bounceAndRetry(ctx, req, err)
	}
	out := make(chan llm.Event, 16)
	go p.relay(ctx, req, ch, out, true)
	return out, nil
}

// open starts one call on the route in use now, without any retry.
func (p *Provider) open(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	rt := p.current()
	if IsClaude(req.Model) {
		return rt.claude.Stream(ctx, Cloak(req))
	}
	return rt.chat.Stream(ctx, req)
}

// Models lists the proxy's models through /models.
func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	return p.current().chat.Models(ctx)
}

// IsClaude reports whether a model id goes to the Anthropic route. Only ids
// that begin with "claude" do; a gateway-prefixed id uses chat completions.
func IsClaude(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "claude")
}

// Cloak moves the system prompt into the first user message. Some proxies
// replace a system prompt with the upstream product's own prompt and demote
// the user's, so Scribe's instructions travel as user text instead. The
// caller's request is not modified.
func Cloak(req llm.Request) llm.Request {
	if req.System == "" {
		return req
	}
	msgs := make([]llm.Message, 0, len(req.Messages)+1)
	msgs = append(msgs, llm.UserText(req.System))
	msgs = append(msgs, req.Messages...)
	req.System = ""
	req.Messages = msgs
	return req
}

// State is the proxy's health as seen from a probe.
type State string

const (
	// Running: the proxy answered and accepted the key.
	Running State = "running"
	// NeedsAuth: the proxy answered 401 or 403. It is up, but the key or the
	// provider login is wrong.
	NeedsAuth State = "needs-auth"
	// Unreachable: nothing answered, or it answered something else.
	Unreachable State = "unreachable"
)

// Probe checks GET {base}/models with a bearer key and a two-second limit.
// 200 means Running, 401 or 403 means NeedsAuth, and anything else means
// Unreachable.
func Probe(ctx context.Context, client *http.Client, base, key string) State {
	if client == nil {
		client = &http.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return Unreachable
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Unreachable
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
		return Running
	case http.StatusUnauthorized, http.StatusForbidden:
		return NeedsAuth
	}
	return Unreachable
}

// placeholders are example keys people leave in config files. They are not
// real credentials, so they are never tried.
var placeholders = map[string]bool{
	"your-key":   true,
	"changeme":   true,
	"replace-me": true,
	"xxx":        true,
}

// UsableKey reports whether a key could be a real proxy credential. Empty
// values, placeholders, bcrypt hashes and SHA-256 hex digests are rejected.
// A proxy stores its keys as hashes in some setups, and a hash cannot be sent
// as a bearer token.
func UsableKey(k string) bool {
	k = strings.TrimSpace(k)
	if k == "" || placeholders[strings.ToLower(k)] {
		return false
	}
	if strings.HasPrefix(k, "$2a$") || strings.HasPrefix(k, "$2b$") || strings.HasPrefix(k, "$2y$") {
		return false
	}
	if len(k) == sha256.Size*2 {
		if _, err := hex.DecodeString(k); err == nil {
			return false
		}
	}
	return true
}

// CandidateKeys returns the usable keys in the order to try them: env first
// (PROXY_API_KEY wins whenever it is usable), then the others, without
// duplicates.
func CandidateKeys(env string, others ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range append([]string{env}, others...) {
		k = strings.TrimSpace(k)
		if !UsableKey(k) || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}
