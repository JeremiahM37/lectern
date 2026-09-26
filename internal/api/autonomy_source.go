package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func autoSourceHash(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	for _, c := range revision {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// These commands inspect committed objects only. No hooks, network lazy-fetch,
// replacement objects, global config, worktree filters, or inherited Git env.
func autoSourceCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-C", dir}, args...)...)
	c.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	return c
}

func autoSourceRevision(ctx context.Context, dir, revision string) (string, error) {
	if revision != "HEAD" && !autoSourceHash(revision) {
		return "", errors.New("source_revision must be a full commit hash")
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := autoSourceCommand(c, dir, "rev-parse", "--verify", revision+"^{commit}")
	r, err := cmd.StdoutPipe()
	if err != nil {
		return "", errors.New("source revision unavailable")
	}
	if err = cmd.Start(); err != nil {
		return "", errors.New("source revision unavailable")
	}
	out, readErr := io.ReadAll(io.LimitReader(r, 128))
	err = cmd.Wait()
	got := strings.TrimSpace(string(out))
	if err != nil || readErr != nil || !autoSourceHash(got) {
		return "", errors.New("source revision unavailable")
	}
	if revision != "HEAD" && got != revision {
		return "", errors.New("source_revision must identify a commit directly")
	}
	return got, nil
}

func (s *Server) autoSourceProject(id int64) (*store.Project, error) {
	for _, p := range s.autoProjects() {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, errors.New("project is not an available local snapshot")
}

func autoArchiveSource(ctx context.Context, dir, revision, dest string) error {
	resolved, err := autoSourceRevision(ctx, dir, revision)
	if err != nil {
		return err
	}
	cmd := autoSourceCommand(ctx, dir, "archive", "--format=tar", resolved)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	err = autoExtract(pipe, dest)
	if err != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return err
	}
	return waitErr
}

// Resolve omitted revisions for legacy planner reports BEFORE the two plan
// audits. Never resolve HEAD again when launching a newly admitted proposal.
func (s *Server) pinAutoSources(ctx context.Context, a *autoRecord, items []autonomy.Proposal) error {
	if err := s.validateAutoSources(a, items); err != nil {
		return err
	}
	for i := range items {
		p := &items[i]
		if p.ContinueTaskID > 0 || p.RepairTaskID > 0 {
			continue
		}
		project, err := s.autoSourceProject(p.ProjectID)
		if err != nil {
			return err
		}
		revision, err := autoSourceRevision(ctx, project.RepoPath, "HEAD")
		if err != nil {
			return err
		}
		if p.SourceRevision != "" && p.SourceRevision != revision {
			return fmt.Errorf("item %d: committed source changed since discovery; reread /source?project_id=%d and revise before audit", i, p.ProjectID)
		}
		p.SourceRevision = revision
	}
	return nil
}

func (s *Server) autoSourceContext(ctx context.Context, id, requestedRevision string) (map[string]any, error) {
	pid, err := strconv.ParseInt(id, 10, 64)
	if err != nil || pid <= 0 {
		return nil, errors.New("positive project_id required")
	}
	p, err := s.autoSourceProject(pid)
	if err != nil {
		return nil, err
	}
	if requestedRevision == "" {
		requestedRevision = "HEAD"
	} else if !autoSourceHash(requestedRevision) {
		return nil, errors.New("source_revision must be a full commit hash")
	}
	revision, err := autoSourceRevision(ctx, p.RepoPath, requestedRevision)
	if err != nil {
		return nil, err
	}
	tree, err := autoSourceTree(ctx, p.RepoPath, revision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project_id": p.ID, "source_revision": revision, "source_tree": tree, "working_tree_activity": autoSourceActivity(ctx, p.RepoPath),
		"tree_scope": "Git tree of the resolved source commit. Snapshot commit IDs normally differ. Matching trees corroborate paths, blobs and executable bits only within the same Git object format. Export attributes or ignored tracked files can change an archived snapshot tree; mismatch is diagnostic, not a new admission gate.",
		"scope":      "Committed source only; uncommitted files are excluded. This is provenance, not ownership clearance, checkpoint approval, or permission to repeat rejected work. New milestones require both plan audits; retain continuation/repair lineage for existing work."}, nil
}

// Inspect only a previously resolved commit; no replacement objects or lazy fetch.
func autoSourceTree(ctx context.Context, dir, revision string) (string, error) {
	if !autoSourceHash(revision) {
		return "", errors.New("source_revision must be a full commit hash")
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := autoSourceCommand(c, dir, "rev-parse", "--verify", revision+"^{tree}")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", errors.New("source tree unavailable")
	}
	if err = cmd.Start(); err != nil {
		return "", errors.New("source tree unavailable")
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, 128))
	err = cmd.Wait()
	tree := strings.TrimSpace(string(out))
	if err != nil || readErr != nil || !autoSourceHash(tree) {
		return "", errors.New("source tree unavailable")
	}
	return tree, nil
}
