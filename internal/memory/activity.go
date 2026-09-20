package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Change is one thing a session's agent did to the memory store.
type Change struct {
	Kind         string `json:"kind"` // learned | changed | retracted | expired
	At           string `json:"at"`
	Text         string `json:"text"`
	Path         string `json:"path"`
	Agent        string `json:"agent"`
	Topic        string `json:"topic"`
	ReplacedText string `json:"replaced_text,omitempty"`
}

// Activity is what one session wrote, as the store itself recorded it.
type Activity struct {
	Session string         `json:"session"`
	Since   string         `json:"since"`
	Counts  map[string]int `json:"counts"`
	Changes []Change       `json:"changes"`
}

// ActivityProvider is a memory provider that can say what a given run wrote.
// It is what turns "the agent has a memory tool" into something the control
// plane can actually see: without it AgentDeck knew what it wrote itself at a
// handoff and nothing of what the agent wrote on its own.
type ActivityProvider interface {
	SessionActivity(ctx context.Context, session string, since time.Time) (Activity, error)
}

// SessionActivity asks Grimoire for the writes stamped with this session key.
// The key reaches those writes through the agent's environment, so an agent
// launched before that existed, or one whose MCP server was not handed the
// variable, simply has nothing recorded under it.
func (g *Grimoire) SessionActivity(ctx context.Context, session string, since time.Time) (Activity, error) {
	out := Activity{Session: session, Counts: map[string]int{}, Changes: []Change{}}
	if session == "" {
		return out, fmt.Errorf("no session key")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	q := url.Values{"session": {session}, "since": {since.UTC().Format(time.RFC3339)}, "limit": {"100"}}
	req, err := http.NewRequestWithContext(ctx, "GET", g.BaseURL+"/api/memory/changes?"+q.Encode(), nil)
	if err != nil {
		return out, err
	}
	g.auth(req)
	resp, err := g.Client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("grimoire memory changes: %s", resp.Status)
	}
	var body Activity
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return out, err
	}
	out.Since = body.Since
	if body.Counts != nil {
		out.Counts = body.Counts
	}
	if body.Changes != nil {
		out.Changes = body.Changes
	}
	return out, nil
}
