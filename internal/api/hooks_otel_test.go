package api_test

// Cost per outcome (docs/outcomes.md): POST /api/hook/otel/session/{id}/v1/*
// and /api/hook/otel/attempt/{id}/v1/* — Claude Code's own OTLP/HTTP JSON
// exporter, authenticated by the session's/attempt's own existing hook
// token, exactly like the agent hook group in hooks_agentevents_test.go.

import (
	"fmt"
	"testing"
)

const otelHookMetricsFixture = `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
	{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[{"key":"model","value":{"stringValue":"claude-opus-4-6"}}],"asDouble":0.25}]}}
]}]}]}`

const otelHookLogsFixture = `{"resourceLogs":[{"scopeLogs":[{"logRecords":[
	{"attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"cost_usd","value":{"doubleValue":0.01}}]}
]}]}]}`

func TestHookSessionOTelRequiresThatSessionsOwnToken(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "b", "agent": "claude"})
	tokA := hookToken(t, h, a.id())
	tokB := hookToken(t, h, b.id())

	metricsPath := fmt.Sprintf("/api/hook/otel/session/%d/v1/metrics", a.id())
	logsPath := fmt.Sprintf("/api/hook/otel/session/%d/v1/logs", a.id())

	if code := h.status("POST", metricsPath, nil); code != 401 {
		t.Fatalf("no token on metrics: got %d, want 401", code)
	}
	if code, _ := h.request("POST", metricsPath, nil, map[string]string{"Authorization": "Bearer not-a-real-token"}); code != 401 {
		t.Fatalf("wrong token on metrics: got %d, want 401", code)
	}
	if code, _ := h.request("POST", metricsPath, nil, map[string]string{"Authorization": "Bearer " + tokB}); code != 401 {
		t.Fatalf("session B's token against A's endpoint: got %d, want 401", code)
	}
	if code, body := h.rawRequest("POST", metricsPath, otelHookMetricsFixture, tokA); code != 200 {
		t.Fatalf("own token on metrics: got %d, want 200 (%s)", code, body)
	}

	if code := h.status("POST", logsPath, nil); code != 401 {
		t.Fatalf("no token on logs: got %d, want 401", code)
	}
	if code, body := h.rawRequest("POST", logsPath, otelHookLogsFixture, tokA); code != 200 {
		t.Fatalf("own token on logs: got %d, want 200 (%s)", code, body)
	}

	fresh, err := h.App.DB.Session(a.id())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.OtelActiveAt == nil {
		t.Error("otel_active_at should be set after a successful metrics ingest")
	}
	if fresh.CostUSD == nil || *fresh.CostUSD != 0.25 {
		t.Errorf("session cost_usd = %v, want 0.25", fresh.CostUSD)
	}
}

// attemptToken reaches into the store directly, the same way hookToken does
// for a session — the API never returns an attempt's token in a response
// body an unrelated caller could read.
func attemptToken(t *testing.T, h *harness, id int64) string {
	t.Helper()
	att, err := h.App.DB.Attempt(id)
	if err != nil {
		t.Fatal(err)
	}
	if att.Token == "" {
		t.Fatalf("attempt %d has no token", id)
	}
	return att.Token
}

func TestHookAttemptOTelRequiresThatAttemptsOwnToken(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.run(pid, "otel attempt", "echo hi", obj{})
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	tok := attemptToken(t, h, att.ID)

	metricsPath := fmt.Sprintf("/api/hook/otel/attempt/%d/v1/metrics", att.ID)
	logsPath := fmt.Sprintf("/api/hook/otel/attempt/%d/v1/logs", att.ID)

	if code := h.status("POST", metricsPath, nil); code != 401 {
		t.Fatalf("no token: got %d, want 401", code)
	}
	if code, _ := h.request("POST", metricsPath, nil, map[string]string{"Authorization": "Bearer not-a-real-token"}); code != 401 {
		t.Fatalf("wrong token: got %d, want 401", code)
	}
	if code, body := h.rawRequest("POST", metricsPath, otelHookMetricsFixture, tok); code != 200 {
		t.Fatalf("own token on metrics: got %d, want 200 (%s)", code, body)
	}
	if code, body := h.rawRequest("POST", logsPath, otelHookLogsFixture, tok); code != 200 {
		t.Fatalf("own token on logs: got %d, want 200 (%s)", code, body)
	}

	row, err := h.App.DB.OtelAttemptUsageRow(att.ID)
	if err != nil || row == nil {
		t.Fatalf("otel_attempt_usage row missing: %v", err)
	}
	// metrics (overwrite semantics) landed 0.25; logs (additive) added 0.01.
	if row.CostUSD < 0.25999 || row.CostUSD > 0.26001 {
		t.Errorf("cost_usd = %v, want ~0.26", row.CostUSD)
	}
}

func TestHookAttemptOTelUnknownAttemptIs401NotEnumerable(t *testing.T) {
	h := newHarness(t)
	code := h.status("POST", "/api/hook/otel/attempt/999999/v1/metrics", nil)
	if code != 401 {
		t.Fatalf("unknown attempt id: got %d, want 401 (never a distinguishable 404)", code)
	}
}
