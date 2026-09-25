package budget

import (
	"fmt"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/budget.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDefaultConfigIsInertAndValid(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
	if cfg.Overall.hasCap() {
		t.Error("a fresh install must start with nothing capped")
	}
	if cfg.Overall.Mode != ModeWarn {
		t.Errorf("default mode should be warn, got %q", cfg.Overall.Mode)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	bad := []Config{
		{Overall: Limit{DailyUSD: -1}},
		{Overall: Limit{Mode: "yolo"}},
		{PerAgent: map[string]Limit{"": {DailyUSD: 5}}},
		{PerAgent: map[string]Limit{"claude": {Mode: "nope"}}},
		{Thresholds: []int{0, 75}},
		{Thresholds: []int{101}},
		{QuotaThresholds: []int{-5}},
		{AnomalyMultiplier: 0.5},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d: expected a validation error for %+v", i, cfg)
		}
	}
}

func TestLoadSaveRoundtrip(t *testing.T) {
	db := testDB(t)
	// Load on a fresh DB returns defaults, not an error.
	got := Load(db)
	if got.Overall.hasCap() {
		t.Fatal("fresh DB should load defaults")
	}
	cfg := Config{
		Overall:    Limit{DailyUSD: 10, WeeklyUSD: 50, Mode: ModeStop},
		PerAgent:   map[string]Limit{"codex": {DailyUSD: 2, Mode: ModeWarn}},
		Thresholds: []int{50, 90, 100},
	}
	if _, err := Save(db, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded := Load(db)
	if reloaded.Overall.DailyUSD != 10 || reloaded.Overall.WeeklyUSD != 50 || reloaded.Overall.Mode != ModeStop {
		t.Errorf("overall did not round-trip: %+v", reloaded.Overall)
	}
	if reloaded.PerAgent["codex"].DailyUSD != 2 {
		t.Errorf("per-agent did not round-trip: %+v", reloaded.PerAgent)
	}
	// normalize() must have filled in the quota default that was never set.
	if len(reloaded.QuotaThresholds) == 0 {
		t.Error("quota_thresholds should default when unset")
	}
}

func TestSaveRejectsInvalidConfig(t *testing.T) {
	db := testDB(t)
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: -1}}); err == nil {
		t.Fatal("expected a validation error")
	}
	// nothing should have been persisted
	if Load(db).Overall.hasCap() {
		t.Error("an invalid save must not have written anything")
	}
}

var seedSpendSeq int

func seedSpend(t *testing.T, db *store.DB, agent string, costUSD float64, when time.Time) {
	t.Helper()
	seedSpendSeq++
	suffix := fmt.Sprintf("%s-%d", agent, seedSpendSeq)
	target, err := db.InsertTarget(&store.Target{Name: "t-" + suffix, Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p-" + suffix, TargetID: target.ID, RepoPath: "/mock"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&store.Task{ProjectID: proj.ID, Title: "t", Agent: agent})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1})
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := float64(when.Unix())
	startedAt := finishedAt - 3600
	if err := db.Update("attempts", att.ID, map[string]any{
		"status": "done", "started_at": startedAt, "finished_at": finishedAt,
		"result_json": store.J(map[string]any{"cost_usd": costUSD}),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGateBlocksOnlyStopModeAtOrOver100(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	seedSpend(t, db, "claude", 12.00, now)

	// warn mode: never blocks, however much was spent
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: 10, Mode: ModeWarn}}); err != nil {
		t.Fatal(err)
	}
	if err := Gate(db, ""); err != nil {
		t.Errorf("warn mode must never block: %v", err)
	}

	// stop mode, under the cap: allowed
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: 100, Mode: ModeStop}}); err != nil {
		t.Fatal(err)
	}
	if err := Gate(db, ""); err != nil {
		t.Errorf("under cap must not block: %v", err)
	}

	// stop mode, at/over the cap: blocked
	if _, err := Save(db, Config{Overall: Limit{DailyUSD: 10, Mode: ModeStop}}); err != nil {
		t.Fatal(err)
	}
	if err := Gate(db, ""); err == nil {
		t.Error("stop mode at 100% should block")
	}
}

func TestGatePerAgentIsIndependentOfOverall(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	seedSpend(t, db, "codex", 12.00, now)
	if _, err := Save(db, Config{
		Overall:  Limit{}, // no overall cap at all
		PerAgent: map[string]Limit{"codex": {DailyUSD: 5, Mode: ModeStop}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Gate(db, "codex"); err == nil {
		t.Error("codex's own exhausted budget should block a codex dispatch")
	}
	if err := Gate(db, "claude"); err != nil {
		t.Errorf("claude has no cap of its own and should not be blocked: %v", err)
	}
	if err := Gate(db, ""); err != nil {
		t.Errorf("no agent named means only the overall (uncapped) limit applies: %v", err)
	}
}

func TestPeriodKeysAreStableWithinAPeriod(t *testing.T) {
	monday := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC) // a Monday
	sameWeekLater := monday.Add(5 * 24 * time.Hour)
	if periodKeyWeekly(monday) != periodKeyWeekly(sameWeekLater) {
		t.Errorf("same ISO week should share a period key: %q vs %q",
			periodKeyWeekly(monday), periodKeyWeekly(sameWeekLater))
	}
	nextWeek := monday.Add(8 * 24 * time.Hour)
	if periodKeyWeekly(monday) == periodKeyWeekly(nextWeek) {
		t.Error("a week later must be a different period key")
	}
	if periodKeyDaily(monday) == periodKeyDaily(monday.Add(24*time.Hour)) {
		t.Error("a day later must be a different period key")
	}
}
