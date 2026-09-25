package outcomes

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func bp(b bool) *bool       { return &b }
func i64(v int64) *int64    { return &v }
func fl(v float64) *float64 { return &v }

// seedFact writes one outcome_facts row directly (db.UpsertOutcomeFact),
// bypassing Rebuild's own derivation entirely — these tests are about
// Aggregate's math, not about deriving facts from attempts/sessions (see
// outcomes_test.go for that).
func seedFact(t *testing.T, db *store.DB, f *store.OutcomeFact) {
	t.Helper()
	if f.Date == "" {
		f.Date = "2020-06-15"
	}
	if f.UpdatedAt == 0 {
		f.UpdatedAt = store.Now()
	}
	if err := db.UpsertOutcomeFact(f); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateCostPerPassAndPerAccepted(t *testing.T) {
	db := testDB(t)
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 1, Agent: "claude", Model: "opus",
		CostUSD: 1.0, CostSource: "otel", CheckPassed: bp(true), Accepted: true, LinesKept: i64(50), TimeToPassS: fl(60)})
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 2, Agent: "claude", Model: "opus",
		CostUSD: 2.0, CostSource: "otel", CheckPassed: bp(false)})
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 3, Agent: "claude", Model: "opus",
		CostUSD: 0.5, CostSource: "otel", CheckPassed: bp(true), TimeToPassS: fl(30)})

	out, err := Aggregate(db, "2000-01-01", GroupAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 group, got %d: %+v", len(out), out)
	}
	row := out[0]
	if row.Passed != 2 {
		t.Fatalf("passed = %d, want 2", row.Passed)
	}
	if row.CostPerPass == nil || *row.CostPerPass != 1.75 { // 3.5 / 2
		t.Fatalf("cost_per_pass = %v, want 1.75", row.CostPerPass)
	}
	if row.CostPerAccepted == nil || *row.CostPerAccepted != 3.5 { // 3.5 / 1
		t.Fatalf("cost_per_accepted = %v, want 3.5", row.CostPerAccepted)
	}
	if row.CostPer100Lines == nil || *row.CostPer100Lines != 7.0 { // 3.5 * 100 / 50
		t.Fatalf("cost_per_100_lines = %v, want 7.0", row.CostPer100Lines)
	}
	want := 2.0 / 3.5 * 10 // ~5.714286
	if row.PassesPer10USD == nil {
		t.Fatal("passes_per_10usd is nil")
	} else if diff := *row.PassesPer10USD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("passes_per_10usd = %v, want %v", *row.PassesPer10USD, want)
	}
	if row.MedianTimeToPassS == nil || *row.MedianTimeToPassS != 45 { // median(60,30)
		t.Fatalf("median_time_to_pass_s = %v, want 45", row.MedianTimeToPassS)
	}
}

func TestAggregateRanksHighestSpendFirst(t *testing.T) {
	db := testDB(t)
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 1, Agent: "claude", Model: "opus",
		CostUSD: 1.0, CostSource: "otel", CheckPassed: bp(true), Accepted: true})
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 2, Agent: "codex", Model: "gpt",
		CostUSD: 9.0, CostSource: "otel", CheckPassed: bp(true), Accepted: true})

	out, err := Aggregate(db, "2000-01-01", GroupAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Key != "codex" || out[1].Key != "claude" {
		t.Fatalf("want codex (spend 9.0) ranked before claude (1.0): %+v", out)
	}
}

func TestAggregateGroupByModelAndProject(t *testing.T) {
	db := testDB(t)
	proj := int64(7)
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 1, ProjectID: &proj,
		Agent: "claude", Model: "opus", CostUSD: 1.0, CostSource: "otel", CheckPassed: bp(true)})
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 2, ProjectID: &proj,
		Agent: "claude", Model: "sonnet", CostUSD: 2.0, CostSource: "otel", CheckPassed: bp(true)})

	byModel, err := Aggregate(db, "2000-01-01", GroupModel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(byModel) != 2 {
		t.Fatalf("want 2 model groups, got %d: %+v", len(byModel), byModel)
	}

	byProject, err := Aggregate(db, "2000-01-01", GroupProject, map[int64]string{7: "librarr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byProject) != 1 || byProject[0].Label != "librarr" {
		t.Fatalf("want one 'librarr' project group, got %+v", byProject)
	}
	if byProject[0].CostUSD != 3.0 {
		t.Fatalf("project group cost = %v, want 3.0", byProject[0].CostUSD)
	}
}

func TestAggregatePartialAndEstimatedFlags(t *testing.T) {
	db := testDB(t)
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 1, Agent: "codex", Model: "gpt",
		CostUSD: 0, CostSource: ""})
	out, err := Aggregate(db, "2000-01-01", GroupAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Partial {
		t.Fatalf("a fact with no cost source must mark the group partial: %+v", out)
	}
	if out[0].Estimated {
		t.Fatalf("a fact with no cost source must not also read as estimated: %+v", out)
	}

	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 2, Agent: "codex", Model: "gpt",
		CostUSD: 5, CostSource: "estimated"})
	out, err = Aggregate(db, "2000-01-01", GroupAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].Estimated {
		t.Fatalf("an estimated-cost fact must mark the group estimated: %+v", out)
	}
}

func TestAggregateCutoffExcludesOlderFacts(t *testing.T) {
	db := testDB(t)
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 1, Date: "2019-01-01",
		Agent: "claude", Model: "opus", CostUSD: 1.0, CostSource: "otel"})
	seedFact(t, db, &store.OutcomeFact{Scope: "attempt", RefID: 2, Date: "2020-06-15",
		Agent: "claude", Model: "opus", CostUSD: 2.0, CostSource: "otel"})

	out, err := Aggregate(db, "2020-01-01", GroupAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].CostUSD != 2.0 {
		t.Fatalf("cutoff should drop the 2019 fact: %+v", out)
	}
}

func TestValidGroup(t *testing.T) {
	for _, ok := range []string{"agent", "model", "project"} {
		if !ValidGroup(ok) {
			t.Errorf("%q should be a valid group", ok)
		}
	}
	for _, bad := range []string{"", "task", "AGENT"} {
		if ValidGroup(bad) {
			t.Errorf("%q should not be a valid group", bad)
		}
	}
}
