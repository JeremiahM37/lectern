package store

import (
	"strings"
	"testing"
)

// seedProject gives a project a task and an attempt with a row in every
// table that references a project, task or attempt, and returns the ids.
func seedProject(t *testing.T, db *DB, targetID int64, name string) (projectID, taskID, attemptID, sessionID int64) {
	t.Helper()
	p, err := db.InsertProject(&Project{Name: name, TargetID: targetID, RepoPath: "/r/" + name})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&Task{ProjectID: p.ID, Title: name, Prompt: "p", Status: "review"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&Attempt{TaskID: task.ID, N: 1, Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&Session{ProjectID: &p.ID, TargetID: targetID, Name: name, Agent: "claude",
		Workdir: "/w", TmuxSession: "lec-" + name})
	if err != nil {
		t.Fatal(err)
	}
	now := Now()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO events(attempt_id,seq,ts,type) VALUES(?,1,?,'x')`, []any{att.ID, now}},
		{`INSERT INTO approvals(attempt_id,tool_name,created_at) VALUES(?,'Bash',?)`, []any{att.ID, now}},
		{`INSERT INTO claims(repo_key,scope_kind,scope,holder,attempt_id,created_at,expires_at)
			VALUES('k','topic','t','agent',?,?,?)`, []any{att.ID, now, now + 60}},
		{`INSERT INTO otel_attempt_usage(attempt_id,updated_at) VALUES(?,?)`, []any{att.ID, now}},
		{`INSERT INTO memories(project_id,note,created_by_attempt,created_at) VALUES(?,'n',?,?)`, []any{p.ID, att.ID, now}},
		{`INSERT INTO task_messages(task_id,request_id,text,created_at) VALUES(?,'r','m',?)`, []any{task.ID, now}},
		{`INSERT INTO task_takeovers(task_id,attempt_id,status,created_at) VALUES(?,?,'ready',?)`, []any{task.ID, att.ID, now}},
		{`INSERT INTO ci_watches(task_id,project_id,target_id,pr_url,last_change_at,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?)`, []any{task.ID, p.ID, targetID, "https://github.com/a/b/pull/" + name, now, now, now}},
		{`INSERT INTO usage_daily(date,task_id) VALUES('2026-09-26',?)`, []any{task.ID}},
		{`INSERT INTO trigger_sources(project_id,kind,created_at,updated_at) VALUES(?,'github',?,?)`, []any{p.ID, now, now}},
		{`INSERT INTO trigger_events(source_id,project_id,external_id,created_at)
			VALUES((SELECT max(id) FROM trigger_sources),?,'e1',?)`, []any{p.ID, now}},
		{`INSERT INTO eval_suites(name,project_id,created_at) VALUES('s',?,?)`, []any{p.ID, now}},
		{`INSERT INTO eval_cases(suite_id,name) VALUES((SELECT max(id) FROM eval_suites),'c')`, nil},
		{`INSERT INTO eval_runs(suite_id,created_at) VALUES((SELECT max(id) FROM eval_suites),?)`, []any{now}},
		{`INSERT INTO eval_results(run_id,case_id,variant_idx,repeat_idx,task_id,attempt_id)
			VALUES((SELECT max(id) FROM eval_runs),(SELECT max(id) FROM eval_cases),0,0,?,?)`, []any{task.ID, att.ID}},
		{`INSERT INTO session_wraps(session_id,project_id,summary,created_at) VALUES(?,?,'w',?)`, []any{sess.ID, p.ID, now}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", strings.Fields(q.sql)[2], err)
		}
	}
	return p.ID, task.ID, att.ID, sess.ID
}

func count(t *testing.T, db *DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// dependents are the rows seedProject creates, keyed by how each finds the
// project: a count per table, so a leftover names its table.
func dependents(t *testing.T, db *DB, projectID, taskID, attemptID int64) map[string]int {
	return map[string]int{
		"projects":           count(t, db, `SELECT count(*) FROM projects WHERE id=?`, projectID),
		"tasks":              count(t, db, `SELECT count(*) FROM tasks WHERE id=?`, taskID),
		"attempts":           count(t, db, `SELECT count(*) FROM attempts WHERE id=?`, attemptID),
		"events":             count(t, db, `SELECT count(*) FROM events WHERE attempt_id=?`, attemptID),
		"approvals":          count(t, db, `SELECT count(*) FROM approvals WHERE attempt_id=?`, attemptID),
		"claims":             count(t, db, `SELECT count(*) FROM claims WHERE attempt_id=?`, attemptID),
		"otel_attempt_usage": count(t, db, `SELECT count(*) FROM otel_attempt_usage WHERE attempt_id=?`, attemptID),
		"memories":           count(t, db, `SELECT count(*) FROM memories WHERE project_id=?`, projectID),
		"task_messages":      count(t, db, `SELECT count(*) FROM task_messages WHERE task_id=?`, taskID),
		"task_takeovers":     count(t, db, `SELECT count(*) FROM task_takeovers WHERE task_id=?`, taskID),
		"ci_watches":         count(t, db, `SELECT count(*) FROM ci_watches WHERE task_id=?`, taskID),
		"usage_daily":        count(t, db, `SELECT count(*) FROM usage_daily WHERE task_id=?`, taskID),
		"trigger_sources":    count(t, db, `SELECT count(*) FROM trigger_sources WHERE project_id=?`, projectID),
		"trigger_events":     count(t, db, `SELECT count(*) FROM trigger_events WHERE project_id=?`, projectID),
		"eval_suites":        count(t, db, `SELECT count(*) FROM eval_suites WHERE project_id=?`, projectID),
		"eval_results":       count(t, db, `SELECT count(*) FROM eval_results WHERE task_id=?`, taskID),
		"sessions":           count(t, db, `SELECT count(*) FROM sessions WHERE project_id=?`, projectID),
		"session_wraps":      count(t, db, `SELECT count(*) FROM session_wraps WHERE project_id=?`, projectID),
	}
}

func TestDeleteProjectRemovesEveryDependentAtomically(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	pid, tid, aid, sid := seedProject(t, db, target.ID, "gone")
	opid, otid, oaid, _ := seedProject(t, db, target.ID, "kept")
	before := dependents(t, db, pid, tid, aid)
	for table, n := range before {
		if n == 0 {
			t.Fatalf("seed left %s empty", table)
		}
	}

	// A row the transaction cannot remove (a skill the API should have
	// cleaned first) fails the delete — and nothing at all is gone.
	if _, err := db.Exec(`INSERT INTO project_skills(project_id,target_id,agent,skill_id,source_id,source_path,
		entry_name,target_rel,created_at) VALUES(?,?,'claude','s','src','/s','e','r',?)`, pid, target.ID, Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProject(pid); err == nil {
		t.Fatal("a project with a leftover skill must not delete")
	}
	if after := dependents(t, db, pid, tid, aid); !equalCounts(before, after) {
		t.Fatalf("a failed delete removed rows:\nbefore %v\nafter  %v", before, after)
	}

	if _, err := db.Exec(`DELETE FROM project_skills WHERE project_id=?`, pid); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProject(pid); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	for table, n := range dependents(t, db, pid, tid, aid) {
		if n != 0 {
			t.Errorf("%s still has %d rows for the deleted project", table, n)
		}
	}
	if s, err := db.Session(sid); err != nil || s.ProjectID != nil {
		t.Fatalf("the session must survive, unassigned: %+v %v", s, err)
	}
	if n := count(t, db, `SELECT count(*) FROM session_wraps WHERE session_id=?`, sid); n != 1 {
		t.Fatalf("the session's wrap must survive, got %d", n)
	}
	for table, n := range dependents(t, db, opid, otid, oaid) {
		if n == 0 {
			t.Errorf("deleting one project removed another's %s", table)
		}
	}
}

func TestDeleteTasksRemovesEveryDependent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, _ := db.InsertTarget(&Target{Name: "t", Kind: "local"})
	pid, tid, aid, _ := seedProject(t, db, target.ID, "p")
	child, err := db.InsertTask(&Task{ProjectID: pid, Title: "child", Status: "backlog", ParentTaskID: &tid})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTasks(tid); err != nil {
		t.Fatalf("DeleteTasks: %v", err)
	}
	for _, table := range []string{"tasks", "attempts", "events", "approvals", "claims", "otel_attempt_usage",
		"task_messages", "task_takeovers", "ci_watches", "usage_daily"} {
		if n := dependents(t, db, pid, tid, aid)[table]; n != 0 {
			t.Errorf("%s still has %d rows for the deleted task", table, n)
		}
	}
	if c, err := db.Task(child.ID); err != nil || c.ParentTaskID != nil {
		t.Fatalf("a child task is orphaned, not deleted: %+v %v", c, err)
	}
	if n := count(t, db, `SELECT count(*) FROM projects WHERE id=?`, pid); n != 1 {
		t.Fatal("deleting a task must not touch its project")
	}
}

// Every foreign key into projects, tasks or attempts must be handled by the
// delete path, or deleting a project fails the way it did before: a
// FOREIGN KEY error half-way through. A new table with such a key has to be
// added to taskDependents or projectDependents.
func TestDeleteCoversEveryForeignKey(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handled := strings.Join(append(append([]string{}, taskDependents...), projectDependents...), "\n")
	// project_skills is cleaned by the API first, through the target's
	// executor; DeleteProject refuses (and rolls back) if any are left.
	exempt := map[string]bool{"project_skills": true}
	rows, err := db.Query(`SELECT m.name, f."table" FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type='table' AND f."table" IN ('projects','tasks','attempts')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			t.Fatal(err)
		}
		seen++
		if exempt[child] || (child == "tasks" && parent == "projects") || (child == "attempts" && parent == "tasks") {
			continue
		}
		if !strings.Contains(handled, "FROM "+child+" ") && !strings.Contains(handled, "UPDATE "+child+" ") {
			t.Errorf("%s references %s but the delete path never clears it", child, parent)
		}
	}
	if seen < 10 {
		t.Fatalf("found only %d foreign keys; the schema query is wrong", seen)
	}
}

func equalCounts(a, b map[string]int) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return len(a) == len(b)
}
