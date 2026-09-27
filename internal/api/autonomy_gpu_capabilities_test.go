package api

import (
	"strings"
	"testing"
)

func TestGPUCapabilityRequiresQualificationCompleteTransportAndControlIdentity(t *testing.T) {
	q := autoGPUQualifiedRuntime{RuntimeKey: strings.Repeat("a", 64), ReceiptSHA: strings.Repeat("b", 64)}
	helpers := map[string]map[string]any{}
	control := map[string]string{}
	for _, name := range []string{"runtime", "snapshot", "lease", "executor", "telemetry"} {
		helpers[name] = map[string]any{"status": "installed", "sha256": strings.Repeat("c", 64)}
		control[name+"_sha256"] = strings.Repeat("c", 64)
	}
	control["supervisor_sha256"] = strings.Repeat("c", 64)
	runner := []byte("gpu-source-prepare gpu-research-status gpu-research-stop gpu-source-manifest")
	if got := autoGPUCapabilityFrom(q, true, runner, helpers, control); got["status"] != "on_demand" {
		t.Fatal(got)
	}
	if got := autoGPUCapabilityFrom(q, false, runner, helpers, control); got["status"] != "unavailable" {
		t.Fatal("missing qualification advertised")
	}
	if got := autoGPUCapabilityFrom(q, true, []byte("old runner"), helpers, control); got["status"] != "unavailable" {
		t.Fatal("incomplete runner advertised")
	}
	control["executor_sha256"] = strings.Repeat("d", 64)
	if got := autoGPUCapabilityFrom(q, true, runner, helpers, control); got["status"] != "unavailable" {
		t.Fatal("unqualified replacement helper advertised")
	}
}
func TestGPUResearchPromptNamesCurrentSourceAndReceiptLimits(t *testing.T) {
	prompt := autoGPUResearchPrompt("builder")
	for _, value := range []string{"source_paths", "/research-runs", "GET never launches", "/source", "changed source", "scientific"} {
		if !strings.Contains(prompt, value) {
			t.Fatalf("missing %q", value)
		}
	}
	if autoGPUResearchPrompt("auditor_a") != "" {
		t.Fatal("unadmitted auditor offered execution")
	}
}
