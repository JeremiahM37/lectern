package api

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Each target-side script here runs as `lectern helper` on a target whose
// lectern binary is known, and as the unchanged Python everywhere else.
func TestTargetScriptsRunTheTargetsLecternHelper(t *testing.T) {
	plain := executor.NewMock(0)
	withLectern := executor.NewMock(0)
	executor.SetTargetEnv(withLectern, executor.TargetEnv{Lectern: "/opt/lectern/bin/lectern"})
	const bin = "/opt/lectern/bin/lectern helper "
	checks := []struct {
		name   string
		render func(ex executor.Executor) string
		want   string
	}{
		{"ports", func(ex executor.Executor) string { return portsCommand(ex, "/w") }, bin + "ports /w"},
		{"review", func(ex executor.Executor) string { return reviewCommand(ex, "/w", "staged", "a b.txt") }, bin + "review /w staged 'a b.txt'"},
		{"review-git", func(ex executor.Executor) string { return reviewGitCommand(ex, "/w", "status", "e30=") }, bin + "review-git /w status e30="},
		{"workspace-files", func(ex executor.Executor) string {
			runWorkspaceScript(context.Background(), workspaceRef{ex: ex, dir: "/w"}, 10, "search", ".", "q", "0")
			log := ex.(*executor.Mock).CmdLog()
			return log[len(log)-1]
		}, bin + "workspace-files /w . search '' q 0"},
	}
	for _, c := range checks {
		if got := c.render(plain); !strings.HasPrefix(got, "python3 -c ") {
			t.Errorf("%s without lectern: %s", c.name, got)
		}
		if got := c.render(withLectern); got != c.want {
			t.Errorf("%s with lectern: %s", c.name, got)
		}
	}
}
