package mcp

// A thin, read/write client for Grimoire's note API, used only by
// start_session and send_to_session so a brainstorm's context can travel as
// a Grimoire note (an existing one to attach, or a new "brief" the tool
// writes on the caller's behalf) instead of only as raw text in a message.
//
// This deliberately does NOT go through lectern's own HTTP API or its
// server-side memory.Grimoire provider (internal/memory/memory.go): the MCP
// process is a separate binary that may be talking to a lectern instance
// with no memory provider configured at all, or a different Grimoire than
// the one lectern's server uses. It reaches Grimoire directly, and is built
// to degrade: an unreachable Grimoire produces a clear error the calling
// tool can report or swallow, never a panic or a silent no-op.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var grimoireClient = &http.Client{Timeout: 10 * time.Second}

// grimoireBaseURL resolves Grimoire's address the way the task asked:
// LECTERN_GRIMOIRE_URL (this MCP process's own override), then GRIMOIRE_URL
// (the ambient one other homelab tools use), then the local default.
func grimoireBaseURL() string {
	for _, key := range []string{"LECTERN_GRIMOIRE_URL", "GRIMOIRE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://127.0.0.1:9111"
}

// grimoireNote is the slice of Grimoire's note view this package needs.
type grimoireNote struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// fetchGrimoireNote reads one note by its vault path (e.g. "projects/foo.md").
func fetchGrimoireNote(path string) (*grimoireNote, error) {
	base := grimoireBaseURL()
	req, err := http.NewRequest("GET", base+"/api/notes/"+strings.TrimLeft(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := grimoireClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Grimoire is unreachable at %s (%w) — note %q was not attached", base, err, path)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no Grimoire note at %q", path)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Grimoire GET /api/notes/%s: %s — %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	var note grimoireNote
	if err := json.Unmarshal(raw, &note); err != nil {
		return nil, fmt.Errorf("Grimoire returned something unreadable for %q: %w", path, err)
	}
	return &note, nil
}

// createGrimoireNote saves a brief for later sessions to find, and returns
// its vault path. Callers treat a failure here as non-fatal to the tool they
// are part of — saving the brief is a bonus, not a precondition of starting
// or messaging a session.
func createGrimoireNote(title, body string, tags []string) (string, error) {
	base := grimoireBaseURL()
	payload, err := json.Marshal(map[string]any{"title": title, "body": body, "tags": tags})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", base+"/api/notes", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Grimoire-Agent", "lectern")
	resp, err := grimoireClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Grimoire is unreachable at %s (%w) — the brief was not saved as a note", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("Grimoire POST /api/notes: %s — %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var note grimoireNote
	if err := json.Unmarshal(raw, &note); err != nil {
		return "", fmt.Errorf("Grimoire returned something unreadable for the new note: %w", err)
	}
	return note.Path, nil
}
