package worktree

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryRepo(t *testing.T) (string, func(string, ...string) string) {
	t.Helper()
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "base"), []byte("original"), 0600)
	git(repo, "add", ".")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "base")
	return repo, git
}

func TestPartialRecoveryValidatesWithoutDiscardingChanges(t *testing.T) {
	for _, mode := range []string{"clean", "dirty", "changed-head", "foreign-owner", "owner-symlink"} {
		t.Run(mode, func(t *testing.T) {
			repo, git := recoveryRepo(t)
			plan := PlanInteractive(repo, 72, InteractiveOptions{})
			ex := executor.NewLocal()
			if err := RunInteractive(context.Background(), ex, "create", plan); err != nil {
				t.Fatal(err)
			}
			owner := filepath.Join(git(plan.Path, "rev-parse", "--absolute-git-dir"), "lectern-owner")
			os.Remove(owner)
			switch mode {
			case "dirty":
				os.WriteFile(filepath.Join(plan.Path, "keep"), []byte("user work"), 0600)
			case "changed-head":
				os.WriteFile(filepath.Join(plan.Path, "base"), []byte("new commit"), 0600)
				git(plan.Path, "add", ".")
				git(plan.Path, "-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "user changes")
			case "foreign-owner":
				os.WriteFile(owner, []byte("another owner"), 0600)
			case "owner-symlink":
				os.Symlink(filepath.Join(plan.Path, "base"), owner)
			}
			err := RunInteractive(context.Background(), ex, "recover", plan)
			if mode == "clean" || mode == "dirty" {
				if err != nil {
					t.Fatal(err)
				}
				value, e := os.ReadFile(owner)
				if e != nil || string(value) != plan.Token {
					t.Fatal("matching allocation was not recovered")
				}
				err = RunInteractive(context.Background(), ex, "remove", plan)
				if mode == "clean" {
					if err != nil {
						t.Fatal(err)
					}
					git(repo, "rev-parse", plan.Branch)
				} else {
					if err == nil {
						t.Fatal("dirty recovered allocation was removed")
					}
					value, e := os.ReadFile(filepath.Join(plan.Path, "keep"))
					if e != nil || string(value) != "user work" {
						t.Fatal("user work changed")
					}
				}
			} else {
				if err == nil {
					t.Fatal("mismatched allocation was claimed")
				}
				if mode == "changed-head" {
					if _, e := os.Stat(owner); !os.IsNotExist(e) {
						t.Fatal("changed revision acquired ownership")
					}
				}
				if mode == "owner-symlink" {
					value, _ := os.ReadFile(filepath.Join(plan.Path, "base"))
					if string(value) != "original" {
						t.Fatal("symlink target changed")
					}
				}
			}
		})
	}
}

func TestSingleCheckoutOrphanCanBeCancelledAndRecovered(t *testing.T) {
	repo, git := recoveryRepo(t)
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	os.WriteFile(filepath.Join(repo, ".git/hooks/post-checkout"), []byte("#!/bin/sh\ntouch "+executor.ShellQuote(started)+"\nwhile [ ! -f "+executor.ShellQuote(release)+" ]; do sleep .05; done\n"), 0700)
	t.Cleanup(func() { os.WriteFile(release, []byte("release"), 0600) })
	plan := PlanInteractive(repo, 73, InteractiveOptions{})
	raw, _ := json.Marshal(plan)
	process := exec.Command("python3", "-c", setupControlScript+"\n"+interactiveScript, "create", string(raw))
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer process.Process.Kill()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e := os.Stat(started); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	process.Process.Kill()
	process.Wait()
	ex := executor.NewLocal()
	if err := RunInteractive(context.Background(), ex, "recover", plan); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("recovery raced a live orphan: %v", err)
	}
	if err := RunInteractive(context.Background(), ex, "cancel", plan); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		err := RunInteractive(context.Background(), ex, "recover", plan)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphan did not become recoverable: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if plan.Commit == "" {
		t.Fatal("target-side recorded revision was lost")
	}
	if err := RunInteractive(context.Background(), ex, "remove", plan); err != nil {
		t.Fatal(err)
	}
	git(repo, "rev-parse", plan.Branch)
}
