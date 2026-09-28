package vocab

import "testing"

func TestSessionStateIsTruthful(t *testing.T) {
	cases := []struct {
		name          string
		facts         SessionFacts
		state, reason string
	}{
		// The audit's inversion: an idle prompt is not "needs you"...
		{"idle at prompt", SessionFacts{Status: "waiting", AgentState: "waiting_input"}, StateIdle, ""},
		{"quiet", SessionFacts{Status: "idle"}, StateIdle, ""},
		// ...and a session blocked on an approval is not "working".
		{"blocked on approval", SessionFacts{Status: "running", PendingApproval: true}, StateNeedsYou, ReasonApproval},
		{"permission prompt", SessionFacts{Status: "waiting", AgentState: "waiting_permission"}, StateNeedsYou, ReasonPermission},
		{"busy", SessionFacts{Status: "running", AgentState: "working"}, StateWorking, ""},
		{"starting", SessionFacts{Status: "starting"}, StateWorking, ReasonStarting},
		{"setting up", SessionFacts{SetupState: "creating", PendingApproval: true}, StateWorking, ReasonSettingUp},
		{"setup failed", SessionFacts{SetupState: "failed", Ended: true}, StateEnded, ReasonSetupFailed},
		{"ended with a stale approval", SessionFacts{Status: "dead", Ended: true, PendingApproval: true}, StateEnded, ""},
		{"released", SessionFacts{Status: "running", Ended: true}, StateEnded, ReasonUntracked},
		{"agent exited", SessionFacts{Status: "waiting", AgentExited: true}, StateEnded, ReasonAgentExited},
		{"interrupted", SessionFacts{Status: "interrupted"}, StateEnded, ReasonInterrupted},
		{"archived", SessionFacts{Status: "dead", Ended: true, Archived: true}, StateEnded, ReasonArchived},
	}
	for _, c := range cases {
		state, reason := SessionState(c.facts)
		if state != c.state || reason != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, state, reason, c.state, c.reason)
		}
	}
}

func TestLabels(t *testing.T) {
	for state, want := range map[string]string{StateWorking: "Working", StateNeedsYou: "Needs you", StateIdle: "Idle", StateEnded: "Ended", "odd": "odd"} {
		if got := Label(state); got != want {
			t.Errorf("Label(%q) = %q, want %q", state, got, want)
		}
	}
	if ReasonLabel(ReasonAgentExited) != "agent exited" || ReasonLabel("") != "" {
		t.Error("reason labels")
	}
}
