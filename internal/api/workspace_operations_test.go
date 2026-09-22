package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/worktree"
)

func extensionAPI(t *testing.T, command string) (*harness, *store.Session, int64) {
	t.Helper()
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "extension local", Kind: "local"})
	h.decode("PUT", "/api/agents", []obj{{"name": "extension-fixture", "command": "sleep 600"}}, 200, nil)
	var projects []obj
	for i := 0; i < 3; i++ {
		repo := filepath.Join(t.TempDir(), "repository")
		os.Mkdir(repo, 0700)
		for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "base"}} {
			if out, e := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); e != nil {
				t.Fatalf("git: %v %s", e, out)
			}
		}
		var project obj
		input := obj{"name": fmt.Sprintf("repo %d", i), "target_id": target.ID, "repo_path": repo}
		if i == 2 {
			input["setup_cmd"] = command
			input["env"] = obj{"EXTENSION_ENV": "private-extension-value"}
		}
		h.decode("POST", "/api/projects", input, 201, &project)
		projects = append(projects, project)
	}
	var row obj
	h.decode("POST", "/api/sessions", obj{"name": "Existing interactive group", "agent": "extension-fixture", "project_id": projects[0].id(), "worktree": obj{"extra_repositories": []obj{{"project_id": projects[1].id()}}}}, 201, &row)
	saved, err := h.App.DB.Session(row.id())
	if err != nil {
		t.Fatal(err)
	}
	return h, saved, projects[2].id()
}
func awaitExtension(t *testing.T, h *harness, id int64) *store.WorkspaceOperation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		op, err := h.App.DB.WorkspaceOperation(id)
		if err != nil {
			t.Fatal(err)
		}
		if !op.Active() {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation did not settle: %+v", op)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func checkOriginalSession(t *testing.T, h *harness, before *store.Session) {
	t.Helper()
	after, err := h.App.DB.Session(before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TmuxSession != before.TmuxSession || after.Workdir != before.Workdir || after.TrackingIdentity != before.TrackingIdentity || after.SetupState != before.SetupState || after.EndedAt != nil {
		t.Fatal("extension replaced or ended original session")
	}
	if err := exec.Command("tmux", "has-session", "-t", "="+before.TmuxSession).Run(); err != nil {
		t.Fatal("original terminal stopped")
	}
}

func TestExtensionAPIBackgroundCompletionAndPrivacy(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	h, before, project := extensionAPI(t, "touch "+shellq.Quote(started)+"; while [ ! -f "+shellq.Quote(release)+" ]; do sleep .05; done; echo prepared > prepared")
	t.Cleanup(func() { os.WriteFile(release, []byte("release"), 0600) })
	endpoint := fmt.Sprintf("/api/sessions/%d/worktree", before.ID)
	start := time.Now()
	var response obj
	h.decode("POST", endpoint+"/repositories", obj{"project_id": project}, 202, &response)
	if time.Since(start) > 2*time.Second {
		t.Fatal("request waited for checkout completion")
	}
	h.decode("POST", endpoint+"/repositories", obj{"project_id": project}, 409, nil)
	h.decode("DELETE", endpoint, nil, 409, nil)
	checkOriginalSession(t, h, before)
	operation := fmt.Sprintf("%s/operations/%d", endpoint, response.id())
	status, body := h.request("GET", operation, nil, nil)
	if status != 200 || strings.Contains(string(body), "private-extension-value") || strings.Contains(string(body), "plan_json") {
		t.Fatal("operation response exposes captured environment")
	}
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/worktree/operations/%d", before.ID+999, response.id()), nil, 404, nil)
	os.WriteFile(release, []byte("release"), 0600)
	op := awaitExtension(t, h, response.id())
	if op.State != "complete" {
		t.Fatalf("extension failed: %s", op.Error)
	}
	checkOriginalSession(t, h, before)
	var progress obj
	h.decode("GET", endpoint, nil, 200, &progress)
	if len(progress["repositories"].([]any)) != 3 || progress["control_token"] != nil {
		t.Fatal("expanded workspace missing or exposes cancellation identity")
	}
	var rows []obj
	h.decode("GET", endpoint+"/operations", nil, 200, &rows)
	if len(rows) != 1 {
		t.Fatal("operation history missing")
	}
	h.decode("POST", operation+"/cancel", obj{}, 200, nil)
}

func TestExtensionAPICancelKeepsExistingSession(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	escaped := filepath.Join(root, "escaped")
	h, before, project := extensionAPI(t, "echo keep > prepared; touch "+shellq.Quote(started)+"; while [ ! -f "+shellq.Quote(release)+" ]; do sleep .05; done; touch "+shellq.Quote(escaped))
	t.Cleanup(func() { os.WriteFile(release, []byte("release"), 0600) })
	endpoint := fmt.Sprintf("/api/sessions/%d/worktree", before.ID)
	var response obj
	h.decode("POST", endpoint+"/repositories", obj{"project_id": project}, 202, &response)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook not started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.decode("POST", fmt.Sprintf("%s/operations/%d/cancel", endpoint, response.id()), obj{}, 200, nil)
	op := awaitExtension(t, h, response.id())
	if op.State != "cancelled" {
		t.Fatalf("cancellation hidden: %+v", op)
	}
	os.WriteFile(release, []byte("release"), 0600)
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatal("cancelled checkout kept writing")
	}
	checkOriginalSession(t, h, before)
	saved, _ := h.App.DB.Session(before.ID)
	var plan worktree.Interactive
	json.Unmarshal([]byte(saved.WorktreeJSON), &plan)
	if body, _ := os.ReadFile(filepath.Join(plan.Repositories[2].Worktree.Path, "prepared")); string(body) != "keep\n" {
		t.Fatal("cancellation discarded files")
	}
}

func TestExtensionRecoveryFencesUnstartedWorker(t *testing.T) {
	h, before, project := extensionAPI(t, "echo should-not-run > prepared")
	var plan worktree.Interactive
	json.Unmarshal([]byte(before.WorktreeJSON), &plan)
	source, _ := h.App.DB.Project(project)
	next, err := worktree.PlanWorkspaceExtension(&plan, worktree.RepositorySource{Name: source.Name, Repo: source.RepoPath, SetupCommand: source.SetupCmd}, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	op, err := h.App.DB.BeginWorkspaceOperation(before.ID, before.WorktreeJSON, store.J(next))
	if err != nil {
		t.Fatal(err)
	}
	// New manager has no in-memory worker; the durable plan is its only evidence.
	old := h.App.Sessions
	restarted := sessions.New(old.DB, old.Reg, old.Bus, old.Launcher, nil, old.Log)
	recovered, err := restarted.RecoverWorkspaceOperation(context.Background(), before.ID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "failed" {
		t.Fatalf("unstarted operation not reconciled: %+v", recovered)
	}
	target, _ := h.App.DB.Target(before.TargetID)
	ex, _ := h.App.Reg.For(target)
	if err := worktree.RunInteractive(context.Background(), ex, "extend", next); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("late worker was not fenced: %v", err)
	}
	if _, err := os.Stat(next.Repositories[2].Worktree.Path); !os.IsNotExist(err) {
		t.Fatal("late worker allocated files")
	}
	checkOriginalSession(t, h, before)
}
