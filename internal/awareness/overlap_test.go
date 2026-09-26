package awareness

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// perRowOverlap is the overlap chip exactly as the session list used to
// compute it, one row at a time through Peers — kept here as the reference
// Overlaps must agree with.
func perRowOverlap(t *testing.T, tr *Tracker, row *store.Session) *Overlap {
	t.Helper()
	if row.RepoKey == "" || row.RepoKey == RepoKeyNone {
		return nil
	}
	now := store.Now()
	self, err := tr.RecentFiles(row.ID, now-EditWarnWindow.Seconds(), now)
	if err != nil || len(self) == 0 {
		return nil
	}
	selfPaths := map[string]bool{}
	for _, f := range self {
		selfPaths[f.RelPath] = true
	}
	peers, err := tr.Peers(row)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if p.Kind != "session" {
			continue
		}
		var shared []string
		for _, f := range p.Files {
			if f.AgeSeconds <= EditWarnWindow.Seconds() && selfPaths[f.RelPath] {
				shared = append(shared, f.RelPath)
			}
		}
		if len(shared) > 0 {
			return &Overlap{SessionID: p.SessionID, Name: p.Name, Files: shared}
		}
	}
	return nil
}

func batchOverlaps(t *testing.T, db *store.DB, rows []*store.Session) map[int64]*Overlap {
	t.Helper()
	edits, err := db.SessionFileEditsSince(store.Now() - EditWarnWindow.Seconds())
	if err != nil {
		t.Fatal(err)
	}
	live, err := db.LiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	return Overlaps(rows, live, edits)
}

func TestOverlapsMatchesPerRowPeers(t *testing.T) {
	tr, db, target := newTestTracker(t)
	rng := rand.New(rand.NewSource(7))
	now := store.Now()
	repos := []string{"1:/repo-a/.git", "1:/repo-b/.git", RepoKeyNone, ""}
	for i := 0; i < 40; i++ {
		s := mustSession(t, db, target.ID, fmt.Sprintf("s%02d", i), fmt.Sprintf("/w/%d", i))
		s = setRepoKey(t, db, s, repos[rng.Intn(len(repos))], "/repo")
		// a mix of fresh, borderline and stale edits over a small set of files,
		// so rows share files with some peers and not others
		for j := rng.Intn(5); j > 0; j-- {
			age := []float64{10, 600, 1700, 1900, 4000}[rng.Intn(5)]
			if err := db.UpsertSessionFileEdit(s.ID, s.RepoKey, fmt.Sprintf("f%d.go", rng.Intn(8)), now-age); err != nil {
				t.Fatal(err)
			}
		}
		if rng.Intn(6) == 0 { // an ended session is never anyone's peer
			if err := db.Update("sessions", s.ID, map[string]any{"ended_at": now, "status": "dead"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := db.Sessions(true)
	if err != nil {
		t.Fatal(err)
	}
	got := batchOverlaps(t, db, rows)
	matched := 0
	for _, row := range rows {
		want := perRowOverlap(t, tr, row)
		if !reflect.DeepEqual(got[row.ID], want) {
			t.Fatalf("session %d (%s, repo %q): batch %+v, per-row %+v", row.ID, row.Name, row.RepoKey, got[row.ID], want)
		}
		if want != nil {
			matched++
		}
	}
	if matched < 5 {
		t.Fatalf("scenario too sparse to prove anything: only %d overlaps", matched)
	}
}

func TestOverlapsPicksFirstLivePeerAndItsFileOrder(t *testing.T) {
	_, db, target := newTestTracker(t)
	now := store.Now()
	key := "1:/repo/.git"
	a := setRepoKey(t, db, mustSession(t, db, target.ID, "a", "/w/a"), key, "/repo")
	b := setRepoKey(t, db, mustSession(t, db, target.ID, "b", "/w/b"), key, "/repo")
	c := setRepoKey(t, db, mustSession(t, db, target.ID, "c", "/w/c"), key, "/repo")
	other := setRepoKey(t, db, mustSession(t, db, target.ID, "other-repo", "/w/o"), "1:/elsewhere/.git", "/elsewhere")
	edit := func(s *store.Session, path string, age float64) {
		t.Helper()
		if err := db.UpsertSessionFileEdit(s.ID, s.RepoKey, path, now-age); err != nil {
			t.Fatal(err)
		}
	}
	edit(a, "x.go", 5)
	edit(a, "y.go", 5)
	edit(b, "y.go", 30)
	edit(b, "x.go", 20)
	edit(c, "x.go", 1)
	edit(other, "x.go", 1)
	rows := []*store.Session{a, b, c, other}
	got := batchOverlaps(t, db, rows)
	want := &Overlap{SessionID: b.ID, Name: "b", Files: []string{"x.go", "y.go"}}
	if !reflect.DeepEqual(got[a.ID], want) {
		t.Fatalf("a: got %+v want %+v", got[a.ID], want)
	}
	if got[c.ID] == nil || got[c.ID].SessionID != a.ID {
		t.Fatalf("c should overlap the first live peer, a: %+v", got[c.ID])
	}
	if got[other.ID] != nil {
		t.Fatalf("a session in another repository overlapped: %+v", got[other.ID])
	}
}
