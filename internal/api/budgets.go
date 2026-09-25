package api

import (
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/budget"
)

// getBudgets is GET /api/budgets: the configured limits plus their live
// spend/percent/blocked state — the Settings editor and the Usage page's
// bars both read this one shape (docs/budgets.md).
func (s *Server) getBudgets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, budget.BuildStatus(s.DB))
}

// putBudgets is PUT /api/budgets: replaces the whole configuration. Like
// PUT /api/settings, a caller sends the full desired shape rather than a
// partial patch — there is exactly one config blob, not a set of
// independently-addressable keys.
func (s *Server) putBudgets(w http.ResponseWriter, r *http.Request) {
	var cfg budget.Config
	if err := decodeBody(r, &cfg); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if _, err := budget.Save(s.DB, cfg); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	writeJSON(w, 200, budget.BuildStatus(s.DB))
}
