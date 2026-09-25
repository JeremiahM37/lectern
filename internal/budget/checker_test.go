package budget

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// spyNotifier mirrors internal/alerts_test.go's pattern: a real ntfy sink so
// BuildPayloads always produces something Hook can capture, no network.
func spyNotifier(t *testing.T, db *store.DB) (*sinks.Notifier, func() []string) {
	t.Helper()
	if err := db.SetSetting("ntfy_server", "https://ntfy.example"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("ntfy_topic", "lec"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var titles []string
	n := &sinks.Notifier{DB: db, BaseURL: "http://lectern", Log: slog.Default(),
		Hook: func(p []sinks.Payload) {
			mu.Lock()
			defer mu.Unlock()
			for _, pay := range p {
				if title, ok := pay.Body["title"].(string); ok {
					titles = append(titles, title)
				}
			}
		}}
	return n, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), titles...)
	}
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestCheckerFiresEachThresholdExactlyOncePerPeriod(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	now := time.Now()
	seedSpend(t, db, "claude", 9.50, now) // 95% of a $10 daily cap
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: 10, Mode: ModeWarn}, Thresholds: []int{75, 90, 100}}); err != nil {
		t.Fatal(err)
	}
	c := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}

	c.Tick(context.Background())
	first := sent()
	if !contains(first, "75%") || !contains(first, "90%") {
		t.Fatalf("crossing straight to 95%% should fire both 75%% and 90%%: %v", first)
	}
	for _, title := range first {
		if strings.Contains(title, "100%") || strings.Contains(title, "exhausted") {
			t.Errorf("95%% spend must not fire the 100%% threshold: %v", first)
		}
	}

	// Ticking again with nothing changed must not re-send 75%/90%.
	c.Tick(context.Background())
	if len(sent()) != len(first) {
		t.Errorf("re-ticking with no new spend re-sent alerts: before=%v after=%v", first, sent())
	}

	// Now cross 100%: only the new threshold fires.
	seedSpend(t, db, "claude", 1.00, now)
	c.Tick(context.Background())
	final := sent()
	if len(final) != len(first)+1 {
		t.Fatalf("crossing 100%% should add exactly one alert: before=%v after=%v", first, final)
	}
	if !contains(final, "exhausted") {
		t.Errorf("the new alert should be the 100%% one: %v", final)
	}
}

func TestCheckerRestartDoesNotResend(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	now := time.Now()
	seedSpend(t, db, "claude", 10.00, now)
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: 10, Mode: ModeWarn}}); err != nil {
		t.Fatal(err)
	}
	c1 := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}
	c1.Tick(context.Background())
	before := sent()
	if len(before) == 0 {
		t.Fatal("expected at least one alert at 100%")
	}
	// A fresh Checker (simulating a process restart — no in-memory state
	// carried over) must see the same persisted budget_alerts_sent rows and
	// not resend.
	c2 := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}
	c2.Tick(context.Background())
	if len(sent()) != len(before) {
		t.Errorf("a fresh Checker resent alerts after 'restart': before=%v after=%v", before, sent())
	}
}

func TestCheckerNewPeriodFiresAgain(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	day1 := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	seedSpend(t, db, "claude", 10.00, day1)
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: 10, Mode: ModeWarn}}); err != nil {
		t.Fatal(err)
	}
	c := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return day1 }}
	c.Tick(context.Background())
	firstCount := len(sent())
	if firstCount == 0 {
		t.Fatal("expected an alert on day 1")
	}

	day2 := day1.Add(24 * time.Hour)
	seedSpend(t, db, "claude", 10.00, day2)
	c.Clock = func() time.Time { return day2 }
	c.Tick(context.Background())
	if len(sent()) <= firstCount {
		t.Errorf("a new daily period must be able to alert again: %v", sent())
	}
}

func TestCheckerQuotaThresholds(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	now := time.Now()
	if err := db.SetSetting("rate_limits", store.J(map[string]any{
		"at":        float64(now.Unix()),
		"five_hour": map[string]any{"used_percentage": 80.0, "resets_at": float64(now.Add(time.Hour).Unix())},
		"seven_day": map[string]any{"used_percentage": 50.0, "resets_at": float64(now.Add(24 * time.Hour).Unix())},
	})); err != nil {
		t.Fatal(err)
	}
	c := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}
	c.Tick(context.Background())
	got := sent()
	if !contains(got, "5-hour") {
		t.Errorf("80%% five-hour usage should cross the default 75%% threshold: %v", got)
	}
	if contains(got, "7-day") {
		t.Errorf("50%% seven-day usage should not alert: %v", got)
	}
	// Re-ticking the same window must not resend.
	before := len(got)
	c.Tick(context.Background())
	if len(sent()) != before {
		t.Error("quota alert resent within the same window")
	}
}

func TestCheckerAnomalyDetection(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	now := time.Now()

	// Baseline: several cheap finished sessions, ~$1/hour.
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "baseline", Agent: "claude",
			Workdir: "/x", TmuxSession: "lec-base"})
		if err != nil {
			t.Fatal(err)
		}
		createdAt := float64(now.Add(-2 * time.Hour).Unix())
		endedAt := float64(now.Add(-1 * time.Hour).Unix())
		if err := db.Update("sessions", sess.ID, map[string]any{
			"created_at": createdAt, "ended_at": endedAt, "cost_usd": 1.0,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// An active session running hot: $1/minute for 10 minutes => $60/hour,
	// far more than 3x the ~$1/hour baseline.
	hot, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "runaway", Agent: "claude",
		Workdir: "/x", TmuxSession: "lec-hot"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", hot.ID, map[string]any{
		"created_at": float64(now.Add(-10 * time.Minute).Unix()), "cost_usd": 10.0,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Save(db, Config{AnomalyEnabled: true, AnomalyMultiplier: 3}); err != nil {
		t.Fatal(err)
	}
	c := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}
	c.Tick(context.Background())
	got := sent()
	if !contains(got, "runaway") {
		t.Errorf("the hot session should trigger an anomaly alert: %v", got)
	}

	// Same day, same entity: must not re-fire.
	before := len(got)
	c.Tick(context.Background())
	if len(sent()) != before {
		t.Error("anomaly alert resent for the same session on the same day")
	}
}

func TestCheckerAnomalyDisabledDoesNothing(t *testing.T) {
	db := testDB(t)
	notifier, sent := spyNotifier(t, db)
	now := time.Now()
	target, _ := db.InsertTarget(&store.Target{Name: "t", Kind: "mock"})
	for i := 0; i < 4; i++ {
		sess, _ := db.InsertSession(&store.Session{TargetID: target.ID, Name: "baseline", Agent: "claude",
			Workdir: "/x", TmuxSession: "lec-base"})
		db.Update("sessions", sess.ID, map[string]any{
			"created_at": float64(now.Add(-2 * time.Hour).Unix()),
			"ended_at":   float64(now.Add(-1 * time.Hour).Unix()), "cost_usd": 1.0,
		})
	}
	hot, _ := db.InsertSession(&store.Session{TargetID: target.ID, Name: "runaway", Agent: "claude",
		Workdir: "/x", TmuxSession: "lec-hot"})
	db.Update("sessions", hot.ID, map[string]any{
		"created_at": float64(now.Add(-10 * time.Minute).Unix()), "cost_usd": 10.0,
	})
	if _, err := Save(db, Config{AnomalyEnabled: false}); err != nil {
		t.Fatal(err)
	}
	c := &Checker{DB: db, Notifier: notifier, Clock: func() time.Time { return now }}
	c.Tick(context.Background())
	if contains(sent(), "runaway") {
		t.Error("anomaly detection must be skippable")
	}
}
