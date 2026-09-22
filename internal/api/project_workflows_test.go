package api_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/skills"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestProjectWorkflowAPIListsDisabledAndValidatesToggles(t *testing.T) {
	h := newHarness(t)
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "workflow-api", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	var project obj
	h.decode("POST", "/api/projects", obj{"name": "workflow project", "target_id": target.ID, "repo_path": "/mock/workflows", "default_agent": "claude"}, 201, &project)

	var listed obj
	h.decode("GET", "/api/projects/"+itoa(project.id())+"/workflows?agent=claude", nil, 200, &listed)
	rows, ok := listed["workflows"].([]any)
	if !ok || len(rows) != 3 { // spec-kit, maestro, delegate
		t.Fatalf("workflows=%v", listed["workflows"])
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["enabled"] != false {
			t.Fatalf("new workflow unexpectedly enabled: %v", row)
		}
		if _, ok := row["commands"].([]any); !ok {
			t.Fatalf("commands missing: %v", row)
		}
	}
	if listed["reload_required"] != true {
		t.Fatalf("reload_required=%v", listed["reload_required"])
	}

	if got := h.status("PUT", "/api/projects/"+itoa(project.id())+"/workflows/spec-kit", obj{"agent": "claude"}); got != 422 {
		t.Fatalf("missing enabled status=%d", got)
	}
	if got := h.status("PUT", "/api/projects/"+itoa(project.id())+"/workflows/nope", obj{"agent": "claude", "enabled": true}); got != 404 {
		t.Fatalf("unknown workflow status=%d", got)
	}
	if got := h.status("PUT", "/api/projects/"+itoa(project.id())+"/workflows/spec-kit", obj{"agent": "gemini", "enabled": true}); got != 422 {
		t.Fatalf("unknown agent status=%d", got)
	}
}

func TestProjectWorkflowGETRejectsUnknownAgent(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Mock = true })
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "workflow-agent", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	var project obj
	h.decode("POST", "/api/projects", obj{"name": "workflow agent", "target_id": target.ID, "repo_path": "/mock/workflows"}, 201, &project)
	if got := h.status("GET", "/api/projects/"+itoa(project.id())+"/workflows?agent=gemini", nil); got != 422 {
		t.Fatalf("unknown agent status=%d", got)
	}
}

func TestProjectWorkflowEnableRejectsSandboxBeforeStaging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "workflow-sandbox", Kind: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	var project obj
	repo := t.TempDir()
	h.decode("POST", "/api/projects", obj{"name": "workflow sandbox", "target_id": target.ID, "repo_path": repo}, 201, &project)
	path := "/api/projects/" + itoa(project.id()) + "/workflows/spec-kit"
	var rejected obj
	h.decode("PUT", path, obj{"agent": "claude", "enabled": true}, 409, &rejected)
	if !strings.Contains(rejected.str("detail"), "persistent local or SSH/pct target") {
		t.Fatalf("sandbox rejection is not actionable: %v", rejected)
	}
	if rows, err := h.App.DB.ProjectSkills(project.id(), "claude"); err != nil || len(rows) != 0 {
		t.Fatalf("sandbox enable left attachment: %+v err=%v", rows, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".lectern")); !os.IsNotExist(err) {
		t.Fatalf("sandbox enable staged target source: %v", err)
	}
	if rows, err := h.App.DB.MaterializationsAt(target.ID, repo); err != nil || len(rows) != 0 {
		t.Fatalf("sandbox enable left materialization: %+v err=%v", rows, err)
	}
	if entries, err := os.ReadDir(repo); err != nil || len(entries) != 0 {
		t.Fatalf("sandbox enable changed host project directory: %+v err=%v", entries, err)
	}
}

func TestProjectWorkflowLifecycleReassertsAndPreservesProjectFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("workflow fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern", "-c", "user.email=lectern@example.invalid", "add", ".").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern", "-c", "user.email=lectern@example.invalid", "commit", "-qm", "fixture").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "workflow-lifecycle", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var project obj
	h.decode("POST", "/api/projects", obj{"name": "workflow lifecycle", "target_id": target.ID, "repo_path": repo, "default_agent": "claude"}, 201, &project)
	path := "/api/projects/" + itoa(project.id()) + "/workflows/"
	if got := h.status("PUT", path+"spec-kit", obj{"agent": "claude", "enabled": true}); got != 200 {
		t.Fatalf("enable spec-kit status=%d", got)
	}
	rows, err := h.App.DB.ProjectSkills(project.id(), "claude")
	if err != nil || len(rows) != 1 {
		t.Fatalf("spec-kit attachments=%+v err=%v", rows, err)
	}
	spec := rows[0]
	if !strings.HasPrefix(spec.SourcePath, filepath.Join(os.Getenv("HOME"), ".lectern", "workflows", "spec-kit")) {
		t.Fatalf("source was not target-local: %q", spec.SourcePath)
	}
	if _, err := os.Stat(filepath.Join(spec.SourcePath, "upstream", "LICENSE")); err != nil {
		t.Fatalf("upstream support missing: %v", err)
	}
	link := filepath.Join(repo, ".claude", "skills", "lectern-spec-kit")
	if targetPath, err := os.Readlink(link); err != nil || targetPath != spec.SourcePath {
		t.Fatalf("spec-kit link=%q err=%v", targetPath, err)
	}
	if got := h.status("PUT", path+"spec-kit", obj{"agent": "claude", "enabled": true}); got != 200 {
		t.Fatalf("repeat enable status=%d", got)
	}
	rows, _ = h.App.DB.ProjectSkills(project.id(), "claude")
	if len(rows) != 1 {
		t.Fatalf("repeat enable duplicated attachment: %d", len(rows))
	}
	child := filepath.Join(root, "child")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "workflow-child", child).CombinedOutput(); err != nil {
		t.Fatalf("git worktree: %v %s", err, out)
	}
	if err := skills.Reassert(context.Background(), executor.NewLocal(), h.App.DB, mustProject(t, h, project.id()), "claude", child); err != nil {
		t.Fatalf("reassert child: %v", err)
	}
	childLink := filepath.Join(child, ".claude", "skills", "lectern-spec-kit")
	if _, err := os.Lstat(childLink); err != nil {
		t.Fatalf("child workflow link missing: %v", err)
	}
	adapter := filepath.Join(spec.SourcePath, "workflow.py")
	if out, err := exec.Command("python3", adapter, "specify", "--project", repo).CombinedOutput(); err != nil {
		t.Fatalf("spec-kit adapter: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".specify", "spec.md"), []byte("generated specification\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", path+"spec-kit", obj{"agent": "claude", "enabled": false}, 200, nil)
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("owned workflow link remains: %v", err)
	}
	if _, err := os.Lstat(childLink); !os.IsNotExist(err) {
		t.Fatalf("child workflow link remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".specify", "spec.md")); err != nil {
		t.Fatalf("generated project file removed: %v", err)
	}
	if err := os.WriteFile(link, []byte("foreign workflow skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.status("PUT", path+"spec-kit", obj{"agent": "claude", "enabled": true}); got != 409 {
		t.Fatalf("foreign collision status=%d", got)
	}
	if content, err := os.ReadFile(link); err != nil || string(content) != "foreign workflow skill" {
		t.Fatalf("foreign collision changed: %q %v", content, err)
	}
	if rows, err := h.App.DB.ProjectSkills(project.id(), "claude"); err != nil || len(rows) != 0 {
		t.Fatalf("collision left attachment: %+v err=%v", rows, err)
	}
	if got := h.status("PUT", path+"maestro", obj{"agent": "codex", "enabled": true}); got != 200 {
		t.Fatalf("enable maestro status=%d", got)
	}
	maestroRows, err := h.App.DB.ProjectSkills(project.id(), "codex")
	if err != nil || len(maestroRows) != 1 {
		t.Fatalf("maestro attachments=%+v err=%v", maestroRows, err)
	}
	maestroSkill := filepath.Join(repo, ".agents", "skills", "lectern-maestro")
	if _, err := os.Lstat(maestroSkill); err != nil {
		t.Fatalf("maestro link missing: %v", err)
	}
	var afterMaestro obj
	h.decode("GET", "/api/projects/"+itoa(project.id())+"/workflows?agent=claude", nil, 200, &afterMaestro)
	for _, raw := range afterMaestro["workflows"].([]any) {
		row := raw.(map[string]any)
		if row["id"] == "spec-kit" && row["enabled"] != false {
			t.Fatalf("Claude Spec Kit unexpectedly enabled: %v", row)
		}
	}
	if err := os.WriteFile(filepath.Join(maestroRows[0].SourcePath, "SKILL.md"), []byte("tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := h.status("PUT", path+"maestro", obj{"agent": "codex", "enabled": true}); got != 500 {
		t.Fatalf("tampered source status=%d", got)
	}
}

func mustProject(t *testing.T, h *harness, id int64) *store.Project {
	t.Helper()
	p, err := h.App.DB.Project(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
