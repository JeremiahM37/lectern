package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// oldApprovalsSchema is exactly today's shape (attempt_id NOT NULL, no
// session_id) — what a database created before docs/agent-events.md section
// 3 actually has on disk. TestMigrateApprovalsSessionColumn asserts the
// rebuild in migrate_approvals.go brings one of these up to date without
// losing the row, which earlySchema in store_test.go cannot exercise: that
// fixture predates the approvals table entirely, so Open's own
// CREATE TABLE IF NOT EXISTS already produces the new shape for it.
const oldApprovalsSchema = `
CREATE TABLE tasks(id INTEGER PRIMARY KEY, project_id INTEGER, title TEXT, status TEXT DEFAULT 'backlog', created_at REAL, updated_at REAL);
CREATE TABLE attempts(id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL REFERENCES tasks(id), n INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'queued');
-- sessions is deliberately NOT created here: Open's own
-- CREATE TABLE IF NOT EXISTS builds the real, current sessions table (this
-- fixture only needs to reproduce the approvals table's old shape), and the
-- rebuilt approvals.session_id FK needs a real sessions(id) to reference.
CREATE TABLE approvals(
  id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES attempts(id),
  tool_name TEXT NOT NULL, input_json TEXT DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'pending',
  decided_by TEXT DEFAULT '', note TEXT DEFAULT '',
  created_at REAL, decided_at REAL
);
CREATE INDEX idx_approvals_status ON approvals(status);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT);
`

func TestMigrateApprovalsSessionColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-approvals.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(oldApprovalsSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO tasks(id,title,created_at,updated_at) VALUES(1,'t',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO attempts(id,task_id,n,status) VALUES(1,1,1,'running')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO approvals(id,attempt_id,tool_name,input_json,status,created_at)
		VALUES(1,1,'Bash','{"command":"ls"}','pending',5)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("an existing pre-session-approvals database must open: %v", err)
	}
	defer db.Close()

	ap, err := db.Approval(1)
	if err != nil {
		t.Fatalf("old approval lost: %v", err)
	}
	if ap.AttemptID != 1 || ap.ToolName != "Bash" || ap.Status != "pending" || ap.SessionID != 0 {
		t.Fatalf("old approval data changed: %+v", ap)
	}

	// The whole point of the rebuild: a session can now own an approval.
	target, err := db.InsertTarget(&Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&Session{TargetID: target.ID, Name: "s", Agent: "claude", Workdir: "/x", TmuxSession: "lec-1"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertSessionApproval(sess.ID, "Edit", `{"file_path":"a.go"}`)
	if err != nil {
		t.Fatalf("InsertSessionApproval after migration: %v", err)
	}
	sap, err := db.Approval(id)
	if err != nil {
		t.Fatal(err)
	}
	if sap.SessionID != sess.ID || sap.AttemptID != 0 || sap.ToolName != "Edit" {
		t.Fatalf("session approval wrong shape: %+v", sap)
	}

	// Idempotent: reopening must not fail or re-rebuild a table that is
	// already in the new shape.
	db.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	defer db2.Close()
	again, err := db2.Approval(1)
	if err != nil || again.ToolName != "Bash" {
		t.Fatalf("row lost on reopen: %+v %v", again, err)
	}
}
