package memory

import (
	"testing"
	"time"
)

func TestStaleCountMatchesTheStaleActionRule(t *testing.T) {
	s := mkStores(t)
	old := fmtTime(time.Now().Add(-120 * 24 * time.Hour))
	add := func(db *DB, title string, pinned bool) {
		t.Helper()
		rec, _, err := db.Add(Record{Category: CatFact, Title: title, Body: title + " body text"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec("UPDATE memories SET last_used = ?, uses = 0, pinned = ? WHERE id = ?", old, boolInt(pinned), rec.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.StaleCount(); n != 0 {
		t.Fatalf("empty stores: StaleCount = %d", n)
	}
	add(s.Project, "old project fact", false)
	add(s.Global, "old global fact", false)
	add(s.Project, "old but pinned", true)
	if _, _, err := s.Project.Add(Record{Category: CatFact, Title: "recent fact", Body: "recent body text"}); err != nil {
		t.Fatal(err)
	}
	if n := s.StaleCount(); n != 2 {
		t.Fatalf("StaleCount = %d, want 2 (old, unpinned, both stores)", n)
	}
}
