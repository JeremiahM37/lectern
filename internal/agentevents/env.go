package agentevents

import (
	"fmt"
	"strings"
)

// Env names injected into every interactive session's process environment at
// launch (see internal/sessions.Manager.launch), read back by whatever the
// agent's own hooks/statusline scripts run with. EnvHookURL is the base for
// one session's hook endpoints — POST $LECTERN_HOOK_URL/<event> and
// $LECTERN_HOOK_URL/statusline — not a single fixed URL.
const (
	EnvHookToken = "LECTERN_HOOK_TOKEN"
	EnvHookURL   = "LECTERN_HOOK_URL"
)

// OTelEnv builds the environment Claude Code's own OpenTelemetry exporter
// reads (docs/outcomes.md, https://code.claude.com/docs/en/monitoring-usage):
// CLAUDE_CODE_ENABLE_TELEMETRY plus OTLP/HTTP JSON metrics+logs exporters
// pointed at lectern's own receiver
// (POST otelBaseURL/v1/metrics and otelBaseURL/v1/logs — see
// internal/api's hookSessionOTel/hookAttemptOTel), authenticated with the
// same bearer token as the rest of that session's/attempt's hook group so no
// second secret needs minting. lectern.session_id or lectern.attempt_id rides
// as a resource attribute purely for a human reading raw OTLP JSON while
// debugging — routing itself is by URL path and token, never by this
// attribute, so a caller that drops it is still authenticated correctly.
//
// Only http/json is offered: lectern's receiver is a small JSON decoder (see
// internal/agentevents/otel.go), not a full OTLP/gRPC or protobuf server, and
// http/json is one of Claude Code's three documented exporter protocols.
func OTelEnv(otelBaseURL, token, resourceKey string, refID int64) map[string]string {
	return map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_METRICS_EXPORTER":        "otlp",
		"OTEL_LOGS_EXPORTER":           "otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL":  "http/json",
		"OTEL_EXPORTER_OTLP_ENDPOINT":  strings.TrimRight(otelBaseURL, "/"),
		"OTEL_EXPORTER_OTLP_HEADERS":   "Authorization=Bearer " + token,
		"OTEL_RESOURCE_ATTRIBUTES":     fmt.Sprintf("%s=%d", resourceKey, refID),
	}
}

// OTelResourceSessionKey/OTelResourceAttemptKey are the resource attribute
// names OTelEnv uses to correlate a payload with the lectern row that
// launched it — see OTelEnv's doc comment on why correlation is informational
// only, not part of the auth boundary.
const (
	OTelResourceSessionKey = "lectern.session_id"
	OTelResourceAttemptKey = "lectern.attempt_id"
)

// OTelOptOutSetting is the settings row that turns OTel env injection off —
// default on, same "explicit '0' opts out" convention internal/alerts and
// internal/awareness already use.
const OTelOptOutSetting = "otel_telemetry"
