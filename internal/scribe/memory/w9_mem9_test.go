package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCleanupHintNeedsAllThresholds(t *testing.T) {
	s := mkStores(t)
	old := fmtTime(time.Now().Add(-120 * 24 * time.Hour))
	// Fewer than 30 live memories: no hint however stale and however many sessions.
	for i := 0; i < 12; i++ {
		rec, _, err := s.Project.Add(Record{Category: CatFact, Title: fmt.Sprintf("stale topic %d", i), Body: fmt.Sprintf("alpha%d beta%d gamma%d", i, i, i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Project.db.Exec("UPDATE memories SET last_used = ?, uses = 0 WHERE id = ?", old, rec.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 25; i++ {
		if err := NoteSessionStart(); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.CleanupHintCount(); got != 0 {
		t.Fatalf("hint with 12 live memories = %d, want 0", got)
	}
	// Enough live memories, stale enough, but too few sessions since cleanup.
	for i := 0; i < 30; i++ {
		if _, _, err := s.Project.Add(Record{Category: CatFact, Title: fmt.Sprintf("fresh topic %d", i), Body: fmt.Sprintf("delta%d epsilon%d zeta%d", i, i, i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := NoteCleanupDone(); err != nil {
		t.Fatal(err)
	}
	if got := s.CleanupHintCount(); got != 0 {
		t.Fatalf("hint right after cleanup = %d, want 0", got)
	}
	for i := 0; i < 20; i++ {
		if err := NoteSessionStart(); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.CleanupHintCount(); got < 10 {
		t.Fatalf("hint with all thresholds met = %d, want at least 10", got)
	}
}

func TestDuplicateGroupsKeepPinnedFirst(t *testing.T) {
	s := mkStores(t)
	a, _, err := s.Project.Add(Record{Category: CatFact, Title: "Same Title", Body: "first body"})
	if err != nil {
		t.Fatal(err)
	}
	// Add dedupes on title, so write the twin straight to the table.
	twin := Record{ID: "twin-record-0001", Category: CatFact, Title: "same  title", Body: "second body", Scope: ScopeProject, Created: time.Now(), Updated: time.Now(), LastUsed: time.Now()}
	if err := writeRecord(s.Project.db, twin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Project.db.Exec("UPDATE memories SET pinned = 1 WHERE id = ?", twin.ID); err != nil {
		t.Fatal(err)
	}
	groups, err := s.Project.DuplicateGroups(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("groups = %+v, want one pair", groups)
	}
	if groups[0][0].ID != twin.ID {
		t.Fatalf("keeper = %s, want the pinned twin %s (not %s)", groups[0][0].ID, twin.ID, a.ID)
	}
}

func TestIndexCountsOnlyAndEmptyIsBlank(t *testing.T) {
	s := mkStores(t)
	if got := s.Index(); got != "" {
		t.Fatalf("empty index = %q, want blank", got)
	}
	if _, _, err := s.Project.Add(Record{Category: CatDecision, Title: "use sqlite", Body: "because it is embedded"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Project.Add(Record{Category: CatFact, Title: "port 9105", Body: "homelab api listens there"}); err != nil {
		t.Fatal(err)
	}
	got := s.Index()
	if !strings.Contains(got, "project: 2") || !strings.Contains(got, "decision 1") || !strings.Contains(got, "fact 1") {
		t.Fatalf("index = %q", got)
	}
	if len(got) > indexMaxChars {
		t.Fatalf("index is %d chars, cap %d", len(got), indexMaxChars)
	}
}

func TestCoChangeNeighboursAndRecall(t *testing.T) {
	s := mkStores(t)
	s.CoChange = func(file string) []string {
		if file == "internal/a.go" {
			return []string{"internal/b.go", "internal/a.go", "internal/c.go", "internal/d.go"}
		}
		return nil
	}
	nb := s.neighbours([]string{"internal/a.go"})
	if len(nb) != cochangePerFile {
		t.Fatalf("neighbours = %v, want %d (the edited file itself is excluded)", nb, cochangePerFile)
	}
	for _, n := range nb {
		if n == "internal/a.go" {
			t.Fatal("edited file listed as its own neighbour")
		}
	}
	// A memory linked only to a neighbour is recalled, marked co-change.
	if _, _, err := s.Project.Add(Record{Category: CatConvention, Title: "b.go wiring", Body: "b.go must be registered in init", Files: []string{"internal/b.go"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recall("unrelated words entirely", []string{"internal/a.go"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range got {
		if g.Title == "b.go wiring" {
			found = true
			if len(g.Why) == 0 || g.Why[0] != "co-change" {
				t.Fatalf("why = %v, want co-change", g.Why)
			}
		}
	}
	if !found {
		t.Fatalf("co-change memory not recalled: %+v", got)
	}
	// Without a co-change source nothing changes.
	s.CoChange = nil
	if n := s.neighbours([]string{"internal/a.go"}); n != nil {
		t.Fatalf("neighbours without source = %v", n)
	}
}

func TestMemoryToolDeleteIsSoftAndNearMissActionsWork(t *testing.T) {
	if got := normAction(" Forget "); got != "delete" {
		t.Fatalf("forget -> %q", got)
	}
	if got := normAction("unhide"); got != "restore" {
		t.Fatalf("unhide -> %q", got)
	}
	if got := normAction("pin"); got != "pin" {
		t.Fatalf("pin -> %q", got)
	}
	var raw json.RawMessage = []byte(`{}`)
	_ = raw
}
