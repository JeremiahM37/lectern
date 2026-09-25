package agentevents

import (
	"strconv"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestIngestOTelMetricsBooksDeltaAndMarksActive(t *testing.T) {
	db, in, sess := newTestSession(t)

	first := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
		{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[{"key":"model","value":{"stringValue":"opus"}}],"asDouble":0.5}]}},
		{"name":"claude_code.token.usage","sum":{"dataPoints":[
			{"attributes":[{"key":"type","value":{"stringValue":"input"}}],"asInt":"1000"},
			{"attributes":[{"key":"type","value":{"stringValue":"output"}}],"asInt":"500"}
		]}}
	]}]}]}`
	if err := in.IngestOTelMetrics(sess, []byte(first)); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.OtelActiveAt == nil {
		t.Fatal("otel_active_at should be set after the first successful ingest")
	}
	if fresh.CostUSD == nil || *fresh.CostUSD != 0.5 {
		t.Fatalf("session cost_usd = %v, want 0.5", fresh.CostUSD)
	}
	in2, out2, err := db.UsageDailySessionTotals(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if in2 != 1000 || out2 != 500 {
		t.Fatalf("usage_daily booked %d in / %d out, want 1000/500", in2, out2)
	}

	// A second, larger cumulative reading should book only the DELTA.
	second := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
		{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[],"asDouble":0.8}]}},
		{"name":"claude_code.token.usage","sum":{"dataPoints":[
			{"attributes":[{"key":"type","value":{"stringValue":"input"}}],"asInt":"1500"},
			{"attributes":[{"key":"type","value":{"stringValue":"output"}}],"asInt":"600"}
		]}}
	]}]}]}`
	if err := in.IngestOTelMetrics(fresh, []byte(second)); err != nil {
		t.Fatal(err)
	}
	fresh, err = db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.CostUSD == nil || *fresh.CostUSD != 0.8 {
		t.Fatalf("session cost_usd after second ingest = %v, want 0.8 (latest cumulative)", fresh.CostUSD)
	}
	in2, out2, err = db.UsageDailySessionTotals(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if in2 != 1500 || out2 != 600 {
		t.Fatalf("usage_daily booked totals after second ingest = %d/%d, want 1500/600 (cumulative, not doubled)", in2, out2)
	}
}

func TestIngestStatuslineStopsBookingOnceOTelIsActive(t *testing.T) {
	db, in, sess := newTestSession(t)

	otelBody := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
		{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[{"key":"model","value":{"stringValue":"opus"}}],"asDouble":0.10}]}}
	]}]}]}`
	if err := in.IngestOTelMetrics(sess, []byte(otelBody)); err != nil {
		t.Fatal(err)
	}
	afterOtel, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterOtel.CostUSD == nil || *afterOtel.CostUSD != 0.10 {
		t.Fatalf("cost after OTel = %v, want 0.10", afterOtel.CostUSD)
	}

	// The statusline still ticks (the script is independent of OTel), reporting
	// a much larger cumulative cost — precedence says this must not land.
	if err := in.IngestStatusline(afterOtel, []byte(fixtureStatusline)); err != nil {
		t.Fatal(err)
	}
	afterStatusline, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStatusline.CostUSD == nil || *afterStatusline.CostUSD != 0.10 {
		t.Fatalf("cost_usd changed after statusline while OTel is active: got %v, want unchanged 0.10", afterStatusline.CostUSD)
	}
	// fixtureStatusline's context_window/rate_limits ARE NOT covered by OTel
	// and must still update.
	if afterStatusline.ContextUsedPct == nil || *afterStatusline.ContextUsedPct != 4 {
		t.Errorf("context_used_pct should still update from the statusline: %v", afterStatusline.ContextUsedPct)
	}
	if afterStatusline.Rate5hPct == nil || *afterStatusline.Rate5hPct != 4 {
		t.Errorf("rate_5h_pct should still update from the statusline: %v", afterStatusline.Rate5hPct)
	}

	in2, _, err := db.UsageDailySessionTotals(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// fixtureStatusline reports total_input_tokens 36451 — if it had been
	// allowed to book, this would be nonzero.
	if in2 != 0 {
		t.Errorf("usage_daily must not have booked anything from the suppressed statusline tick, got %d input tokens", in2)
	}
}

func TestIngestOTelMetricsForAttemptOverwritesNotAdds(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&store.Task{ProjectID: proj.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1})
	if err != nil {
		t.Fatal(err)
	}

	first := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
		{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[{"key":"model","value":{"stringValue":"opus"}}],"asDouble":0.3}]}}
	]}]}]}`
	if err := IngestOTelMetricsForAttempt(db, att.ID, []byte(first)); err != nil {
		t.Fatal(err)
	}
	row, err := db.OtelAttemptUsageRow(att.ID)
	if err != nil || row == nil || row.CostUSD != 0.3 {
		t.Fatalf("row after first ingest = %+v, err=%v, want cost 0.3", row, err)
	}

	second := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
		{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[],"asDouble":0.9}]}}
	]}]}]}`
	if err := IngestOTelMetricsForAttempt(db, att.ID, []byte(second)); err != nil {
		t.Fatal(err)
	}
	row, err = db.OtelAttemptUsageRow(att.ID)
	if err != nil || row == nil || row.CostUSD != 0.9 {
		t.Fatalf("row after second ingest = %+v, err=%v, want 0.9 (overwrite, not 1.2)", row, err)
	}
}

func TestIngestOTelLogsForAttemptAdds(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&store.Task{ProjectID: proj.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1})
	if err != nil {
		t.Fatal(err)
	}

	req := func(cost float64) string {
		return `{"resourceLogs":[{"scopeLogs":[{"logRecords":[
			{"attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"cost_usd","value":{"doubleValue":` +
			strconv.FormatFloat(cost, 'f', -1, 64) + `}},{"key":"input_tokens","value":{"intValue":"10"}},{"key":"output_tokens","value":{"intValue":"5"}}]}
		]}]}]}`
	}
	if err := IngestOTelLogsForAttempt(db, att.ID, []byte(req(0.01))); err != nil {
		t.Fatal(err)
	}
	if err := IngestOTelLogsForAttempt(db, att.ID, []byte(req(0.02))); err != nil {
		t.Fatal(err)
	}
	row, err := db.OtelAttemptUsageRow(att.ID)
	if err != nil || row == nil {
		t.Fatalf("row missing: %v", err)
	}
	if row.CostUSD < 0.02999 || row.CostUSD > 0.03001 {
		t.Errorf("cost_usd = %v, want ~0.03 (two api_request events additive)", row.CostUSD)
	}
	if row.InputTokens != 20 || row.OutputTokens != 10 {
		t.Errorf("tokens = %d/%d, want 20/10", row.InputTokens, row.OutputTokens)
	}
}
