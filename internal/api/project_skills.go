package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/skills"
	"github.com/JeremiahM37/lectern/internal/store"
)

type skillAttachIn struct {
	TargetID int64  `json:"target_id"`
	Agent    string `json:"agent"`
	SkillID  string `json:"skill_id"`
}

func (s *Server) skillExecutor(r *http.Request, p *store.Project, targetID int64) (*store.Target, executor.Executor, error) {
	// Kept as a small adapter so handlers never run discovery on the control plane.
	t, err := s.DB.Target(targetID)
	if err != nil {
		return nil, nil, err
	}
	if p != nil && p.TargetID != targetID {
		return nil, nil, fmt.Errorf("target does not own project")
	}
	ex, err := s.Reg.For(t)
	return t, ex, err
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	var p *store.Project
	var err error
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			httpError(w, 400, "invalid project_id")
			return
		}
		p, err = s.DB.Project(id)
		if err != nil {
			httpError(w, 404, "no such project")
			return
		}
	}
	targetID := int64(0)
	if raw := r.URL.Query().Get("target_id"); raw != "" {
		targetID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			httpError(w, 400, "invalid target_id")
			return
		}
	}
	if p == nil && targetID == 0 {
		httpError(w, 400, "project_id or target_id is required")
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent == "" && p != nil {
		agent = p.DefaultAgent
	}
	if agent == "" {
		agent = "claude"
	}
	if p != nil {
		targetID = p.TargetID
	}
	_, ex, err := s.skillExecutor(r, p, targetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer ex.Close()
	out, err := skills.Discover(r.Context(), ex, p, agent)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"skills": out})
}

func (s *Server) projectSkills(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	_, err = s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	agent := r.URL.Query().Get("agent")
	rows, err := s.DB.ProjectSkills(id, agent)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"attachments": rows})
}

func (s *Server) attachProjectSkill(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	p, err := s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	var in skillAttachIn
	if err = decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if in.TargetID == 0 {
		in.TargetID = p.TargetID
	}
	if in.TargetID != p.TargetID {
		httpError(w, 400, "target_id does not match project")
		return
	}
	if strings.TrimSpace(in.Agent) == "" {
		in.Agent = p.DefaultAgent
	}
	if in.SkillID == "" {
		httpError(w, 422, "skill_id is required")
		return
	}
	_, ex, err := s.skillExecutor(r, p, in.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer ex.Close()
	available, err := skills.Discover(r.Context(), ex, p, in.Agent)
	if err != nil {
		respondErr(w, err)
		return
	}
	var selected *skills.Skill
	for i := range available {
		if available[i].ID == in.SkillID {
			v := available[i]
			selected = &v
			break
		}
	}
	if selected == nil {
		httpError(w, 404, "skill %q was not found on target", in.SkillID)
		return
	}
	rel := strings.ReplaceAll(filepath.ToSlash(filepath.Join(map[bool]string{true: ".claude", false: ".agents"}[in.Agent == "claude"], "skills", selected.EntryName)), "\\", "/")
	marker := skills.Marker(0, p.RepoPath)
	x := &store.ProjectSkill{ProjectID: id, TargetID: in.TargetID, Agent: in.Agent, SkillID: selected.ID, SourceID: selected.Source, SourcePath: selected.SourcePath, EntryName: selected.EntryName, TargetRel: rel, SourceDigest: selected.Digest, ExcludeMarker: marker}
	created, err := s.DB.InsertProjectSkill(x)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			httpError(w, 409, "skill is already attached")
		} else {
			respondErr(w, err)
		}
		return
	}
	created.ExcludeMarker = skills.Marker(created.ID, p.RepoPath)
	if err = s.DB.Update("project_skills", created.ID, map[string]any{"exclude_marker": created.ExcludeMarker}); err != nil {
		respondErr(w, err)
		return
	}
	if err = s.DB.UpsertMaterialization(&store.SkillMaterialization{AttachmentID: created.ID, TargetID: created.TargetID, WorktreePath: p.RepoPath, TargetPath: filepath.Join(p.RepoPath, rel), SourcePath: created.SourcePath, TargetRel: created.TargetRel}); err != nil {
		// Keep the desired attachment as durable pending intent. No target link
		// has been attempted, so a later retry can recover without guessing.
		respondErr(w, err)
		return
	}
	_, sourcePath, state, matErr := skills.MaterializeState(r.Context(), ex, p, *selected, in.Agent, p.RepoPath, created.ID, false)
	if matErr != nil {
		if strings.Contains(matErr.Error(), "already exists") {
			// A deterministic destination collision means this request never
			// established ownership. Roll back the newly-created intent so a
			// foreign file does not trap the project in a permanently pending
			// attachment. If the DB cleanup itself fails, retain the evidence.
			mats, lookupErr := s.DB.Materializations(created.ID)
			if lookupErr != nil {
				respondErr(w, lookupErr)
				return
			}
			for _, m := range mats {
				if deleteErr := s.DB.DeleteMaterialization(m.ID); deleteErr != nil {
					httpError(w, 500, "skill destination already exists and pending intent could not be rolled back: %v", deleteErr)
					return
				}
			}
			if deleteErr := s.DB.DeleteProjectSkill(created.ID); deleteErr != nil {
				httpError(w, 500, "skill destination already exists and pending intent could not be rolled back: %v", deleteErr)
				return
			}
			httpError(w, 409, "skill destination already exists; foreign content was preserved")
		} else {
			respondErr(w, matErr)
		}
		return
	}
	if err = s.DB.UpsertMaterialization(&store.SkillMaterialization{AttachmentID: created.ID, TargetID: created.TargetID, WorktreePath: p.RepoPath, TargetPath: filepath.Join(p.RepoPath, rel), SourcePath: sourcePath, TargetRel: rel, State: state}); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"attachment": created, "target_path": filepath.Join(p.RepoPath, rel), "mode": "symlink", "owned": state == "owned"})
}

func (s *Server) detachProjectSkill(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	aid, err := strconv.ParseInt(r.PathValue("attachment_id"), 10, 64)
	if err != nil {
		httpError(w, 404, "no such attachment")
		return
	}
	p, err := s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	x, err := s.DB.ProjectSkill(aid)
	if err != nil || x.ProjectID != id {
		httpError(w, 404, "no such attachment")
		return
	}
	_, ex, err := s.skillExecutor(r, p, x.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer ex.Close()
	preserved := []string{}
	pendingCancelled := false
	mats, matErr := s.DB.Materializations(aid)
	if matErr != nil {
		respondErr(w, matErr)
		return
	}
	if len(mats) == 0 {
		preserved = append(preserved, filepath.Join(p.RepoPath, x.TargetRel))
	}
	for _, m := range mats {
		if err := skills.RemoveMaterialization(r.Context(), ex, p, x, m); err != nil {
			if m.State == "pending" && strings.Contains(err.Error(), "ownership is not established") {
				// Cancellation of an unproven intent may discard only its DB
				// evidence; the foreign target path remains untouched.
				if deleteErr := s.DB.DeleteMaterialization(m.ID); deleteErr != nil {
					preserved = append(preserved, fmt.Sprintf("%s (database: %v)", m.TargetPath, deleteErr))
				} else {
					preserved = append(preserved, m.TargetPath)
					pendingCancelled = true
				}
				continue
			}
			preserved = append(preserved, m.TargetPath)
		} else {
			if err := s.DB.DeleteMaterialization(m.ID); err != nil {
				preserved = append(preserved, fmt.Sprintf("%s (database: %v)", m.TargetPath, err))
			}
		}
	}
	if pendingCancelled {
		remaining, lookupErr := s.DB.Materializations(aid)
		if lookupErr != nil {
			respondErr(w, lookupErr)
			return
		}
		if len(remaining) == 0 {
			if deleteErr := s.DB.DeleteProjectSkill(aid); deleteErr != nil {
				respondErr(w, deleteErr)
				return
			}
			writeJSON(w, 200, map[string]any{"removed": true, "preserved": preserved})
			return
		}
	}
	if len(preserved) > 0 {
		httpError(w, 409, "skill ownership retained; changed or foreign target paths were preserved: %s", strings.Join(preserved, ", "))
		return
	}
	if err = s.DB.DeleteProjectSkill(aid); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": true, "preserved": preserved})
}
