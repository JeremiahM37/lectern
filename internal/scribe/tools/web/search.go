package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

type searchTool struct{ w *webTools }

type searchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (t *searchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "web_search",
		Description: "Search the web and return numbered results (title, URL, snippet). " +
			"Check the codebase and the conversation first; search when the answer is outside them. " +
			"Follow up with fetch_page on a promising URL to read it.",
		Schema: tools.Schema(map[string]any{
			"query":       tools.Str("The search terms."),
			"max_results": tools.Int(fmt.Sprintf("How many results to return. Default %d, maximum %d.", defaultSearchResults, maxSearchResults)),
		}, "query"),
	}
}

func (t *searchTool) ReadOnly() bool { return true }

func (t *searchTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a searchArgs
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v. web_search takes {\"query\": string, \"max_results\": integer}.", err)
	}
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return tools.Errorf("web_search needs a non-empty query. Pass the search terms in \"query\".")
	}
	n := a.MaxResults
	if n <= 0 {
		n = defaultSearchResults
	}
	if n > maxSearchResults {
		n = maxSearchResults
	}
	if ok, msg := t.w.approve(ctx, env, "web_search", "Search the web", q); !ok {
		return tools.Result{Output: msg, IsError: true}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	key := strings.ToLower(q) + "\x00" + fmt.Sprint(n)
	results, cached := t.w.searches.Get(key)
	var err error
	if !cached {
		results, err = t.w.search(ctx, q, n)
		if err == nil && len(results) > 0 {
			t.w.searches.Put(key, results)
		}
	}
	if err != nil {
		return tools.Errorf("web_search failed: %s. Retry once with different terms, or use fetch_page on a URL you already know.", describeErr(err))
	}
	if len(results) == 0 {
		return tools.Result{Output: fmt.Sprintf("No results for %q. Try different terms.", q), Summary: "0 results"}
	}
	summary := fmt.Sprintf("%d results", len(results))
	if cached {
		summary += " (cached)"
	}
	return tools.Result{
		Output:  formatResults(results),
		Summary: summary,
	}
}

// search runs the configured backends in order: Brave when a key is set,
// then SearXNG when a URL is set, else DuckDuckGo. Brave falls through on any
// failure or an empty answer, so a bad key does not end web search.
func (w *webTools) search(ctx context.Context, q string, n int) ([]searchResult, error) {
	if w.opts.BraveAPIKey != "" {
		if rs, err := w.brave(ctx, q, n); err == nil && len(rs) > 0 {
			return rs, nil
		}
	}
	if w.opts.SearchURL != "" {
		return w.searxng(ctx, q, n)
	}
	return w.duckduckgo(ctx, q, n)
}

// brave queries the Brave Search web API with the subscription key.
func (w *webTools) brave(ctx context.Context, q string, n int) ([]searchResult, error) {
	u, err := url.Parse(w.braveURL)
	if err != nil {
		return nil, err
	}
	qs := u.Query()
	qs.Set("q", q)
	qs.Set("count", fmt.Sprint(n))
	u.RawQuery = qs.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", w.opts.BraveAPIKey)
	req.Header.Set("User-Agent", w.opts.UserAgent)
	resp, err := w.searchCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Brave Search answered HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSearchBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("Brave Search returned data that is not JSON: %v", err)
	}
	var out []searchResult
	for _, r := range payload.Web.Results {
		if len(out) >= n {
			break
		}
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, searchResult{
			Title:   collapse(stripTags(r.Title)),
			URL:     r.URL,
			Snippet: collapse(stripTags(r.Description)),
		})
	}
	return out, nil
}

// stripTags removes the <strong> highlights Brave wraps around matches and
// decodes entities.
func stripTags(s string) string {
	return html.UnescapeString(tagPattern.ReplaceAllString(s, ""))
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

func formatResults(rs []searchResult) string {
	var b strings.Builder
	for i, r := range rs {
		title := r.Title
		if title == "" {
			title = r.URL
		}
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
		if i < len(rs)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// searxng queries a SearXNG instance's JSON API.
func (w *webTools) searxng(ctx context.Context, q string, n int) ([]searchResult, error) {
	u, err := searxURL(w.opts.SearchURL, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", w.opts.UserAgent)
	resp, err := w.searchCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("SearXNG answered 403, which usually means its JSON format is disabled (enable \"json\" under search.formats in its settings.yml)")
		}
		return nil, fmt.Errorf("SearXNG answered HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSearchBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("SearXNG returned data that is not JSON: %v", err)
	}
	var out []searchResult
	for _, r := range payload.Results {
		if len(out) >= n {
			break
		}
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, searchResult{
			Title:   collapse(r.Title),
			URL:     r.URL,
			Snippet: collapse(r.Content),
		})
	}
	return out, nil
}

// duckduckgo posts to DuckDuckGo's no-JavaScript HTML endpoint.
func (w *webTools) duckduckgo(ctx context.Context, q string, n int) ([]searchResult, error) {
	form := url.Values{"q": {q}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.ddgURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", w.opts.UserAgent)
	resp, err := w.searchCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchBytes))
	if err != nil {
		return nil, err
	}
	results := parseDuckDuckGo(string(body), n)
	if len(results) == 0 && strings.Contains(strings.ToLower(string(body)), "captcha") {
		return nil, fmt.Errorf("DuckDuckGo returned a CAPTCHA page instead of results (set searchURL to a SearXNG instance to avoid this)")
	}
	return results, nil
}

// parseDuckDuckGo reads result titles (a.result__a), snippets
// (.result__snippet) and URLs from the HTML endpoint. Redirect wrappers are
// unwrapped via their uddg parameter. Sponsored blocks (.result--ad) are skipped.
func parseDuckDuckGo(src string, max int) []searchResult {
	var out []searchResult
	pending := -1 // index of the last result still waiting for a snippet
	var walk func(n *node, inAd bool)
	walk = func(n *node, inAd bool) {
		for _, c := range n.kids {
			if c.tag == "" || dropped[c.tag] {
				continue
			}
			ad := inAd || hasClass(c, "result--ad")
			switch {
			case c.tag == "a" && hasClass(c, "result__a"):
				if !ad {
					if target := ddgTarget(c.attrs["href"]); target != "" {
						out = append(out, searchResult{Title: collapse(inlineText(c)), URL: target})
						pending = len(out) - 1
					}
				}
			case hasClass(c, "result__snippet"):
				if !ad && pending >= 0 && out[pending].Snippet == "" {
					out[pending].Snippet = collapse(inlineText(c))
					pending = -1
				}
				continue
			}
			walk(c, ad)
		}
	}
	walk(parseHTML(src), false)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// ddgTarget turns a DuckDuckGo result href into the destination URL.
func ddgTarget(href string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(u.Path, "/l/") && (u.Host == "" || strings.HasSuffix(u.Host, "duckduckgo.com")) {
		if t := u.Query().Get("uddg"); t != "" {
			href = t
			u, err = url.Parse(href)
			if err != nil {
				return ""
			}
		}
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return u.String()
	}
	return ""
}

// hasClass reports whether n's class attribute contains cls as a token.
func hasClass(n *node, cls string) bool {
	for _, f := range strings.Fields(n.attrs["class"]) {
		if f == cls {
			return true
		}
	}
	return false
}
