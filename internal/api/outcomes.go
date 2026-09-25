package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/outcomes"
)

// getOutcomes is GET /api/outcomes?days=N&group=agent|model|project — see
// docs/outcomes.md. It recomputes outcome_facts for the window first
// (outcomes.Rebuild is idempotent and cheap at homelab scale — see its own
// doc comment) so a read is never stale, then aggregates.
func (s *Server) getOutcomes(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 366 {
			days = n
		}
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "agent"
	}
	if !outcomes.ValidGroup(group) {
		httpError(w, 400, "group must be agent, model, or project")
		return
	}
	if err := outcomes.Rebuild(s.DB, days); err != nil {
		respondErr(w, err)
		return
	}
	cutoffDate := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	projects, _ := s.DB.Projects()
	projectName := map[int64]string{}
	for _, p := range projects {
		projectName[p.ID] = p.Name
	}

	rows, err := outcomes.Aggregate(s.DB, cutoffDate, outcomes.GroupKind(group), projectName)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"days": days, "group": group, "generated_at": time.Now().Unix(), "rows": rows,
	})
}

// getModelPrices/putModelPrices back the optional per-model $/1M-token table
// (docs/outcomes.md "Estimates"): used only to estimate cost for an agent
// that reports tokens but no dollar figure of its own.
func (s *Server) getModelPrices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, outcomes.LoadPrices(s.DB))
}

func (s *Server) putModelPrices(w http.ResponseWriter, r *http.Request) {
	var body outcomes.PriceConfig
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	saved, err := outcomes.SavePrices(s.DB, body)
	if err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	writeJSON(w, 200, saved)
}
