package api

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// newProjectDirMarker starts the command that makes a new project's folder,
// so the mock target can recognise it.
const newProjectDirMarker = "# lectern-new-project\n"

// projectDirSlug turns a project name into one folder name: what the New
// session forms create under ~/projects when asked for a new project.
func projectDirSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ', r == '/', r == '\\':
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-.")
	}
	return slug
}

// newProjectDirCommand creates dir (or $HOME/projects/<slug> when dir is
// blank) on the target, makes it a Git repository with one empty commit when
// it is not one already, so worktrees work from the start, and prints the
// real path. The target's shell resolves $HOME: the control plane's is not
// the target's.
func newProjectDirCommand(dir, slug string) (string, error) {
	var expr string
	switch {
	case dir == "":
		expr = `"$HOME/projects/"` + shellq.Quote(slug)
	case dir == "~" || strings.HasPrefix(dir, "~/"):
		expr = `"$HOME"` + shellq.Quote(strings.TrimPrefix(dir, "~"))
	case path.IsAbs(dir):
		expr = shellq.Quote(path.Clean(dir))
	default:
		return "", fmt.Errorf("repo_path must be absolute or start with ~/")
	}
	return newProjectDirMarker + "d=" + expr + `; mkdir -p -- "$d" && cd -- "$d" && ` +
		`{ git rev-parse --git-dir >/dev/null 2>&1 || { git init -q && git symbolic-ref HEAD refs/heads/main && ` +
		`git -c user.name=lectern -c user.email=lectern@localhost commit -q --allow-empty -m "Initial commit"; } >/dev/null 2>&1; }; pwd -P`, nil
}

// makeProjectDir runs newProjectDirCommand on the project's target.
func (s *Server) makeProjectDir(ctx context.Context, targetID int64, dir, name string) (string, error) {
	slug := projectDirSlug(name)
	if dir == "" && slug == "" {
		return "", fmt.Errorf("the name %q has no letters or digits to name a folder after", name)
	}
	cmd, err := newProjectDirCommand(dir, slug)
	if err != nil {
		return "", err
	}
	target, err := s.DB.Target(targetID)
	if err != nil {
		return "", fmt.Errorf("no such target")
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return "", err
	}
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(r.Stdout)
	if !r.OK() || out == "" {
		return "", fmt.Errorf("could not create the project folder: %s", strings.TrimSpace(r.Stderr))
	}
	return out, nil
}
