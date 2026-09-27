package api

import (
	"bytes"
	"encoding/json"
	"path/filepath"
)

func autoGPUCapability(dependencies, runner string) map[string]any {
	helpers := map[string]map[string]any{}
	for _, name := range []string{"runtime", "snapshot", "lease", "executor", "telemetry"} {
		helpers[name] = autoCapabilityInstallation("/usr/local/libexec/lectern-autonomy-gpu-" + name + ".py")
	}
	runnerBytes, _ := autoReadRootRegular(runner, 2<<20)
	qualification, err := autoGPUQualification(filepath.Join(filepath.Dir(dependencies), "jobs"))
	raw, _ := autoReadRootImmutable(filepath.Join(dependencies, "gpu/qualification.json"), 65536)
	var proof struct {
		Helpers map[string]string `json:"helpers"`
	}
	_ = json.Unmarshal(raw, &proof)
	return autoGPUCapabilityFrom(qualification, err == nil, runnerBytes, helpers, proof.Helpers)
}
func autoGPUCapabilityFrom(q autoGPUQualifiedRuntime, qualified bool, runner []byte, helpers map[string]map[string]any, control map[string]string) map[string]any {
	result := map[string]any{
		"capability": "gpu_research", "schema_version": 1, "status": "unavailable", "helpers": helpers,
		"target": "aiserver-amd-research-v1", "profile": "gpu-screen600", "endpoint": "/research-runs",
		"input":  map[string]any{"script": "Python entrypoint; source files available read-only under /source; scratch under /scratch", "argv": "up to32 arguments", "trial": "0..4 explicit trial ordinal", "source_paths": "up to64 canonical relative files/directories; default[.]; select source explicitly around venv/node_modules/caches; selected links are refused"},
		"limits": map[string]any{"seconds": 600, "cpu_cores": 4, "ram_bytes": 8 << 30, "scratch_bytes": 2 << 30, "output_bytes": 16 << 20, "source_bytes": 512 << 20, "request_wire_bytes": 2 << 20, "rolling_root_gpu_minutes_per_24h": 60},
		"scope":  "Registration and prior bounded hardware qualification only. Each run must bind captured source, qualified helpers/runtime, live thermal/headroom screening and shared physical lease. Not a claim of current availability, full kernel compatibility, performance, or hard per-process GPU-memory isolation.",
		"source": "Async immutable capture with selected-path manifest and before/after inventories; not an atomic filesystem snapshot. Existing artifact archives remain unchanged.",
		"result": "GET /research-runs?id=ID polls without starting; offset retrieves bounded output, manifest_offset retrieves exact source manifest. Executed receipts prove mechanical execution, not scientific validity. An unchanged tree/protocol/runtime/trial reuses its recorded result; changed source may rerun the same protocol.",
	}
	if !qualified || !autoHash256(q.RuntimeKey) || !autoHash256(q.ReceiptSHA) {
		result["reason"] = "No completed matching GPU qualification is registered"
		return result
	}
	for _, token := range []string{"gpu-source-prepare", "gpu-research-status", "gpu-research-stop", "gpu-source-manifest"} {
		if !bytes.Contains(runner, []byte(token)) {
			result["reason"] = "Installed runner does not provide the complete GPU lifecycle"
			return result
		}
	}
	for _, name := range []string{"runtime", "snapshot", "lease", "executor", "telemetry"} {
		if helpers[name]["status"] != "installed" {
			result["reason"] = "A required trusted GPU helper is not installed"
			return result
		}
	}
	for _, name := range []string{"runtime", "lease", "executor", "telemetry"} {
		key := name + "_sha256"
		if name == "runtime" {
			key = "supervisor_sha256"
		}
		expected := control[key]
		if !autoHash256(expected) || helpers[name]["sha256"] != expected {
			result["reason"] = "GPU control helpers differ from the measured qualification"
			return result
		}
	}
	result["status"] = "on_demand"
	result["runtime_key"] = q.RuntimeKey
	result["qualification_sha256"] = q.ReceiptSHA
	return result
}

func autoGPUResearchPrompt(role string) string {
	if role != "builder" && role != "reviewer" {
		return ""
	}
	return "\nGPU research: inspect /capabilities for gpu_research availability before depending on it. For a registered qualified runtime, POST /research-runs with {script,argv,trial,source_paths}; choose explicit relative source paths (for example src, tests, pyproject.toml) to omit dependency/runtime/cache trees openly. The controller asynchronously captures current files and exposes the selection manifest. The complete JSON request must fit 2 MiB after escaping; individual field limits do not override that total. Keep the returned ID and poll GET /research-runs?id=ID; GET never launches. Source is read-only /source, scratch is /scratch, network is disabled; the Python script may explicitly add /source to sys.path. Read output with &offset=0 and source manifest with &manifest_offset=0. Shared lease contention, thermal/headroom screening, quota and rolling GPU time can defer execution. Stop or cleanup uncertainty is not a successful result. Use unchanged protocol on changed source when testing a fix; identical source/protocol/runtime/trial reuses prior execution, and deliberate repeated measurements require a new trial ordinal. GPU receipts establish execution and exact inputs only; assess correctness, controls and scientific usefulness independently. Do not request SSH, root commands, arbitrary devices, service restarts or public network access.\n"
}
