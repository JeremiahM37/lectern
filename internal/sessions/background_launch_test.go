package sessions

import (
	"context"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestSetupPollDistinguishesActiveCompletedAndInterrupted(t *testing.T) {
	m, row := pollRig(t)
	m.DB.Update("sessions", row.ID, map[string]any{"setup_state": "creating", "created_at": store.Now() - 120})
	row, _ = m.DB.Session(row.ID)
	m.activeSetups = map[int64]bool{row.ID: true}
	m.applyPane(row, "", true)
	current, _ := m.DB.Session(row.ID)
	if current.EndedAt != nil {
		t.Fatal("live setup was ended by the missing-terminal poll")
	}
	delete(m.activeSetups, row.ID)
	m.DB.Update("sessions", row.ID, map[string]any{"setup_state": "ready"})
	m.applyPane(row, "", true)
	current, _ = m.DB.Session(row.ID)
	if current.EndedAt != nil || current.SetupState != "ready" {
		t.Fatal("stale poll overwrote successful setup")
	}
	m.DB.Update("sessions", row.ID, map[string]any{"setup_state": "creating"})
	m.applyPane(row, "", true)
	current, _ = m.DB.Session(row.ID)
	if current.EndedAt == nil || current.SetupState != "failed" || current.SetupError == "" {
		t.Fatal("interrupted setup was left running")
	}
}

func TestInterruptedSetupDoesNotRequireTargetConnectivity(t *testing.T) {
	m, row := pollRig(t)
	m.DB.Update("sessions", row.ID, map[string]any{"setup_state": "creating"})
	m.Reg = nil // No target executor may be needed to identify controller loss.
	m.Poll(context.Background())
	current, err := m.DB.Session(row.ID)
	if err != nil || current.SetupState != "failed" || current.EndedAt == nil {
		t.Fatalf("interrupted setup required target connectivity: %v", err)
	}
}
