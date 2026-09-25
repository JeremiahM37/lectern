package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Reviewer is a memory provider that accepts an operator's verdict on one
// delivered item. "That was useful" and "that is wrong" are different claims —
// the first tunes ranking, the second asks for review — so they are two calls
// rather than one with a flag.
//
// Both are irreversible from lectern's side: they change the operator's own
// memory store, which is why the API requires a human principal for them
// (docs/memory-visibility.md).
type Reviewer interface {
	// Feedback records whether one item helped. helpful=false is "not
	// relevant", not "wrong".
	Feedback(ctx context.Context, itemID string, helpful bool, note string) error
	// Challenge marks one item as possibly wrong, for review. A store that
	// supports this keeps the claim and the objection side by side rather than
	// silently deleting either.
	Challenge(ctx context.Context, itemID, reason string) error
}

// Feedback posts an operator's verdict on one item.
func (g *Grimoire) Feedback(ctx context.Context, itemID string, helpful bool, note string) error {
	body, err := json.Marshal(map[string]any{"id": itemID, "helpful": helpful, "note": note})
	if err != nil {
		return err
	}
	return g.review(ctx, "/api/memory/feedback", "feedback", body)
}

// Challenge asks the store to review one item as possibly wrong.
func (g *Grimoire) Challenge(ctx context.Context, itemID, reason string) error {
	body, err := json.Marshal(map[string]any{"id": itemID, "reason": reason})
	if err != nil {
		return err
	}
	return g.review(ctx, "/api/memory/challenge", "challenge", body)
}

// review posts one reviewer call and turns any refusal into a message the
// operator can act on. The endpoint's body cannot be assumed — an older or
// newer Grimoire may answer differently — so whatever it says is surfaced
// rather than swallowed into a bare status code.
func (g *Grimoire) review(ctx context.Context, path, what string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", g.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	g.auth(req)
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("grimoire %s: %w", what, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		detail := strings.TrimSpace(string(raw))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("grimoire %s: %s", what, detail)
	}
	return nil
}
