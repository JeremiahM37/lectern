package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/skills"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
	"path"
	"strings"
)

// WorkspaceProgress reads an atomic target receipt without blocking a live
// checkout or overwriting the launcher's newer database state.
func (m *Manager) WorkspaceProgress(ctx context.Context, id int64) (*worktree.Interactive, error) {
	row, ex, err := m.resolve(id)
	if err != nil {
		return nil, err
	}
	var plan worktree.Interactive
	if json.Unmarshal([]byte(row.WorktreeJSON), &plan) != nil || len(plan.Repositories) == 0 {
		return nil, fmt.Errorf("this session does not own a grouped workspace")
	}
	if err := worktree.RunInteractive(ctx, ex, "status", &plan); err != nil {
		return nil, err
	}
	plan.RedactOwnership()
	return &plan, nil
}

func (m *Manager) RemoveWorktree(ctx context.Context, id int64) error {
	return m.mutateWorktree(ctx, id, "remove")
}

func (m *Manager) RecoverWorktree(ctx context.Context, id int64) error {
	return m.mutateWorktree(ctx, id, "recover")
}

func (m *Manager) mutateWorktree(ctx context.Context, id int64, operation string) error {
	pending, pendingErr := m.DB.WorkspaceOperations(id, true)
	if pendingErr != nil {
		return pendingErr
	}
	if len(pending) > 0 {
		return fmt.Errorf("a repository addition is still active; inspect or cancel it before changing the workspace")
	}
	if m.setupActive(id) {
		return fmt.Errorf("workspace setup is still running; inspect or cancel it before changing the worktree")
	}
	row, ex, err := m.resolve(id)
	if err != nil {
		return err
	}
	var plan worktree.Interactive
	if row.WorktreeJSON == "" || json.Unmarshal([]byte(row.WorktreeJSON), &plan) != nil {
		return fmt.Errorf("this session does not own a worktree")
	}
	canonical, err := canonicalWorkspaceAllocation(ctx, ex, plan.Path)
	if err != nil {
		return err
	}
	use, err := m.reserveWorkspacePaths(row.TargetID, true, plan.Path, canonical)
	if err != nil {
		return err
	}
	defer m.releaseWorkspacePaths(use)
	// Another cleanup may have completed between the initial read and reservation.
	row, err = m.DB.Session(id)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(row.WorktreeJSON), &plan); err != nil {
		return err
	}
	if plan.State == "removed" {
		return nil
	}
	live, err := m.DB.LiveSessions()
	if err != nil {
		return err
	}
	for _, other := range live {
		if other.TargetID == row.TargetID && (path.Clean(other.Workdir) == path.Clean(plan.Path) || strings.HasPrefix(path.Clean(other.Workdir), path.Clean(plan.Path)+"/")) {
			return fmt.Errorf("end session %d before changing its worktree", other.ID)
		}
	}
	if operation == "remove" && row.ProjectID != nil {
		if project, projectErr := m.DB.Project(*row.ProjectID); projectErr == nil {
			// Remove only symlinks recorded as Lectern-owned. Foreign files and
			// changed links remain, so the normal worktree cleanliness guard can
			// explain why cleanup is blocked.
			if cleanErr := skills.Clean(ctx, ex, m.DB, project, plan.Path); cleanErr != nil {
				return cleanErr
			}
		}
	}
	if err := worktree.RunInteractive(ctx, ex, operation, &plan); err != nil {
		if saveErr := m.DB.Update("sessions", id, map[string]any{"worktree_json": store.J(plan)}); saveErr != nil {
			return fmt.Errorf("%w; could not save workspace progress: %v", err, saveErr)
		}
		if fresh, loadErr := m.DB.Session(id); loadErr == nil {
			m.publish(fresh)
		}
		return err
	}
	if err := m.DB.Update("sessions", id, map[string]any{"worktree_json": store.J(plan)}); err != nil {
		return err
	}
	if fresh, err := m.DB.Session(id); err == nil {
		m.publish(fresh)
	}
	return nil
}

// Resolve all selections before inserting a session or touching a target. Extra
// repositories inherit the target, never its environment or agent configuration.
func (m *Manager) workspaceSources(o LaunchOpts) ([]worktree.RepositorySource, error) {
	if o.Worktree == nil || len(o.Worktree.ExtraRepositories) == 0 {
		return nil, nil
	}
	if o.ProjectID == nil || o.Workdir != "" {
		return nil, fmt.Errorf("a multi-repository workspace needs a primary project and no working-directory override")
	}
	if len(o.Worktree.ExtraRepositories) > 7 {
		return nil, fmt.Errorf("a workspace supports at most eight repositories")
	}
	primary, err := m.DB.Project(*o.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("primary project is unavailable: %w", err)
	}
	if primary.TargetID != o.TargetID {
		return nil, fmt.Errorf("all workspace projects must use the session target")
	}
	seen := map[int64]bool{primary.ID: true}
	primaryID := primary.ID
	sources := []worktree.RepositorySource{{Name: primary.Name, Repo: primary.RepoPath, Base: o.Worktree.Base, ProjectID: &primaryID, SetupCommand: primary.SetupCmd, SetupEnv: m.ProjectEnv(&primaryID)}}
	for _, selected := range o.Worktree.ExtraRepositories {
		if seen[selected.ProjectID] {
			return nil, fmt.Errorf("a workspace project was selected more than once")
		}
		project, err := m.DB.Project(selected.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("workspace project %d is unavailable", selected.ProjectID)
		}
		if project.TargetID != o.TargetID {
			return nil, fmt.Errorf("all workspace projects must use the session target")
		}
		id := project.ID
		sources = append(sources, worktree.RepositorySource{Name: project.Name, Repo: project.RepoPath, Base: selected.Base, ProjectID: &id, SetupCommand: project.SetupCmd, SetupEnv: m.ProjectEnv(&id)})
		seen[id] = true
	}
	return sources, nil
}

// WorkspaceAt resolves a grouped allocation shared by fresh forks or resumed
// sessions. Sharing the directory never gives another session removal ownership.
func (m *Manager) WorkspaceAt(targetID int64, directory string) (*worktree.Interactive, error) {
	if directory == "" {
		return nil, nil
	}
	rows, err := m.DB.Query(`SELECT worktree_json FROM sessions WHERE target_id=? AND worktree_json<>''`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var workspace worktree.Interactive
		if json.Unmarshal([]byte(raw), &workspace) == nil && len(workspace.Repositories) > 0 && path.Clean(workspace.Path) == path.Clean(directory) {
			return &workspace, nil
		}
	}
	return nil, rows.Err()
}

// Fork from each child's committed revision, while keeping the stable source
// repository for future cleanup even after the parent allocation is removed.
func workspaceForkSources(ctx context.Context, ex executor.Executor, workspace *worktree.Interactive, primaryBase string) ([]worktree.RepositorySource, error) {
	sources := []worktree.RepositorySource{}
	for i, repo := range workspace.Repositories {
		base := "HEAD"
		if i == 0 && strings.TrimSpace(primaryBase) != "" {
			base = strings.TrimSpace(primaryBase)
		}
		result, err := ex.Run(ctx, "git --no-optional-locks -C "+shellq.Quote(repo.Worktree.Path)+" rev-parse --verify --end-of-options "+shellq.Quote(base+"^{commit}"), executor.RunOpts{Timeout: 30})
		if err != nil || !result.OK() {
			return nil, fmt.Errorf("could not resolve committed fork base for repository %q", repo.Name)
		}
		sources = append(sources, worktree.RepositorySource{Name: repo.Name, ProjectID: repo.ProjectID, Repo: repo.Worktree.Repo, Base: strings.TrimSpace(result.Stdout), SetupCommand: repo.Worktree.SetupCommand, SetupEnv: repo.Worktree.SetupEnv})
	}
	return sources, nil
}
