package awareness

import (
	"io"
	"log/slog"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func newTestTracker(t *testing.T) (*Tracker, *store.DB, *store.Target) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	reg := executor.NewRegistry(false, 0)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(db, reg, log), db, target
}

// setRepoKey pokes a session's repo_key/repo_toplevel directly, standing in
// for a completed background resolution — every test in this file is about
// the peers/briefing/warning LOGIC, not git itself (see real_test.go for
// that).
func setRepoKey(t *testing.T, db *store.DB, sess *store.Session, key, toplevel string) *store.Session {
	t.Helper()
	if err := db.Update("sessions", sess.ID, map[string]any{"repo_key": key, "repo_toplevel": toplevel}); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

func mustSession(t *testing.T, db *store.DB, targetID int64, name, workdir string) *store.Session {
	t.Helper()
	s, err := db.InsertSession(&store.Session{TargetID: targetID, Name: name, Agent: "claude",
		Workdir: workdir, TmuxSession: "lec-" + name})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFilePathFromToolInput(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  string
		ok    bool
	}{
		{"Edit", map[string]any{"file_path": "/a/b.go"}, "/a/b.go", true},
		{"Write", map[string]any{"file_path": "/a/c.go"}, "/a/c.go", true},
		{"MultiEdit", map[string]any{"file_path": "/a/d.go"}, "/a/d.go", true},
		{"NotebookEdit", map[string]any{"notebook_path": "/a/n.ipynb"}, "/a/n.ipynb", true},
		{"NotebookEdit", map[string]any{"file_path": "/a/n2.ipynb"}, "/a/n2.ipynb", true},
		{"Read", map[string]any{"file_path": "/a/b.go"}, "", false},
		{"Bash", map[string]any{"command": "ls"}, "", false},
		{"Edit", map[string]any{}, "", false},
	}
	for _, c := range cases {
		got, ok := FilePathFromToolInput(c.tool, c.input)
		if ok != c.ok || got != c.want {
			t.Errorf("FilePathFromToolInput(%s, %v) = %q, %v; want %q, %v", c.tool, c.input, got, ok, c.want, c.ok)
		}
	}
}

func TestRelPathRejectsOutsideToplevel(t *testing.T) {
	if rel, ok := RelPath("/repo", "/repo/a/b.go"); !ok || rel != "a/b.go" {
		t.Fatalf("got %q, %v", rel, ok)
	}
	if _, ok := RelPath("/repo", "/repo"); ok {
		t.Fatalf("editing the toplevel itself should not resolve to a rel_path")
	}
	if _, ok := RelPath("/repo", "/elsewhere/a.go"); ok {
		t.Fatalf("a path outside toplevel must not resolve")
	}
	if _, ok := RelPath("", "/repo/a.go"); ok {
		t.Fatalf("empty toplevel must not resolve")
	}
}

func TestRecordEditRequiresResolvedRepoKey(t *testing.T) {
	tr, db, target := newTestTracker(t)
	sess := mustSession(t, db, target.ID, "a", "/repo")
	// repo_key is "" (not yet resolved) — RecordEdit must be a no-op.
	tr.RecordEdit(sess, "/repo/a.go")
	edits, err := db.SessionFileEditsFor(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 0 {
		t.Fatalf("expected no edits recorded before repo_key resolves, got %v", edits)
	}

	sess = setRepoKey(t, db, sess, RepoKeyNone, "")
	tr.RecordEdit(sess, "/repo/a.go")
	edits, _ = db.SessionFileEditsFor(sess.ID, 0)
	if len(edits) != 0 {
		t.Fatalf("expected no edits recorded for a non-git session, got %v", edits)
	}
}

func TestPeersEmptyForUnresolvedSession(t *testing.T) {
	tr, db, target := newTestTracker(t)
	sess := mustSession(t, db, target.ID, "a", "/repo")
	peers, err := tr.Peers(sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected no peers for an unresolved session, got %v", peers)
	}
}

func TestBriefingDedupAndTTL(t *testing.T) {
	tr, db, target := newTestTracker(t)
	a := mustSession(t, db, target.ID, "a", "/repo")
	b := mustSession(t, db, target.ID, "b", "/repo-wt")
	a = setRepoKey(t, db, a, "1:/repo/.git", "/repo")
	b = setRepoKey(t, db, b, "1:/repo/.git", "/repo-wt")
	tr.RecordEdit(a, "/repo/x.go")

	text1, ok1 := tr.Briefing(b)
	if !ok1 || text1 == "" {
		t.Fatalf("expected a briefing the first time, got ok=%v text=%q", ok1, text1)
	}
	b, _ = db.Session(b.ID)
	if b.AwarenessBriefingHash == "" || b.AwarenessBriefingAt == nil {
		t.Fatalf("expected dedup state to be recorded")
	}

	// Nothing changed — a second call within the TTL must be suppressed.
	_, ok2 := tr.Briefing(b)
	if ok2 {
		t.Fatalf("expected the unchanged briefing to be suppressed")
	}

	// Simulate TTL expiry: back-date the last-sent time past BriefingTTL.
	old := store.Now() - BriefingTTL.Seconds() - 1
	if err := db.Update("sessions", b.ID, map[string]any{"awareness_briefing_at": old}); err != nil {
		t.Fatal(err)
	}
	b, _ = db.Session(b.ID)
	text3, ok3 := tr.Briefing(b)
	if !ok3 || text3 != text1 {
		t.Fatalf("expected the same unchanged briefing to re-send after TTL, got ok=%v text=%q", ok3, text3)
	}

	// A genuinely new peer file changes the hash and must not be suppressed
	// even inside the TTL.
	old2 := store.Now()
	if err := db.Update("sessions", b.ID, map[string]any{"awareness_briefing_at": old2}); err != nil {
		t.Fatal(err)
	}
	tr.RecordEdit(a, "/repo/y.go")
	b, _ = db.Session(b.ID)
	text4, ok4 := tr.Briefing(b)
	if !ok4 || text4 == text1 {
		t.Fatalf("expected a changed briefing to bypass dedup, got ok=%v text=%q", ok4, text4)
	}
}

func TestBriefingNoPeersNoText(t *testing.T) {
	tr, db, target := newTestTracker(t)
	sess := mustSession(t, db, target.ID, "solo", "/repo")
	sess = setRepoKey(t, db, sess, "1:/repo/.git", "/repo")
	if _, ok := tr.Briefing(sess); ok {
		t.Fatalf("expected no briefing with zero peers")
	}
}

func TestEditWarningWindow(t *testing.T) {
	tr, db, target := newTestTracker(t)
	a := mustSession(t, db, target.ID, "a", "/repo")
	b := mustSession(t, db, target.ID, "b", "/repo")
	a = setRepoKey(t, db, a, "1:/repo/.git", "/repo")
	b = setRepoKey(t, db, b, "1:/repo/.git", "/repo")

	// An edit just now: inside the warn window.
	tr.RecordEdit(a, "/repo/x.go")
	text, ok := tr.EditWarning(b, "Edit", map[string]any{"file_path": "/repo/x.go"})
	if !ok || text == "" {
		t.Fatalf("expected a warning for a recent same-file edit, got ok=%v", ok)
	}
	if !ok {
		t.Fatal("expected ok")
	}

	// An edit outside the 30-minute window must not warn.
	stale := store.Now() - EditWarnWindow.Seconds() - 60
	if err := db.UpsertSessionFileEdit(a.ID, a.RepoKey, "old.go", stale); err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.EditWarning(b, "Edit", map[string]any{"file_path": "/repo/old.go"}); ok {
		t.Fatalf("expected no warning for an edit outside the warn window")
	}

	// A file nobody touched: no warning.
	if _, ok := tr.EditWarning(b, "Edit", map[string]any{"file_path": "/repo/untouched.go"}); ok {
		t.Fatalf("expected no warning for an untouched file")
	}

	// An untracked tool never warns, even for the same path.
	if _, ok := tr.EditWarning(b, "Read", map[string]any{"file_path": "/repo/x.go"}); ok {
		t.Fatalf("expected no warning for a non-tracked tool")
	}
}

func TestPruneSessionFileEdits(t *testing.T) {
	_, db, target := newTestTracker(t)
	sess := mustSession(t, db, target.ID, "a", "/repo")
	old := store.Now() - EditRetention.Seconds() - 60
	if err := db.UpsertSessionFileEdit(sess.ID, "1:/repo/.git", "old.go", old); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertSessionFileEdit(sess.ID, "1:/repo/.git", "new.go", store.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneSessionFileEdits(store.Now() - EditRetention.Seconds()); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.SessionFileEditsFor(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].RelPath != "new.go" {
		t.Fatalf("expected only the fresh row to survive pruning, got %v", remaining)
	}
}

func TestDuplicatePromptsJaccard(t *testing.T) {
	tr, db, target := newTestTracker(t)
	a := mustSession(t, db, target.ID, "a", "/repo")
	b := mustSession(t, db, target.ID, "b", "/repo-wt")
	c := mustSession(t, db, target.ID, "c", "/other-repo")
	setRepoKey(t, db, a, "1:/repo/.git", "/repo")
	setRepoKey(t, db, b, "1:/repo/.git", "/repo-wt")
	setRepoKey(t, db, c, "1:/other/.git", "/other-repo")

	now := store.Now()
	if err := db.Update("sessions", a.ID, map[string]any{
		"last_prompt_excerpt": "add a rename button to the session card", "last_prompt_at": now}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", b.ID, map[string]any{
		"last_prompt_excerpt": "add a rename button on the session card", "last_prompt_at": now}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", c.ID, map[string]any{
		"last_prompt_excerpt": "add a rename button to the session card", "last_prompt_at": now}); err != nil {
		t.Fatal(err)
	}

	pairs, err := tr.DuplicatePrompts()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 {
		t.Fatalf("expected exactly one duplicate pair (a,b share a repo; c does not), got %v", pairs)
	}
	got := map[int64]bool{pairs[0].SessionAID: true, pairs[0].SessionBID: true}
	if !got[a.ID] || !got[b.ID] {
		t.Fatalf("expected the pair to be (a,b), got %+v", pairs[0])
	}
	if pairs[0].Score < 0.5 {
		t.Fatalf("expected score >= 0.5, got %v", pairs[0].Score)
	}
}

func TestPromptExcerptClips(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	got := PromptExcerpt(long)
	if len([]rune(got)) > PromptExcerptChars+1 {
		t.Fatalf("expected clipped excerpt, got %d runes", len([]rune(got)))
	}
	if PromptExcerpt("short") != "short" {
		t.Fatalf("short prompt should be unchanged")
	}
}
