package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/store"
)

type promotionIdentity struct {
	Agent            string `json:"agent"`
	CID              string `json:"cid"`
	PID              int    `json:"pid"`
	ProcStart        string `json:"proc_start"`
	Workspace        string `json:"workdir"`
	RootPID          int    `json:"root_pid"`
	RootStart        string `json:"root_start"`
	PaneID           string `json:"pane_id"`
	NativeHome       string `json:"native_home"`
	BootID           string `json:"boot_id"`
	TargetID         int64  `json:"target_id"`
	TmuxSession      string `json:"tmux_session"`
	TrackingIdentity string `json:"tracking_identity"`
}

type promotionPreview struct {
	SessionID      int64             `json:"session_id"`
	Identity       promotionIdentity `json:"identity"`
	CandidateAgent string            `json:"candidate_agent"`
	CandidateCID   string            `json:"candidate_cid"`
	Workdir        string            `json:"workdir"`
	TargetID       int64             `json:"target_id"`
	Target         *store.Target     `json:"target"`
	Existing       []*store.Project  `json:"existing_projects"`
}

func (s *Server) resolvePromotionIdentity(r *http.Request, row *store.Session) (promotionIdentity, error) {
	target, err := s.DB.Target(row.TargetID)
	if err != nil {
		return promotionIdentity{}, err
	}
	candidates := []string{}
	if row.Agent == "claude" || row.Agent == "codex" {
		candidates = []string{row.Agent}
	} else {
		candidates = []string{"claude", "codex"}
	}
	found := make([]promotionIdentity, 0, 2)
	var ambiguous error
	for _, agent := range candidates {
		home := ""
		ex, e := s.Reg.For(target)
		if e != nil {
			continue
		}
		evidence, e := sessions.CaptureNativeEvidence(r.Context(), ex, agent, row.Workdir, home, row.TmuxSession, row.TrackingIdentity, true)
		if e == nil {
			found = append(found, promotionIdentity{Agent: agent, CID: evidence.ID, PID: evidence.PID, ProcStart: evidence.ProcStart, Workspace: evidence.Workspace, RootPID: evidence.RootPID, RootStart: evidence.RootStart, PaneID: evidence.PaneID, NativeHome: evidence.NativeHome, BootID: evidence.BootID, TargetID: row.TargetID, TmuxSession: row.TmuxSession, TrackingIdentity: row.TrackingIdentity})
		} else if strings.Contains(e.Error(), "ambiguous") {
			ambiguous = e
		}
	}
	if ambiguous != nil {
		return promotionIdentity{}, ambiguous
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return promotionIdentity{}, fmt.Errorf("both Claude and Codex appear to own this terminal; promotion is ambiguous")
	}
	return promotionIdentity{}, fmt.Errorf("could not prove a Claude or Codex process and exact native conversation in this terminal; leave the agent running and retry")
}

func cleanPromotionPath(p string) string { return filepath.Clean(strings.TrimSpace(p)) }

func (s *Server) previewPromotion(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	identity, err := s.resolvePromotionIdentity(r, row)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	target, err := s.DB.Target(row.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	projects, err := s.DB.Projects()
	if err != nil {
		respondErr(w, err)
		return
	}
	existing := make([]*store.Project, 0)
	for _, p := range projects {
		if p.TargetID == row.TargetID && cleanPromotionPath(p.RepoPath) == cleanPromotionPath(identity.Workspace) {
			existing = append(existing, p)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, promotionPreview{SessionID: row.ID, Identity: identity, CandidateAgent: identity.Agent, CandidateCID: identity.CID, Workdir: identity.Workspace, TargetID: row.TargetID, Target: target, Existing: existing})
}
