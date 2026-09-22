package sessions

// These tests are deliberately written against a hand-built pre-migration
// database and a scripted real-executor boundary. They do not start tmux or
// a Lectern process; the root runner may execute them only after the
// isolation barrier has been approved.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

type checkpointFixtureExecutor struct {
	native bool
}

func (e *checkpointFixtureExecutor) Run(_ context.Context, cmd string, _ executor.RunOpts) (executor.Result, error) {
	switch {
	case strings.Contains(cmd, "cat /proc/sys/kernel/random/boot_id"):
		return executor.Result{Stdout: "11111111-2222-3333-4444-555555555555\n"}, nil
	case strings.Contains(cmd, "tmux has-session"):
		return executor.Result{}, nil
	case strings.Contains(cmd, "#{session_name}"):
		return executor.Result{Stdout: "lec-s1\n"}, nil
	case strings.Contains(cmd, "native_identity") && strings.Contains(cmd, "json.dumps"):
		if e.native {
			return executor.Result{Stdout: `{"state":"identified","id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`}, nil
		}
		return executor.Result{RC: 1}, nil
	case strings.Contains(cmd, "@lectern-tracking-identity"):
		return executor.Result{Stdout: "0123456789abcdef0123456789abcdef\n"}, nil
	case strings.Contains(cmd, "python3 -c"):
		return executor.Result{Stdout: "/home/test/.codex\n"}, nil
	default:
		return executor.Result{RC: 1}, nil
	}
}
func (*checkpointFixtureExecutor) ReadFile(context.Context, string, int64) ([]byte, error) {
	return nil, nil
}
func (*checkpointFixtureExecutor) WriteFile(context.Context, string, []byte) error { return nil }
func (*checkpointFixtureExecutor) Close() error                                    { return nil }

func writeOldCheckpointDB(t *testing.T, native bool) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE targets(id INTEGER PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL, host TEXT, port INTEGER, user TEXT, key_path TEXT, workroot TEXT, max_concurrent INTEGER, sandbox INTEGER, context_json TEXT, memory_dir TEXT, command_prefix TEXT, status TEXT, info_json TEXT, created_at REAL);
CREATE TABLE sessions(id INTEGER PRIMARY KEY, project_id INTEGER, target_id INTEGER NOT NULL, name TEXT NOT NULL, agent TEXT NOT NULL, model TEXT, workdir TEXT NOT NULL, tmux_session TEXT NOT NULL, status TEXT NOT NULL, origin TEXT NOT NULL, pane_hash TEXT, pane_tail TEXT, context_pct INTEGER, last_activity_at REAL, created_at REAL, updated_at REAL, ended_at REAL, archived_at REAL, archive_text TEXT, worktree_json TEXT, group_path TEXT, tracking_identity TEXT, resume_id TEXT, launch_config_json TEXT, setup_cancel_requested INTEGER DEFAULT 0, setup_state TEXT DEFAULT '', setup_error TEXT DEFAULT '');`)
	if err != nil {
		t.Fatal(err)
	}
	launch := `{"version":1,"spec":{"name":"codex","command":"codex"}}`
	_, err = db.Exec(`INSERT INTO targets VALUES(1,'local','local','',22,'root','','',2,0,'[]','','','ready','{}',1)`)
	if err == nil {
		_, err = db.Exec(`INSERT INTO sessions(project_id,target_id,name,agent,model,workdir,tmux_session,status,origin,pane_hash,pane_tail,created_at,updated_at,worktree_json,group_path,tracking_identity,resume_id,launch_config_json) VALUES(NULL,1,'s1','codex','', '/tmp/work','lec-s1','idle','lectern','','',1,1,'{}','', '0123456789abcdef0123456789abcdef',?,?)`, func() string {
			if native {
				return ""
			}
			return "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
		}(), launch)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestExportReadsPreMigrationSchemaAndCapturesTargetAndLaunch(t *testing.T) {
	dbPath := writeOldCheckpointDB(t, true)
	ex := &checkpointFixtureExecutor{native: true}
	m, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Sessions) != 1 || m.Sessions[0].IdentityState != IdentityVerifiedCurrent || m.Sessions[0].NativeRecoveryCID == "" {
		t.Fatalf("unexpected export: %+v", m.Sessions)
	}
	check, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	rows, err := check.Query("PRAGMA table_info(sessions)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var nn int
		var def any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "boot_id" || name == "native_recovery_cid" {
			t.Fatalf("export migrated old schema by adding %s", name)
		}
	}
	manifest := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := WriteCheckpoint(manifest, m); err != nil {
		t.Fatal(err)
	}
	mode, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0600 {
		t.Fatalf("manifest mode %o", mode.Mode().Perm())
	}
	var decoded CheckpointManifest
	b, _ := os.ReadFile(manifest)
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Sessions[0].LaunchConfigSHA256 == "" || decoded.Sessions[0].TargetFingerprint == "" {
		t.Fatal("manifest omitted durable configuration")
	}
}

func TestImportUsesExportedBootAndCIDAndIsIdempotent(t *testing.T) {
	dbPath := writeOldCheckpointDB(t, true)
	ex := &checkpointFixtureExecutor{native: true}
	m, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil })
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := WriteCheckpoint(manifest, m); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", 1, map[string]any{"name": "renamed by operator"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	report, err := ImportCheckpoint(manifest, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 1 {
		t.Fatalf("report %+v", report)
	}
	rowDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	row, err := rowDB.Session(1)
	rowDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	if row.BootID != m.Sessions[0].BootID || row.NativeRecoveryCID != m.Sessions[0].NativeRecoveryCID {
		t.Fatalf("import masked checkpoint: %+v", row)
	}
	repeated, err := ImportCheckpoint(manifest, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Imported != 0 || len(repeated.Skipped) != 1 || repeated.Skipped[0].Reason != "checkpoint already populated" {
		t.Fatalf("repeat report %+v", repeated)
	}
}

func TestUnknownNativeIdentityIsRetainedForCoverageReport(t *testing.T) {
	dbPath := writeOldCheckpointDB(t, false)
	ex := &checkpointFixtureExecutor{native: false}
	m, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Sessions) != 1 || m.Sessions[0].IdentityState != IdentityDurableExplicit {
		t.Fatalf("expected durable explicit evidence: %+v", m.Sessions)
	}
	// Remove the explicit ID only in the manifest copy to exercise the unknown
	// state without changing the old source DB.
	m.Sessions[0].NativeRecoveryCID, m.Sessions[0].IdentityState = "", IdentityUnknown
	if err := WriteCheckpoint(filepath.Join(t.TempDir(), "unknown.json"), m); err != nil {
		t.Fatal(err)
	}
}

func TestImportSkipsEndedOrChangedRowsWithoutAbortingManifest(t *testing.T) {
	dbPath := writeOldCheckpointDB(t, true)
	ex := &checkpointFixtureExecutor{native: true}
	m, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil })
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := WriteCheckpoint(manifest, m); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", 1, map[string]any{"workdir": "/operator-changed"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	report, err := ImportCheckpoint(manifest, dbPath)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || len(report.Skipped) != 1 || report.Skipped[0].Reason != "durable session identity changed" {
		t.Fatalf("changed row report %+v", report)
	}
}

func TestImportSkipsEndedRowWithoutAbortingManifest(t *testing.T) {
	dbPath := writeOldCheckpointDB(t, true)
	ex := &checkpointFixtureExecutor{native: true}
	m, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil })
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := WriteCheckpoint(manifest, m); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", 1, map[string]any{"ended_at": store.Now()}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	report, err := ImportCheckpoint(manifest, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || len(report.Skipped) != 1 || report.Skipped[0].Reason != "session ended or archived" {
		t.Fatalf("ended row report %+v", report)
	}
}

func TestExportSkipsABlankShellInsteadOfRefusingTheUpgrade(t *testing.T) {
	dbPath := writeOldCheckpointDB(t, true)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// What the New terminal button creates: no agent, so no launch
	// configuration, no tracking identity and no conversation to resume.
	_, err = db.Exec(`INSERT INTO sessions(target_id,name,agent,workdir,tmux_session,status,origin,created_at,updated_at)
 VALUES(1,'Shell · local','shell','/scratch/shell-1','lec-s99','idle','lectern',1,1)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ex := &checkpointFixtureExecutor{native: true}
	m, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil })
	if err != nil {
		t.Fatalf("one open shell must not block an upgrade: %v", err)
	}
	if len(m.Sessions) != 1 || m.Sessions[0].ID != 1 {
		t.Fatalf("the agent session is still covered and the shell is not listed: %+v", m.Sessions)
	}
	// A real agent row that cannot be described is still a hard stop.
	db, _ = store.Open(dbPath)
	_, err = db.Exec(`INSERT INTO sessions(target_id,name,agent,workdir,tmux_session,status,origin,created_at,updated_at)
 VALUES(1,'broken','claude','/w','lec-s98','idle','lectern',1,1)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportCheckpoint(context.Background(), dbPath, func(*store.Target) (executor.Executor, error) { return ex, nil }); err == nil {
		t.Fatal("an agent session with no launch configuration must still refuse the export")
	}
}
