// Package copilot implements llm.Provider for GitHub Copilot. The user's
// OAuth token (the one the editor writes to apps.json) is exchanged for a
// short-lived bearer, which is cached until shortly before it expires. Chat
// calls then go to the Copilot API as OpenAI-compatible chat completions,
// with the editor-identifying headers the API expects.
//
// The integration is unofficial: it uses the same internal endpoints as the
// editor extension. The header values below are not given by the behavioural
// spec, so they are the ones the public Copilot Chat client sends and must be
// checked against the live API before the provider is relied on.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

const (
	defaultTokenURL = "https://api.github.com/copilot_internal/v2/token"
	defaultBaseURL  = "https://api.githubcopilot.com"
	// refreshMargin renews the bearer this long before it expires.
	refreshMargin = time.Minute
	// fallbackLifetime applies when the exchange does not say when the token
	// expires.
	fallbackLifetime = 25 * time.Minute
	maxTokenBody     = 64 << 10
)

// editorHeaders identify the client to the Copilot API. Not given by the
// spec; see the package comment.
var editorHeaders = map[string]string{
	"Editor-Version":         "vscode/1.99.3",
	"Editor-Plugin-Version":  "copilot-chat/0.26.7",
	"Copilot-Integration-Id": "vscode-chat",
	"User-Agent":             "GitHubCopilotChat/0.26.7",
}

// Options configures the provider.
type Options struct {
	// OAuthToken is the GitHub OAuth token from the editor (COPILOT_API_KEY).
	OAuthToken string
	// TokenURL defaults to the Copilot token exchange on api.github.com.
	TokenURL string
	// BaseURL defaults to https://api.githubcopilot.com.
	BaseURL    string
	HTTPClient *http.Client
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// TokenSource exchanges the OAuth token for bearers and caches the latest one.
type TokenSource struct {
	opts Options

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewTokenSource returns a source with defaults applied.
func NewTokenSource(opts Options) *TokenSource {
	if opts.TokenURL == "" {
		opts.TokenURL = defaultTokenURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &TokenSource{opts: opts}
}

// Token returns a bearer that is valid now, exchanging the OAuth token when
// the cached one is missing or close to expiry.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := ts.opts.Now()
	if ts.token != "" && now.Before(ts.expires.Add(-refreshMargin)) {
		return ts.token, nil
	}
	tok, exp, err := ts.exchange(ctx, now)
	if err != nil {
		return "", err
	}
	ts.token, ts.expires = tok, exp
	return tok, nil
}

// exchange trades the OAuth token for a bearer at the token endpoint.
func (ts *TokenSource) exchange(ctx context.Context, now time.Time) (string, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.opts.TokenURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("copilot: build token request: %w", err)
	}
	req.Header.Set("Authorization", "token "+ts.opts.OAuthToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", editorHeaders["User-Agent"])

	resp, err := ts.opts.HTTPClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("copilot: token exchange: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", time.Time{}, fmt.Errorf("copilot: token exchange refused (HTTP %d); check COPILOT_API_KEY holds the editor's OAuth token", resp.StatusCode)
		}
		return "", time.Time{}, fmt.Errorf("copilot: token exchange HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", time.Time{}, fmt.Errorf("copilot: decode token: %w", err)
	}
	if body.Token == "" {
		return "", time.Time{}, fmt.Errorf("copilot: token exchange returned no token")
	}
	exp := now.Add(fallbackLifetime)
	if body.ExpiresAt > 0 {
		exp = time.Unix(body.ExpiresAt, 0)
	}
	return body.Token, exp, nil
}

// New returns the Copilot provider: an OpenAI-compatible adapter whose bearer
// comes from a TokenSource.
func New(opts Options) *openai.Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	ts := NewTokenSource(opts)
	return openai.New(openai.Options{
		ID:          "copilot",
		BaseURL:     opts.BaseURL,
		HTTPClient:  opts.HTTPClient,
		Headers:     editorHeaders,
		TokenSource: ts.Token,
	})
}
