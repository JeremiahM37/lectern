package api

import (
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/onboard"
)

// onboardingStatus backs the web app's first-run checklist. It answers the
// same questions `lectern doctor` asks on the command line (internal/onboard
// is the single source of truth for both) plus the two counts that decide
// whether the checklist should show at all: a project to dispatch into, and
// a session that's actually run.
func (s *Server) onboardingStatus(w http.ResponseWriter, r *http.Request) {
	projects, err := s.DB.Projects()
	if err != nil {
		respondErr(w, err)
		return
	}
	sessions, err := s.DB.Sessions(true)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"agents":   onboard.DetectAgents(s.Cfg.ClaudeBin, s.Cfg.CodexBin, s.Cfg.GeminiBin),
		"tmux":     onboard.CheckTmux(),
		"git":      onboard.CheckGit(),
		"python":   onboard.CheckPython(),
		"projects": len(projects),
		"sessions": len(sessions),
		"mock":     s.Cfg.Mock,
	})
}
