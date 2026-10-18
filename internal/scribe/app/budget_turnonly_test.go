package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The agent reports the session's running total; the budget must count only
// what each turn added, or every earlier turn is counted again.
func TestTurnOnlyCountsWhatTheTurnAdded(t *testing.T) {
	before := llm.Usage{InputTokens: 30000, OutputTokens: 500, CacheWriteTokens: 100}
	stats := agent.TurnStats{Tokens: llm.Usage{InputTokens: 55000, OutputTokens: 800, CacheWriteTokens: 100}}
	got := turnTokens(turnOnly(stats, before))
	if got != 25000+300 {
		t.Fatalf("turn tokens = %d, want %d", got, 25300)
	}
	// Three turns of a growing session sum to the session total, not more.
	var sum int
	prev := llm.Usage{}
	for _, total := range []int{10000, 30000, 60000} {
		s := agent.TurnStats{Tokens: llm.Usage{InputTokens: total}}
		sum += turnTokens(turnOnly(s, prev))
		prev = s.Tokens
	}
	if sum != 60000 {
		t.Fatalf("three turns counted %d, want the session total 60000", sum)
	}
	// A total below the start (a reset session) counts the turn whole.
	if got := turnTokens(turnOnly(agent.TurnStats{Tokens: llm.Usage{InputTokens: 500}}, llm.Usage{InputTokens: 9000})); got != 500 {
		t.Fatalf("reset session counted %d, want 500", got)
	}
}
