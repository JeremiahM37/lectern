package worktree

import (
	"context"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCancelSetupStopsOwnedCheckoutAndRetainsFiles(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		name := "single"
		if grouped {
			name = "grouped"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			os.Mkdir(repo, 0700)
			git := func(args ...string) {
				t.Helper()
				if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
			}
			git("init", "-q")
			os.WriteFile(filepath.Join(repo, "base"), []byte("preserve source"), 0600)
			git("add", ".")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "base")
			started := filepath.Join(root, "started")
			release := filepath.Join(root, "release")
			os.WriteFile(filepath.Join(repo, ".git/hooks/post-checkout"), []byte("#!/bin/sh\nprintf keep > cancellation-artifact\ntouch "+executor.ShellQuote(started)+"\nwhile [ ! -f "+executor.ShellQuote(release)+" ]; do sleep .05; done\ntouch "+executor.ShellQuote(filepath.Join(root, "escaped"))+"\n"), 0700)
			t.Cleanup(func() { os.WriteFile(release, []byte("release"), 0600) })
			plan := PlanInteractive(repo, 42, InteractiveOptions{})
			if grouped {
				var err error
				plan, err = PlanMultiWorkspace([]RepositorySource{{Name: "repo", Repo: repo}}, 42, InteractiveOptions{})
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
					t.Fatal("checkout hook never started")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := RunInteractive(context.Background(), executor.NewLocal(), "cancel", &cancelPlan); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "cancelled") {
					t.Fatalf("cancellation hidden: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("checkout did not stop")
			}
			dest := plan.Path
			if grouped {
				dest = plan.Repositories[0].Worktree.Path
			}
			if content, err := os.ReadFile(filepath.Join(dest, "cancellation-artifact")); err != nil || string(content) != "keep" {
				t.Fatal("cancel removed allocated files")
			}
			os.WriteFile(release, []byte("release"), 0600)
			time.Sleep(150 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
				t.Fatal("cancel left the checkout hook running")
			}
			if plan.State != "failed" {
				t.Fatalf("cancelled allocation not retained as failed: %s", plan.State)
			}
		})
	}
}

func TestSetupCancellationRecordRejectsReplacementAndSymlinks(t *testing.T) {
	script := setupControlScript + `
import tempfile
with tempfile.TemporaryDirectory() as folder:
 p={'token':'a'*32,'path':folder+'/allocation','repo':folder+'/repo'}
 control=SetupControl(p)
 assert not control.access()
 control.access(cancel=True)
 assert control.access()
 replacement=pathlib.Path(folder)/'replacement'
 replacement.write_text(json.dumps(dict(control.identity,cancelled=False)))
 os.replace(replacement,control.path)
 try:control.check()
 except ValueError:pass
 else:raise AssertionError('replacement silently cleared cancellation')
 control.path.unlink()
 victim=pathlib.Path(folder)/'keep'
 victim.write_text('untouched')
 control.path.symlink_to(victim)
 try:SetupControl(p)
 except (OSError,ValueError):pass
 else:raise AssertionError('followed cancellation symlink')
 assert victim.read_text()=='untouched'
 control.path.unlink()
 control.path.write_text(json.dumps({'token':'b'*32,'path':p['path'],'repo':p['repo']}))
 try:SetupControl(p)
 except ValueError:pass
 else:raise AssertionError('accepted foreign cancellation identity')
`
	if out, err := exec.Command("python3", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("cancellation metadata: %v %s", err, out)
	}
}
