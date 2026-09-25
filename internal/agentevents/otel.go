// Package agentevents' otel.go decodes the OTLP/HTTP JSON payloads Claude
// Code's own OpenTelemetry exporter sends when CLAUDE_CODE_ENABLE_TELEMETRY=1
// and OTEL_EXPORTER_OTLP_PROTOCOL=http/json — see
// https://code.claude.com/docs/en/monitoring-usage and docs/outcomes.md for
// the field names this was built against.
//
// Only the shapes lectern reads are declared; OTLP's JSON mapping (the
// protobuf JSON encoding) serializes every int64/uint64 as a JSON STRING, not
// a bare number — asInt and timeUnixNano both do this — so otlpInt below
// tolerates a quoted OR bare number rather than assuming the newer/stricter
// form.
package agentevents

import (
	"encoding/json"
	"strconv"
)

// otlpInt accepts either a JSON string ("1234", OTLP's own int64 mapping) or
// a bare JSON number, so a fixture written either way still parses.
type otlpInt int64

func (v *otlpInt) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" || s == "null" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*v = otlpInt(n)
	return nil
}

type otlpAnyValue struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *otlpInt `json:"intValue"`
	DoubleValue *float64 `json:"doubleValue"`
	BoolValue   *bool    `json:"boolValue"`
}

// asString renders whichever variant is set, for attribute values lectern
// only ever reads as text (model, type, event.name, ...).
func (v otlpAnyValue) asString() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return strconv.FormatInt(int64(*v.IntValue), 10)
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'g', -1, 64)
	case v.BoolValue != nil:
		if *v.BoolValue {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func (v otlpAnyValue) asFloat() float64 {
	switch {
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.IntValue != nil:
		return float64(*v.IntValue)
	default:
		return 0
	}
}

type otlpKV struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

func kvLookup(attrs []otlpKV, key string) (otlpAnyValue, bool) {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return otlpAnyValue{}, false
}

func kvString(attrs []otlpKV, key string) string {
	v, ok := kvLookup(attrs, key)
	if !ok {
		return ""
	}
	return v.asString()
}

// ---- metrics --------------------------------------------------------------

type otlpNumberDataPoint struct {
	Attributes []otlpKV `json:"attributes"`
	AsDouble   *float64 `json:"asDouble"`
	AsInt      *otlpInt `json:"asInt"`
}

func (p otlpNumberDataPoint) value() float64 {
	switch {
	case p.AsDouble != nil:
		return *p.AsDouble
	case p.AsInt != nil:
		return float64(*p.AsInt)
	default:
		return 0
	}
}

type otlpAggregation struct {
	DataPoints []otlpNumberDataPoint `json:"dataPoints"`
}

type otlpMetric struct {
	Name  string           `json:"name"`
	Sum   *otlpAggregation `json:"sum"`
	Gauge *otlpAggregation `json:"gauge"`
}

func (m otlpMetric) dataPoints() []otlpNumberDataPoint {
	if m.Sum != nil {
		return m.Sum.DataPoints
	}
	if m.Gauge != nil {
		return m.Gauge.DataPoints
	}
	return nil
}

type otlpScopeMetrics struct {
	Metrics []otlpMetric `json:"metrics"`
}

type otlpResourceMetrics struct {
	Resource struct {
		Attributes []otlpKV `json:"attributes"`
	} `json:"resource"`
	ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
}

type otlpMetricsRequest struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}

// Claude Code's own metric names (see the package doc's docs link). Exported
// so tests and internal/outcomes can build fixtures without repeating the
// literal strings.
const (
	MetricCostUsage    = "claude_code.cost.usage"
	MetricTokenUsage   = "claude_code.token.usage"
	MetricLinesOfCode  = "claude_code.lines_of_code.count"
	MetricPullRequests = "claude_code.pull_request.count"
	MetricCommits      = "claude_code.commit.count"
)

// OTelMetricsSummary is everything internal/outcomes needs out of one OTLP
// metrics export. Every *USD/*Tokens/*Lines/*PullRequests/*Commits field is
// the SUM, across every data point this one payload carried (i.e. across
// every distinct attribute combination — typically one per model), of that
// metric's value — which is the correct way to fold multiple cumulative
// series into one running total. See IngestOTelMetrics for how the caller
// turns that running total into a delta.
type OTelMetricsSummary struct {
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
	LinesAdded   int64
	LinesRemoved int64
	PullRequests int64
	Commits      int64
	// Model is the most recently seen model attribute across every data
	// point in this payload — good enough for "what was this session/attempt
	// using", not a per-model breakdown (usage_daily already buckets by
	// model on the DELTA this produces, not on this summary).
	Model string
}

// ParseOTLPMetrics decodes one OTLP/HTTP JSON metrics export request body.
// Unknown metric names are ignored — this reads exactly the five Claude Code
// metrics named above and nothing else, so a future Claude Code adding new
// metrics never breaks ingestion.
func ParseOTLPMetrics(body []byte) (*OTelMetricsSummary, error) {
	var req otlpMetricsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	out := &OTelMetricsSummary{}
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				for _, dp := range m.dataPoints() {
					if model := kvString(dp.Attributes, "model"); model != "" {
						out.Model = model
					}
					switch m.Name {
					case MetricCostUsage:
						out.CostUSD += dp.value()
					case MetricTokenUsage:
						switch kvString(dp.Attributes, "type") {
						case "input":
							out.InputTokens += int64(dp.value())
						case "output":
							out.OutputTokens += int64(dp.value())
						case "cacheRead", "cacheCreation":
							// Folded into input, matching resultUsageTokens'
							// existing treatment of Claude's cache_read/
							// cache_creation input tokens elsewhere.
							out.InputTokens += int64(dp.value())
						}
					case MetricLinesOfCode:
						switch kvString(dp.Attributes, "type") {
						case "added":
							out.LinesAdded += int64(dp.value())
						case "removed":
							out.LinesRemoved += int64(dp.value())
						}
					case MetricPullRequests:
						out.PullRequests += int64(dp.value())
					case MetricCommits:
						out.Commits += int64(dp.value())
					}
				}
			}
		}
	}
	return out, nil
}

// ---- logs -------------------------------------------------------------

type otlpLogRecord struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpScopeLogs struct {
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpResourceLogs struct {
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

// EventAPIRequest is claude_code.api_request's event.name value.
const EventAPIRequest = "api_request"

// OTelAPIRequest is one claude_code.api_request log record's attributes that
// lectern reads. cost_usd/input_tokens/output_tokens are not documented as
// standard api_request attributes today, but are read opportunistically
// (some Claude Code builds/proxies add them) as a secondary cost source —
// see docs/outcomes.md's precedence note. When absent, CostUSD is 0 and the
// caller falls back to the cost.usage metric.
type OTelAPIRequest struct {
	Model        string
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
}

// OTelLogsSummary is everything internal/outcomes needs out of one OTLP logs
// export: how many api_request events it carried, and their combined cost/
// tokens where the log recorded them.
type OTelLogsSummary struct {
	APIRequests  int
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
	Model        string
}

// ParseOTLPLogs decodes one OTLP/HTTP JSON logs export request body and
// summarises its claude_code.api_request events (event.name == "api_request"
// on the record's attributes — Claude Code puts the event name in an
// attribute, not in log body text). Every other event
// (user_prompt/assistant_response/tool_result/tool_decision/...) is ignored:
// none of them carry cost or outcome information lectern uses today.
func ParseOTLPLogs(body []byte) (*OTelLogsSummary, error) {
	var req otlpLogsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	out := &OTelLogsSummary{}
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				if kvString(lr.Attributes, "event.name") != EventAPIRequest {
					continue
				}
				out.APIRequests++
				if v, ok := kvLookup(lr.Attributes, "cost_usd"); ok {
					out.CostUSD += v.asFloat()
				}
				if v, ok := kvLookup(lr.Attributes, "input_tokens"); ok {
					out.InputTokens += int64(v.asFloat())
				}
				if v, ok := kvLookup(lr.Attributes, "output_tokens"); ok {
					out.OutputTokens += int64(v.asFloat())
				}
				if model := kvString(lr.Attributes, "model"); model != "" {
					out.Model = model
				}
			}
		}
	}
	return out, nil
}
