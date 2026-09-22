package delegation

import (
	"strings"
	"testing"
)

func TestLoadDefaultsAndRoundTrip(t *testing.T) {
	got := Load(func(string) string { return "" })
	if got.Enabled || got.PermissionMode != "acceptEdits" || got.CorrectionCycles != 1 {
		t.Fatalf("defaults: %+v", got)
	}
	stored := Settings{Enabled: true, WorkerAgent: "flash-builder", WorkerModel: "deepseek-flash"}.Encode()
	got = Load(func(string) string { return stored })
	if !got.Enabled || got.WorkerAgent != "flash-builder" || got.PermissionMode != "acceptEdits" || got.CorrectionCycles != 1 {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	// A corrupt value never disables the defaults.
	if got := Load(func(string) string { return "{not json" }); got.PermissionMode != "acceptEdits" {
		t.Fatalf("corrupt value: %+v", got)
	}
}

func TestValidateRefusesEnablingWithoutAWorker(t *testing.T) {
	s := Defaults()
	s.Enabled = true
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "worker") {
		t.Fatalf("enabled without a worker accepted: %v", err)
	}
	s.WorkerAgent = "x"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.PermissionMode = "yolo"
	if err := s.Validate(); err == nil {
		t.Fatal("bad permission mode accepted")
	}
}

func TestPresetSpecRunsCodexAgainstDeepSeekByFlagsOnly(t *testing.T) {
	spec := PresetSpec("sk-test")
	if spec.Name != PresetAgent || spec.Command != "codex" || spec.Task == nil {
		t.Fatalf("spec: %+v", spec)
	}
	args := strings.Join(spec.Task.Args, " ")
	for _, want := range []string{"model_provider=deepseek", `base_url="https://api.deepseek.com"`, `wire_api="responses"`, "exec --json"} {
		if !strings.Contains(args, want) {
			t.Fatalf("task args lack %q: %s", want, args)
		}
	}
	if spec.Task.OutputMode != "codex" {
		t.Fatalf("output mode %q: the worker's stream is codex's", spec.Task.OutputMode)
	}
	if spec.Env["DEEPSEEK_API_KEY"] != "sk-test" {
		t.Fatal("key not in the agent environment")
	}
	if len(spec.Task.ResumeArgs) == 0 || spec.Task.PermissionArgs["acceptEdits"] == nil {
		t.Fatalf("a correction cycle needs resume args and acceptEdits needs a sandbox: %+v", spec.Task)
	}
	if strings.Contains(args, "--sandbox") {
		t.Fatal("a sandbox flag in the fixed args would conflict with the permission mode's")
	}
	if PresetSpec("").Env["DEEPSEEK_API_KEY"] != "" {
		t.Fatal("an empty key must not be stored")
	}
}

func TestWorkerPromptFramesTheBriefAndAsksForAReport(t *testing.T) {
	p := WorkerPrompt("  Add stats().\n")
	if !strings.HasSuffix(strings.TrimSpace(p), "Add stats().") {
		t.Fatalf("brief not last: %q", p[len(p)-60:])
	}
	for _, want := range []string{"STATUS:", "CHANGED:", "VERIFIED:", "Do not commit"} {
		if !strings.Contains(p, want) {
			t.Fatalf("preamble lacks %q", want)
		}
	}
}
