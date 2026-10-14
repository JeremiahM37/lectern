package store

import "testing"

func TestEffectCheckHashDedupeAndEnforcementWindow(t *testing.T) {
	db, err := Open(":memory:")
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

	old := Now() - 7200
	recent := Now()
	if _, err := db.InsertEffectCheck(&EffectCheck{SessionID: sess.ID, MessageHash: "h1", CreatedAt: old, Enforced: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertEffectCheck(&EffectCheck{SessionID: sess.ID, MessageHash: "h2", CreatedAt: recent, Enforced: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertEffectCheck(&EffectCheck{SessionID: sess.ID, MessageHash: "h3", CreatedAt: recent}); err != nil {
		t.Fatal(err)
	}

	hour := Now() - 3600
	n, err := db.CountEnforcedEffectChecks(sess.ID, hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("enforced in the last hour = %d; want 1 (the two hours ago and the unenforced one do not count)", n)
	}

	for hash, want := range map[string]bool{"h1": true, "h2": true, "h3": true, "h4": false} {
		got, err := db.HasEffectCheck(sess.ID, hash)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("HasEffectCheck(%q) = %v; want %v", hash, got, want)
		}
	}

	latest, err := db.LatestEffectCheck(sess.ID)
	if err != nil || latest == nil || latest.MessageHash != "h3" {
		t.Fatalf("latest = %+v, %v; want the h3 row", latest, err)
	}
	if string(latest.Results) != "[]" {
		t.Fatalf("empty results should read back as [], got %s", latest.Results)
	}
}
