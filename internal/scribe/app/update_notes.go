package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// releaseInfo is what the update check reads from the release feed: the tag
// (always), and the notes and page address when the feed carries them. The
// GitHub latest-release document has all three in one response, so the
// changelog costs no request beyond the tag check.
type releaseInfo struct {
	Tag     string
	Body    string
	HTMLURL string
}

// maxReleaseNotes caps the changelog lines kept for the notice.
const maxReleaseNotes = 20

// fetchRelease reads the release feed once. It matches version.LatestTag's
// request and its errors, and also keeps the release body and page.
func fetchRelease(ctx context.Context, client *http.Client, url string) (releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("User-Agent", "lectern-version/"+version.Version)
	res, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("%s: %s", url, res.Status)
	}
	var doc struct {
		Tag     string `json:"tag_name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		return releaseInfo{}, err
	}
	if doc.Tag == "" {
		return releaseInfo{}, errors.New("the release list had no tag")
	}
	return releaseInfo{Tag: doc.Tag, Body: doc.Body, HTMLURL: doc.HTMLURL}, nil
}

// releaseNotes turns a release body into one line per change, newest first
// as the author wrote them. Bullet items are the changes; a body with no
// bullets falls back to its plain lines. Headings and blank lines are dropped.
func releaseNotes(body string) []string {
	var bullets, plain []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") {
			continue
		}
		if item, ok := bulletText(line); ok {
			bullets = append(bullets, item)
			continue
		}
		plain = append(plain, line)
	}
	out := bullets
	if len(out) == 0 {
		out = plain
	}
	if len(out) > maxReleaseNotes {
		out = out[:maxReleaseNotes]
	}
	for i := range out {
		out[i] = strings.NewReplacer("**", "", "`", "").Replace(oneLineStr(out[i]))
	}
	return out
}

// bulletText strips a markdown list marker ("- ", "* ", "+ ", "1. ") and
// reports whether line was a list item.
func bulletText(line string) (string, bool) {
	for _, m := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, m) {
			return strings.TrimSpace(line[len(m):]), true
		}
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' ' {
		return strings.TrimSpace(line[i+2:]), true
	}
	return "", false
}
