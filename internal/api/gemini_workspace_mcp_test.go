package api_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Interactive Gemini gets project MCP servers through .gemini/settings.json
// only in a workspace Lectern created: the user's own checkout is untouched,
// a Lectern worktree gets the file (invisible to git), and ending the session
// removes it again.
func TestGeminiProjectMCPOnlyInLecternWorkspaces(t *testing.T) {
	requireRealTools(t)
	isolateTmux(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("LECTERN_SCRATCH_ROOT", filepath.Join(root, "scratch"))
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0o700)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return string(out)
	}
	git(repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "proof"), []byte("base\n"), 0o600)
	git(repo, "add", ".")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "local gemini", Kind: "local"})
	project, err := h.App.DB.InsertProject(&store.Project{Name: "gemini-mcp", TargetID: target.ID, RepoPath: repo,
		MCPJSON: `{"ops":{"command":"python3","args":["-m","ops"]}}`})
	if err != nil {
		t.Fatal(err)
	}
	// Stand in for the Gemini binary; the MCP translation keys on the name.
	h.decode("PUT", "/api/agents", []obj{{"name": "gemini", "command": "sleep 600"}}, 200, nil)

	var own obj
	h.decode("POST", "/api/sessions", obj{"project_id": project.ID, "agent": "gemini", "yolo": false}, 201, &own)
	if _, err := os.Stat(filepath.Join(repo, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("the user's own checkout must not be written to: %v", err)
	}
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", int64(own.num("id"))), nil, 200, nil)

	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": project.ID, "agent": "gemini", "worktree": obj{"branch": "gemini-mcp"}, "yolo": false}, 201, &row)
	dir := row["workspace"].(map[string]any)["path"].(string)
	raw, err := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	if err != nil || !strings.Contains(string(raw), `"command": "python3"`) {
		t.Fatalf("a Lectern worktree should get the project's servers: %s %v", raw, err)
	}
	if s := git(dir, "status", "--porcelain", "--untracked-files=all"); s != "" {
		t.Fatalf("the settings file must not show as a change: %q", s)
	}
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", int64(row.num("id"))), nil, 200, nil)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, ".gemini")); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ending the session should remove the servers Lectern wrote")
}
