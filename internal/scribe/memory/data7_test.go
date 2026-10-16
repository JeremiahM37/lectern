package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newStores opens a project and a global store under a temp SCRIBE_HOME.
func mkStores(t *testing.T) *Stores {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHideRemovesFromSearchListAndRecallUntilRestored(t *testing.T) {
	s := mkStores(t)
	db := s.Project
	rec, _, err := db.Add(Record{Category: CatFact, Title: "Zebra cache location", Body: "The zebra cache lives under the build directory"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Hide(rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Search("zebra", 10); len(got) != 0 {
		t.Fatalf("hidden record still searchable: %d hits", len(got))
	}
	if got, _ := db.List(ListOptions{}); len(got) != 0 {
		t.Fatalf("hidden record still listed: %d", len(got))
	}
	if got, _ := db.Recall("zebra cache", nil, 5); len(got) != 0 {
		t.Fatalf("hidden record still recalled: %d", len(got))
	}
	hidden, err := db.ListHidden(10)
	if err != nil || len(hidden) != 1 || !hidden[0].Hidden {
		t.Fatalf("ListHidden = %+v, %v", hidden, err)
	}
	// The record is kept, so a full read still finds it.
	if got, err := db.Get(rec.ID); err != nil || !got.Hidden {
		t.Fatalf("Get hidden = %+v, %v", got, err)
	}
	if err := db.Restore(rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Search("zebra", 10); len(got) != 1 {
		t.Fatalf("restored record not searchable: %d", len(got))
	}
	if hidden, _ := db.ListHidden(10); len(hidden) != 0 {
		t.Fatalf("restored record still in hidden list")
	}
}

func TestHiddenRecordDoesNotDedupeNewSave(t *testing.T) {
	s := mkStores(t)
	db := s.Project
	first, _, _ := db.Add(Record{Category: CatFact, Title: "Port note", Body: "The queue runs on port nine"})
	if err := db.Hide(first.ID); err != nil {
		t.Fatal(err)
	}
	_, deduped, err := db.Add(Record{Category: CatFact, Title: "Port note", Body: "The queue runs on port nine"})
	if err != nil {
		t.Fatal(err)
	}
	if deduped {
		t.Fatal("a save matched a hidden record; it should create a new one")
	}
}

func TestHideByPrefixAndAmbiguityStillApply(t *testing.T) {
	s := mkStores(t)
	db := s.Project
	rec, _, _ := db.Add(Record{Category: CatTodo, Title: "Prefix target", Body: "hide me by a prefix"})
	if err := db.Hide(rec.ID[:8]); err != nil {
		t.Fatal(err)
	}
	if err := db.Hide("no-such-id"); err == nil {
		t.Fatal("hiding an unknown id should fail")
	}
}

func TestSessionStampsNewRecords(t *testing.T) {
	s := mkStores(t)
	if err := s.SetSession("sess-42", ""); err != nil {
		t.Fatal(err)
	}
	rec, _, err := s.Project.Add(Record{Category: CatFact, Title: "Stamped", Body: "saved in sess-42"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Project.Get(rec.ID)
	if err != nil || got.SessionID != "sess-42" {
		t.Fatalf("session id = %q, %v", got.SessionID, err)
	}
	// Records saved before a session was bound keep an empty id.
	s.SetSession("", "")
	other, _, _ := s.Project.Add(Record{Category: CatFact, Title: "Unstamped", Body: "no session bound"})
	if got, _ := s.Project.Get(other.ID); got.SessionID != "" {
		t.Fatalf("unbound save got session %q", got.SessionID)
	}
}

func TestInjectDoesNotRepeatSurfacedMemoriesAndPersists(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "session")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetSession("sess-a", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Project.Add(Record{Category: CatConvention, Title: "Quokka linting", Body: "Run the quokka linter before commits"}); err != nil {
		t.Fatal(err)
	}
	first := s.Inject("quokka linter commits", nil)
	if !strings.Contains(first, "Quokka linting") {
		t.Fatalf("first injection missing the memory: %q", first)
	}
	if again := s.Inject("quokka linter commits", nil); again != "" {
		t.Fatalf("memory injected twice in one session: %q", again)
	}
	// A resumed session reads the same set back from its directory.
	s2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.SetSession("sess-a", dir); err != nil {
		t.Fatal(err)
	}
	if got := s2.Inject("quokka linter commits", nil); got != "" {
		t.Fatalf("resumed session re-injected: %q", got)
	}
	// A new session starts with an empty set.
	if err := s2.SetSession("sess-b", filepath.Join(t.TempDir(), "other")); err != nil {
		t.Fatal(err)
	}
	if got := s2.Inject("quokka linter commits", nil); !strings.Contains(got, "Quokka linting") {
		t.Fatalf("new session did not get the memory: %q", got)
	}
	// ResetSurfaced lets it return after compaction.
	if err := s2.ResetSurfaced(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Inject("quokka linter commits", nil); got == "" {
		t.Fatal("reset did not allow re-injection")
	}
}

func TestSavePolicyRoundTripAndRefusesUnknownValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", PolicyFile)
	if err := SavePolicy(path, Policy{WriteScope: "global", ReadScope: "project"}); err != nil {
		t.Fatal(err)
	}
	b := readFileString(t, path)
	if !strings.Contains(b, `"writeScope": "global"`) || !strings.Contains(b, `"readScope": "project"`) {
		t.Fatalf("saved file = %s", b)
	}
	if err := SavePolicy(path, Policy{WriteScope: "sometimes"}); err == nil {
		t.Fatal("unknown writeScope was written")
	}
	if err := SavePolicy(path, Policy{ReadScope: "everything"}); err == nil {
		t.Fatal("unknown readScope was written")
	}
	// The empty policy means default and writes no keys.
	if err := SavePolicy(path, Policy{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFileString(t, path), "writeScope") {
		t.Fatal("empty policy wrote keys")
	}
}

func TestPolicyPathNeedsAProjectForProjectScope(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	if _, err := PolicyPath(ScopeProject, ""); err == nil {
		t.Fatal("project scope with no root should fail")
	}
	p, err := PolicyPath(ScopeGlobal, "")
	if err != nil || filepath.Base(p) != PolicyFile {
		t.Fatalf("global path = %q, %v", p, err)
	}
}

func TestHiddenTimestampMovesWithUpdate(t *testing.T) {
	s := mkStores(t)
	db := s.Project
	rec, _, _ := db.Add(Record{Category: CatFact, Title: "Clock", Body: "clock check body"})
	later := time.Now().Add(time.Hour)
	db.now = func() time.Time { return later }
	if err := db.Hide(rec.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := db.ListHidden(5)
	if len(got) != 1 || got[0].Updated.Before(later.Add(-time.Second)) {
		t.Fatalf("hidden updated = %v", got)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
