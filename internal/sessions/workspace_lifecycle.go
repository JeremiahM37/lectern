package sessions

import (
	"context"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"path"
	"strings"
)

// Workspace operations reserve paths only while they use them. A slow checkout
// must not hold up cleanup on another target or in an unrelated directory.
// These controller reservations complement the target's allocation locks.
type workspaceUse struct {
	target   int64
	paths    []string
	removing bool
}

func workspacePathsOverlap(a, b string) bool {
	a, b = path.Clean(a), path.Clean(b)
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}

func (m *Manager) reserveWorkspacePaths(target int64, removing bool, paths ...string) (*workspaceUse, error) {
	use := &workspaceUse{target: target, removing: removing}
	if err := m.extendWorkspacePaths(use, paths...); err != nil {
		return nil, err
	}
	return use, nil
}

func (m *Manager) extendWorkspacePaths(use *workspaceUse, paths ...string) error {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	for other := range m.workspaceUses {
		if other == use || other.target != use.target || (!other.removing && !use.removing) {
			continue
		}
		for _, requested := range paths {
			if requested == "" {
				continue
			}
			for _, active := range other.paths {
				if workspacePathsOverlap(requested, active) {
					return fmt.Errorf("workspace is in use by another setup or cleanup; retry when that operation finishes")
				}
			}
		}
	}
	if m.workspaceUses == nil {
		m.workspaceUses = map[*workspaceUse]bool{}
	}
	for _, requested := range paths {
		if requested != "" {
			use.paths = append(use.paths, path.Clean(requested))
		}
	}
	m.workspaceUses[use] = true
	return nil
}

func (m *Manager) releaseWorkspacePaths(use *workspaceUse) {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	delete(m.workspaceUses, use)
}

// Existing source directories are resolved on their own target so a symlink
// alias cannot evade a cleanup reservation. Launch already requires a real cwd.
func canonicalWorkspaceSource(ctx context.Context, ex executor.Executor, directory string) (string, error) {
	result, err := ex.Run(ctx, "cd -P -- "+shellq.Quote(directory)+" && pwd -P", executor.RunOpts{Timeout: 10})
	if err != nil {
		return "", err
	}
	if !result.OK() {
		return "", fmt.Errorf("working directory is unavailable on target: %s", directory)
	}
	return strings.TrimSuffix(result.Stdout, "\n"), nil
}

// Allocations may not exist yet (or may be partially removed). Worktree targets
// already require Python; realpath also resolves symlinks in existing parents.
func canonicalWorkspaceAllocation(ctx context.Context, ex executor.Executor, directory string) (string, error) {
	result, err := ex.Run(ctx, "python3 -c "+shellq.Quote("import os,sys;print(os.path.realpath(sys.argv[1]))")+" "+shellq.Quote(directory), executor.RunOpts{Timeout: 10})
	if err != nil {
		return "", err
	}
	if !result.OK() {
		return "", fmt.Errorf("could not resolve workspace allocation on target")
	}
	return strings.TrimSuffix(result.Stdout, "\n"), nil
}
