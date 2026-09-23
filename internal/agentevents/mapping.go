// Package agentevents ingests the push-based lifecycle and usage signals an
// agent reports about itself — Claude Code's settings-driven hooks and
// statusline, and (where 0.155 supports it) Codex's hooks.json/notify — and
// turns them into session state, per contract in docs/agent-events.md
// section 2. It never launches or scrapes anything; that stays in
// internal/sessions. The two meet only at the session row and the bus.
package agentevents

// Session lifecycle states, distinct from the older screen-derived
// sessions.Status enum (starting|running|waiting|idle|dead). A session can be
// hook-driven (state_source="hook") or, absent hooks or after a 10-minute
// silence, screen-derived (state_source="screen") — see poll.go's applyPane.
const (
	StateWorking           = "working"
	StateWaitingInput      = "waiting_input"
	StateWaitingPermission = "waiting_permission"
	StateIdle              = "idle"
	StateError             = "error"
	StateEnded             = "ended"
)

// SourceHook and SourceScreen are the values stored in sessions.state_source.
const (
	SourceHook   = "hook"
	SourceScreen = "screen"
)

// Claude/Codex hook event names, exactly as they arrive in hook_event_name
// (both CLIs use the same vocabulary for the events they share — verified
// against the codex 0.155.1 binary's embedded hook JSON schemas, which
// reuse Claude Code's wire format byte-for-byte down to field names).
const (
	EventSessionStart      = "SessionStart"
	EventUserPromptSubmit  = "UserPromptSubmit"
	EventPreToolUse        = "PreToolUse"
	EventPostToolUse       = "PostToolUse"
	EventNotification      = "Notification" // Claude only; codex has no equivalent hook
	EventStop              = "Stop"
	EventStopFailure       = "StopFailure" // Claude only
	EventPreCompact        = "PreCompact"
	EventSessionEnd        = "SessionEnd"
	EventPermissionRequest = "PermissionRequest"
	EventSubagentStart     = "SubagentStart" // codex extension
	EventSubagentStop      = "SubagentStop"  // codex extension
	EventInterrupt         = "Interrupt"     // codex extension
	// EventAgentTurnComplete is not a hooks.json event: it is synthesized by
	// the small notify script codex's `-c notify=[...]` invokes on
	// agent-turn-complete (see settings.go), which lectern maps to the same
	// event name so it flows through the one ingest path below.
	EventAgentTurnComplete = "AgentTurnComplete"
)

// Notification.notification_type values (Claude only).
const (
	NotificationPermissionPrompt = "permission_prompt"
	NotificationIdlePrompt       = "idle_prompt"
)

// MapEventState is the mapping table from docs/agent-events.md section 2. ok
// is false for an event that is accepted but does not itself imply a state
// change (SessionStart, PreCompact, SubagentStart/Stop, Interrupt, and a
// Notification whose notification_type is neither of the two known values) —
// the caller still records that a hook reached the session (hook_seen_at)
// without touching agent_state.
func MapEventState(event, notificationType string) (state string, ok bool) {
	switch event {
	case EventUserPromptSubmit, EventPreToolUse, EventPostToolUse:
		return StateWorking, true
	case EventNotification:
		switch notificationType {
		case NotificationPermissionPrompt:
			return StateWaitingPermission, true
		case NotificationIdlePrompt:
			return StateWaitingInput, true
		}
		return "", false
	case EventPermissionRequest:
		// Codex has no Notification event; PermissionRequest is both the
		// approval hold point (section 3, extension point in ingest.go) and
		// the only signal that the session is blocked on a permission — so it
		// carries the same state a Claude Notification(permission_prompt)
		// would.
		return StateWaitingPermission, true
	case EventStop:
		return StateIdle, true
	case EventStopFailure:
		return StateError, true
	case EventSessionEnd:
		return StateEnded, true
	case EventAgentTurnComplete:
		return StateIdle, true
	default:
		return "", false
	}
}

// StatusForState maps the new hook-driven agent_state onto the pre-existing
// sessions.status enum (sessions.StatusRunning etc., defined in package
// sessions) so surfaces that only know the old column keep working the
// moment a hook fires, without waiting for the next screen poll. It returns
// ok=false for a state with no natural old-status equivalent, in which case
// the caller leaves status untouched.
//
// This never runs from the screen-scrape path — applyPane keeps deriving
// status the way it always has (docs/agent-events.md section 2). It only
// gives hook-driven updates a head start; the next poll tick reconciles
// status from the pane exactly as before, so there is no way for the two to
// permanently disagree.
func StatusForState(state string) (status string, ok bool) {
	switch state {
	case StateWorking:
		return "running", true
	case StateWaitingInput, StateWaitingPermission:
		return "waiting", true
	case StateIdle, StateError, StateEnded:
		return "idle", true
	default:
		return "", false
	}
}
