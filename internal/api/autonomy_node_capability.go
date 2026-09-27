package api

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
)

var autoNodeCapabilityHelpers = []string{"autonomy-node-runtime.py", "node-project-dependencies.py", "node-dependencies.py"}

// Catalog metadata proves registration only. Full executable and installed-file
// verification still happens in the privileged runner before each delivery.
func autoNodeToolingDiscovery(root string, read func(string, int64) ([]byte, error)) map[string]any {
	unavailable := func(reason string) map[string]any { return map[string]any{"status": "unavailable", "reason": reason} }
	raw, err := read(filepath.Join(root, "active.json"), 1024)
	if err != nil {
		return unavailable("No readable immutable Node tooling selector")
	}
	var active struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &active) != nil || !autoHash256(active.Key) {
		return unavailable("Invalid Node tooling selector")
	}
	raw, err = read(filepath.Join(root, active.Key, "manifest.json"), 32<<20)
	if err != nil {
		return unavailable("Registered Node tooling manifest is unavailable")
	}
	var manifest map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&manifest) != nil {
		return unavailable("Invalid Node tooling manifest")
	}
	if manifest["key"] != active.Key || manifest["kind"] != "node-tooling" || manifest["schema_version"] != json.Number("1") || manifest["policy"] != "npm-locked-offline-v1" {
		return unavailable("Node tooling registration identity differs")
	}
	delete(manifest, "key")
	canonical, err := autoMaintenanceCanonical(manifest)
	if err != nil || autoSHA(canonical) != active.Key {
		return unavailable("Node tooling manifest checksum differs")
	}
	machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if runtime.GOOS != "linux" || machine == "" || manifest["platform"] != "Linux" || manifest["machine"] != machine {
		return unavailable("Registered Node tooling target is incompatible")
	}
	node, nok := manifest["node_version"].(string)
	npm, pok := manifest["npm_version"].(string)
	files, fok := manifest["files"].([]any)
	if !nok || !pok || node == "" || npm == "" || len(node) > 128 || len(npm) > 128 || !fok || len(files) == 0 {
		return unavailable("Node tooling version or inventory metadata is missing")
	}
	return map[string]any{"status": "registered", "runtime_digest": active.Key, "node_version": node, "npm_version": npm, "manifest_sha256": autoSHA(raw), "scope": "Immutable registered metadata only; the runner verifies all runtime files before use. This is not a worker delivery receipt."}
}

func autoNodeCapability(runner map[string]any, helpers map[string]map[string]any, tooling map[string]any) map[string]any {
	status := "on_demand"
	if runner["status"] != "installed" || tooling["status"] != "registered" {
		status = "unavailable"
	}
	for _, name := range autoNodeCapabilityHelpers {
		if helpers[name]["status"] != "installed" {
			status = "unavailable"
		}
	}
	return map[string]any{
		"capability": "node_packages", "schema_version": 1, "condition": "offline_node_available", "status": status, "helpers": helpers, "tooling": tooling,
		"inputs":         "Choose either exact npm package pins in requirements, or package_json and package_lock naming same-directory canonical archive-relative package.json/package-lock.json files. Source bytes and hashes are bound to the immutable archived assignment; paths are never host paths. At least one module or binary probe is required; these are fixed selectors, not commands or arbitrary argv.",
		"examples":       []map[string]any{{"capability": "node_packages", "schema_version": 1, "condition": "offline_node_available", "requirements": []string{"@playwright/mcp@0.0.80"}, "binaries": []string{"playwright-mcp"}, "evidence": []string{"Retained source test invokes this exact MCP package"}}, {"capability": "node_packages", "schema_version": 1, "condition": "offline_node_available", "package_json": "web/package.json", "package_lock": "web/package-lock.json", "modules": []string{"typescript"}, "evidence": []string{"Retained source lock and test require this module"}}},
		"request":        "Use typed requirements in an eligible blocked builder/reviewer report. The controller retains the same assignment and mixed-capability blockers, provisions offline dependencies and retries only after verification. Independently audited diagnosis may correct unsupported inputs; /requirements retains exact failures and /environments lists reviewed reusable environments.",
		"limits":         map[string]any{"direct_pins": 32, "modules": 32, "binaries": 32, "archive_relative_path_bytes": 512, "locked_packages": 512, "tarball_bytes": 64 << 20, "installed_bytes": 1 << 30, "request_bytes": 16 << 10},
		"supports":       []string{"exact npm registry packages and lockfile v3", "frozen resolved lock before installation and retry", "offline isolated npm lifecycle hooks", "module and binary probes in a credential-free namespace", "genuine offline npm/npx with writable per-worker cache", "same exact runtime in independent reviewers and trusted expert/private tests"},
		"excludes":       []string{"arbitrary worker registry URLs", "git/file/link/workspace dependency sources", "host package hooks or shell commands", "ordinary-worker registry network access"},
		"delivery":       "New workers receive /opt/node, /opt/node-project and node_modules at the selected source manifest directory (direct pins: /work/node_modules). Trusted tests expose selected modules through /scratch/node_modules for default and nested scratch work. Existing running workers do not gain mounts retroactively.",
		"verification":   "Read the node field of /prerequisite for exact source/admission, generation, input_key, bundle_key, lock_sha256, runtime_digest and probe_sha256. on_demand does not prove a particular dependency resolves or this worker received it. Independent project tests must still pass.",
		"failure_states": "checking/recovering/waiting are pending; unavailable/failed retain explicit diagnostics. Unsupported inputs enter bounded independently reviewed diagnosis; temporary transport failures retain bounded retry/backoff. No failure is a successful empty environment.",
		"network_status": "Not probed by discovery; only the fixed provisioner socket accesses the bounded registry broker, never the ordinary research socket.", "worker_receipt": "/prerequisite", "receipts": "/requirements",
	}
}
func autoInstalledNodeCapability(dependencies, runnerPath string, runner map[string]any) map[string]any {
	helpers := map[string]map[string]any{}
	for _, name := range autoNodeCapabilityHelpers {
		helpers[name] = autoCapabilityInstallation(filepath.Join(filepath.Dir(runnerPath), name))
	}
	return autoNodeCapability(runner, helpers, autoNodeToolingDiscovery(filepath.Join(dependencies, "node-tooling"), autoReadRootImmutable))
}
