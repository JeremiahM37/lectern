package worktree

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

	"github.com/JeremiahM37/lectern/internal/executor"
)

func TestWorkspaceSetupCommandsRunInEachCheckout(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("grouped=%t/fail=%t", grouped, fail), func(t *testing.T) {
				repo, git := recoveryRepo(t)
				hook := "printf '%s' \"$FIXTURE\" > prepared; pwd; python3 -c 'print(\"x\"*10000)'; echo finished"
				if fail {
					hook += "; exit 23"
				}
				plan := PlanInteractive(repo, 81, InteractiveOptions{})
				plan.SetupCommand = hook
				plan.SetupEnv = map[string]string{"FIXTURE": "project environment"}
				if grouped {
					var err error
					plan, err = PlanMultiWorkspace([]RepositorySource{{Name: "repo", Repo: repo, SetupCommand: hook, SetupEnv: plan.SetupEnv}}, 81, InteractiveOptions{})
					if err != nil {
						t.Fatal(err)
					}
				}
				err := RunInteractive(context.Background(), executor.NewLocal(), "create", plan)
				child := plan
				if grouped {
					child = plan.Repositories[0].Worktree
				}
				if fail {
					if err == nil || !strings.Contains(err.Error(), "status 23") || child.SetupState != "failed" {
						t.Fatalf("failure not retained: %v %+v", err, child)
					}
				} else if err != nil || child.SetupState != "complete" {
					t.Fatalf("setup failed: %v %+v", err, child)
				}
				if len(child.SetupOutput) > 4096 || !strings.Contains(child.SetupOutput, "finished") {
					t.Fatal("output tail not bounded/preserved")
				}
				body, _ := os.ReadFile(filepath.Join(child.Path, "prepared"))
				if string(body) != "project environment" {
					t.Fatal("wrong hook cwd or environment")
				}
				if git(repo, "status", "--porcelain") != "" {
					t.Fatal("setup modified source repository")
				}
				plan.RedactOwnership()
				if child.SetupEnv != nil {
					t.Fatal("workspace response leaks setup environment")
				}
			})
		}
	}
}

func TestCancelWorkspaceSetupCommandRetainsFiles(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprint(grouped), func(t *testing.T) {
			repo, _ := recoveryRepo(t)
			root := t.TempDir()
			started := filepath.Join(root, "started")
			release := filepath.Join(root, "release")
			escaped := filepath.Join(root, "escaped")
			hook := "printf keep > prepared; touch " + executor.ShellQuote(started) + "; while [ ! -f " + executor.ShellQuote(release) + " ]; do sleep .05; done; touch " + executor.ShellQuote(escaped)
			plan := PlanInteractive(repo, 82, InteractiveOptions{})
			plan.SetupCommand = hook
			if grouped {
				var err error
				plan, err = PlanMultiWorkspace([]RepositorySource{{Name: "repo", Repo: repo, SetupCommand: hook}}, 82, InteractiveOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			cancelPlan := *plan
			done := make(chan error, 1)
			go func() { done <- RunInteractive(context.Background(), executor.NewLocal(), "create", plan) }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("setup not started")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := RunInteractive(context.Background(), executor.NewLocal(), "cancel", &cancelPlan); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "cancelled") {
					t.Fatalf("cancel hidden: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("setup did not stop")
			}
			os.WriteFile(release, []byte("release"), 0600)
			time.Sleep(150 * time.Millisecond)
			if _, err := os.Stat(escaped); !os.IsNotExist(err) {
				t.Fatal("cancelled command continued")
			}
			child := plan
			if grouped {
				child = plan.Repositories[0].Worktree
			}
			body, _ := os.ReadFile(filepath.Join(child.Path, "prepared"))
			if string(body) != "keep" {
				t.Fatal("cancel discarded files")
			}
		})
	}
}

func TestSetupCommandOutlivesSupervisorWithoutRerunning(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("grouped=%t/cancelled=%t", grouped, cancelled), func(t *testing.T) {
				repo, _ := recoveryRepo(t)
				root := t.TempDir()
				started := filepath.Join(root, "started")
				release := filepath.Join(root, "release")
				hook := "echo run >> " + executor.ShellQuote(started) + "; while [ ! -f " + executor.ShellQuote(release) + " ]; do sleep .05; done; echo completed"
				t.Cleanup(func() { os.WriteFile(release, []byte("release"), 0600) })
				plan := PlanInteractive(repo, 83, InteractiveOptions{})
				plan.SetupCommand = hook
				if grouped {
					var err error
					plan, err = PlanMultiWorkspace([]RepositorySource{{Name: "repo", Repo: repo, SetupCommand: hook}}, 83, InteractiveOptions{})
					if err != nil {
						t.Fatal(err)
					}
				}
				raw, _ := json.Marshal(plan)
				args := []string{"-c", setupControlScript + "\n" + interactiveScript, "create", string(raw)}
				if grouped {
					args = []string{"-c", setupControlScript + "\n" + multiWorkerScript, "create", string(raw), multiPreflightScript, setupControlScript + "\n" + interactiveScript}
				}
				process := exec.Command("python3", args...)
				if err := process.Start(); err != nil {
					t.Fatal(err)
				}
				defer process.Process.Kill()
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(started); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("setup did not start")
					}
					time.Sleep(20 * time.Millisecond)
				}
				process.Process.Kill()
				process.Wait()
				ex := executor.NewLocal()
				if err := RunInteractive(context.Background(), ex, "recover", plan); err == nil {
					t.Fatal("recovery raced active setup")
				}
				if cancelled {
					if err := RunInteractive(context.Background(), ex, "cancel", plan); err != nil {
						t.Fatal(err)
					}
				} else {
					os.WriteFile(release, []byte("release"), 0600)
				}
				deadline = time.Now().Add(5 * time.Second)
				for {
					err := RunInteractive(context.Background(), ex, "recover", plan)
					if err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("orphan recovery: %v", err)
					}
					time.Sleep(50 * time.Millisecond)
				}
				child := plan
				if grouped {
					child = plan.Repositories[0].Worktree
				}
				// Group progress is published by its supervisor; if it died before receiving
				// the worker result, completed hook execution is intentionally not assumed.
				want := "complete"
				if cancelled || grouped {
					want = "interrupted"
				}
				if child.SetupState != want {
					t.Fatalf("hook status %q, want %q", child.SetupState, want)
				}
				body, _ := os.ReadFile(started)
				if string(body) != "run\n" {
					t.Fatal("recovery reran setup")
				}
			})
		}
	}
}

func TestWorkspaceSetupTimeoutStopsFurtherWrites(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprint(grouped), func(t *testing.T) {
			repo, _ := recoveryRepo(t)
			root := t.TempDir()
			escaped := filepath.Join(root, "escaped")
			hook := "echo keep > prepared; sleep 3; touch " + executor.ShellQuote(escaped)
			plan := PlanInteractive(repo, 84, InteractiveOptions{})
			plan.SetupCommand = hook
			if grouped {
				var err error
				plan, err = PlanMultiWorkspace([]RepositorySource{{Name: "repo", Repo: repo, SetupCommand: hook}}, 84, InteractiveOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Internal worker budget is 1s: exercise the owning monitor's deadline,
			// not the outer executor killing its caller while work remains alive.
			raw, _ := json.Marshal(plan)
			script := setupControlScript + "\n" + interactiveScript
			args := []string{"-c", script, "create", string(raw), "-", "1"}
			if grouped {
				args = []string{"-c", setupControlScript + "\n" + multiWorkerScript, "create", string(raw), multiPreflightScript, script, "2"}
			}
			started := time.Now()
			output, err := exec.Command("python3", args...).CombinedOutput()
			if err == nil {
				t.Fatalf("timeout succeeded: %s", output)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("setup exceeded worker budget")
			}
			time.Sleep(3200 * time.Millisecond)
			if _, err := os.Stat(escaped); !os.IsNotExist(err) {
				t.Fatal("timed-out setup kept running")
			}
			dest := plan.Path
			if grouped {
				dest = plan.Repositories[0].Worktree.Path
			}
			if body, _ := os.ReadFile(filepath.Join(dest, "prepared")); string(body) != "keep\n" {
				t.Fatal("timeout discarded setup artifact")
			}
		})
	}
}
