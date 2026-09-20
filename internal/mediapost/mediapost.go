// Package mediapost is the one way a file or link reaches AgentDeck's media
// feed. The MCP tool and the CLI both post through it, so an agent calling a
// tool and a script calling `agentdeck post` cannot drift apart.
package mediapost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Post is one thing to show the operator: exactly one of Path or URL.
type Post struct {
	Path      string
	URL       string
	Title     string
	Note      string
	SessionID int64
	Source    string // mcp | cli
}

// SessionID is the AgentDeck session this process was started in, when the
// launcher said so. It is exact where TmuxSession is an inference, and it
// survives what tmux does not: an agent that starts its tools with a scrubbed
// environment forwards a named variable far more readily than TMUX.
func SessionID() int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("AGENTDECK_SESSION_ID")), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// TmuxSession names the tmux session this process runs inside, which is how a
// post finds its AgentDeck session without the agent knowing its own id: the
// poster is a child of the agent, and the agent lives in the session's pane.
func TmuxSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := []string{"display-message", "-p"}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = append(args, "-t", pane)
	}
	out, err := exec.CommandContext(ctx, "tmux", append(args, "#S")...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Send posts to the AgentDeck API at base and returns the created row as JSON.
func Send(base, token string, p Post) ([]byte, error) {
	p.Path, p.URL = strings.TrimSpace(p.Path), strings.TrimSpace(p.URL)
	if (p.Path == "") == (p.URL == "") {
		return nil, fmt.Errorf("give exactly one of a file path or a url")
	}
	endpoint := strings.TrimRight(base, "/") + "/api/media"
	// What the environment says is sent as a hint, apart from an id someone
	// typed: inherited from another AgentDeck's session it names nothing here,
	// and that must not turn a post into an error.
	tmux, hint := "", int64(0)
	if p.SessionID == 0 {
		hint, tmux = SessionID(), TmuxSession()
	}
	var req *http.Request
	if p.URL != "" {
		raw, err := json.Marshal(map[string]any{"url": p.URL, "title": p.Title, "note": p.Note,
			"session_id": p.SessionID, "hint_session_id": hint, "tmux_session": tmux, "source": p.Source})
		if err != nil {
			return nil, err
		}
		if req, err = http.NewRequest("POST", endpoint, bytes.NewReader(raw)); err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		f, err := os.Open(p.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", p.Path)
		}
		q := url.Values{"name": {filepath.Base(p.Path)}, "title": {p.Title}, "note": {p.Note},
			"tmux_session": {tmux}, "source": {p.Source}}
		if p.SessionID > 0 {
			q.Set("session_id", strconv.FormatInt(p.SessionID, 10))
		}
		if hint > 0 {
			q.Set("hint_session_id", strconv.FormatInt(hint, 10))
		}
		if req, err = http.NewRequest("POST", endpoint+"?"+q.Encode(), f); err != nil {
			return nil, err
		}
		req.ContentLength = st.Size()
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// A recording can take minutes to stream over a slow link; the API clients'
	// one-minute budget is for JSON calls, not for this.
	resp, err := (&http.Client{Timeout: time.Hour}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("agentdeck unreachable at %s: %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var failure struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &failure) == nil && failure.Detail != "" {
			return nil, fmt.Errorf("%s", failure.Detail)
		}
		return nil, fmt.Errorf("post failed: %s", resp.Status)
	}
	return raw, nil
}
