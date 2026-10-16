package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func callTool(t *testing.T, tool *skillsTool, root, args string) tools.Result {
	t.Helper()
	return tool.Run(context.Background(), &tools.Env{Workdir: root}, json.RawMessage(args))
}

func TestSkillsToolLoadUnloadListActive(t *testing.T) {
	root, _ := env(t)
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "release"), skill("release", "Cut a release.", "Tag, then push."))
	tool := &skillsTool{}

	if res := callTool(t, tool, root, `{"action":"list_active"}`); res.IsError || !strings.Contains(res.Output, "No skills are loaded") {
		t.Fatalf("empty active set: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"load","name":"release"}`); res.IsError {
		t.Fatalf("load: %+v", res)
	}
	// Loading twice records the skill once.
	callTool(t, tool, root, `{"action":"load","name":"release"}`)
	res := callTool(t, tool, root, `{"action":"list_active"}`)
	if res.IsError || res.Output != "release (project)" {
		t.Fatalf("list_active after load: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"unload","name":"release"}`); res.IsError || !strings.Contains(res.Output, "stop following them") {
		t.Fatalf("unload: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"list_active"}`); !strings.Contains(res.Output, "No skills are loaded") {
		t.Fatalf("list_active after unload: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"unload","name":"release"}`); !res.IsError || !strings.Contains(res.Output, "not loaded") {
		t.Fatalf("second unload should fail: %+v", res)
	}
}

func TestSkillsToolActiveReportsRemovedSkill(t *testing.T) {
	root, _ := env(t)
	dir := filepath.Join(root, ".scribe", "skills", "gone")
	writeSkill(t, dir, skill("gone", "Soon removed.", "x"))
	tool := &skillsTool{}
	callTool(t, tool, root, `{"action":"load","name":"gone"}`)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, tool, root, `{"action":"list_active"}`)
	if !strings.Contains(res.Output, "gone (no longer installed)") {
		t.Fatalf("removed skill not flagged: %+v", res)
	}
}

func TestSkillsToolSearchAndInstallID(t *testing.T) {
	root, _ := env(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("q") != "review" {
			_, _ = w.Write([]byte(`{"skills":[],"count":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"skills":[{"id":"owner/repo/review","skillId":"review","name":"review","installs":42,"source":"owner/repo"}],"count":1}`))
	}))
	defer srv.Close()
	tool := &skillsTool{registry: &Registry{BaseURL: srv.URL, Client: srv.Client()}}

	res := callTool(t, tool, root, `{"action":"search","query":"review"}`)
	if res.IsError || !strings.Contains(res.Output, "owner/repo/review (install id owner/repo#review, 42 installs)") {
		t.Fatalf("search: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"search"}`); !res.IsError {
		t.Fatalf("search without a query should fail: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"search","query":"nothing-matches"}`); res.IsError || !strings.Contains(res.Output, "No registry skills") {
		t.Fatalf("empty search: %+v", res)
	}
}

func TestSkillsToolSearchRegistryDown(t *testing.T) {
	root, _ := env(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	tool := &skillsTool{registry: &Registry{BaseURL: srv.URL, Client: srv.Client()}}
	res := callTool(t, tool, root, `{"action":"search","query":"review"}`)
	if !res.IsError || !strings.Contains(res.Output, "could not be reached") || !strings.Contains(res.Output, "action install") {
		t.Fatalf("registry down should say what to do instead: %+v", res)
	}
}

func TestSkillsToolInstallFromLocalRepository(t *testing.T) {
	root, _ := env(t)
	repo := t.TempDir()
	writeSkill(t, filepath.Join(repo, "skills", "review"), skill("review", "Reviews code.", "Read the diff."))
	tool := &skillsTool{}

	res := callTool(t, tool, root, `{"action":"install","id":"`+repo+`#review"}`)
	if res.IsError || !strings.Contains(res.Output, "Installed skill review (project)") {
		t.Fatalf("install: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, ".scribe", "skills", "review", SkillFile)); err != nil {
		t.Fatalf("skill not copied into the project: %v", err)
	}
	if res := callTool(t, tool, root, `{"action":"install","id":"`+repo+`#review"}`); !res.IsError {
		t.Fatalf("second install of the same skill should fail: %+v", res)
	}
	if res := callTool(t, tool, root, `{"action":"install"}`); !res.IsError || !strings.Contains(res.Output, "install needs an id") {
		t.Fatalf("install without an id: %+v", res)
	}
}

func TestInstallSourceForms(t *testing.T) {
	local := t.TempDir()
	cases := map[string]string{
		"owner/repo#review":          "https://github.com/owner/repo.git#review",
		"owner/repo":                 "https://github.com/owner/repo.git",
		local + "#review":            local + "#review",
		"https://example.com/x.git#": "https://example.com/x.git#",
		"git@host:owner/repo.git":    "git@host:owner/repo.git",
	}
	for in, want := range cases {
		if got := installSource(in); got != want {
			t.Errorf("installSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillsToolReadOnlyCall(t *testing.T) {
	tool := &skillsTool{}
	if tool.ReadOnlyCall(json.RawMessage(`{"action":"install","id":"a/b"}`)) {
		t.Fatal("install writes files and must not be read-only")
	}
	for _, act := range []string{"list", "search", "list_active", "load", "unload"} {
		if !tool.ReadOnlyCall(json.RawMessage(`{"action":"` + act + `"}`)) {
			t.Fatalf("%s should be read-only", act)
		}
	}
}
