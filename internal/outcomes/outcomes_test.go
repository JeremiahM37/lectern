package outcomes

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/outcomes.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fixtureProject sets up the target/project graph every attempt needs.
func fixtureProject(t *testing.T, db *store.DB) *store.Project {
	t.Helper()
	target, err := db.InsertTarget(&store.Target{Name: "t1", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p1", TargetID: target.ID, RepoPath: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	return proj
}

// fixtureAttempt creates a task with one attempt, both finished, with the
// given task status (which, together with N==maxN, is what "accepted" reads).
func fixtureAttempt(t *testing.T, db *store.DB, proj *store.Project, taskStatus string) *store.Attempt {
	t.Helper()
	task, err := db.InsertTask(&store.Task{ProjectID: proj.ID, Title: "t", Agent: "claude", Status: taskStatus})
	if err != nil {
		t.Fatal(err)
	}
	started := store.Now() - 120
	finished := store.Now()
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "done",
		StartedAt: &started, FinishedAt: &finished})
	if err != nil {
		t.Fatal(err)
	}
	return att
}

func TestRebuildAttemptCostPrecedenceOtelBeatsResultJSON(t *testing.T) {
	db := testDB(t)
	proj := fixtureProject(t, db)
	att := fixtureAttempt(t, db, proj, "done")
	if err := db.Update("attempts", att.ID, map[string]any{
		"result_json": `{"cost_usd": 9.99, "usage": {"input_tokens": 1000, "output_tokens": 500}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertOtelAttemptUsage(att.ID, "claude-opus-4", 0.42, 100, 50, 0, 0, 0, 0, store.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, err := db.OutcomeFactsSince("2000-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(facts))
	}
	f := facts[0]
	if f.CostUSD != 0.42 {
		t.Errorf("OTel cost should win over result_json: got %v", f.CostUSD)
	}
	if f.CostSource != "otel" {
		t.Errorf("cost_source = %q, want otel", f.CostSource)
	}
	if f.Model != "claude-opus-4" {
		t.Errorf("model = %q, want claude-opus-4", f.Model)
	}
}

func TestRebuildAttemptFallsBackToResultJSONThenEstimate(t *testing.T) {
	db := testDB(t)
	proj := fixtureProject(t, db)

	// result_json present, no OTel.
	att1 := fixtureAttempt(t, db, proj, "done")
	if err := db.Update("attempts", att1.ID, map[string]any{
		"result_json": `{"cost_usd": 1.23, "usage": {"input_tokens": 10, "output_tokens": 5}}`,
	}); err != nil {
		t.Fatal(err)
	}

	// tokens only (codex-style), no cost anywhere, but a price table entry exists.
	att2 := fixtureAttempt(t, db, proj, "done")
	if err := db.Update("attempts", att2.ID, map[string]any{
		"model":       "gpt-codex",
		"result_json": `{"context_tokens": 1000000, "output_tokens": 1000000}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SavePrices(db, PriceConfig{Prices: map[string]ModelPrice{
		"gpt-codex": {InputPer1M: 2, OutputPer1M: 8},
	}}); err != nil {
		t.Fatal(err)
	}

	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, err := db.OutcomeFactsSince("2000-01-01")
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[int64]*store.OutcomeFact{}
	for _, f := range facts {
		byRef[f.RefID] = f
	}
	f1 := byRef[att1.ID]
	if f1 == nil || f1.CostUSD != 1.23 || f1.CostSource != "result_json" {
		t.Fatalf("attempt 1 wants result_json cost 1.23, got %+v", f1)
	}
	f2 := byRef[att2.ID]
	if f2 == nil {
		t.Fatal("attempt 2 fact missing")
	}
	wantEstimate := 1.0*2 + 1.0*8 // 1M tokens each way at $2/$8 per 1M
	if f2.CostUSD != wantEstimate || f2.CostSource != "estimated" {
		t.Fatalf("attempt 2 wants estimated cost %v, got %+v", wantEstimate, f2)
	}
}

func TestRebuildAttemptAcceptedOnlyForWinningLatestAttempt(t *testing.T) {
	db := testDB(t)
	proj := fixtureProject(t, db)
	task, err := db.InsertTask(&store.Task{ProjectID: proj.ID, Title: "t", Agent: "claude", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	started, finished := store.Now()-60, store.Now()
	loser, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, StartedAt: &started, FinishedAt: &finished})
	if err != nil {
		t.Fatal(err)
	}
	winner, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 2, StartedAt: &started, FinishedAt: &finished})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("attempts", winner.ID, map[string]any{
		"diff_stat_json": `[{"additions": 40, "deletions": 10}]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, _ := db.OutcomeFactsSince("2000-01-01")
	byRef := map[int64]*store.OutcomeFact{}
	for _, f := range facts {
		byRef[f.RefID] = f
	}
	if byRef[loser.ID].Accepted {
		t.Error("attempt #1 was not the latest N and must not be marked accepted")
	}
	if !byRef[winner.ID].Accepted {
		t.Error("attempt #2 (highest N, task done) must be marked accepted")
	}
	if byRef[winner.ID].LinesKept == nil || *byRef[winner.ID].LinesKept != 50 {
		t.Errorf("accepted attempt's lines_kept = %v, want 50 (40+10)", byRef[winner.ID].LinesKept)
	}
	if byRef[loser.ID].LinesKept != nil {
		t.Error("a non-accepted attempt must not report lines_kept")
	}
}

func TestRebuildAttemptCheckPassedAndTimeToPass(t *testing.T) {
	db := testDB(t)
	proj := fixtureProject(t, db)
	att := fixtureAttempt(t, db, proj, "done")
	if err := db.Update("attempts", att.ID, map[string]any{
		"verify_json": `{"cmd":"go test ./...", "rc": 0, "output": "ok"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, _ := db.OutcomeFactsSince("2000-01-01")
	f := facts[0]
	if f.CheckPassed == nil || !*f.CheckPassed {
		t.Fatalf("check_passed = %v, want true", f.CheckPassed)
	}
	if f.TimeToPassS == nil || *f.TimeToPassS < 100 {
		t.Errorf("time_to_pass_s = %v, want ~120s", f.TimeToPassS)
	}

	// A non-zero rc is a fail, and an attempt with no verify_json at all is unknown.
	att2 := fixtureAttempt(t, db, proj, "done")
	db.Update("attempts", att2.ID, map[string]any{"verify_json": `{"cmd":"x","rc":1,"output":"boom"}`})
	att3 := fixtureAttempt(t, db, proj, "done")
	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, _ = db.OutcomeFactsSince("2000-01-01")
	byRef := map[int64]*store.OutcomeFact{}
	for _, f := range facts {
		byRef[f.RefID] = f
	}
	if byRef[att2.ID].CheckPassed == nil || *byRef[att2.ID].CheckPassed {
		t.Error("rc=1 must be check_passed=false")
	}
	if byRef[att3.ID].CheckPassed != nil {
		t.Error("no verify_json at all must be check_passed=nil (unknown), not false")
	}
}

func TestRebuildAttemptEvalPass(t *testing.T) {
	db := testDB(t)
	proj := fixtureProject(t, db)
	att := fixtureAttempt(t, db, proj, "done")
	suite, err := db.InsertEvalSuite(&store.EvalSuite{Name: "s", ProjectID: proj.ID})
	if err != nil {
		t.Fatal(err)
	}
	kase, err := db.InsertEvalCase(&store.EvalCase{SuiteID: suite.ID, Name: "c"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.InsertEvalRun(&store.EvalRun{SuiteID: suite.ID})
	if err != nil {
		t.Fatal(err)
	}
	attID := att.ID
	if _, err := db.InsertEvalResult(&store.EvalResult{RunID: run.ID, CaseID: kase.ID, AttemptID: &attID, Status: "passed"}); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, _ := db.OutcomeFactsSince("2000-01-01")
	f := facts[0]
	if f.EvalPass == nil || !*f.EvalPass {
		t.Fatalf("eval_pass = %v, want true", f.EvalPass)
	}
}

func TestRebuildSessionUsesLatestCheckAndOtelPrecedence(t *testing.T) {
	db := testDB(t)
	target, err := db.InsertTarget(&store.Target{Name: "t1", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "s1", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	cost := 3.5
	now := store.Now()
	if err := db.Update("sessions", sess.ID, map[string]any{
		"cost_usd": cost, "otel_active_at": now, "updated_at": now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertSessionCheck(&store.SessionCheck{SessionID: sess.ID, Status: "failed", StartedAt: now - 10, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	passedAt := now + 5
	if _, err := db.InsertSessionCheck(&store.SessionCheck{SessionID: sess.ID, Status: "passed", StartedAt: now, FinishedAt: &passedAt}); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, err := db.OutcomeFactsSince("2000-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("want 1 session fact, got %d", len(facts))
	}
	f := facts[0]
	if f.Scope != "session" || f.RefID != sess.ID {
		t.Fatalf("unexpected fact: %+v", f)
	}
	if f.CheckPassed == nil || !*f.CheckPassed {
		t.Error("latest check (passed) should win over the earlier failed one")
	}
	if f.CostSource != "otel" {
		t.Errorf("cost_source = %q, want otel (otel_active_at set)", f.CostSource)
	}
	if f.CostUSD != cost {
		t.Errorf("cost_usd = %v, want %v", f.CostUSD, cost)
	}
}
