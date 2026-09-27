package trackers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Creds are what an HTTP-token forge (Bitbucket, Gitea/Forgejo, Azure
// DevOps) authenticates with. They come from the project's tracker
// connection and are sent only to that host.
type Creds struct {
	// BaseURL overrides the host's API root (a self-hosted server, or a test
	// server); empty uses the host's public default.
	BaseURL string
	// Username goes with Token as HTTP Basic where the host wants it
	// (Bitbucket Cloud: the Atlassian account email or username).
	Username string
	Token    string
}

// rest is a small JSON client for one host.
type rest struct {
	name string
	base string
	auth func(*http.Request)
	http *http.Client
}

func basicAuth(user, pass string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
}

func headerAuth(value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", value) }
}

// do sends one request. path is relative to base unless it is a full URL.
// A 401/403 becomes an Auth CLIError (the fix is the token, not a retry).
func (c rest) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = strings.TrimRight(c.base, "/") + "/" + strings.TrimLeft(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.auth != nil {
		c.auth(req)
	}
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return &CLIError{Auth: true, Msg: fmt.Sprintf("%s refused the token (HTTP %d): %s", c.name, resp.StatusCode, restErrorText(raw))}
	case resp.StatusCode == 404:
		return fmt.Errorf("%s: not found (%s)", c.name, restErrorText(raw))
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s returned HTTP %d: %s", c.name, resp.StatusCode, restErrorText(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(raw)
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s returned unexpected output: %s", c.name, clip(string(raw), 200))
	}
	return nil
}

// restErrorText digs the message out of the error bodies these hosts send.
func restErrorText(raw []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &e) == nil {
		switch {
		case e.Message != "":
			return e.Message
		case len(e.Errors) > 0 && e.Errors[0].Message != "":
			return e.Errors[0].Message
		}
		if m, ok := e.Error.(map[string]any); ok {
			if s, ok := m["message"].(string); ok {
				return s
			}
		}
		if s, ok := e.Error.(string); ok {
			return s
		}
	}
	return clip(string(raw), 200)
}

func unsupported(host, what string) error {
	return fmt.Errorf("%w: %s has no %s", ErrUnsupported, host, what)
}

// Every adapter satisfies Forge; the ones with a merge queue, Queuer.
var (
	_ Forge  = (*GitHub)(nil)
	_ Forge  = (*GitLab)(nil)
	_ Forge  = (*Gitea)(nil)
	_ Forge  = (*BitbucketCloud)(nil)
	_ Forge  = (*BitbucketServer)(nil)
	_ Forge  = (*Azure)(nil)
	_ Queuer = (*GitHub)(nil)
	_ Queuer = (*GitLab)(nil)
)
