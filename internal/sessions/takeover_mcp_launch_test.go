package sessions

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
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

// Takeover of a background run keeps its captured MCP declaration for the
// agents with an MCP adapter, and never hands Codex overrides to an agent
// that has none (Gemini).
func TestPrepareTakeoverTranslatesCapturedMCPPerAgent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "takeover", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/mock/repo"})
	if err != nil {
		t.Fatal(err)
	}
	reg := executor.NewRegistry(true, 0)
	m := New(db, reg, bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Specs = func() []Spec {
		return append(Builtins(), Spec{Name: "opencode", Command: "opencode"}, Spec{Name: "copilot", Command: "copilot"})
	}
	ex, err := reg.For(target)
	if err != nil {
		t.Fatal(err)
	}
	att := &store.Attempt{ID: 9, WorktreePath: "/mock/repo", MCPSnapshot: 1,
		MCPJSON: `{"old_tools":{"command":"old-mcp"}}`}
	for _, tc := range []struct {
		agent   string
		env     string
		args    []string
		noCodex bool
	}{
		{agent: "opencode", env: "OPENCODE_CONFIG"},
		{agent: "copilot", args: []string{"--additional-mcp-config", "@/tmp/lectern-mcp-state/lectern/mcp/mock/mcp.json"}},
		{agent: "gemini", noCodex: true},
	} {
		opts, err := m.PrepareTakeover(context.Background(), ex, &store.Task{Agent: tc.agent, Title: "t"}, project, att, map[string]string{})
		if err != nil {
			t.Fatalf("%s: %v", tc.agent, err)
		}
		if tc.env != "" && opts.Env[tc.env] != "/tmp/lectern-mcp-state/lectern/mcp/mock/mcp.json" {
			t.Fatalf("%s: takeover env should name the private MCP file: %v", tc.agent, opts.Env)
		}
		if tc.args != nil && strings.Join(opts.ExtraArgs, " ") != strings.Join(tc.args, " ") {
			t.Fatalf("%s: takeover args = %v, want %v", tc.agent, opts.ExtraArgs, tc.args)
		}
		if tc.noCodex && len(opts.ExtraArgs) != 0 {
			t.Fatalf("%s must not receive another agent's MCP arguments: %v", tc.agent, opts.ExtraArgs)
		}
	}
}
