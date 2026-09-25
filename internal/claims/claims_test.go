package claims

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func newTestTracker(t *testing.T) (*Tracker, *store.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(db, log), db
}

// mustSession inserts a real session row and returns its id — claims.session_id
// is a foreign key, so a test that sets it needs a row that actually exists.
func mustSession(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	target, err := db.InsertTarget(&store.Target{Name: "t-" + name, Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: name, Agent: "claude",
		Workdir: "/repo", TmuxSession: "lec-" + name})
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func TestCreateValidatesScopeKind(t *testing.T) {
	tr, _ := newTestTracker(t)
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: "bogus", Scope: "x", Holder: "a"}); err == nil {
		t.Fatalf("expected an error for an invalid scope_kind")
	}
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "", Holder: "a"}); err == nil {
		t.Fatalf("expected an error for an empty scope")
	}
	if _, err := tr.Create(Input{RepoKey: "", ScopeKind: ScopeTopic, Scope: "x", Holder: "a"}); err == nil {
		t.Fatalf("expected an error for an empty repo_key")
	}
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: ""}); err == nil {
		t.Fatalf("expected an error for an empty holder")
	}
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopePaths, Scope: "x", Holder: "a"}); err == nil {
		t.Fatalf("expected an error for a paths claim with no globs")
	}
}

func TestCreateDefaultsAndClampsTTL(t *testing.T) {
	tr, _ := newTestTracker(t)
	c, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "rename button", Holder: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ExpiresAt - c.CreatedAt; got != DefaultTTL.Seconds() {
		t.Fatalf("expected default TTL %v, got %v seconds", DefaultTTL.Seconds(), got)
	}

	c2, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: "a", TTLMinutes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.ExpiresAt - c2.CreatedAt; got != MinTTL.Seconds() {
		t.Fatalf("expected TTL clamped to the minimum, got %v", got)
	}

	c3, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: "a", TTLMinutes: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if got := c3.ExpiresAt - c3.CreatedAt; got != MaxTTL.Seconds() {
		t.Fatalf("expected TTL clamped to the maximum, got %v", got)
	}
}

func TestLifecycleCreateListRelease(t *testing.T) {
	tr, db := newTestTracker(t)
	sid := mustSession(t, db, "a")
	c, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopePaths, Paths: []string{"frontend/src/sessions/**"},
		Holder: "sess-a", HolderKind: "session", SessionID: &sid, Agent: "claude", Intent: "rename button"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := tr.ActiveForRepo("1:/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != c.ID {
		t.Fatalf("expected the new claim to be active, got %v", active)
	}
	if err := tr.Release(c.ID); err != nil {
		t.Fatal(err)
	}
	active, err = tr.ActiveForRepo("1:/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("expected no active claims after release, got %v", active)
	}
	// Releasing an already-released claim is a no-op, not an error.
	if err := tr.Release(c.ID); err != nil {
		t.Fatalf("re-releasing must not error: %v", err)
	}
}

func TestExpiryViaSweep(t *testing.T) {
	tr, db := newTestTracker(t)
	c, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: "a", TTLMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Back-date expiry into the past to simulate a lapsed TTL.
	if err := db.RenewClaim(c.ID, store.Now()-1); err != nil {
		t.Fatal(err)
	}
	released, err := tr.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("expected sweep to release exactly 1 claim, got %d", released)
	}
	fresh, err := db.Claim(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ReleasedAt == nil {
		t.Fatalf("expected the claim to be released after sweep")
	}
}

func TestRenewActivityExtendsExpiry(t *testing.T) {
	tr, db := newTestTracker(t)
	sid := mustSession(t, db, "a")
	c, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: "a",
		HolderKind: "session", SessionID: &sid, TTLMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the TTL almost lapsing.
	nearExpiry := store.Now() + 1
	if err := db.RenewClaim(c.ID, nearExpiry); err != nil {
		t.Fatal(err)
	}
	tr.RenewActivity(sid)
	fresh, err := db.Claim(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ExpiresAt <= nearExpiry+1 {
		t.Fatalf("expected activity to push expiry out well past the near-lapse value, got %v vs %v", fresh.ExpiresAt, nearExpiry)
	}
	// A sweep right after must NOT release it now that it was renewed.
	released, err := tr.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 {
		t.Fatalf("expected the renewed claim to survive a sweep, got %d released", released)
	}
}

func TestExtendClaim(t *testing.T) {
	tr, _ := newTestTracker(t)
	c, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: "a", TTLMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	before := c.ExpiresAt
	extended, err := tr.Extend(c.ID, 0) // 0 = reuse original TTL
	if err != nil {
		t.Fatal(err)
	}
	if extended.ExpiresAt <= before {
		t.Fatalf("expected extend to push expiry forward, got %v vs %v", extended.ExpiresAt, before)
	}
	if err := tr.Release(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Extend(c.ID, 0); err == nil {
		t.Fatalf("expected extending a released claim to fail")
	}
}

func TestAutoReleaseOnSessionEnd(t *testing.T) {
	tr, db := newTestTracker(t)
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "a", Agent: "claude",
		Workdir: "/repo", TmuxSession: "lec-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "x", Holder: "a",
		HolderKind: "session", SessionID: &sess.ID, TTLMinutes: 120}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", sess.ID, map[string]any{"agent_state": "ended"}); err != nil {
		t.Fatal(err)
	}
	released, err := tr.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("expected the ended session's claim to be released by the sweep, got %d", released)
	}
}

func TestAutoReleaseOnAttemptFinish(t *testing.T) {
	tr, db := newTestTracker(t)
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&store.Task{ProjectID: proj.ID, Title: "t", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "running", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTask, Scope: "1", Holder: "attempt #" + itoa(att.ID),
		HolderKind: "attempt", AttemptID: &att.ID, TTLMinutes: 1440}); err != nil {
		t.Fatal(err)
	}
	released, err := tr.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 {
		t.Fatalf("expected a running attempt's claim to survive a sweep, got %d released", released)
	}
	if err := db.Update("attempts", att.ID, map[string]any{"status": "done", "finished_at": store.Now()}); err != nil {
		t.Fatal(err)
	}
	released, err = tr.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("expected the finished attempt's claim to be released by the sweep, got %d", released)
	}
}

func itoa(id int64) string {
	if id == 0 {
		return "0"
	}
	neg := id < 0
	if neg {
		id = -id
	}
	var buf [20]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---- overlap detection: globs -----------------------------------------------

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"frontend/src/sessions/**", "frontend/src/sessions/SessionCard.tsx", true},
		{"frontend/src/sessions/**", "frontend/src/board/Board.tsx", false},
		{"*", "README.md", true},
		{"*", "a/README.md", false},
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "sub/main.go", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestPathsOverlap(t *testing.T) {
	if !PathsOverlap([]string{"frontend/src/sessions/**"}, []string{"frontend/src/sessions/SessionCard.tsx"}) {
		t.Fatalf("expected a directory glob to overlap a file inside it")
	}
	if !PathsOverlap([]string{"frontend/src/sessions/**"}, []string{"frontend/src/sessions/sub/**"}) {
		t.Fatalf("expected a directory glob to overlap a nested subdirectory glob")
	}
	if PathsOverlap([]string{"frontend/src/sessions/**"}, []string{"frontend/src/board/**"}) {
		t.Fatalf("expected sibling directories not to overlap")
	}
}

func TestOverlappingPathClaimsExcludesSelfAndNonMatches(t *testing.T) {
	tr, db := newTestTracker(t)
	codexSID := mustSession(t, db, "codex")
	otherSID := mustSession(t, db, "other")
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopePaths, Paths: []string{"frontend/src/sessions/**"},
		Holder: "codex-session", HolderKind: "session", SessionID: &codexSID, Agent: "codex", Intent: "rename button"}); err != nil {
		t.Fatal(err)
	}
	// Same session editing the same area: never warns about itself.
	self, err := tr.OverlappingPathClaims("1:/repo", "frontend/src/sessions/SessionCard.tsx", codexSID)
	if err != nil {
		t.Fatal(err)
	}
	if len(self) != 0 {
		t.Fatalf("expected no self-overlap, got %v", self)
	}
	// A different session editing the same file: warns.
	other, err := tr.OverlappingPathClaims("1:/repo", "frontend/src/sessions/SessionCard.tsx", otherSID)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Fatalf("expected exactly one overlapping claim, got %v", other)
	}
	// An unrelated file: no warning.
	none, err := tr.OverlappingPathClaims("1:/repo", "frontend/src/board/Board.tsx", otherSID)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no overlap for an unrelated file, got %v", none)
	}
}

// ---- overlap detection: topics ----------------------------------------------

func TestTopicScore(t *testing.T) {
	a := "add a rename button to the session card"
	b := "add a rename button on the session card"
	if score := TopicScore(a, b); score < 0.5 {
		t.Fatalf("expected near-duplicate topics to score >= 0.5, got %v", score)
	}
	c := "fix the docker compose healthcheck"
	if score := TopicScore(a, c); score >= 0.5 {
		t.Fatalf("expected unrelated topics to score < 0.5, got %v", score)
	}
}

func TestOverlappingTopicClaims(t *testing.T) {
	tr, db := newTestTracker(t)
	sid := mustSession(t, db, "a")
	if _, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopeTopic, Scope: "rename button in session card",
		Holder: "a", HolderKind: "session", SessionID: &sid}); err != nil {
		t.Fatal(err)
	}
	overlaps, err := tr.OverlappingTopicClaims("1:/repo", "please rename the button in the session card")
	if err != nil {
		t.Fatal(err)
	}
	if len(overlaps) != 1 {
		t.Fatalf("expected one overlapping topic claim, got %v", overlaps)
	}
	none, err := tr.OverlappingTopicClaims("1:/repo", "upgrade the docker base image")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no overlap for an unrelated prompt, got %v", none)
	}
}

// ---- rendering ---------------------------------------------------------------

func TestBriefingSectionAndEditWarningText(t *testing.T) {
	tr, db := newTestTracker(t)
	sid := mustSession(t, db, "scratch-terminals")
	c, err := tr.Create(Input{RepoKey: "1:/repo", ScopeKind: ScopePaths, Paths: []string{"frontend/src/sessions/**"},
		Holder: "scratch terminals", HolderKind: "session", SessionID: &sid, Agent: "Codex", Intent: "rename button"})
	if err != nil {
		t.Fatal(err)
	}
	wantID := fmt.Sprintf("session #%d", sid)
	section := BriefingSection([]*store.Claim{c})
	for _, want := range []string{wantID, "Codex", "frontend/src/sessions/**", "rename button"} {
		if !contains(section, want) {
			t.Errorf("expected briefing section to mention %q, got %q", want, section)
		}
	}
	warning := EditWarning(c)
	for _, want := range []string{wantID, "Codex", "frontend/src/sessions/**", "rename button", "Coordinate"} {
		if !contains(warning, want) {
			t.Errorf("expected edit warning to mention %q, got %q", want, warning)
		}
	}
}

func TestBriefingSectionEmptyForNoClaims(t *testing.T) {
	if got := BriefingSection(nil); got != "" {
		t.Fatalf("expected empty briefing for no claims, got %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
