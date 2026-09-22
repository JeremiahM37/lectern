package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestPromoteNewProjectAndBindIsAtomicAndBootSafe(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "promotion", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&Session{TargetID: target.ID, Name: "shell", Agent: "shell", Workdir: "/old", TmuxSession: "lec-shell", BootID: "", TrackingIdentity: "track"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", sess.ID, map[string]any{"tracking_identity": "track"}); err != nil {
		t.Fatal(err)
	}
	sess, _ = db.Session(sess.ID)
	p, err := db.PromoteNewProjectAndBind(context.Background(), &Project{Name: "p", TargetID: target.ID, RepoPath: "/new", DefaultAgent: "claude"}, sess.ID, "lec-shell", sess.BootID, "boot-new", "track", "track-new", "cid", "cfg")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := db.Session(sess.ID)
	if row.ProjectID == nil || *row.ProjectID != p.ID || row.BootID != "boot-new" {
		t.Fatalf("binding=%+v", row)
	}
	if _, err := db.PromoteNewProjectAndBind(context.Background(), &Project{Name: "stale", TargetID: target.ID, RepoPath: "/x", DefaultAgent: "codex"}, sess.ID, "lec-shell", "boot-new", "other", "track-new", "track-new2", "cid2", "cfg"); err == nil {
		t.Fatal("stale promotion succeeded")
	}
	projects, _ := db.Projects()
	for _, item := range projects {
		if item.Name == "stale" {
			t.Fatal("stale project was committed")
		}
	}
}

// earlySchema is the shape the Python service created, before sessions, wraps,
// capability profiles, command prefixes and the rest. Reproduced here rather
// than fixtured from a real file so the test states exactly which columns an old
// database is missing.
const earlySchema = `
CREATE TABLE targets(
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  kind TEXT NOT NULL DEFAULT 'ssh',
  host TEXT DEFAULT '', port INTEGER DEFAULT 22, user TEXT DEFAULT 'root',
  key_path TEXT DEFAULT '', workroot TEXT DEFAULT '',
  max_concurrent INTEGER DEFAULT 4, sandbox INTEGER DEFAULT 0,
  status TEXT DEFAULT 'unknown', info_json TEXT DEFAULT '{}',
  created_at REAL);
CREATE TABLE projects(
  id INTEGER PRIMARY KEY, name TEXT NOT NULL,
  target_id INTEGER NOT NULL REFERENCES targets(id),
  repo_path TEXT NOT NULL, default_base_branch TEXT DEFAULT 'main',
  workroot_override TEXT DEFAULT '', policy_json TEXT DEFAULT '{}',
  created_at REAL);
CREATE TABLE tasks(
  id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id),
  title TEXT NOT NULL, prompt TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'backlog',
  priority INTEGER DEFAULT 2, labels_json TEXT DEFAULT '[]',
  agent TEXT DEFAULT 'claude', model TEXT DEFAULT '',
  permission_mode TEXT DEFAULT 'acceptEdits', base_branch TEXT DEFAULT '',
  created_at REAL, updated_at REAL);
CREATE TABLE attempts(
  id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL REFERENCES tasks(id),
  n INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'queued',
  token TEXT NOT NULL DEFAULT '',
  prompt TEXT DEFAULT '', resume_session TEXT DEFAULT '',
  worktree_path TEXT DEFAULT '', branch TEXT DEFAULT '', tmux_session TEXT DEFAULT '',
  session_id TEXT DEFAULT '', log_offset INTEGER DEFAULT 0,
  started_at REAL, finished_at REAL, exit_code INTEGER,
  result_json TEXT DEFAULT '{}', diff_stat_json TEXT DEFAULT '{}');
CREATE TABLE events(
  id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES attempts(id),
  seq INTEGER NOT NULL, ts REAL, type TEXT NOT NULL, payload_json TEXT DEFAULT '{}');
CREATE TABLE approvals(
  id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES attempts(id),
  tool_name TEXT NOT NULL, input_json TEXT DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'pending',
  decided_by TEXT DEFAULT '', note TEXT DEFAULT '',
  created_at REAL, decided_at REAL);
CREATE TABLE push_subscriptions(
  id INTEGER PRIMARY KEY, endpoint TEXT UNIQUE NOT NULL, keys_json TEXT NOT NULL,
  created_at REAL);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT);
`

// The cutover claim was that an existing database opens unchanged. That is
// load-bearing — a wrong migration on first boot loses a board — so it is worth
// asserting against a real old schema rather than trusting the ALTER list.
func TestOpenMigratesAPreSessionsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(earlySchema); err != nil {
		t.Fatal(err)
	}
	// data the operator would not want to lose
	if _, err := raw.Exec(`INSERT INTO targets(id,name,kind,host,created_at)
		VALUES(1,'aiserver','local','',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO projects(id,name,target_id,repo_path,created_at)
		VALUES(1,'librarr',1,'/srv/librarr',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO tasks(id,project_id,title,status,created_at,updated_at)
		VALUES(1,1,'an old task','done',1,1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("an existing database must open: %v", err)
	}
	defer db.Close()

	// every row survived
	tgt, err := db.Target(1)
	if err != nil || tgt.Name != "aiserver" {
		t.Fatalf("target lost: %+v %v", tgt, err)
	}
	proj, err := db.Project(1)
	if err != nil || proj.RepoPath != "/srv/librarr" {
		t.Fatalf("project lost: %+v %v", proj, err)
	}
	task, err := db.Task(1)
	if err != nil || task.Title != "an old task" {
		t.Fatalf("task lost: %+v %v", task, err)
	}

	// columns added since arrive with usable defaults, not NULLs that break a scan
	if proj.CapabilityProfile != "restricted" {
		t.Errorf("capability_profile default: %q", proj.CapabilityProfile)
	}
	if proj.MCPJSON != "{}" || proj.ContextJSON != "[]" {
		t.Errorf("json column defaults: mcp=%q context=%q", proj.MCPJSON, proj.ContextJSON)
	}
	if tgt.ContextJSON != "[]" || tgt.CommandPrefix != "" {
		t.Errorf("target defaults: context=%q prefix=%q", tgt.ContextJSON, tgt.CommandPrefix)
	}

	// and the tables that did not exist at all are usable
	if _, err := db.Sessions(false); err != nil {
		t.Errorf("sessions table missing after migration: %v", err)
	}
	if _, err := db.Wraps(1, 5); err != nil {
		t.Errorf("session_wraps table missing after migration: %v", err)
	}
}

// Opening the same database twice must be a no-op, not a second round of ALTERs
// that errors out. This runs on every service start.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	for i := 0; i < 3; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if _, err := db.InsertTarget(&Target{Name: "t", Kind: "local"}); err != nil && i == 0 {
			t.Fatalf("insert: %v", err)
		}
		db.Close()
	}
}

// diff_stat_json is '{}' until a diff is captured, and a running card that
// assumed a list once threw and aborted the whole column render.
func TestUnjListCoercesAnObjectToAnEmptyList(t *testing.T) {
	if got := UnjList("{}"); got == nil || len(got) != 0 {
		t.Fatalf("an object must coerce to an empty list, got %#v", got)
	}
	if got := UnjList(""); got == nil || len(got) != 0 {
		t.Fatalf("empty must coerce to an empty list, got %#v", got)
	}
	if got := UnjList("not json"); got == nil || len(got) != 0 {
		t.Fatalf("garbage must coerce to an empty list, got %#v", got)
	}
	if got := UnjList(`[{"path":"a.py"}]`); len(got) != 1 {
		t.Fatalf("a real list must survive: %#v", got)
	}
}

func TestUnjObjNeverReturnsNil(t *testing.T) {
	for _, in := range []string{"", "[]", "garbage", "null"} {
		if got := UnjObj(in); got == nil {
			t.Errorf("UnjObj(%q) returned nil — every caller indexes it", in)
		}
	}
	if got := UnjObj(`{"cost_usd":0.5}`); got["cost_usd"] != 0.5 {
		t.Errorf("a real object must survive: %#v", got)
	}
}

func TestUpdateWritesOnlyTheNamedColumns(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tgt, _ := db.InsertTarget(&Target{Name: "t", Kind: "ssh", Host: "h", User: "root"})
	if err := db.Update("targets", tgt.ID, map[string]any{"host": "h2"}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := db.Target(tgt.ID)
	if fresh.Host != "h2" || fresh.User != "root" || fresh.Name != "t" {
		t.Fatalf("update touched more than it was asked to: %+v", fresh)
	}
	// an empty update is a no-op rather than invalid SQL
	if err := db.Update("targets", tgt.ID, map[string]any{}); err != nil {
		t.Fatalf("empty update: %v", err)
	}
}

func TestErrNotFoundRatherThanSQLNoRows(t *testing.T) {
	db, _ := Open(filepath.Join(t.TempDir(), "nf.db"))
	defer db.Close()
	for name, err := range map[string]error{
		"target":  second(db.Target(999)),
		"project": second(db.Project(999)),
		"task":    second(db.Task(999)),
		"session": second(db.Session(999)),
	} {
		if err != ErrNotFound {
			t.Errorf("%s: got %v, want ErrNotFound (callers branch on it)", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestWorktreeMigrationPreservesExistingSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions-before-worktrees.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "existing", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.InsertSession(&Session{TargetID: target.ID, Name: "Keep running", Agent: "codex", Workdir: "/saved/workspace", TmuxSession: "keep-this"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE sessions DROP COLUMN worktree_json"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Session(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != session.Name || got.TmuxSession != session.TmuxSession || got.Workdir != session.Workdir || got.WorktreeJSON != "" {
		t.Fatalf("existing session changed: %+v", got)
	}
}

func TestGroupMigrationPreservesWorktreeMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "before-groups.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := db.InsertTarget(&Target{Name: "existing", Kind: "local"})
	session, err := db.InsertSession(&Session{TargetID: target.ID, Name: "Existing worktree", Workdir: "/saved/worktree", TmuxSession: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := `{"repo":"/saved/repo","path":"/saved/worktree","token":"preserve"}`
	if err := db.Update("sessions", session.ID, map[string]any{"worktree_json": metadata}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE sessions DROP COLUMN group_path"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Session(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupPath != "" || got.WorktreeJSON != metadata || got.TmuxSession != "keep" {
		t.Fatalf("migration changed existing session: %+v", got)
	}
}

func TestNativeResumeIdentitySurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "resume", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := db.InsertSession(&Session{TargetID: target.ID, Name: "resumed", ResumeID: "11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Session(s.ID)
	if err != nil || got.ResumeID != s.ResumeID {
		t.Fatalf("resume identity lost: %+v %v", got, err)
	}
	rows, err := db.Sessions(true)
	if err != nil || len(rows) != 1 || rows[0].ResumeID != s.ResumeID {
		t.Fatalf("list identity lost: %+v %v", rows, err)
	}
}

func TestArchiveMetadataAndSnapshotSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "archive", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.InsertSession(&Session{TargetID: target.ID, Name: "record", GroupPath: "Work/Archive"})
	if err != nil {
		t.Fatal(err)
	}
	now := Now()
	if err := db.Update("sessions", row.ID, map[string]any{"ended_at": now, "archived_at": now, "archive_text": "Retained output Ω"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Session(row.ID)
	if err != nil || got.ArchivedAt == nil || *got.ArchivedAt != now || got.GroupPath != row.GroupPath {
		t.Fatalf("archive metadata lost: %+v %v", got, err)
	}
	var snapshot string
	if err := db.QueryRow("SELECT archive_text FROM sessions WHERE id=?", row.ID).Scan(&snapshot); err != nil || snapshot != "Retained output Ω" {
		t.Fatalf("snapshot lost: %q %v", snapshot, err)
	}
}

func TestLaunchConfigurationMigrationPrivacyAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "configuration.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "config", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.InsertSession(&Session{TargetID: target.ID, Name: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE sessions DROP COLUMN launch_config_json"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Session(row.ID)
	if err != nil || got.LaunchConfigJSON != "" {
		t.Fatalf("legacy migration: %v", err)
	}
	config := `{"version":1,"spec":{"name":"claude","command":"original","env":{"TOKEN":"private"}}}`
	if err := db.Update("sessions", row.ID, map[string]any{"launch_config_json": config}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("database file not private: %s %v", suffix, info.Mode())
		}
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err = db.Session(row.ID)
	if err != nil || got.LaunchConfigJSON != config {
		t.Fatalf("snapshot lost: %v", err)
	}
	rows, err := db.Sessions(true)
	if err != nil || len(rows) != 1 || rows[0].LaunchConfigJSON != config {
		t.Fatalf("listing lost snapshot: %v", err)
	}
}

func TestBackgroundSetupStateMigratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "setup", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.InsertSession(&Session{TargetID: target.ID, Name: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"setup_state", "setup_error", "setup_cancel_requested"} {
		if _, err := db.Exec("ALTER TABLE sessions DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := db.Session(row.ID)
	if err != nil || legacy.SetupState != "" || legacy.SetupError != "" || legacy.SetupCancelRequested {
		t.Fatalf("legacy setup state: %v", err)
	}
	if err := db.Update("sessions", row.ID, map[string]any{"setup_state": "failed", "setup_error": "checkout failed", "setup_cancel_requested": true}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, err := db.Session(row.ID)
	if err != nil || loaded.SetupState != "failed" || loaded.SetupError != "checkout failed" || !loaded.SetupCancelRequested {
		t.Fatalf("setup result was not durable: %v", err)
	}
}
