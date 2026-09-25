package agentevents

import "testing"

// otelMetricsFixture is a realistic OTLP/HTTP JSON metrics export, built
// from Claude Code's own documented metric names and attributes
// (https://code.claude.com/docs/en/monitoring-usage — see docs/outcomes.md).
// asInt uses OTLP's actual int64-as-JSON-string encoding on one data point
// and a bare number on another, since a real exporter and a hand-built test
// fixture might do either and this package must tolerate both.
const otelMetricsFixture = `{
  "resourceMetrics": [{
    "resource": {"attributes": [{"key":"lectern.session_id","value":{"stringValue":"42"}}]},
    "scopeMetrics": [{
      "metrics": [
        {"name":"claude_code.cost.usage","sum":{"dataPoints":[
          {"attributes":[{"key":"model","value":{"stringValue":"claude-opus-4-6"}}],"asDouble":0.1234}
        ]}},
        {"name":"claude_code.token.usage","sum":{"dataPoints":[
          {"attributes":[{"key":"type","value":{"stringValue":"input"}},{"key":"model","value":{"stringValue":"claude-opus-4-6"}}],"asInt":"1000"},
          {"attributes":[{"key":"type","value":{"stringValue":"output"}}],"asInt":500},
          {"attributes":[{"key":"type","value":{"stringValue":"cacheRead"}}],"asInt":"150"},
          {"attributes":[{"key":"type","value":{"stringValue":"cacheCreation"}}],"asInt":"50"}
        ]}},
        {"name":"claude_code.lines_of_code.count","sum":{"dataPoints":[
          {"attributes":[{"key":"type","value":{"stringValue":"added"}}],"asInt":"12"},
          {"attributes":[{"key":"type","value":{"stringValue":"removed"}}],"asInt":"3"}
        ]}},
        {"name":"claude_code.pull_request.count","sum":{"dataPoints":[{"attributes":[],"asInt":"1"}]}},
        {"name":"claude_code.commit.count","sum":{"dataPoints":[{"attributes":[],"asInt":"2"}]}},
        {"name":"claude_code.session.count","sum":{"dataPoints":[{"attributes":[{"key":"start_type","value":{"stringValue":"fresh"}}],"asInt":"1"}]}}
      ]
    }]
  }]
}`

func TestParseOTLPMetricsFixture(t *testing.T) {
	m, err := ParseOTLPMetrics([]byte(otelMetricsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if m.CostUSD != 0.1234 {
		t.Errorf("cost_usd = %v, want 0.1234", m.CostUSD)
	}
	// input(1000) + cacheRead(150) + cacheCreation(50) = 1200
	if m.InputTokens != 1200 {
		t.Errorf("input tokens = %d, want 1200", m.InputTokens)
	}
	if m.OutputTokens != 500 {
		t.Errorf("output tokens = %d, want 500", m.OutputTokens)
	}
	if m.LinesAdded != 12 || m.LinesRemoved != 3 {
		t.Errorf("lines = +%d/-%d, want +12/-3", m.LinesAdded, m.LinesRemoved)
	}
	if m.PullRequests != 1 {
		t.Errorf("pull_requests = %d, want 1", m.PullRequests)
	}
	if m.Commits != 2 {
		t.Errorf("commits = %d, want 2", m.Commits)
	}
	if m.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want claude-opus-4-6", m.Model)
	}
	// claude_code.session.count is an unread metric name — it must not
	// crash or otherwise pollute the summary.
}

func TestParseOTLPMetricsSumsMultipleDataPointsAndScopes(t *testing.T) {
	body := `{"resourceMetrics":[
		{"scopeMetrics":[{"metrics":[
			{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[],"asDouble":1.0}]}}
		]}]},
		{"scopeMetrics":[{"metrics":[
			{"name":"claude_code.cost.usage","sum":{"dataPoints":[{"attributes":[],"asDouble":2.5}]}}
		]}]}
	]}`
	m, err := ParseOTLPMetrics([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if m.CostUSD != 3.5 {
		t.Errorf("cost_usd across two resourceMetrics blocks = %v, want 3.5", m.CostUSD)
	}
}

func TestParseOTLPMetricsGaugeShape(t *testing.T) {
	body := `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[
		{"name":"claude_code.cost.usage","gauge":{"dataPoints":[{"attributes":[],"asDouble":9.9}]}}
	]}]}]}`
	m, err := ParseOTLPMetrics([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if m.CostUSD != 9.9 {
		t.Errorf("gauge-shaped metric should parse the same as sum: got %v", m.CostUSD)
	}
}

func TestParseOTLPMetricsEmptyAndMalformed(t *testing.T) {
	m, err := ParseOTLPMetrics([]byte(`{}`))
	if err != nil || m == nil || m.CostUSD != 0 {
		t.Fatalf("empty body should parse to a zero summary, got %+v err=%v", m, err)
	}
	if _, err := ParseOTLPMetrics([]byte(`not json`)); err == nil {
		t.Error("malformed JSON should error — the caller decides how lenient to be")
	}
}

const otelLogsFixture = `{
  "resourceLogs": [{
    "scopeLogs": [{
      "logRecords": [
        {"attributes":[
          {"key":"event.name","value":{"stringValue":"api_request"}},
          {"key":"model","value":{"stringValue":"claude-opus-4-6"}},
          {"key":"cost_usd","value":{"doubleValue":0.02}},
          {"key":"input_tokens","value":{"intValue":"100"}},
          {"key":"output_tokens","value":{"intValue":"50"}}
        ]},
        {"attributes":[
          {"key":"event.name","value":{"stringValue":"api_request"}},
          {"key":"cost_usd","value":{"doubleValue":0.01}},
          {"key":"input_tokens","value":{"intValue":"40"}},
          {"key":"output_tokens","value":{"intValue":"20"}}
        ]},
        {"attributes":[{"key":"event.name","value":{"stringValue":"user_prompt"}},{"key":"prompt_length","value":{"intValue":"12"}}]}
      ]
    }]
  }]
}`

func TestParseOTLPLogsFixtureCountsOnlyAPIRequests(t *testing.T) {
	l, err := ParseOTLPLogs([]byte(otelLogsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if l.APIRequests != 2 {
		t.Fatalf("api_requests = %d, want 2 (user_prompt must not count)", l.APIRequests)
	}
	if l.CostUSD != 0.03 {
		t.Errorf("cost_usd = %v, want 0.03 (0.02+0.01)", l.CostUSD)
	}
	if l.InputTokens != 140 || l.OutputTokens != 70 {
		t.Errorf("tokens = %d in / %d out, want 140/70", l.InputTokens, l.OutputTokens)
	}
	if l.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want claude-opus-4-6", l.Model)
	}
}

func TestParseOTLPLogsNoAPIRequestEvents(t *testing.T) {
	body := `{"resourceLogs":[{"scopeLogs":[{"logRecords":[
		{"attributes":[{"key":"event.name","value":{"stringValue":"tool_result"}}]}
	]}]}]}`
	l, err := ParseOTLPLogs([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if l.APIRequests != 0 || l.CostUSD != 0 {
		t.Errorf("no api_request records should summarise to zero: %+v", l)
	}
}
