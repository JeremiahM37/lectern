// Package vocab holds the words Lectern shows people, so the web app, the
// phone, the terminal client and the CLI say the same thing
// (docs/design/simple-ui.md "Vocabulary").
//
// The session status is the one that matters most: four states, computed here
// from the facts the server already records. "Needs you" is reserved for a
// session that is actually blocked on a person — a pending approval or a
// permission prompt — and never used for an agent sitting idle at its prompt.
package vocab

// Session states. The API sends the key as `state` and the English words as
// `state_label`.
const (
	StateWorking  = "working"
	StateNeedsYou = "needs_you"
	StateIdle     = "idle"
	StateEnded    = "ended"
)

// Reasons refine a state; the web app shows them after the state's label
// ("Ended · agent exited"). Empty when the state says it all.
const (
	ReasonStarting    = "starting"
	ReasonSettingUp   = "setting_up"
	ReasonSetupFailed = "setup_failed"
	ReasonAgentExited = "agent_exited"
	ReasonInterrupted = "interrupted"
	ReasonArchived    = "archived"
	ReasonUntracked   = "untracked"
	ReasonApproval    = "approval"
	ReasonPermission  = "permission_prompt"
)

var labels = map[string]string{
	StateWorking:  "Working",
	StateNeedsYou: "Needs you",
	StateIdle:     "Idle",
	StateEnded:    "Ended",
}

var reasonLabels = map[string]string{
	ReasonStarting:    "starting",
	ReasonSettingUp:   "setting up",
	ReasonSetupFailed: "setup failed",
	ReasonAgentExited: "agent exited",
	ReasonInterrupted: "interrupted",
	ReasonArchived:    "archived",
	ReasonUntracked:   "no longer tracked",
	ReasonApproval:    "approval waiting",
	ReasonPermission:  "permission prompt",
}

// Label is a state's English words, or the key itself for an unknown state.
func Label(state string) string {
	if label, ok := labels[state]; ok {
		return label
	}
	return state
}

// ReasonLabel is a reason's English words ("" for no reason).
func ReasonLabel(reason string) string {
	if label, ok := reasonLabels[reason]; ok {
		return label
	}
	return reason
}

// SessionFacts is what SessionState needs to know about one session. The
// fields mirror columns of the sessions table: Status is the screen-derived
// status (starting|running|waiting|idle|dead|interrupted), AgentState the
// hook-driven one (working|waiting_input|waiting_permission|idle|error|ended).
type SessionFacts struct {
	Status          string
	AgentState      string
	SetupState      string
	Ended           bool
	Archived        bool
	AgentExited     bool
	PendingApproval bool
}

// SessionState maps a session's facts onto the four states, most final
// first: an ended session never "needs you", however stale its approvals.
func SessionState(f SessionFacts) (state, reason string) {
	switch {
	case f.Archived:
		return StateEnded, ReasonArchived
	case f.SetupState == "failed":
		return StateEnded, ReasonSetupFailed
	case f.Ended && f.Status != "dead":
		// A released adopted session: its process runs on, untracked.
		return StateEnded, ReasonUntracked
	case f.Ended || f.Status == "dead":
		return StateEnded, ""
	case f.Status == "interrupted":
		return StateEnded, ReasonInterrupted
	case f.AgentExited:
		return StateEnded, ReasonAgentExited
	case f.SetupState == "creating":
		return StateWorking, ReasonSettingUp
	case f.PendingApproval:
		return StateNeedsYou, ReasonApproval
	case f.AgentState == "waiting_permission":
		return StateNeedsYou, ReasonPermission
	case f.Status == "starting":
		return StateWorking, ReasonStarting
	case f.Status == "running":
		return StateWorking, ""
	}
	return StateIdle, ""
}
