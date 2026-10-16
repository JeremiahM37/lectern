package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
)

// GitCommitWith commits with message. With stageAll it first stages every
// change (git add -A); without it, only what is already staged is committed.
// Either way, files that look like secrets are unstaged before the commit.
func (b *scribeBackend) GitCommitWith(message string, stageAll bool) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("the commit message is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	wd := b.current().Workdir
	if stageAll {
		if out, err := gitRun(ctx, wd, "add", "-A"); err != nil {
			return fmt.Errorf("git add: %s", out)
		}
	}
	// Unstage secret-looking files so a blanket add never commits them.
	var left []string
	if out, err := gitRun(ctx, wd, "diff", "--cached", "--name-only", "-z"); err == nil {
		for _, f := range strings.Split(out, "\x00") {
			if _, bad := exec.Sensitive(f); bad && f != "" {
				left = append(left, f)
			}
		}
	}
	if len(left) > 0 {
		if out, err := gitRun(ctx, wd, append([]string{"reset", "-q", "HEAD", "--"}, left...)...); err != nil {
			return fmt.Errorf("git reset %s: %s", strings.Join(left, ", "), out)
		}
	}
	if out, err := gitRun(ctx, wd, "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %s", out)
	}
	if len(left) > 0 {
		return fmt.Errorf("committed, but left out files that look like secrets: %s. Stage them by hand if they belong in the repo", strings.Join(left, ", "))
	}
	return nil
}

// GitInit creates a repository in the workdir. Running it in a repository is
// harmless: git reports the existing one and leaves it alone.
func (b *scribeBackend) GitInit() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := gitRun(ctx, b.current().Workdir, "init")
	if err != nil {
		return out, fmt.Errorf("git init: %s", out)
	}
	return out, nil
}

// GitBranch creates a branch at HEAD and switches to it (git checkout -b).
// Git validates the name and refuses one that exists or is not a repository.
func (b *scribeBackend) GitBranch(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the branch name is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, b.current().Workdir, "checkout", "-b", name); err != nil {
		return fmt.Errorf("git checkout -b %s: %s", name, out)
	}
	return nil
}

// GitPull fetches and merges the upstream branch. It never waits for a
// credential or merge-message prompt: git fails instead, and the message says so.
func (b *scribeBackend) GitPull() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := osexec.CommandContext(ctx, "git", "-C", b.current().Workdir, "pull", "--no-edit")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git pull: %s", text)
	}
	return text, nil
}
