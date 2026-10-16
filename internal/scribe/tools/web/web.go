// Package web holds Scribe's web tools: web_search (SearXNG or DuckDuckGo
// HTML) and fetch_page (a readable-text fetch with an SSRF guard).
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Options configures the web tools.
type Options struct {
	// SearchURL is a SearXNG base URL (for example http://searx.internal:8888).
	// Empty selects the DuckDuckGo HTML backend. SearXNG is usually a local
	// service the user configured on purpose, so its address is not guarded.
	SearchURL string
	// BraveAPIKey, when set, makes web_search try the Brave Search API first
	// and fall back to the other backends when Brave fails or returns
	// nothing. Empty skips Brave.
	BraveAPIKey string
	// HTTPClient is optional. Search always uses it when set. fetch_page
	// uses a copy of it when set (with its redirect policy replaced by the
	// guard), so pass a client with a custom Transport only if you accept
	// that the dial-time address check is then the caller's responsibility.
	HTTPClient *http.Client
	// UserAgent is sent on every request. Empty uses a Scribe default.
	UserAgent string
	// AllowPrivate lets fetch_page reach loopback, private, link-local and
	// CGNAT addresses. Off by default. Tests use it with httptest.
	AllowPrivate bool
	// JinaAPIKey is sent as a bearer token to the Jina Reader. It is never
	// logged, never shown to the model and never placed in an error message.
	// Empty uses the Jina Reader's free tier.
	JinaAPIKey string
	// JinaReader, when true, reads pages through the Jina Reader
	// (r.jina.ai) before a local fetch. It sends each URL to a third party,
	// so it is off unless the user turned it on in settings.
	JinaReader bool
}

const (
	defaultUserAgent     = "Mozilla/5.0 (compatible; Scribe/1.0; +https://github.com/JeremiahM37/lectern)"
	defaultDDGURL        = "https://html.duckduckgo.com/html/"
	defaultBraveURL      = "https://api.search.brave.com/res/v1/web/search"
	requestTimeout       = 20 * time.Second
	maxFetchBytes        = 5 << 20
	maxSearchBytes       = 2 << 20
	maxRedirects         = 5
	defaultFetchChars    = 20000
	maxFetchChars        = 120000
	defaultSearchResults = 8
	maxSearchResults     = 20
	defaultJinaURL       = "https://r.jina.ai/"
	jinaTimeout          = 15 * time.Second
)

// webTools is the state shared by both tools: one approval cache and one
// set of HTTP clients.
type webTools struct {
	opts      Options
	ddgURL    string
	braveURL  string
	searchCli *http.Client
	fetchCli  *http.Client
	// jinaCli reaches the Jina Reader. Its address is a fixed public host, so
	// it does not go through the SSRF guard that fetch_page's own client uses.
	jinaCli  *http.Client
	jinaURL  string
	approval approvalCache
	// searches and pages hold results for resultTTL, so a repeated query or
	// URL in one session does not hit the network again.
	searches *ttlCache[[]searchResult]
	pages    *ttlCache[*page]
}

// Register adds web_search and fetch_page to r.
func Register(r *tools.Registry, opts Options) {
	w := newWebTools(opts)
	r.Register(&searchTool{w: w})
	r.Register(&fetchTool{w: w})
}

func newWebTools(opts Options) *webTools {
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	w := &webTools{
		opts:     opts,
		ddgURL:   defaultDDGURL,
		braveURL: defaultBraveURL,
		jinaURL:  defaultJinaURL,
		searches: newTTLCache[[]searchResult](resultTTL, resultCacheSize),
		pages:    newTTLCache[*page](resultTTL, resultCacheSize),
	}
	if opts.HTTPClient != nil {
		s := *opts.HTTPClient
		w.searchCli = &s
		f := *opts.HTTPClient
		w.fetchCli = &f
	} else {
		w.searchCli = &http.Client{Timeout: requestTimeout}
		w.fetchCli = &http.Client{Transport: w.guardedTransport()}
	}
	w.fetchCli.CheckRedirect = w.checkRedirect
	if opts.HTTPClient != nil {
		w.jinaCli = w.searchCli
	} else {
		w.jinaCli = &http.Client{Timeout: jinaTimeout}
	}
	return w
}

// approvalCache asks the host once per session for kind "web". An
// AllowSession answer covers both tools for the rest of the session.
type approvalCache struct {
	mu      sync.Mutex
	allowed bool
}

// approve returns true when the call may proceed. On false, the returned
// string is the message the model should see.
func (w *webTools) approve(ctx context.Context, env *tools.Env, tool, title, detail string) (bool, string) {
	if env != nil && env.Mode == "auto" {
		return true, ""
	}
	w.approval.mu.Lock()
	done := w.approval.allowed
	w.approval.mu.Unlock()
	if done {
		return true, ""
	}
	const denied = "The user denied this web access. Do not retry it; continue without web results and say what you could not check."
	if env == nil || env.Host == nil {
		return false, "No way to ask the user for approval in this session, so web access is off. " + denied
	}
	switch env.Host.Approve(ctx, tools.Approval{Tool: tool, Kind: "web", Title: title, Detail: detail}) {
	case tools.AllowSession:
		w.approval.mu.Lock()
		w.approval.allowed = true
		w.approval.mu.Unlock()
		return true, ""
	case tools.AllowOnce:
		return true, ""
	default:
		return false, denied
	}
}

// describeErr strips the url.Error wrapper so the model sees the cause.
func describeErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// searxURL builds the SearXNG query URL.
func searxURL(base, query string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("search URL %q is not valid: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("search URL %q must start with http:// or https://", base)
	}
	u = u.JoinPath("search")
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
