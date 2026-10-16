package app

// Backend methods behind the wave 10 commands (tui/commands_cmds10.go). They
// are optional interfaces in package tui; backend_optional.go checks that
// *scribeBackend provides each one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// checkpointNameRe is what a checkpoint name may hold: it becomes a git ref.
var checkpointNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ClearTasks empties the task list through the task_list tool, so the
// planner's own lock and its event (which refreshes the panel) do the work.
func (b *scribeBackend) ClearTasks() error {
	a := b.current()
	t, ok := a.Tools.Get("task_list")
	if !ok {
		return nil
	}
	res := t.Run(context.Background(), &tools.Env{}, json.RawMessage(`{"action":"clear"}`))
	if res.IsError {
		return errors.New(res.Output)
	}
	return nil
}

// ContextWindow implements tui.ContextWindower. The window is what the agent
// uses now, which already has any override applied.
func (b *scribeBackend) ContextWindow() (string, int, int) {
	a := b.current()
	m := a.Agent.CurrentModel()
	a.mu.Lock()
	override := a.Cfg.ContextWindowOverrides[m.Ref]
	a.mu.Unlock()
	return m.Ref, m.Info.ContextWindow, override
}

// SetContextWindow implements tui.ContextWindower. It writes the override to
// the global config (0 removes it), then makes the registry and the running
// agent use it straight away.
func (b *scribeBackend) SetContextWindow(tokens int) error {
	a := b.current()
	ref := a.Agent.CurrentModel().Ref
	if !strings.Contains(ref, "/") {
		return fmt.Errorf("the current model %q has no provider/model name, so it cannot take a context window", ref)
	}
	if tokens < 0 {
		return errors.New("a context window cannot be negative")
	}
	if err := b.SetField7("context_window_overrides."+ref, tokens, false); err != nil {
		return err
	}
	a.mu.Lock()
	overrides := a.Cfg.ContextWindowOverrides
	reg := a.Registry
	a.mu.Unlock()
	reg.SetContextOverrides(overrides)
	m, err := a.resolve(ref)
	if err != nil {
		return err
	}
	a.Agent.SetModel(m)
	return nil
}

// SaveCheckpoint implements tui.CheckpointSaver. The working tree, including
// new files that git does not ignore, is written as a commit on
// refs/scribe/checkpoints/<name>. A temporary index keeps the real index and
// HEAD as they were, so the checkpoint changes nothing the user sees. Restore
// a file from it with git checkout <commit> -- <path>.
func (b *scribeBackend) SaveCheckpoint(name string) (string, error) {
	a := b.current()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "checkpoint-" + time.Now().Format("20060102-150405")
	}
	if !checkpointNameRe.MatchString(name) {
		return "", fmt.Errorf("checkpoint name %q: use letters, digits, dots, dashes and underscores (64 at most)", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	wd := a.Workdir
	if _, err := gitRun(ctx, wd, "rev-parse", "--is-inside-work-tree"); err != nil {
		return "", errors.New("this folder is not a git repository, so there is nowhere to save a checkpoint")
	}
	tmp, err := os.MkdirTemp("", "scribe-checkpoint-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(tmp, "index"),
		"GIT_AUTHOR_NAME=Scribe", "GIT_AUTHOR_EMAIL=scribe@localhost",
		"GIT_COMMITTER_NAME=Scribe", "GIT_COMMITTER_EMAIL=scribe@localhost",
	}
	if _, err := gitRunEnv(ctx, wd, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("could not read the working tree: %v", err)
	}
	tree, err := gitRunEnv(ctx, wd, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("could not write the tree: %v", err)
	}
	args := []string{"commit-tree", strings.TrimSpace(tree), "-m", "Scribe checkpoint " + name}
	if head, err := gitRun(ctx, wd, "rev-parse", "--verify", "-q", "HEAD"); err == nil && head != "" {
		args = append(args, "-p", strings.TrimSpace(head))
	}
	commit, err := gitRunEnv(ctx, wd, env, args...)
	if err != nil {
		return "", fmt.Errorf("could not create the checkpoint commit: %v", err)
	}
	commit = strings.TrimSpace(commit)
	ref := "refs/scribe/checkpoints/" + name
	if _, err := gitRun(ctx, wd, "update-ref", ref, commit); err != nil {
		return "", fmt.Errorf("could not write %s: %v", ref, err)
	}
	return name + " (" + commit[:min(len(commit), 12)] + ")", nil
}

// gitRunEnv is gitRun with extra environment variables.
func gitRunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}
