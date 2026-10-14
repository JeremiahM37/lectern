package store

import (
	"path/filepath"
	"testing"
)

// The witness is stored as text beside the approval and read back on every
// approval query; a stale status is a status like any other.
func TestApprovalWitnessRoundTrips(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&Project{Name: "p", TargetID: target.ID, RepoPath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&Task{ProjectID: project.ID, Title: "t", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&Attempt{TaskID: task.ID, N: 1, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertApproval(att.ID, "Bash", `{"command":"git push"}`)
	if err != nil {
		t.Fatal(err)
	}
	const witness = `{"branch":"main","head":"abc","upstream":"","diff_hash":"d"}`
	if err := db.Update("approvals", id, map[string]any{"witness_json": witness}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update("approvals", id, map[string]any{"status": "stale"}); err != nil {
		t.Fatal(err)
	}
	ap, err := db.Approval(id)
	if err != nil {
		t.Fatal(err)
	}
	if ap.WitnessJSON != witness || ap.Status != "stale" {
		t.Fatalf("witness/status not read back: %q %q", ap.WitnessJSON, ap.Status)
	}
	stale, err := db.ApprovalsByStatus("stale")
	if err != nil || len(stale) != 1 || stale[0].WitnessJSON != witness {
		t.Fatalf("listing by status: %v %v", stale, err)
	}
}
