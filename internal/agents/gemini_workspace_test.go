package agents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func geminiRun(t *testing.T, cmd string) *GeminiWorkspaceMCPResult {
	t.Helper()
	r, err := executor.NewLocal().Run(context.Background(), cmd, executor.RunOpts{Timeout: 30})
	if err != nil || !r.OK() {
		t.Fatalf("helper failed: %v %s", err, r.Stderr)
	}
	out, err := ParseGeminiWorkspaceMCP(r.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func geminiFixture(t *testing.T) (repo string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("LECTERN_SCRATCH_ROOT", filepath.Join(root, "scratch"))
	repo = filepath.Join(root, "wt")
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestGeminiWorkspaceMCPCreatesExcludedFileAndCleansUp(t *testing.T) {
	wt := geminiFixture(t)
	servers, err := GeminiMCPServers(map[string]any{
		"ops": map[string]any{"command": "python3", "args": []any{"-m", "ops"}},
		"web": map[string]any{"type": "http", "url": "https://x/mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	out := geminiRun(t, GeminiWorkspaceMCPCommand(wt, 5, true, servers))
	if out.Skipped != "" || !reflect.DeepEqual(out.Added, []string{"ops", "web"}) {
		t.Fatalf("install: %+v", out)
	}
	got := readSettings(t, wt)["mcpServers"].(map[string]any)
	if got["web"].(map[string]any)["type"] != "http" || got["ops"].(map[string]any)["command"] != "python3" {
		t.Fatalf("settings: %v", got)
	}
	if s := gitStatus(t, wt); s != "" {
		t.Fatalf("the settings file must not show up in git status: %q", s)
	}
	// A second session in the same workspace keeps the servers after the first ends.
	geminiRun(t, GeminiWorkspaceMCPCommand(wt, 6, true, servers))
	geminiRun(t, GeminiWorkspaceMCPCommand(wt, 5, false, nil))
	if _, ok := readSettings(t, wt)["mcpServers"].(map[string]any)["ops"]; !ok {
		t.Fatal("servers must stay while another session uses the workspace")
	}
	out = geminiRun(t, GeminiWorkspaceMCPCommand(wt, 6, false, nil))
	if !reflect.DeepEqual(out.Removed, []string{"ops", "web"}) {
		t.Fatalf("remove: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(wt, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("a .gemini directory Lectern created should be gone: %v", err)
	}
	exclude, _ := os.ReadFile(filepath.Join(wt, ".git", "info", "exclude"))
	if strings.Contains(string(exclude), "lectern-gemini-mcp") {
		t.Fatalf("exclude entry should be removed: %q", exclude)
	}
}

func TestGeminiWorkspaceMCPMergesAndKeepsUserServers(t *testing.T) {
	wt := geminiFixture(t)
	os.MkdirAll(filepath.Join(wt, ".gemini"), 0o755)
	user := `{"theme":"dark","mcpServers":{"mine":{"command":"mine"},"ops":{"command":"users-own-ops"}}}`
	os.WriteFile(filepath.Join(wt, ".gemini", "settings.json"), []byte(user), 0o644)
	servers, _ := GeminiMCPServers(map[string]any{"ops": map[string]any{"command": "lectern-ops"}, "extra": map[string]any{"command": "e"}})
	out := geminiRun(t, GeminiWorkspaceMCPCommand(wt, 1, true, servers))
	if !reflect.DeepEqual(out.Added, []string{"extra"}) || !reflect.DeepEqual(out.Kept, []string{"ops"}) {
		t.Fatalf("install: %+v", out)
	}
	s := readSettings(t, wt)
	m := s["mcpServers"].(map[string]any)
	if s["theme"] != "dark" || m["mine"] == nil || m["ops"].(map[string]any)["command"] != "users-own-ops" || m["extra"] == nil {
		t.Fatalf("merge lost user settings: %v", s)
	}
	geminiRun(t, GeminiWorkspaceMCPCommand(wt, 1, false, nil))
	after := readSettings(t, wt)
	var want map[string]any
	json.Unmarshal([]byte(user), &want)
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("cleanup should restore the user's file exactly:\n got %v\nwant %v", after, want)
	}
}

func TestGeminiWorkspaceMCPRefusesUnownedAndTrackedFiles(t *testing.T) {
	wt := geminiFixture(t)
	servers, _ := GeminiMCPServers(map[string]any{"ops": map[string]any{"command": "x"}})
	if out := geminiRun(t, GeminiWorkspaceMCPCommand(wt, 1, false, servers)); out.Skipped != "not-owned" {
		t.Fatalf("a checkout outside Lectern's workspaces must be left alone: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(wt, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("nothing may be written to an unowned checkout")
	}
	// A scratch directory is owned even when the caller cannot tell.
	scratch := filepath.Join(os.Getenv("LECTERN_SCRATCH_ROOT"), "gemini-1")
	os.MkdirAll(scratch, 0o755)
	exec.Command("git", "init", "-q", scratch).Run()
	if out := geminiRun(t, GeminiWorkspaceMCPCommand(scratch, 2, false, servers)); out.Skipped != "" || len(out.Added) != 1 {
		t.Fatalf("scratch workspace should get the servers: %+v", out)
	}
	// A repository that commits .gemini/settings.json is not edited.
	os.MkdirAll(filepath.Join(wt, ".gemini"), 0o755)
	os.WriteFile(filepath.Join(wt, ".gemini", "settings.json"), []byte("{}"), 0o644)
	exec.Command("git", "-C", wt, "add", ".gemini/settings.json").Run()
	if out := geminiRun(t, GeminiWorkspaceMCPCommand(wt, 3, true, servers)); out.Skipped != "tracked" {
		t.Fatalf("a tracked settings file must not be edited: %+v", out)
	}
	// JSON with comments is refused rather than rewritten.
	wt2 := filepath.Join(filepath.Dir(wt), "wt2")
	exec.Command("git", "init", "-q", wt2).Run()
	os.MkdirAll(filepath.Join(wt2, ".gemini"), 0o755)
	os.WriteFile(filepath.Join(wt2, ".gemini", "settings.json"), []byte("{ // mine\n}"), 0o644)
	r, _ := executor.NewLocal().Run(context.Background(), GeminiWorkspaceMCPCommand(wt2, 4, true, servers), executor.RunOpts{Timeout: 30})
	if _, err := ParseGeminiWorkspaceMCP(r.Stdout); err == nil || !strings.Contains(err.Error(), "comments") {
		t.Fatalf("a commented settings file must be refused, got %v", err)
	}
}

// Linked worktrees share one info/exclude; ending one workspace's session must
// not un-hide another workspace's file.
func TestGeminiWorkspaceMCPSiblingWorktreesKeepTheirExcludes(t *testing.T) {
	repo := geminiFixture(t)
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644)
	run("add", "f")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "b")
	a, b := filepath.Join(filepath.Dir(repo), "a"), filepath.Join(filepath.Dir(repo), "b")
	run("worktree", "add", "-q", a, "-b", "a")
	run("worktree", "add", "-q", b, "-b", "b")
	servers, _ := GeminiMCPServers(map[string]any{"ops": map[string]any{"command": "x"}})
	geminiRun(t, GeminiWorkspaceMCPCommand(a, 1, true, servers))
	geminiRun(t, GeminiWorkspaceMCPCommand(b, 2, true, servers))
	geminiRun(t, GeminiWorkspaceMCPCommand(a, 1, false, nil))
	if s := gitStatus(t, b); s != "" {
		t.Fatalf("the other worktree's settings file became visible: %q", s)
	}
	geminiRun(t, GeminiWorkspaceMCPCommand(b, 2, false, nil))
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if strings.Contains(string(exclude), "lectern-gemini-mcp") {
		t.Fatalf("exclude entries should all be removed: %q", exclude)
	}
}
