package main

import (
	"os"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/onboard"
)

func TestRenderDoctorReportAllOK(t *testing.T) {
	report, ok := renderDoctorReport([]doctorCheck{
		check("tmux", true, "/usr/bin/tmux", ""),
		check("git", true, "/usr/bin/git", ""),
		skip("TLS", "not applicable"),
	})
	if !ok {
		t.Errorf("all evaluated checks passed; ok should be true")
	}
	if !strings.Contains(report, "[OK] tmux — /usr/bin/tmux") {
		t.Errorf("report missing tmux OK line:\n%s", report)
	}
	if !strings.Contains(report, "[--] TLS — not applicable") {
		t.Errorf("report missing skipped TLS line:\n%s", report)
	}
	if strings.Contains(report, "fix:") {
		t.Errorf("a passing/skipped check must not print a fix line:\n%s", report)
	}
}

func TestRenderDoctorReportFailurePrintsFix(t *testing.T) {
	report, ok := renderDoctorReport([]doctorCheck{
		check("tmux", false, "not found on PATH", "install tmux"),
	})
	if ok {
		t.Errorf("a failing check must make ok false")
	}
	if !strings.Contains(report, "[FAIL] tmux — not found on PATH") {
		t.Errorf("report missing FAIL line:\n%s", report)
	}
	if !strings.Contains(report, "fix: install tmux") {
		t.Errorf("report missing fix line:\n%s", report)
	}
}

func TestRenderDoctorReportSkipNeverCountsAsFailure(t *testing.T) {
	_, ok := renderDoctorReport([]doctorCheck{
		skip("push keys", "no running instance found"),
	})
	if !ok {
		t.Errorf("a skipped check alone must not fail the overall report")
	}
}

func TestAgentInstallHintNamesKnownAgents(t *testing.T) {
	for _, name := range []string{"claude", "codex", "gemini"} {
		hint := agentInstallHint(name)
		if !strings.Contains(hint, "PATH") {
			t.Errorf("%s hint should mention PATH: %q", name, hint)
		}
	}
	if hint := agentInstallHint("some-custom-cli"); !strings.Contains(hint, "some-custom-cli") {
		t.Errorf("unknown agent hint should still name it: %q", hint)
	}
}

func TestDoctorPassesAWorkingSetupWithOptionalThingsMissing(t *testing.T) {
	cfg := &config.Config{ClaudeCredsPath: "/c/creds.json", CodexCredsPath: "/x/auth.json"}
	agents := []onboard.AgentCheck{
		{Name: "claude", Found: true, Path: "/bin/claude", Builtin: true},
		{Name: "codex", Builtin: true}, {Name: "gemini", Builtin: true}, {Name: "aider"},
	}
	missing := func(string) error { return os.ErrNotExist }
	checks := append(agentChecks(cfg, agents, missing), warn("phone alerts (push keys)", false, "404", "restart it"))
	report, ok := renderDoctorReport(checks)
	if !ok {
		t.Fatalf("one installed agent with optional extras missing must pass:\n%s", report)
	}
	for _, want := range []string{"[--] agent: codex — not installed (optional)", "[WARN] claude sign-in", "fix: restart it", "Lectern can run agents here."} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "codex sign-in") {
		t.Errorf("sign-in checked for an agent that is not installed:\n%s", report)
	}
	// No agent at all is the one thing that fails.
	for i := range agents {
		agents[i].Found = false
	}
	report, ok = renderDoctorReport(agentChecks(cfg, agents, missing))
	if ok || !strings.Contains(report, "[FAIL] an agent to run") {
		t.Fatalf("no agents must fail:\n%s", report)
	}
}

func TestEnvOrAuto(t *testing.T) {
	if got := envOrAuto(""); got != "auto" {
		t.Errorf("empty mode should report auto, got %q", got)
	}
	if got := envOrAuto("token"); got != "token" {
		t.Errorf("explicit mode should pass through, got %q", got)
	}
}
