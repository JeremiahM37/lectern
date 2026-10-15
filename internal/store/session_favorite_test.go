package store

import (
	"path/filepath"
	"testing"
)

// A favorite is a timestamp on the session row. Starring stores when, and
// unstarring clears it, so the Favorites list can tell the two apart and an
// ended session keeps its star for when it is brought back.
func TestSessionFavoriteRoundTripsAndSurvivesEnding(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&Session{TargetID: target.ID, Name: "s", Agent: "claude", Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Session(sess.ID); got.FavoriteAt != nil {
		t.Fatal("a new session must not be a favorite")
	}
	if err := db.SetSessionFavorite(sess.ID, true, 1234); err != nil {
		t.Fatal(err)
	}
	got, err := db.Session(sess.ID)
	if err != nil || got.FavoriteAt == nil || *got.FavoriteAt != 1234 {
		t.Fatalf("starred session: %+v %v", got, err)
	}
	if err := db.Update("sessions", sess.ID, map[string]any{"ended_at": float64(2000)}); err != nil {
		t.Fatal(err)
	}
	ended, _ := db.Session(sess.ID)
	if ended.FavoriteAt == nil {
		t.Fatal("ending a session must keep its favorite")
	}
	if err := db.SetSessionFavorite(sess.ID, false, 5000); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Session(sess.ID); got.FavoriteAt != nil {
		t.Fatal("unstarring must clear favorite_at")
	}
}
