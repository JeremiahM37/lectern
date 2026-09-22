package sessions

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/bus"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

// launchWithMCPOverride executes the actual Manager launch path with the mock
// target only standing in for tmux. The command log is the exact interactive
// command that would have reached a target.
func launchWithMCPOverride(t *testing.T, agent, projectMCP string, strict int, extra []string) string {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "mcp launch", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{
		Name: "mcp project", TargetID: target.ID, RepoPath: "/mock/repo",
		DefaultAgent: agent, MCPJSON: projectMCP, StrictMCP: strict,
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := executor.NewRegistry(true, 0)
	m := New(db, reg, bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Specs = func() []Spec { return Builtins() }
	_, err = m.Launch(context.Background(), LaunchOpts{
		ProjectID: &project.ID, TargetID: target.ID, Agent: agent,
		Workdir: "/mock/repo", Name: "takeover", ExtraArgs: extra,
		SkipProjectMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := reg.For(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range ex.(*executor.Mock).CmdLog() {
		if strings.HasPrefix(cmd, "tmux new-session") {
			return cmd
		}
	}
	t.Fatal("manager did not issue an interactive tmux launch")
	return ""
}

func TestTakeoverLaunchUsesCapturedMCPInsteadOfCurrentProject(t *testing.T) {
	current := `{"new_tools":{"command":"new-mcp"}}`
	cases := []struct {
		name      string
		agent     string
		strict    int
		extra     []string
		want      []string
		forbidden []string
	}{
		{name: "claude empty snapshot", agent: "claude", strict: 1,
			forbidden: []string{"new_tools", "--mcp-config", "--strict-mcp-config"}},
		{name: "codex empty snapshot", agent: "codex", strict: 1,
			forbidden: []string{"new_tools", "mcp_servers.", "--strict-mcp-config"}},
		{name: "codex captured declaration", agent: "codex", strict: 1,
			extra:     []string{"-c", `mcp_servers.old_tools.command="old-mcp"`},
			want:      []string{"mcp_servers.old_tools.command", "old-mcp"},
			forbidden: []string{"new_tools", "mcp_servers.new_tools", "--strict-mcp-config"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := launchWithMCPOverride(t, tc.agent, current, tc.strict, tc.extra)
			for _, want := range tc.want {
				if !strings.Contains(cmd, want) {
					t.Fatalf("launch omitted captured value %q: %s", want, cmd)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(cmd, forbidden) {
					t.Fatalf("launch inherited current MCP value %q: %s", forbidden, cmd)
				}
			}
		})
	}
}
