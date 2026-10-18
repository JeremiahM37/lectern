package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

func TestCompactionThatChangedNothingIsNotShown(t *testing.T) {
	v, _ := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvCompactStart})
	v.Apply(agent.Event{Kind: agent.EvCompactDone, Meta: map[string]any{"messages_before": 7, "messages_after": 7, "tokens_before": 9000, "tokens_after": 9000}})
	if out := plain(v); strings.Contains(out, "Compact") {
		t.Fatalf("a no-op compaction was shown:\n%s", out)
	}
	v.Apply(agent.Event{Kind: agent.EvCompactStart})
	v.Apply(agent.Event{Kind: agent.EvCompactDone, Meta: map[string]any{"messages_before": 7, "messages_after": 7, "tokens_before": 26000, "tokens_after": 18000}})
	if out := plain(v); !strings.Contains(out, "older tool results shortened") {
		t.Fatalf("a compaction that shortened results should say so:\n%s", out)
	}
}
