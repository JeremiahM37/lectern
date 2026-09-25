package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// otelBodyLimit is generous for a periodic metrics/logs export (Claude Code's
// own default export interval is 60s for metrics, 5s for logs — a batch is a
// few KB to tens of KB even from a busy session) while still bounding what an
// unauthenticated-by-design route group (see sessionFromHookAuth's own doc
// comment) will buffer for a caller holding a wrong or no token.
const otelBodyLimit = 4 << 20

// hookSessionOTelMetrics is POST /api/hook/otel/session/{id}/v1/metrics — the
// endpoint Claude Code's own OTLP/HTTP JSON metrics exporter posts to once
// internal/sessions.Manager.launch has pointed OTEL_EXPORTER_OTLP_ENDPOINT at
// it (see agentevents.OTelEnv). Same session hook token auth as every other
// /api/hook/session/{id}/* route.
func (s *Server) hookSessionOTelMetrics(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFromHookAuth(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, otelBodyLimit))
	if err := s.Events.IngestOTelMetrics(sess, body); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{})
}

// hookSessionOTelLogs is POST /api/hook/otel/session/{id}/v1/logs — the logs
// half of the pair above.
func (s *Server) hookSessionOTelLogs(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFromHookAuth(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, otelBodyLimit))
	if err := s.Events.IngestOTelLogs(sess, body); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{})
}

// attemptFromHookAuth is attemptFromHookAuth's session-scoped twin
// (sessionFromHookAuth): resolve the {id} path value, check Authorization:
// Bearer against that exact attempt's own token, in constant time. A missing
// attempt and a wrong token both answer 401 identically — same
// non-enumerable-by-id property sessionFromHookAuth documents.
func (s *Server) attemptFromHookAuth(w http.ResponseWriter, r *http.Request) (*store.Attempt, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 401, "unauthorized")
		return nil, false
	}
	att, err := s.DB.Attempt(id)
	if err != nil {
		httpError(w, 401, "unauthorized")
		return nil, false
	}
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !agentevents.ValidToken(att.Token, supplied) {
		httpError(w, 401, "unauthorized")
		return nil, false
	}
	return att, true
}

// hookAttemptOTelMetrics is POST /api/hook/otel/attempt/{id}/v1/metrics — the
// headless-task-attempt twin of hookSessionOTelMetrics, pointed at by
// internal/scheduler.stageRuntime's own OTelEnv injection.
func (s *Server) hookAttemptOTelMetrics(w http.ResponseWriter, r *http.Request) {
	att, ok := s.attemptFromHookAuth(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, otelBodyLimit))
	if err := agentevents.IngestOTelMetricsForAttempt(s.DB, att.ID, body); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{})
}

// hookAttemptOTelLogs is POST /api/hook/otel/attempt/{id}/v1/logs.
func (s *Server) hookAttemptOTelLogs(w http.ResponseWriter, r *http.Request) {
	att, ok := s.attemptFromHookAuth(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, otelBodyLimit))
	if err := agentevents.IngestOTelLogsForAttempt(s.DB, att.ID, body); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{})
}
