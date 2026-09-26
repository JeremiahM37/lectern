package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// A fixed explicit note path makes a timeout after persistence safe to retry.
// Do not use fact extraction/reconciliation for controller-authored receipts:
// one checkpoint must not become many inferred facts or a growing shared note.
func (g *Grimoire) rememberCheckpoint(ctx context.Context, e Entry) error {
	path := fmt.Sprintf("checkpoints/lectern/%x.md", sha256.Sum256([]byte(e.Project+"\x00"+e.Key)))
	body := strings.TrimSpace(e.Text)
	read := func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", g.BaseURL+"/api/notes/"+path, nil)
		if err != nil {
			return false, err
		}
		g.auth(req)
		res, err := g.Client.Do(req)
		if err != nil {
			return false, err
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return false, nil
		}
		if res.StatusCode != http.StatusOK {
			return false, fmt.Errorf("checkpoint read: HTTP %d", res.StatusCode)
		}
		var note struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&note); err != nil {
			return false, err
		}
		if strings.TrimSpace(note.Body) != body {
			return false, fmt.Errorf("checkpoint key collision; existing note preserved")
		}
		return true, nil
	}
	if found, err := read(); found || err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"path": path, "title": "Lectern checkpoint: " + e.Key,
		"body": body, "tags": []string{"lectern", "checkpoint"},
		"frontmatter": map[string]string{"agent": "lectern", "project": e.Project, "session": e.Session}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", g.BaseURL+"/api/notes", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return err
	} // next attempt reads the durable note first
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		found, err := read()
		if err != nil {
			return err
		}
		if found {
			return nil
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("checkpoint write: HTTP %d", res.StatusCode)
	}
	return nil
}
