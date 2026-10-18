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

func TestClearedPlanHandoverIsNotShownAsTheUsersMessage(t *testing.T) {
	v, _ := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: agent.ClearedPlanPrefix + "Implement the plan now. SECRET-PLAN-BODY"})
	if out := plain(v); strings.Contains(out, "SECRET-PLAN-BODY") {
		t.Fatalf("handover shown as a user message:\n%s", out)
	}
}

func TestCommandErrorIsTitledCommandFailed(t *testing.T) {
	v, _ := newView(t, Options{})
	v.AddSystem("error", "Command error: /model: anthropic: no API key")
	v.AddSystem("info", "next") // the newest record types in; this one is settled
	out := plain(v)
	if !strings.Contains(out, "Command failed") || strings.Contains(out, "Something went wrong") || !strings.Contains(out, "no API key") {
		t.Fatalf("command error rendering:\n%s", out)
	}
}
