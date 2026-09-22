package api_test

import (
	"encoding/json"
	"fmt"
	"github.com/JeremiahM37/lectern/internal/worktree"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestProjectSetupRunsBeforeAgentAndStopsFailedLaunch(t *testing.T) {
	requireRealTools(t)
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Mock = false })
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			os.Mkdir(repo, 0700)
			for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "base"}} {
				if b, e := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); e != nil {
					t.Fatalf("git: %v %s", e, b)
				}
			}
			target, _ := h.App.DB.InsertTarget(&store.Target{Name: "setup target", Kind: "local"})
			// Real tmux launches a synthetic agent only; its marker proves ordering.
			agent := filepath.Join(root, "agent")
			os.WriteFile(agent, []byte("#!/bin/sh\ntest -f prepared || exit 29\nprintf started > agent-started\nsleep 600\n"), 0700)
			h.decode("PUT", "/api/agents", []obj{{"name": "setup-fixture", "command": agent}}, 200, nil)
			command := "printf '%s' \"$SETUP_VALUE\" > prepared; echo fixture-setup"
			if fail {
				command += "; exit 17"
			}
			var project obj
			h.decode("POST", "/api/projects", obj{"name": "setup project", "target_id": target.ID, "repo_path": repo, "setup_cmd": command, "env": obj{"SETUP_VALUE": "captured-env"}}, 201, &project)
			if project["setup_cmd"] != command {
				t.Fatal("setup not saved")
			}
			status, body := h.request("POST", "/api/sessions", obj{"project_id": project.id(), "agent": "setup-fixture", "worktree": obj{}, "yolo": false}, nil)
			if fail {
				if status < 400 || !strings.Contains(string(body), "status 17") {
					t.Fatalf("failed hook not surfaced: %d %s", status, body)
				}
			} else if status != 201 {
				t.Fatalf("launch: %d %s", status, body)
			}
			rows, err := h.App.DB.Sessions(true)
			if err != nil || len(rows) != 1 {
				t.Fatalf("retained session: %v %v", rows, err)
			}
			var plan worktree.Interactive
			if err := json.Unmarshal([]byte(rows[0].WorktreeJSON), &plan); err != nil {
				t.Fatal(err)
			}
			prepared, e := os.ReadFile(filepath.Join(plan.Path, "prepared"))
			if e != nil || string(prepared) != "captured-env" {
				t.Fatalf("setup artifact: %q %v", prepared, e)
			}
			marker := filepath.Join(plan.Path, "agent-started")
			if fail {
				if _, e := os.Stat(marker); !os.IsNotExist(e) {
					t.Fatal("agent started after setup failed")
				}
				if plan.SetupState != "failed" {
					t.Fatal("failure status missing")
				}
			} else {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, e := os.Stat(marker); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("agent did not start after setup")
					}
					time.Sleep(20 * time.Millisecond)
				}
				h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", rows[0].ID), nil, 200, nil)
				if plan.SetupState != "complete" {
					t.Fatal("completion status missing")
				}
			}
			if _, e := os.Stat(filepath.Join(repo, "prepared")); !os.IsNotExist(e) {
				t.Fatal("setup modified source")
			}
			endpoint := fmt.Sprintf("/api/projects/%d", project.id())
			h.decode("PATCH", endpoint, obj{"setup_cmd": ""}, 200, nil)
			changed, _ := h.App.DB.Project(project.id())
			if changed.SetupCmd != "" {
				t.Fatal("cannot clear setup command")
			}
			h.decode("PATCH", endpoint, obj{"setup_cmd": strings.Repeat("x", 16385)}, 422, nil)
		})
	}
}

func TestGroupedProjectSetupUsesEachRepositoryEnvironment(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "group setup", Kind: "local"})
	h.decode("PUT", "/api/agents", []obj{{"name": "group-setup-fixture", "command": "sleep 600"}}, 200, nil)
	var projects []obj
	for _, name := range []string{"primary", "extra"} {
		repo := filepath.Join(t.TempDir(), name)
		os.Mkdir(repo, 0700)
		for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "base"}} {
			if b, e := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); e != nil {
				t.Fatalf("git: %v %s", e, b)
			}
		}
		var project obj
		h.decode("POST", "/api/projects", obj{"name": name, "target_id": target.ID, "repo_path": repo, "setup_cmd": "printf '%s' \"$REPO_VALUE\" > prepared", "env": obj{"REPO_VALUE": name}}, 201, &project)
		projects = append(projects, project)
	}
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": projects[0].id(), "agent": "group-setup-fixture", "worktree": obj{"extra_repositories": []obj{{"project_id": projects[1].id()}}}}, 201, &row)
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", row.id()), nil, 200, nil)
	saved, _ := h.App.DB.Session(row.id())
	var plan worktree.Interactive
	if err := json.Unmarshal([]byte(saved.WorktreeJSON), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 2 {
		t.Fatal("group incomplete")
	}
	for _, repo := range plan.Repositories {
		body, err := os.ReadFile(filepath.Join(repo.Worktree.Path, "prepared"))
		if err != nil || string(body) != repo.Name || repo.Worktree.SetupState != "complete" {
			t.Fatalf("wrong repository setup: %s %q %v", repo.Name, body, err)
		}
	}
	raw, _ := json.Marshal(row["workspace"])
	if strings.Contains(string(raw), "setup_env") {
		t.Fatal("public workspace exposes environment snapshot")
	}
	// Existing-directory sessions must not execute the creation lifecycle again.
	source := projects[0]["repo_path"].(string)
	var existing obj
	h.decode("POST", "/api/sessions", obj{"project_id": projects[0].id(), "agent": "group-setup-fixture"}, 201, &existing)
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", existing.id()), nil, 200, nil)
	if _, err := os.Stat(filepath.Join(source, "prepared")); !os.IsNotExist(err) {
		t.Fatal("ordinary launch ran checkout setup")
	}
}
