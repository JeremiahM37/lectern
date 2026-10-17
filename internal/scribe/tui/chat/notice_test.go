package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

func TestNoticeEventIsShown(t *testing.T) {
	v, _ := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvNotice, Text: "Plan approved: switched to Default mode to implement it."})
	if out := plain(v); !strings.Contains(out, "switched to Default mode") {
		t.Fatalf("notice not rendered:\n%s", out)
	}
}
