package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/skills"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/workflows"
)

const workflowReloadRequired = true

func workflowSkillID(id string) string  { return "lectern-workflow/" + id }
func workflowSourceID(id string) string { return "lectern-bundled/" + id }

func workflowAgent(r *http.Request, p *store.Project) (string, error) {
	agent := r.URL.Query().Get("agent")
	if agent == "" && p != nil {
		agent = p.DefaultAgent
	}
	if !oneOf(agent, "claude", "codex") {
		return "", invalid("agent must be claude or codex")
	}
	return agent, nil
}

func (s *Server) projectWorkflows(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, http.StatusNotFound, "no such project")
		return
	}
	p, err := s.DB.Project(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "no such project")
		return
	}
	agent, err := workflowAgent(r, p)
	if err != nil {
		respondErr(w, err)
		return
	}
	attachments, err := s.DB.ProjectSkills(id, agent)
	if err != nil {
		respondErr(w, err)
		return
	}
	enabled := make(map[string]bool)
	for _, attachment := range attachments {
		for _, definition := range workflows.Definitions() {
			if attachment.SkillID == workflowSkillID(definition.ID) && attachment.SourceID == workflowSourceID(definition.ID) {
				// A durable attachment is desired state; it is enabled only once
				// its project worktree has a proven owned or native materialization.
				// This keeps a failed remote setup visible as disabled/retryable.
				mats, matErr := s.DB.Materializations(attachment.ID)
				if matErr != nil {
					respondErr(w, matErr)
					return
				}
				for _, m := range mats {
					if filepath.Clean(m.WorktreePath) == filepath.Clean(p.RepoPath) && (m.State == "owned" || m.State == "preexisting") {
						enabled[definition.ID] = true
						break
					}
				}
			}
		}
	}
	out := make([]map[string]any, 0, len(workflows.Definitions()))
	for _, definition := range workflows.Definitions() {
		out = append(out, map[string]any{
			"id": definition.ID, "name": definition.Name, "description": definition.Description,
			"version": definition.Version, "upstream_url": definition.UpstreamURL,
			"enabled": enabled[definition.ID], "commands": definition.Commands,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": out, "reload_required": workflowReloadRequired})
}

type workflowToggleIn struct {
	Agent   string `json:"agent"`
	Enabled *bool  `json:"enabled"`
}

func (s *Server) putProjectWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, http.StatusNotFound, "no such project")
		return
	}
	p, err := s.DB.Project(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "no such project")
		return
	}
	workflowID := r.PathValue("workflow_id")
	definition, ok := workflows.DefinitionFor(workflowID)
	if !ok {
		httpError(w, http.StatusNotFound, "no such workflow")
		return
	}
	var in workflowToggleIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%s", err)
		return
	}
	if !oneOf(in.Agent, "claude", "codex") {
		httpError(w, http.StatusUnprocessableEntity, "agent must be claude or codex")
		return
	}
	if in.Enabled == nil {
		httpError(w, http.StatusUnprocessableEntity, "enabled is required")
		return
	}
	target, err := s.DB.Target(p.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if *in.Enabled && target.Kind == "sandbox" {
		httpError(w, http.StatusConflict, "workflow integrations require a persistent local or SSH/pct target; sandbox targets are not supported")
		return
	}

	_, ex, err := s.skillExecutor(r, p, p.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer ex.Close()

	attachment, err := s.workflowAttachment(id, p.TargetID, in.Agent, definition.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if *in.Enabled {
		if err := s.enableProjectWorkflow(r, p, ex, attachment, definition, in.Agent); err != nil {
			if strings.Contains(err.Error(), "destination already exists") {
				httpError(w, http.StatusConflict, "%s", err)
			} else {
				respondErr(w, err)
			}
			return
		}
	} else if attachment != nil {
		if err := s.disableProjectWorkflow(r, p, ex, attachment); err != nil {
			if strings.Contains(err.Error(), "preserved") || strings.Contains(err.Error(), "ownership") {
				httpError(w, http.StatusConflict, "%s", err)
			} else {
				respondErr(w, err)
			}
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *in.Enabled, "reload_required": workflowReloadRequired})
}

func (s *Server) workflowAttachment(projectID, targetID int64, agent, id string) (*store.ProjectSkill, error) {
	rows, err := s.DB.ProjectSkills(projectID, agent)
	if err != nil {
		return nil, err
	}
	want := workflowSkillID(id)
	source := workflowSourceID(id)
	for _, row := range rows {
		if row.TargetID == targetID && row.SkillID == want && row.SourceID == source {
			return row, nil
		}
	}
	return nil, nil
}

func (s *Server) enableProjectWorkflow(r *http.Request, p *store.Project, ex executor.Executor, attachment *store.ProjectSkill, definition workflows.Definition, agent string) error {
	sourcePath, digest, err := workflows.Stage(r.Context(), ex, definition.ID)
	if err != nil {
		return err
	}
	created := false
	if attachment == nil {
		rel := filepath.ToSlash(filepath.Join(map[bool]string{true: ".claude", false: ".agents"}[agent == "claude"], "skills", workflowEntryName(definition.ID)))
		attachment, err = s.DB.InsertProjectSkill(&store.ProjectSkill{
			ProjectID: p.ID, TargetID: p.TargetID, Agent: agent,
			SkillID: workflowSkillID(definition.ID), SourceID: workflowSourceID(definition.ID),
			SourcePath: sourcePath, EntryName: workflowEntryName(definition.ID), TargetRel: rel,
			SourceDigest: digest,
		})
		if err != nil {
			return err
		}
		created = true
		attachment.ExcludeMarker = skills.Marker(attachment.ID, p.RepoPath)
		if err := s.DB.Update("project_skills", attachment.ID, map[string]any{"exclude_marker": attachment.ExcludeMarker}); err != nil {
			return err
		}
	} else {
		if attachment.SourcePath != sourcePath || attachment.SourceDigest != "" && attachment.SourceDigest != digest {
			return fmt.Errorf("workflow is attached with a different immutable source")
		}
		expectedRel := filepath.ToSlash(filepath.Join(map[bool]string{true: ".claude", false: ".agents"}[agent == "claude"], "skills", workflowEntryName(definition.ID)))
		if filepath.ToSlash(filepath.Clean(attachment.TargetRel)) != expectedRel || attachment.EntryName != workflowEntryName(definition.ID) {
			return fmt.Errorf("workflow attachment has an unsafe target path")
		}
	}

	mats, err := s.DB.Materializations(attachment.ID)
	if err != nil {
		return err
	}
	state := "pending"
	for _, m := range mats {
		if m.TargetID == attachment.TargetID && filepath.Clean(m.WorktreePath) == filepath.Clean(p.RepoPath) {
			state = m.State
			break
		}
	}
	if err := s.DB.UpsertMaterialization(&store.SkillMaterialization{
		AttachmentID: attachment.ID, TargetID: attachment.TargetID, WorktreePath: p.RepoPath,
		TargetPath: filepath.Join(p.RepoPath, attachment.TargetRel), SourcePath: sourcePath,
		TargetRel: attachment.TargetRel, State: state,
	}); err != nil {
		return err
	}
	x := skills.Skill{ID: attachment.SkillID, Source: attachment.SourceID, SourcePath: sourcePath, EntryName: attachment.EntryName}
	_, materializedSource, resultState, err := skills.MaterializeState(r.Context(), ex, p, x, agent, p.RepoPath, attachment.ID, state == "owned")
	if err != nil {
		if created && strings.Contains(err.Error(), "destination already exists") {
			if mats, lookupErr := s.DB.Materializations(attachment.ID); lookupErr == nil {
				for _, m := range mats {
					if deleteErr := s.DB.DeleteMaterialization(m.ID); deleteErr != nil {
						return fmt.Errorf("workflow destination already exists and pending intent could not be rolled back: %w", deleteErr)
					}
				}
			} else {
				return fmt.Errorf("workflow destination already exists and pending intent could not be inspected: %w", lookupErr)
			}
			if deleteErr := s.DB.DeleteProjectSkill(attachment.ID); deleteErr != nil {
				return fmt.Errorf("workflow destination already exists and pending intent could not be rolled back: %w", deleteErr)
			}
		}
		return err
	}
	return s.DB.UpsertMaterialization(&store.SkillMaterialization{
		AttachmentID: attachment.ID, TargetID: attachment.TargetID, WorktreePath: p.RepoPath,
		TargetPath: filepath.Join(p.RepoPath, attachment.TargetRel), SourcePath: materializedSource,
		TargetRel: attachment.TargetRel, State: resultState,
	})
}

func workflowEntryName(id string) string {
	if id == "spec-kit" {
		return "lectern-spec-kit"
	}
	return "lectern-maestro"
}

func (s *Server) disableProjectWorkflow(r *http.Request, p *store.Project, ex executor.Executor, x *store.ProjectSkill) error {
	mats, err := s.DB.Materializations(x.ID)
	if err != nil {
		return err
	}
	if len(mats) == 0 {
		return s.DB.DeleteProjectSkill(x.ID)
	}
	preserved := []string{}
	pendingCancelled := false
	for _, m := range mats {
		if err := skills.RemoveMaterialization(r.Context(), ex, p, x, m); err != nil {
			if m.State == "pending" && strings.Contains(err.Error(), "ownership is not established") {
				if deleteErr := s.DB.DeleteMaterialization(m.ID); deleteErr != nil {
					preserved = append(preserved, fmt.Sprintf("%s (database: %v)", m.TargetPath, deleteErr))
				} else {
					pendingCancelled = true
				}
				continue
			}
			preserved = append(preserved, m.TargetPath)
			continue
		}
		if err := s.DB.DeleteMaterialization(m.ID); err != nil {
			preserved = append(preserved, fmt.Sprintf("%s (database: %v)", m.TargetPath, err))
		}
	}
	if len(preserved) > 0 {
		return fmt.Errorf("workflow ownership retained; changed or foreign target paths were preserved: %s", strings.Join(preserved, ", "))
	}
	if pendingCancelled {
		remaining, err := s.DB.Materializations(x.ID)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return fmt.Errorf("workflow ownership retained; pending target paths were preserved")
		}
	}
	return s.DB.DeleteProjectSkill(x.ID)
}
