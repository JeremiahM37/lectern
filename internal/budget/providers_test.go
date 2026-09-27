package budget

import (
	"context"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func usageSession(t *testing.T, db *store.DB, agent string, p5, p7 int, reset float64) {
	t.Helper()
	target, err := db.TargetByName("box")
	if err != nil {
		if target, err = db.InsertTarget(&store.Target{Name: "box", Kind: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: agent, Agent: agent, Workdir: "/x", TmuxSession: "lec-" + agent})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", s.ID, map[string]any{"rate_5h_pct": p5, "rate_5h_reset": reset,
		"rate_7d_pct": p7, "rate_7d_reset": reset + 86400, "usage_at": float64(time.Now().Unix())}); err != nil {
		t.Fatal(err)
	}
}

// Every provider's usage window warns once at 80% by default: push, and the
// card flag the Usage page reads.
func TestProviderUsageWarnsOncePerWindowAtEightyPercent(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	now := time.Now()
	reset := float64(now.Add(time.Hour).Unix())
	usageSession(t, db, "codex", 83, 20, reset)
	usageSession(t, db, "gemini", 60, 10, reset)
	c := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}
	c.Tick(context.Background())
	got := sent()
	if !contains(got, "Codex 5-hour usage at 83%") {
		t.Fatalf("no Codex warning: %v", got)
	}
	if contains(got, "Gemini") || contains(got, "7-day") {
		t.Fatalf("warned below 80%%: %v", got)
	}
	c.Tick(context.Background())
	if len(sent()) != len(got) {
		t.Fatal("resent within the same window")
	}
	wins := ProviderWindows(db, now)
	if len(wins) != 4 {
		t.Fatalf("windows: %+v", wins)
	}
	// A window whose reset has passed is over, and not shown.
	if len(ProviderWindows(db, now.Add(2*time.Hour))) != 2 {
		t.Fatal("expired 5-hour windows still listed")
	}
}

func TestUsageWarnPercentIsConfigurable(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	if _, err := Save(db, Config{UsageWarnPercent: 50}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	usageSession(t, db, "gemini", 60, 10, float64(now.Add(time.Hour).Unix()))
	(&Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}).Tick(context.Background())
	if !contains(sent(), "Gemini CLI 5-hour usage at 60%") {
		t.Fatalf("got %v", sent())
	}
	if DefaultConfig().UsageWarnPercent != 80 || Load(testDB(t)).UsageWarnPercent != 80 {
		t.Fatal("the default warning is 80%")
	}
	if (Config{UsageWarnPercent: 101}).Validate() == nil {
		t.Fatal("accepted 101%")
	}
}
