package api_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/scheduler"
)

// withCreds gives a harness fabricated control-plane OAuth credentials. The
// host's real ~/.claude/.credentials.json must never be a test dependency — a
// suite that relied on it passed locally and failed on every CI runner.
func withCreds(t *testing.T) func(*config.Config) {
	return func(c *config.Config) {
		c.ClaudeCredsPath = writeFile(t, filepath.Join(t.TempDir(), "creds.json"),
			`{"access_token": "test", "refresh_token": "test"}`)
	}
}

func (h *harness) sandboxProject(name, repo string) obj {
	h.t.Helper()
	tgt := h.post("/api/targets",
		obj{"name": "sb-" + name, "kind": "sandbox", "host": "110", "sandbox": true}, 201)
	return h.post("/api/projects",
		obj{"name": name, "target_id": tgt.id(), "repo_path": repo}, 201)
}

func TestSandboxFullLifecycle(t *testing.T) {
	h := newHarness(t, withCreds(t))
	p := h.sandboxProject("sbdemo", "https://github.com/user/demo.git")
	task := h.task(p.id(), "sb run", "do it", obj{"permission_mode": "bypassPermissions"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")

	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	if att.SandboxVMID != "9001" {
		t.Fatalf("vmid: %q", att.SandboxVMID)
	}
	for _, want := range []string{
		"sudo pct clone 110 9001", "sudo pct start 9001",
		// auth is provisioned into the container before launch
		".claude/.credentials.json",
		"git clone --branch main https://github.com/user/demo.git /root/work/sbdemo",
		// destroyed right after finalise — the diff and events are already saved
		"sudo pct destroy 9001",
	} {
		if !h.cmdLogHas(want) {
			t.Errorf("missing from the command log: %q", want)
		}
	}
	if h.get(fmt.Sprintf("/api/tasks/%d", task.id())).sub("attempt").str("worktree_path") != "" {
		t.Error("a destroyed sandbox must not leave a worktree path behind")
	}
	// the diff is still reviewable from the control plane's own copy
	d := h.get(fmt.Sprintf("/api/tasks/%d/diff", task.id()))
	if len(d.list("files")) == 0 {
		t.Error("the captured diff did not survive the container")
	}
}

func TestSandboxCancelDestroysContainer(t *testing.T) {
	h := newHarness(t, withCreds(t))
	p := h.sandboxProject("sbcancel", "https://github.com/user/demo.git")
	task := h.task(p.id(), "sb cancel", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	h.post(fmt.Sprintf("/api/tasks/%d/cancel", task.id()), nil, 200)
	h.waitUntil("the container to be destroyed", func() bool {
		return h.cmdLogHas("sudo pct destroy 9001")
	})
	if h.taskStatus(task.id()) != "cancelled" {
		t.Fatalf("status: %s", h.taskStatus(task.id()))
	}
}

func TestSandboxFollowupGetsFreshContainerNoResume(t *testing.T) {
	h := newHarness(t, withCreds(t))
	p := h.sandboxProject("sbfollow", "https://github.com/user/demo.git")
	task := h.task(p.id(), "sb follow", "first pass", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")
	h.post(fmt.Sprintf("/api/tasks/%d/followup", task.id()),
		obj{"feedback": "also add tests"}, 200)
	h.waitStatus(task.id(), "review")

	a2, err := h.App.DB.OneAttemptWhere("task_id=? AND n=2", task.id())
	if err != nil {
		t.Fatal(err)
	}
	if a2.ResumeSession != "" {
		t.Error("the old container is gone — there is no session to resume")
	}
	if !strings.Contains(a2.Prompt, "REQUESTED CHANGES") ||
		!strings.Contains(a2.Prompt, "first pass") {
		t.Errorf("the prompt must carry the context the container cannot: %q", a2.Prompt)
	}
}

func TestSandboxSkipsReviewerGate(t *testing.T) {
	h := newHarness(t, withCreds(t))
	tgt := h.post("/api/targets",
		obj{"name": "sb-gated", "kind": "sandbox", "host": "110"}, 201)
	p := h.post("/api/projects", obj{"name": "sbgated", "target_id": tgt.id(),
		"repo_path": "https://github.com/u/r.git", "review_gate": true}, 201)
	task := h.run(p.id(), "gated sb", "x [mock:approve-verdict]", nil)
	for _, x := range h.getList("/api/tasks") {
		if x.str("created_by") == "reviewer-gate" && int64(x.num("parent_task_id")) == task.id() {
			t.Fatal("a reviewer cannot run in a worktree that no longer exists")
		}
	}
}

// A failure AFTER the container exists must still destroy it — otherwise the
// generic handler marks the attempt failed and leaks the LXC.
func TestSandboxDestroyedOnUnexpectedLaunchError(t *testing.T) {
	h := newHarness(t, withCreds(t))
	scheduler.SandboxInnerHook = func() error { return errors.New("unexpected explosion mid-launch") }
	t.Cleanup(func() { scheduler.SandboxInnerHook = nil })

	p := h.sandboxProject("sbleak", "https://github.com/user/demo.git")
	task := h.task(p.id(), "leaky", "x", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "failed")
	if !h.cmdLogHas("sudo pct destroy 9001") {
		t.Fatalf("the container leaked: %v", h.mock().CmdLog())
	}
}

func TestTemplatePathRepoSkipsClone(t *testing.T) {
	h := newHarness(t, withCreds(t))
	p := h.sandboxProject("sbbaked", "/root/lec-demo")
	task := h.run(p.id(), "baked repo", "y", nil)
	if h.cmdLogHas("git clone") {
		t.Error("a repo baked into the template must not be re-cloned")
	}
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(att.Branch, "lec/task") {
		t.Errorf("branch: %q", att.Branch)
	}
}
