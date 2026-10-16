package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// errJinaAuth is a 401 or 403 from the reader. With a key set, the reader is
// retried on the free tier before giving up.
var errJinaAuth = errors.New("jina reader refused the key")

// fetchJina reads u through the Jina Reader: GET https://r.jina.ai/<url> with
// Accept: text/markdown. With a key, it is sent as a bearer token; if the
// reader refuses that key (401/403) the request is repeated once without it.
// Any other failure returns an error and the caller falls back to a local
// fetch. Error text never includes the key.
func (w *webTools) fetchJina(ctx context.Context, u *url.URL) (*page, error) {
	pg, err := w.jinaRequest(ctx, u, w.opts.JinaAPIKey)
	if errors.Is(err, errJinaAuth) && w.opts.JinaAPIKey != "" {
		pg, err = w.jinaRequest(ctx, u, "")
	}
	if err != nil {
		return nil, err
	}
	if pg.backend == "" {
		pg.backend = "jina"
	}
	return pg, nil
}

func (w *webTools) jinaRequest(ctx context.Context, u *url.URL, key string) (*page, error) {
	ctx, cancel := context.WithTimeout(ctx, jinaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.jinaURL+u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("User-Agent", w.opts.UserAgent)
	backend := "jina"
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		backend = "jina-api"
	}
	resp, err := w.jinaCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jina reader: %s", describeErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, errJinaAuth
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("jina reader answered HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jina reader: reading the body: %v", err)
	}
	truncated := len(data) > maxFetchBytes
	if truncated {
		data = data[:maxFetchBytes]
	}
	title, text := parseJinaBody(string(data))
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("jina reader returned no text")
	}
	return &page{
		finalURL:       u.String(),
		contentType:    "text/markdown",
		title:          title,
		text:           text,
		truncatedBytes: truncated,
		backend:        backend,
	}, nil
}

// parseJinaBody splits the reader's header ("Title: ...", "URL Source: ...")
// from its "Markdown Content:" body. A body without that marker is returned
// whole.
func parseJinaBody(body string) (title, text string) {
	const marker = "Markdown Content:"
	head, rest, found := strings.Cut(body, marker)
	if !found {
		return "", body
	}
	for _, line := range strings.Split(head, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "Title:"); ok && title == "" {
			title = strings.TrimSpace(t)
		}
	}
	return title, strings.TrimLeft(rest, "\r\n")
}
