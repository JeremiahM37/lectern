package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestAgentCredHint(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, detail := agentCredHint(present); !ok || detail != present {
		t.Errorf("existing creds file should report ok=true detail=path: ok=%v detail=%q", ok, detail)
	}
	missing := filepath.Join(dir, "nope.json")
	if ok, detail := agentCredHint(missing); ok || !strings.Contains(detail, missing) {
		t.Errorf("missing creds file should report ok=false with the path in detail: ok=%v detail=%q", ok, detail)
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
