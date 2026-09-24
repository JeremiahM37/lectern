package sessions

// docs/agent-events.md section 2's fallback rule: the screen keeps deriving
// `status` unconditionally, exactly as before, but is only allowed to write
// the newer `agent_state` when no hook has reached the session in the last
// 10 minutes (or ever). These tests exercise applyPane directly against a
// fake target-free rig (poll_failure_test.go's pollRig), the same way the
// existing setup/poll tests in this package do.

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// busyPane reliably derives StatusRunning: "esc to interrupt" is one of
// DeriveStatus's busyMarkers and wins over every other signal.
const busyPane = "Working on it...\nesc to interrupt\n"

func TestApplyPaneWritesAgentStateWhenNoHookHasEverArrived(t *testing.T) {
	m, row := pollRig(t)
	if row.AgentState != "" || row.HookSeenAt != nil {
		t.Fatalf("fixture precondition: %+v", row)
	}
	m.applyPane(row, busyPane, false)
	got, err := m.DB.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentState != agentevents.StateWorking || got.StateSource != agentevents.SourceScreen {
		t.Fatalf("agent_state=%q state_source=%q, want %q/%q", got.AgentState, got.StateSource, agentevents.StateWorking, agentevents.SourceScreen)
	}
}

func TestApplyPaneDoesNotOverwriteAgentStateWhileAHookIsFresh(t *testing.T) {
	m, row := pollRig(t)
	now := store.Now()
	if err := m.DB.Update("sessions", row.ID, map[string]any{
		"agent_state": agentevents.StateIdle, "state_source": agentevents.SourceHook, "state_at": now, "hook_seen_at": now,
	}); err != nil {
		t.Fatal(err)
	}
	row, _ = m.DB.Session(row.ID)

	// The pane says "working" — screen scraping would normally call that
	// StateWorking — but a hook set idle a moment ago, so the screen must
	// leave it alone.
	m.applyPane(row, busyPane, false)
	got, err := m.DB.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentState != agentevents.StateIdle || got.StateSource != agentevents.SourceHook {
		t.Fatalf("screen overwrote a fresh hook state: agent_state=%q state_source=%q", got.AgentState, got.StateSource)
	}
	// status itself must keep being derived from the pane regardless — the
	// contract's "keep deriving status" requirement.
	if got.Status != StatusRunning {
		t.Fatalf("status must still be screen-derived: %q", got.Status)
	}
}

func TestApplyPaneResumesWritingAgentStateOnceAHookGoesStale(t *testing.T) {
	m, row := pollRig(t)
	stale := store.Now() - (agentStateFallbackWindow + 60) // just past the 10-minute window
	if err := m.DB.Update("sessions", row.ID, map[string]any{
		"agent_state": agentevents.StateIdle, "state_source": agentevents.SourceHook, "state_at": stale, "hook_seen_at": stale,
	}); err != nil {
		t.Fatal(err)
	}
	row, _ = m.DB.Session(row.ID)

	m.applyPane(row, busyPane, false)
	got, err := m.DB.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentState != agentevents.StateWorking || got.StateSource != agentevents.SourceScreen {
		t.Fatalf("screen should have reclaimed a stale hook state: agent_state=%q state_source=%q", got.AgentState, got.StateSource)
	}
}

func TestApplyPaneMissingPaneMapsToEndedWhenScreenEligible(t *testing.T) {
	m, row := pollRig(t)
	// Give the row a real age and a settled prior status so the missing-pane
	// branch does not take its own "brand new session" grace-period return.
	if err := m.DB.Update("sessions", row.ID, map[string]any{"created_at": store.Now() - 120, "status": StatusRunning}); err != nil {
		t.Fatal(err)
	}
	row, _ = m.DB.Session(row.ID)
	m.applyPane(row, "", true)
	got, err := m.DB.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDead {
		t.Fatalf("status = %q, want dead", got.Status)
	}
	if got.AgentState != agentevents.StateEnded || got.StateSource != agentevents.SourceScreen {
		t.Fatalf("agent_state=%q state_source=%q, want ended/screen", got.AgentState, got.StateSource)
	}
}
