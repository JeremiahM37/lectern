package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// A target with a lectern binary gets the Go agent-side helpers everywhere the
// Python scripts were staged, installed or named — and the approval flow
// still works end to end through the Go hook's command line.
func TestTargetWithLecternUsesGoAgentHelpers(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	const bin = "/opt/lectern/lectern"
	var projectTarget float64
	for _, p := range h.getList("/api/projects") {
		if p.id() == pid {
			projectTarget = p.num("target_id")
		}
	}
	var mock *executor.Mock
	for _, tg := range h.getList("/api/targets") {
		target, err := h.App.DB.Target(tg.id())
		if err != nil {
			t.Fatal(err)
		}
		ex, err := h.App.Reg.For(target)
		if err != nil {
			t.Fatal(err)
		}
		executor.SetTargetEnv(ex, executor.TargetEnv{Lectern: bin})
		if float64(tg.id()) == projectTarget {
			mock = ex.(*executor.Mock)
		}
	}
	staged := func(suffix string) string {
		for path, data := range mock.Files() {
			if strings.HasSuffix(path, suffix) {
				return string(data)
			}
		}
		t.Fatalf("nothing staged at *%s", suffix)
		return ""
	}
	logHas := func(want string) bool {
		for _, c := range mock.CmdLog() {
			if strings.Contains(c, want) {
				return true
			}
		}
		return false
	}

	task := gated(h, pid, "Gated with lectern")
	appr := h.waitApproval(task.id())
	h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()), obj{"decision": "approved"}, 200)
	h.waitStatus(task.id(), "review")

	gate := staged("/.lectern/settings.json")
	if !strings.Contains(gate, "LECTERN_TOKEN=") || !strings.Contains(gate, bin+" helper approval-hook") ||
		strings.Contains(gate, "hook.py") {
		t.Errorf("the gate does not run the Go hook: %s", gate)
	}
	if prompt := staged("/.lectern/prompt.md"); !strings.Contains(prompt, bin+` helper lec add-task "short title"`) ||
		strings.Contains(prompt, "lec.py") {
		t.Errorf("the prompt does not name the Go task kit:\n%s", prompt)
	}
	if !strings.Contains(staged("/.lectern/env"), "ADK_TOKEN=") {
		t.Error("the Go kit reads the same env file, which must still be staged")
	}
	for path := range mock.Files() {
		if strings.HasSuffix(path, "/lec.py") || strings.HasSuffix(path, "/hook.py") {
			t.Errorf("%s staged although the target runs the Go helpers", path)
		}
	}

	h.session(obj{"project_id": pid, "name": "claude-go", "agent": "claude"})
	h.session(obj{"project_id": pid, "name": "codex-go", "agent": "codex", "permission_mode": "ask"})
	for _, want := range []string{
		bin + " helper claude-settings-install",
		bin + " helper claude-trust",
		bin + " helper codex-hooks-install 1 \"$HOME\"/.lectern/hooks/lectern-codex-hook.py " + bin,
		bin + " helper codex-trust",
		`notify=["` + bin + `","helper","codex-notify"]`,
	} {
		if !logHas(want) {
			t.Errorf("no command ran %s", want)
		}
	}
	for _, python := range []string{"ADKHOOKINSTALL", "ADKCODEXNOTIFY", "ADKCODEXHOOK", "ADKTRUST", "notify=[\"python3"} {
		if logHas(python) {
			t.Errorf("the Python %s still ran on a target with lectern", python)
		}
	}
}
