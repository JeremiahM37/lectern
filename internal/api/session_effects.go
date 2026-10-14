// Session effects: the completion claims in an agent's final message, checked
// against the machine (internal/effects, docs/effect-checks.md). Both routes
// are read-only: the probes run when the agent stops, not when someone asks.
package api

import (
	"net/http"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// sessionEffects is GET /api/sessions/{id}/effects. latest is the newest
// evaluation (null when the session has none); recent holds the last 20,
// newest first, and includes latest.
func (s *Server) sessionEffects(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	latest, err := s.DB.LatestEffectCheck(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	recent, err := s.DB.EffectChecks(row.ID, 20)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"latest": latest, "recent": recent})
}

// effectReliability is GET /api/effects/reliability?days=30. It groups the
// claims checked in the window by the session's agent and model, which is the
// per-agent claim reliability: how often what an agent said was true.
func (s *Server) effectReliability(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 365 {
			httpError(w, 422, "days must be between 1 and 365")
			return
		}
		days = n
	}
	since := store.Now() - float64(days)*86400
	rows, err := s.DB.EffectReliability(since)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "rows": rows})
}
