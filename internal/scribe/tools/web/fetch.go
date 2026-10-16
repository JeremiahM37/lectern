package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

type fetchTool struct{ w *webTools }

type fetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
}

func (t *fetchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "fetch_page",
		Description: "Fetch a web page and return its readable text as Markdown (headings, lists, links, code, tables). " +
			"Navigation, scripts and page chrome are removed. JSON and plain text are returned as-is. " +
			"PDFs and images are not extracted.",
		Schema: tools.Schema(map[string]any{
			"url":       tools.Str("The http:// or https:// address to fetch."),
			"max_chars": tools.Int(fmt.Sprintf("Maximum characters of page text to return. Default %d, maximum %d.", defaultFetchChars, maxFetchChars)),
		}, "url"),
	}
}

func (t *fetchTool) ReadOnly() bool { return true }

func (t *fetchTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a fetchArgs
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v. fetch_page takes {\"url\": string, \"max_chars\": integer}.", err)
	}
	raw := strings.TrimSpace(a.URL)
	if raw == "" {
		return tools.Errorf("fetch_page needs a url. Pass a full http:// or https:// address.")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return tools.Errorf("fetch_page: %q is not a valid URL: %v", raw, err)
	}
	// Refuse blocked targets before asking the user about them.
	if err := t.w.checkURL(ctx, u); err != nil {
		return tools.Errorf("fetch_page: %v. Only public http and https addresses can be fetched.", err)
	}
	if ok, msg := t.w.approve(ctx, env, "fetch_page", "Fetch a web page", u.String()); !ok {
		return tools.Result{Output: msg, IsError: true}
	}
	max := a.MaxChars
	if max <= 0 {
		max = defaultFetchChars
	}
	if max > maxFetchChars {
		max = maxFetchChars
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	pg, cached := t.w.pages.Get(u.String())
	if !cached {
		pg, err = t.w.fetch(ctx, u)
	}
	if err != nil {
		return tools.Errorf("fetch_page failed for %s: %s. The page may be down or blocked; try another source.", u.Redacted(), describeErr(err))
	}

	if !cached && pg.note == "" && len(pg.text) <= maxCachedPageChars {
		t.w.pages.Put(u.String(), pg)
	}
	var b strings.Builder
	if pg.title != "" {
		fmt.Fprintf(&b, "Title: %s\n", pg.title)
	}
	fmt.Fprintf(&b, "URL: %s\n\n", pg.finalURL)
	body := pg.text
	if pg.note != "" {
		body = pg.note
	}
	if body == "" {
		body = "(The page has no readable text. It may be rendered by JavaScript, which fetch_page does not run.)"
	}
	total := utf8.RuneCountInString(body)
	if total > max {
		body = string([]rune(body)[:max])
		body += fmt.Sprintf("\n\n[Truncated: showing the first %d of %d characters. Call fetch_page again with a larger max_chars (up to %d) to read more.]", max, total, maxFetchChars)
	}
	if pg.truncatedBytes {
		body += fmt.Sprintf("\n\n[Stopped reading at %s; the rest of the response was not downloaded.]", humanBytes(maxFetchBytes))
	}
	b.WriteString(body)
	return tools.Result{
		Output:  b.String(),
		Summary: fmt.Sprintf("%d characters", total),
		Meta:    map[string]any{"content_type": pg.contentType, "url": pg.finalURL, "backend": pg.backend},
	}
}

// page is one fetched document after conversion to text.
type page struct {
	finalURL       string
	contentType    string
	title          string
	text           string
	note           string // set instead of text for formats we do not read (PDF)
	truncatedBytes bool
	// backend names where the text came from: "jina", "jina-api" or "local".
	backend string
}

// fetch reads u. With the Jina Reader on, it tries the reader first and falls
// back to a local fetch on any reader failure, so a reader outage or a bad key
// only changes where the text came from. The caller has already run the SSRF
// check on u.
func (w *webTools) fetch(ctx context.Context, u *url.URL) (*page, error) {
	if w.opts.JinaReader {
		if pg, err := w.fetchJina(ctx, u); err == nil {
			return pg, nil
		}
	}
	return w.fetchLocal(ctx, u)
}

// fetchLocal downloads u and converts it to readable text. Every redirect hop
// and every connection goes through the guard.
func (w *webTools) fetchLocal(ctx context.Context, u *url.URL) (*page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", w.opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/*;q=0.9,*/*;q=0.5")
	resp, err := w.fetchCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("the server answered HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the body: %v", err)
	}
	truncated := len(data) > maxFetchBytes
	if truncated {
		data = data[:maxFetchBytes]
	}
	ct := mediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = mediaType(http.DetectContentType(data))
	}
	pg := &page{
		finalURL:       resp.Request.URL.String(),
		contentType:    ct,
		truncatedBytes: truncated,
		backend:        "local",
	}
	base := resp.Request.URL
	switch {
	case ct == "text/html" || ct == "application/xhtml+xml":
		pg.title, pg.text = htmlToMarkdown(string(data), base)
	case ct == "application/pdf":
		size := int64(len(data))
		if resp.ContentLength >= 0 {
			size = resp.ContentLength
		}
		pg.note = fmt.Sprintf("This page is a PDF (%s). fetch_page does not extract PDF text. Tell the user it is a PDF; do not guess its contents.", humanBytes(size))
	case strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "font/") || ct == "application/octet-stream" || ct == "application/zip" ||
		ct == "application/gzip" || ct == "application/x-tar":
		pg.note = fmt.Sprintf("This is a binary file (%s, %s), which fetch_page cannot read as text. Tell the user what it is instead of guessing its contents.", ct, humanBytes(int64(len(data))))
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		pg.text = prettyJSON(data)
	default:
		// text/*, XML and other text-like types pass through unchanged.
		pg.text = string(data)
	}
	return pg, nil
}

// prettyJSON indents valid JSON and returns anything else unchanged.
func prettyJSON(data []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(data), "", "  "); err != nil {
		return string(data)
	}
	return buf.String()
}

// mediaType returns the lowercased type/subtype without parameters.
func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
