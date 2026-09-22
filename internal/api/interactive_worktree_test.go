package api_test

import (
	"fmt"
	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInteractiveSessionWorktreeLifecycle(t *testing.T) {
	requireRealTools(t)
	isolateTmux(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0700)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "proof"), []byte("base\n"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "local worktree", Kind: "local"})
	h.decode("PUT", "/api/agents", []obj{{"name": "test-worktree", "command": "sleep 600"}}, 200, nil)
	var row obj
	input := obj{"group_path": "Work/Worktrees", "name": "isolated session", "target_id": target.ID, "workdir": repo, "agent": "test-worktree", "worktree": obj{"branch": "feature/api"}, "yolo": false}
	h.decode("POST", "/api/sessions", input, 201, &row)
	ws := row["workspace"].(map[string]any)
	dir := ws["path"].(string)
	if row["group_path"] != "Work/Worktrees" || row["workdir"] != dir || dir == repo || ws["token"] != nil {
		t.Fatal(row)
	}
	base := fmt.Sprintf("/api/sessions/%d", int64(row.num("id")))
	h.decode("DELETE", base+"/worktree", nil, 409, nil)
	h.decode("DELETE", base, nil, 200, nil)
	os.WriteFile(filepath.Join(dir, "keep"), []byte("uncommitted"), 0600)
	h.decode("DELETE", base+"/worktree", nil, 409, nil)
	os.Remove(filepath.Join(dir, "keep"))
	h.decode("DELETE", base+"/worktree", nil, 200, &row)
	h.decode("DELETE", base+"/worktree", nil, 200, nil)
	if row["workspace"].(map[string]any)["state"] != "removed" {
		t.Fatal(row)
	}
	// The kept branch cannot silently be reused for a new worktree. Failed
	// allocation remains inspectable and can be cleared without deleting it.
	h.decode("POST", "/api/sessions", input, 409, nil)
	var failed []obj
	h.decode("GET", "/api/sessions?all=true", nil, 200, &failed)
	if len(failed) != 2 || failed[0]["workspace"].(map[string]any)["state"] != "failed" {
		t.Fatal(failed)
	}
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d/worktree", int64(failed[0].num("id"))), nil, 200, nil)
	input["resume"] = true
	h.decode("POST", "/api/sessions", input, 409, nil)
	var all []obj
	h.decode("GET", "/api/sessions?all=true", nil, 200, &all)
	if len(all) != 2 {
		t.Fatalf("allocation lost or invalid request created a session: %v", all)
	}
}

func TestMultiRepositorySessionLifecycle(t *testing.T) {
	requireRealTools(t)
	isolateTmux(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "group local", Kind: "local"})
	other, _ := h.App.DB.InsertTarget(&store.Target{Name: "other local", Kind: "local"})
	h.decode("PUT", "/api/agents", []obj{{"name": "group-test", "command": "sleep 600"}}, 200, nil)
	var projects []*store.Project
	for i := 0; i < 2; i++ {
		repo := t.TempDir()
		git := func(args ...string) {
			t.Helper()
			out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("git: %s", out)
			}
		}
		git("init", "-q")
		os.WriteFile(filepath.Join(repo, "file"), []byte("base"), 0600)
		git("add", ".")
		git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
		p, err := h.App.DB.InsertProject(&store.Project{Name: fmt.Sprintf("repo%d", i), TargetID: target.ID, RepoPath: repo})
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, p)
	}
	input := obj{"name": "grouped", "agent": "group-test", "project_id": projects[0].ID, "worktree": obj{"extra_repositories": []obj{{"project_id": projects[1].ID}}}, "yolo": false}
	var row obj
	h.decode("POST", "/api/sessions", input, 201, &row)
	ws := row["workspace"].(map[string]any)
	dir := ws["path"].(string)
	if row["workdir"] != dir || ws["token"] != nil {
		t.Fatal("group root or public ownership is incorrect")
	}
	repositories := ws["repositories"].([]any)
	if len(repositories) != 2 {
		t.Fatal("missing repositories")
	}
	for _, r := range repositories {
		child := r.(map[string]any)["worktree"].(map[string]any)
		if child["token"] != nil {
			t.Fatal("child token exposed")
		}
		if _, err := os.Stat(filepath.Join(child["path"].(string), "file")); err != nil {
			t.Fatal(err)
		}
	}
	base := fmt.Sprintf("/api/sessions/%d", int64(row.num("id")))
	if len(repositories) != 2 {
		t.Fatal("missing grouped review choices")
	}
	secondPath := repositories[1].(map[string]any)["worktree"].(map[string]any)["path"].(string)
	if err := os.WriteFile(filepath.Join(secondPath, "file"), []byte("second repository change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	filesURL := fmt.Sprintf("/api/term/session/%d", int64(row.num("id")))
	var listing obj
	h.decode("GET", filesURL+"/files", nil, 200, &listing)
	if strings.Contains(fmt.Sprint(listing), ".lectern-") {
		t.Fatal("workspace bookkeeping leaked into file browser")
	}
	for _, name := range []string{".lectern-lock", ".lectern-state.json", ".lectern-process.json"} {
		h.decode("GET", filesURL+"/file?path="+name, nil, 400, nil)
	}
	for _, r := range repositories {
		child := r.(map[string]any)["worktree"].(map[string]any)
		relative := filepath.Base(child["path"].(string)) + "/file"
		code, data := h.request("GET", filesURL+"/file?path="+relative, nil, nil)
		if code != 200 || len(data) == 0 {
			t.Fatal("repository files unavailable through shared root")
		}
	}
	reviewURL := fmt.Sprintf("/api/term/session/%d/changes", int64(row.num("id")))
	var changes obj
	h.decode("GET", reviewURL+"?repository=1&path=file", nil, 200, &changes)
	var shared obj
	sharedInput := obj{"name": "shared grouped continuation", "target_id": target.ID, "workdir": dir, "agent": "group-test", "yolo": false}
	h.decode("POST", "/api/sessions", sharedInput, 201, &shared)
	if shared["workspace"] != nil {
		t.Fatal("shared session inherited removal ownership")
	}
	sharedTerm := fmt.Sprintf("/api/term/session/%d", int64(shared.num("id")))
	var sharedReview obj
	h.decode("GET", sharedTerm+"/changes?repository=1&path=file", nil, 200, &sharedReview)
	if !strings.Contains(sharedReview.str("patch"), "second repository change") {
		t.Fatal("shared session lost repository review")
	}
	h.decode("GET", sharedTerm+"/files", nil, 200, &listing)
	if err := os.WriteFile(filepath.Join(secondPath, "committed-child"), []byte("child branch commit"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "committed-child"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "child commit"}} {
		if output, err := exec.Command("git", append([]string{"-C", secondPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s", output)
		}
	}
	var isolated obj
	h.decode("POST", "/api/sessions", obj{"name": "isolated grouped continuation", "target_id": target.ID, "workdir": dir, "agent": "group-test", "worktree": obj{}, "yolo": false}, 201, &isolated)
	isolatedWS := isolated["workspace"].(map[string]any)
	isolatedChildren := isolatedWS["repositories"].([]any)
	isolatedSecond := isolatedChildren[1].(map[string]any)["worktree"].(map[string]any)
	isolatedPath := isolatedSecond["path"].(string)
	if data, _ := os.ReadFile(filepath.Join(isolatedPath, "committed-child")); string(data) != "child branch commit" {
		t.Fatal("fork missed child committed revision")
	}
	if data, _ := os.ReadFile(filepath.Join(isolatedPath, "file")); string(data) != "base" {
		t.Fatal("fork copied uncommitted parent changes")
	}
	if isolatedSecond["repo"] != projects[1].RepoPath {
		t.Fatal("fork cleanup depends on temporary parent directory")
	}
	if strings.Contains(fmt.Sprint(listing), ".lectern-") {
		t.Fatal("shared session exposed bookkeeping")
	}
	if changes.num("selected_repository") != 1 || !strings.Contains(changes.str("patch"), "second repository change") {
		t.Fatal("review did not use selected repository")
	}
	h.decode("GET", reviewURL+"?repository=0", nil, 200, &changes)
	if strings.Contains(changes.str("patch"), "second repository change") {
		t.Fatal("review mixed repository files")
	}
	h.decode("GET", reviewURL+"?repository=2", nil, 400, nil)
	h.decode("GET", reviewURL+"?repository=../outside", nil, 400, nil)
	if err := os.WriteFile(filepath.Join(secondPath, "file"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	h.decode("DELETE", base+"/worktree", nil, 409, nil)
	h.decode("DELETE", base, nil, 200, nil)
	h.decode("DELETE", base+"/worktree", nil, 409, nil)
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", int64(shared.num("id"))), nil, 200, nil)
	h.decode("DELETE", base+"/worktree", nil, 200, &row)
	if row["workspace"].(map[string]any)["state"] != "removed" {
		t.Fatal("removal not persisted")
	}
	isolatedBase := fmt.Sprintf("/api/sessions/%d", int64(isolated.num("id")))
	h.decode("DELETE", isolatedBase, nil, 200, nil)
	h.decode("DELETE", isolatedBase+"/worktree", nil, 200, nil)
	// Cleanup retains a receipt directory; it is not a usable agent workspace.
	h.decode("POST", "/api/sessions", sharedInput, 409, nil)
	// Selection errors occur before a session is inserted, including an explicitly
	// mismatched target and selecting the primary repository twice.
	var before, after []obj
	h.decode("GET", "/api/sessions?all=true", nil, 200, &before)
	input["target_id"] = other.ID
	h.decode("POST", "/api/sessions", input, 409, nil)
	input["target_id"] = target.ID
	input["worktree"] = obj{"extra_repositories": []obj{{"project_id": projects[0].ID}}}
	h.decode("POST", "/api/sessions", input, 409, nil)
	h.decode("GET", "/api/sessions?all=true", nil, 200, &after)
	if len(after) != len(before) {
		t.Fatal("invalid selection inserted a session")
	}
	hook := filepath.Join(projects[1].RepoPath, ".git/hooks/post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf keep > setup-artifact\necho grouped-hook-failure >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	input["worktree"] = obj{"extra_repositories": []obj{{"project_id": projects[1].ID}}}
	h.decode("POST", "/api/sessions", input, 409, nil)
	h.decode("GET", "/api/sessions?all=true", nil, 200, &after)
	if len(after) != len(before)+1 {
		t.Fatal("failed allocation was not retained")
	}
	var failed obj
	for _, candidate := range after {
		workspace, ok := candidate["workspace"].(map[string]any)
		if ok && workspace["state"] == "failed" {
			failed = candidate
		}
	}
	if failed == nil {
		t.Fatal("failed grouped workspace state was lost")
	}
	failedWS := failed["workspace"].(map[string]any)
	children := failedWS["repositories"].([]any)
	last := children[1].(map[string]any)["worktree"].(map[string]any)
	if last["token"] != nil || last["state"] != "failed" {
		t.Fatal("failed child receipt or token redaction incorrect")
	}
	failedBase := fmt.Sprintf("/api/sessions/%d", int64(failed.num("id")))
	h.decode("DELETE", failedBase+"/worktree", nil, 409, nil)
	if err := os.Remove(filepath.Join(last["path"].(string), "setup-artifact")); err != nil {
		t.Fatal(err)
	}
	h.decode("DELETE", failedBase+"/worktree", nil, 200, &row)
	if row["workspace"].(map[string]any)["state"] != "removed" {
		t.Fatal("failed grouped workspace could not be recovered through API")
	}
}
