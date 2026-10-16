package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// maxSourceChars caps the text one URL contributes to a read.
const maxSourceChars = 1 << 20

// Source is a web page fetched for the read tool.
type Source struct {
	URL       string // final URL after redirects
	Title     string
	Text      string // readable text, Markdown for HTML
	Truncated bool
}

// FetchSource fetches raw for the read tool. It runs the same checks as
// fetch_page: http or https only, the private-address guard before the
// request and at dial time, the per-session web approval, and the download
// cap. The Jina Reader is never used, so the URL is not sent to a third
// party. Each call asks again, because the read tool has no web state of its
// own.
func FetchSource(ctx context.Context, env *tools.Env, raw string) (Source, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return Source{}, fmt.Errorf("%q is not a valid URL: %v", raw, err)
	}
	w := newWebTools(Options{})
	if err := w.checkURL(ctx, u); err != nil {
		return Source{}, fmt.Errorf("%v. Only public http and https addresses can be read", err)
	}
	if ok, msg := w.approve(ctx, env, "read", "Read a web page", u.String()); !ok {
		return Source{}, errors.New(msg)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	pg, err := w.fetch(ctx, u)
	if err != nil {
		return Source{}, fmt.Errorf("could not fetch %s: %s", u.Redacted(), describeErr(err))
	}
	if pg.note != "" {
		return Source{}, errors.New(pg.note)
	}
	src := Source{URL: pg.finalURL, Title: pg.title, Text: pg.text}
	if utf8.RuneCountInString(src.Text) > maxSourceChars {
		src.Text = string([]rune(src.Text)[:maxSourceChars])
		src.Truncated = true
	}
	return src, nil
}
