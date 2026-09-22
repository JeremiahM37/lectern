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

func TestBackgroundWorkspaceSurvivesResponseAndRetainsFailures(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	os.Mkdir(repo, 0700)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "base"), []byte("original"), 0600)
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "base")
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "background fixture", Kind: "local"})
	h.decode("PUT", "/api/agents", []obj{{"name": "background-fixture", "command": "sleep 600"}}, 200, nil)
	// Keep a separate ended allocation on the same target. Its cleanup must not
	// wait behind the held checkout below.
	unrelatedRepo := filepath.Join(root, "unrelated")
	git("clone", "-q", repo, unrelatedRepo)
	var unrelated obj
	h.decode("POST", "/api/sessions", obj{"agent": "background-fixture", "target_id": target.ID, "workdir": unrelatedRepo, "worktree": obj{}, "yolo": false}, 201, &unrelated)
	unrelatedEndpoint := fmt.Sprintf("/api/sessions/%d", unrelated.id())
	h.decode("DELETE", unrelatedEndpoint, nil, 200, nil)
	started, release := filepath.Join(root, "started"), filepath.Join(root, "release")
	hook := filepath.Join(repo, ".git/hooks/post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+shellq.Quote(started)+"\nwhile [ ! -f "+shellq.Quote(release)+" ]; do sleep 0.05; done\n"), 0700)
	t.Cleanup(func() { os.WriteFile(release, []byte("release"), 0600) })
	var row obj
	input := obj{"agent": "background-fixture", "target_id": target.ID, "workdir": repo, "worktree": obj{}, "background": true, "yolo": false}
	if os.Getenv("LECTERN_GROUPED_SETUP_PROOF") == "1" {
		extra := filepath.Join(root, "extra")
		git("clone", "-q", repo, extra)
		var primaryProject, extraProject obj
		h.decode("POST", "/api/projects", obj{"name": "primary", "target_id": target.ID, "repo_path": repo}, 201, &primaryProject)
		h.decode("POST", "/api/projects", obj{"name": "extra", "target_id": target.ID, "repo_path": extra}, 201, &extraProject)
		delete(input, "workdir")
		input["project_id"] = primaryProject.id()
		input["worktree"] = obj{"extra_repositories": []obj{{"project_id": extraProject.id()}}}
	}
	h.decode("POST", "/api/sessions", input, 202, &row)
	id := row.id()
	endpoint := fmt.Sprintf("/api/sessions/%d", id)
	reserved, err := h.App.DB.Session(id)
	if err != nil {
		t.Fatal(err)
	}
	var captured sessions.LaunchConfiguration
	if err := json.Unmarshal([]byte(reserved.LaunchConfigJSON), &captured); err != nil {
		t.Fatalf("reservation lost launch configuration: %v", err)
	}
	if captured.Spec.Command != "sleep 600" {
		t.Fatal("reservation did not capture requested command")
	}
	// Reusable settings may change while checkout runs; this reservation must
	// keep its original command, both at launch and if checkout fails.
	h.decode("PUT", "/api/agents", []obj{{"name": "background-fixture", "command": "sleep 601"}}, 200, nil)
	if row["setup_state"] != "creating" {
		t.Fatal(row)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- h.App.Sessions.RemoveWorktree(context.Background(), unrelated.id()) }()
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatalf("unrelated cleanup failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		// Release before failing so harness cleanup cannot hang on the worker.
		os.WriteFile(release, []byte("release"), 0600)
		<-cleanupDone
		t.Fatal("unrelated cleanup waited behind a slow setup")
	}
	if _, err := os.Stat(release); !os.IsNotExist(err) {
		t.Fatal("checkout was not held during cleanup")
	}
	h.App.DB.Update("sessions", id, map[string]any{"created_at": store.Now() - 180})
	h.App.Sessions.Poll(context.Background())
	current, _ := h.App.DB.Session(id)
	if current.EndedAt != nil || current.SetupState != "creating" {
		t.Fatal("poll ended active setup")
	}
	// Opt-in wall-clock proof crosses the former Git (90s), child (105s),
	// and outer executor (120s) deadlines without slowing every unit run.
	if os.Getenv("LECTERN_SLOW_SETUP_PROOF") == "1" {
		until := time.Now().Add(125 * time.Second)
		for time.Now().Before(until) {
			current, _ = h.App.DB.Session(id)
			if current.SetupState != "creating" || current.EndedAt != nil {
				t.Fatalf("setup abandoned before release: %+v", current)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	h.decode("DELETE", endpoint, nil, 409, nil)
	h.decode("POST", endpoint+"/terminal", obj{}, 409, nil)
	h.decode("DELETE", endpoint+"/worktree", nil, 409, nil)
	os.WriteFile(release, []byte("release"), 0600)
	wait := func(id int64) *store.Session {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			session, err := h.App.DB.Session(id)
			if err != nil {
				t.Fatal(err)
			}
			if session.SetupState != "creating" {
				return session
			}
			if time.Now().After(deadline) {
				t.Fatal("background launch did not finish")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	current = wait(id)
	if current.SetupState != "ready" || current.EndedAt != nil || current.Workdir == repo {
		t.Fatalf("background launch failed: %+v", current)
	}
	h.decode("POST", endpoint+"/setup/cancel", obj{}, 409, nil)
	h.decode("DELETE", endpoint, nil, 200, nil)
	os.WriteFile(hook, []byte("#!/bin/sh\nprintf valuable > setup-artifact\necho background-setup-failure >&2\nexit 1\n"), 0700)
	h.decode("POST", "/api/sessions", input, 202, &row)
	failed := wait(row.id())
	if failed.SetupState != "failed" || failed.EndedAt == nil || !strings.Contains(failed.SetupError, "background-setup-failure") {
		t.Fatalf("failure was hidden: %+v", failed)
	}
	if err := json.Unmarshal([]byte(failed.LaunchConfigJSON), &captured); err != nil {
		t.Fatalf("failed setup lost launch configuration: %v", err)
	}
	if captured.Spec.Command != "sleep 601" {
		t.Fatal("failed setup lost its own requested command")
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "background-fixture", "command": "sleep 602"}}, 200, nil)
	retained, err := h.App.Sessions.SessionLaunchConfiguration(failed)
	if err != nil || retained.Spec.Command != "sleep 601" {
		t.Fatal("failed setup followed later agent settings")
	}
	if failed.WorktreeJSON == "" {
		t.Fatal("failed allocation was lost")
	}
	visible := h.getList("/api/sessions?include_setup_failures=true")
	if len(visible) != 1 || visible[0].id() != failed.ID {
		t.Fatalf("setup failure was not retained in the current view: %v", visible)
	}
	if live := h.getList("/api/sessions"); len(live) != 0 {
		t.Fatal("default API live-only filtering changed")
	}
	// A new held checkout can be cancelled through the public API without
	// releasing its hook, starting an agent, or removing allocated files.
	os.Remove(release)
	os.Remove(started)
	os.WriteFile(hook, []byte("#!/bin/sh\nprintf keep > cancelled-artifact\ntouch "+shellq.Quote(started)+"\nwhile [ ! -f "+shellq.Quote(release)+" ]; do sleep .05; done\n"), 0700)
	h.decode("POST", "/api/sessions", input, 202, &row)
	deadline = time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel checkout did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancelEndpoint := fmt.Sprintf("/api/sessions/%d/setup/cancel", row.id())
	var requested obj
	h.decode("POST", cancelEndpoint, obj{}, 202, &requested)
	if requested["setup_cancel_requested"] != true {
		t.Fatal("cancel request was not retained")
	}
	cancelled := wait(row.id())
	if cancelled.SetupState != "failed" || !cancelled.SetupCancelRequested || !strings.Contains(cancelled.SetupError, "cancelled") {
		t.Fatalf("cancel outcome missing: %s", cancelled.SetupError)
	}
	if err := exec.Command("tmux", "has-session", "-t", "="+cancelled.TmuxSession).Run(); err == nil {
		t.Fatal("cancelled setup started an agent")
	}
	var allocation worktree.Interactive
	if err := json.Unmarshal([]byte(cancelled.WorktreeJSON), &allocation); err != nil {
		t.Fatal(err)
	}
	dest := allocation.Path
	if len(allocation.Repositories) > 0 {
		dest = allocation.Repositories[0].Worktree.Path
	}
	if contents, err := os.ReadFile(filepath.Join(dest, "cancelled-artifact")); err != nil || string(contents) != "keep" {
		t.Fatal("cancellation removed files")
	}
	h.decode("POST", cancelEndpoint, obj{}, 202, nil)

}
