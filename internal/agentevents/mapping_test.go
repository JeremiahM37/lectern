package agentevents

import "testing"

func TestMapEventState(t *testing.T) {
	cases := []struct {
		event, notification string
		want                string
		ok                  bool
	}{
		{EventUserPromptSubmit, "", StateWorking, true},
		{EventPreToolUse, "", StateWorking, true},
		{EventPostToolUse, "", StateWorking, true},
		{EventNotification, NotificationPermissionPrompt, StateWaitingPermission, true},
		{EventNotification, NotificationIdlePrompt, StateWaitingInput, true},
		{EventNotification, "something_else", "", false},
		{EventPermissionRequest, "", StateWaitingPermission, true},
		{EventStop, "", StateIdle, true},
		{EventStopFailure, "", StateError, true},
		{EventSessionEnd, "", StateEnded, true},
		{EventAgentTurnComplete, "", StateIdle, true},
		// Explicitly not mapped per docs/agent-events.md section 2 — accepted
		// and ignored, hook_seen_at only.
		{EventSessionStart, "", "", false},
		{EventPreCompact, "", "", false},
		{EventSubagentStart, "", "", false},
		{EventSubagentStop, "", "", false},
		{EventInterrupt, "", "", false},
		{"SomeFutureEvent", "", "", false},
	}
	for _, c := range cases {
		got, ok := MapEventState(c.event, c.notification)
		if got != c.want || ok != c.ok {
			t.Errorf("MapEventState(%q,%q) = (%q,%v), want (%q,%v)", c.event, c.notification, got, ok, c.want, c.ok)
		}
	}
}

func TestStatusForState(t *testing.T) {
	cases := []struct {
		state string
		want  string
		ok    bool
	}{
		{StateWorking, "running", true},
		{StateWaitingInput, "waiting", true},
		{StateWaitingPermission, "waiting", true},
		{StateIdle, "idle", true},
		{StateError, "idle", true},
		{StateEnded, "idle", true},
		{"unknown", "", false},
	}
	for _, c := range cases {
		got, ok := StatusForState(c.state)
		if got != c.want || ok != c.ok {
			t.Errorf("StatusForState(%q) = (%q,%v), want (%q,%v)", c.state, got, ok, c.want, c.ok)
		}
	}
}
