package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

func TestCurrentGitProjectDetectsRepo(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := currentGitProject(dir); ok {
		t.Fatalf("a plain directory should not look like a git repo")
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, repoPath, ok := currentGitProject(dir)
	if !ok {
		t.Fatalf("a directory with .git/ should be detected")
	}
	if name != filepath.Base(dir) {
		t.Errorf("name = %q, want %q", name, filepath.Base(dir))
	}
	if repoPath != dir {
		// dir may not be absolute in a temp dir on some platforms; compare abs.
		abs, _ := filepath.Abs(dir)
		if repoPath != abs {
			t.Errorf("repoPath = %q, want %q", repoPath, abs)
		}
	}
}

func TestCurrentGitProjectDetectsWorktreeFile(t *testing.T) {
	// A worktree or submodule leaves .git as a FILE, not a directory.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := currentGitProject(dir); !ok {
		t.Fatalf("a .git FILE (worktree/submodule) should still count as a repo")
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	if !samePath(dir, dir) {
		t.Errorf("a path should equal itself")
	}
	if !samePath(dir+string(filepath.Separator), dir) {
		t.Errorf("a trailing separator should not matter")
	}
	if samePath(dir, dir+"-other") {
		t.Errorf("distinct paths must not be equal")
	}
}

// fakeBoard is a minimal stand-in for the targets/projects endpoints `up`
// depends on, so ensureLocalProject's logic — find the local target, look
// for an existing project at this path, create one if missing — is tested
// without a real Lectern process.
type fakeBoard struct {
	targets  []upTargetView
	projects []upProjectView
	nextID   int64
	posts    []map[string]any
}

func newFakeBoard() *http.ServeMux {
	fb := &fakeBoard{
		targets: []upTargetView{{ID: 1, Kind: "local"}},
		nextID:  100,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/targets", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(fb.targets)
	})
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			fb.posts = append(fb.posts, body)
			fb.nextID++
			p := upProjectView{ID: fb.nextID, Name: body["name"].(string), RepoPath: body["repo_path"].(string)}
			fb.projects = append(fb.projects, p)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		_ = json.NewEncoder(w).Encode(fb.projects)
	})
	return mux
}

func TestEnsureLocalProjectCreatesOnce(t *testing.T) {
	srv := httptest.NewServer(newFakeBoard())
	defer srv.Close()
	c := console.New(srv.URL, "")

	id1, created1, err := ensureLocalProject(c, "myrepo", "/srv/myrepo")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !created1 {
		t.Errorf("first call should create a project")
	}
	id2, created2, err := ensureLocalProject(c, "myrepo", "/srv/myrepo")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created2 {
		t.Errorf("second call should find the existing project, not create another")
	}
	if id1 != id2 {
		t.Errorf("id1=%d id2=%d should match", id1, id2)
	}
}

func TestEnsureLocalProjectNeedsLocalTarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/targets", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]upTargetView{{ID: 1, Kind: "ssh"}})
	})
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]upProjectView{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := console.New(srv.URL, "")
	if _, _, err := ensureLocalProject(c, "myrepo", "/srv/myrepo"); err == nil {
		t.Fatalf("expected an error when no local target is registered")
	}
}

func TestFetchOnboardingParsesResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/onboarding", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{
				{"name": "claude", "found": true, "builtin": true},
				{"name": "codex", "found": false, "builtin": true},
			},
			"tmux":     map[string]any{"name": "tmux", "ok": true},
			"git":      map[string]any{"name": "git", "ok": true},
			"projects": 0,
			"sessions": 0,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := console.New(srv.URL, "")
	status, err := fetchOnboarding(c)
	if err != nil {
		t.Fatalf("fetchOnboarding: %v", err)
	}
	if len(status.Agents) != 2 || !status.Agents[0].Found || status.Agents[1].Found {
		t.Errorf("agents not parsed correctly: %+v", status.Agents)
	}
	if !status.Tmux.OK || !status.Git.OK {
		t.Errorf("tmux/git not parsed correctly: %+v %+v", status.Tmux, status.Git)
	}
}
