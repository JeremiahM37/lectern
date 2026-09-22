package scheduler

import (
	"path/filepath"

	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/worktree"
)

// taskWorktree derives the first-attempt allocation. Persisted paths and
// branches are always reused by the caller, so the namespace only affects new
// local allocations. Hosted targets retain the historical layout.
func (s *Scheduler) taskWorktree(c *runCtx, att *store.Attempt) (string, string) {
	branch := worktree.BranchName(c.Task.ID, att.N)
	workroot := firstNonEmpty(c.Project.WorkrootOverride, c.Target.Workroot,
		worktree.DefaultWorkroot(c.Project.RepoPath))
	if s.Cfg != nil && c.Target.Kind == "local" && s.Cfg.WorktreeNamespace != "" {
		workroot = filepath.Clean(worktree.NamespacedWorkroot(workroot, s.Cfg.WorktreeNamespace))
		branch = worktree.NamespacedBranch(branch, s.Cfg.WorktreeNamespace)
	}
	return worktree.Path(workroot, c.Task.ID, att.N), branch
}
