package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Retry and error rows carry the provider's HTTP status, as "[429]".
func TestRetryAndErrorRowsShowHTTPStatus(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvRetry, Category: llm.CatRateLimited, Attempt: 1, MaxAttempts: 3,
		Status: 429, Text: "slow down", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvError, Category: llm.CatOverloaded, Status: 529, Text: "overloaded", At: at(c)})
	out := plain(v)
	if !strings.Contains(out, "↻ Retry 1/3 [429] — Rate Limited") {
		t.Errorf("retry row should show its status code:\n%s", out)
	}
	if !strings.Contains(out, "✗ Overloaded [529] — The provider is overloaded") {
		t.Errorf("error row should show its status code:\n%s", out)
	}
}

// Without a status the rows read exactly as before.
func TestRowsWithoutStatusAreUnchanged(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvError, Category: llm.CatOverloaded, Text: "overloaded", At: at(c)})
	if out := plain(v); strings.Contains(out, "[") && strings.Contains(out, "Overloaded [") {
		t.Errorf("no status should add no code:\n%s", out)
	}
}
