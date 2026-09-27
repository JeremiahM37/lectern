package api

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const autoPythonProvisioner = "/usr/local/libexec/lectern-python-project-dependencies.py"
const autoExpertProbeHelper = "/usr/local/libexec/lectern-autonomy-expert-probe.py"

// This is discovery of a registered mechanism, not proof that a particular
// package resolves or that a running worker received an environment. Only the
// runner's bound receipt and launch record establish those facts.
func autoCapabilityInstallation(path string) map[string]any {
	unavailable := func(reason string) map[string]any {
		return map[string]any{"status": "unavailable", "reason": reason}
	}
	raw, err := autoReadRootRegular(path, 2<<20)
	if err != nil || len(raw) == 0 {
		return unavailable("Registered helper identity could not be read")
	}
	return map[string]any{"status": "installed", "sha256": autoSHA(raw)}
}

func autoCapabilityCatalog(dependencies, runner, pythonHelper string) map[string]any {
	runnerInfo := autoCapabilityInstallation(runner)
	helperInfo := autoCapabilityInstallation(pythonHelper)
	catalog := autoCapabilityCatalogFromInstallation(dependencies, runnerInfo, helperInfo)
	catalog["capabilities"] = append(catalog["capabilities"].([]map[string]any), autoExpertProbeCapability(runnerInfo, autoCapabilityInstallation(autoExpertProbeHelper)))
	return catalog
}

func autoExpertProbeCapability(runner, helper map[string]any) map[string]any {
	status := "unavailable"
	if runner["status"] == "installed" && helper["status"] == "installed" {
		status = "on_demand"
	}
	return map[string]any{
		"capability": "expert_recovery_investigation", "status": status, "helper": helper,
		"discovery":    "GET /expert-recovery; current planner pins exhausted source checkpoints with POST /expert-recovery using project_id and source_task_id",
		"proposal":     "expert_recovery_task_id and expert_progress_key select the pinned source under normal independent plan audits",
		"request":      "During that audit each assigned auditor uses POST /expert-probes with progress_key, Python script and optional fixtures, argv and profile ordinary180. GET /expert-probes discovers its existing probe IDs, including same-assignment report corrections; GET /expert-probes?id=ID polls and records execution evidence.",
		"execution":    "Offline isolated process, immutable source, separate bounded scratch, at most180seconds. Current profile does not provide network access or host commands.",
		"verification": "Execution receipts establish mounted source, runtime, inputs and outputs. Both independent auditors must assess causal relevance and materially changed strategy against prior failures; execution alone is not proof of progress.",
		"limits":       "Durable per-root investigation and repair budgets; unchanged conditions cannot renew eligibility through time, renamed work or rewritten logs. Original acceptance and failed history remain binding.",
		"authority":    "Investigation and independently admitted private repair only. No publication, host-wide permission, automatic approval or quota override.",
	}
}

func autoCapabilityCatalogFromInstallation(dependencies string, runnerInfo, helperInfo map[string]any) map[string]any {
	pythonRuntime := autoPythonTestRuntime(filepath.Join(dependencies, "python"))
	pythonStatus := "on_demand"
	if runnerInfo["status"] != "installed" || helperInfo["status"] != "installed" || pythonRuntime["status"] != "provisioned" {
		pythonStatus = "unavailable"
	}
	goStatus := "on_demand"
	if runnerInfo["status"] != "installed" {
		goStatus = "unavailable"
	}
	return map[string]any{
		"schema_version":        1,
		"reviewed_environments": "/environments",
		"scope":                 "Registered controller capabilities. on_demand means a bounded provisioner exists, not that a requested package is available or installed in this worker. Read /prerequisite and the mounted manifests for exact worker evidence; existing processes do not gain mounts retroactively.",
		"runner":                runnerInfo,
		"capabilities": []map[string]any{
			{
				"capability": "python_test_runtime", "delivery": "default_on_new_worker_launch",
				"discovery": pythonRuntime, "command": "python3 -m pytest",
				"receipt": "/opt/python-test-runtime.json",
			},
			{
				"capability": "python_wheels", "schema_version": 1, "status": pythonStatus,
				"condition": "offline_imports_available", "helper": helperInfo,
				"inputs":         "1–32 evidenced exact distribution pins and concrete import names; project source stays the checkout under test",
				"request":        "Use typed requirements in an eligible blocked builder/reviewer report. An audited diagnosis can establish a supported remedy. The controller retains the assignment, provisions and verifies before retrying.",
				"supports":       []string{"compatible universal and native Python wheels", "transitive dependency resolution", "one environment including supplied pytest", "frozen wheel hashes and executable modes", "runtime hooks confined to isolated execution", "offline import verification"},
				"excludes":       []string{"source builds", "editable/VCS/local-path installs", "host execution of package code", "arbitrary package-manager or host commands"},
				"verification":   "Read the python field of /prerequisite for this worker's Python receipt. Only a source/admission-bound verified receipt plus the launch's used bundle establishes delivery. Independent task tests still determine correctness.",
				"network_status": "Not probed by this catalog; resolver failures and transient outages are reported by the actual bounded attempt",
				"receipts":       "/requirements", "worker_receipt": "/prerequisite",
			},
			{
				"capability": "go_modules", "status": goStatus,
				"request":  "Automatic preflight for eligible source workers with supported go.mod/go.sum inputs",
				"inputs":   "Exact module files and supported installed toolchain; unsupported local replaces or toolchains remain explicit",
				"receipts": "/dependencies", "worker_receipt": "/prerequisite",
			},
			autoBrowserCapability(dependencies),
		},
		"planning":  "Recheck this catalog before repeating a historical missing-capability claim. A supported mechanism makes an independently audited investigation possible; it does not change old verdicts, grant a new repair allowance, or prove an environment works.",
		"authority": "Read-only discovery. No publication, production application, destructive operations or changes to core/quota controls are authorized by a capability entry.",
	}
}

// Keep discovery compact and paginated. Details preserve exact identities and
// provenance; a catalog entry never grants source/repair/publication authority.
func autoDiagnosisEnvironmentDiscovery(a *autoRecord, query url.Values) (any, int) {
	for key, values := range query {
		if len(values) != 1 || key != "diagnosis_task_id" && key != "after" {
			return map[string]string{"error": "Use diagnosis_task_id for details or after for the next index page"}, http.StatusBadRequest
		}
	}
	parse := func(key string) (int64, bool) {
		s, exists := query[key]
		if !exists {
			return 0, true
		}
		id, err := strconv.ParseInt(s[0], 10, 64)
		return id, err == nil && id > 0
	}
	id, valid := parse("diagnosis_task_id")
	after, afterValid := parse("after")
	if !valid || !afterValid || id > 0 && after > 0 {
		return map[string]string{"error": "Expected one positive task ID selector"}, http.StatusBadRequest
	}
	all := autoVerifiedDiagnosisEnvironments(a)
	if id > 0 {
		for _, e := range all {
			if e.DiagnosisTaskID == id {
				return map[string]any{"environment": e, "authority": "Verified environment reuse only; ordinary independently audited source and repair admission still applies"}, http.StatusOK
			}
		}
		return map[string]string{"error": "No independently reviewed verified environment for that diagnosis"}, http.StatusNotFound
	}
	items := []map[string]any{}
	var next int64
	for _, e := range all {
		if e.DiagnosisTaskID <= after {
			continue
		}
		if len(items) == 50 {
			next = items[len(items)-1]["diagnosis_task_id"].(int64)
			break
		}
		items = append(items, map[string]any{
			"diagnosis_task_id": e.DiagnosisTaskID, "origin_task_id": e.OriginTaskID,
			"requirements": e.Request.Requirements, "imports": e.Request.Imports,
			"input_key": e.Receipt.InputKey, "bundle_key": e.Receipt.BundleKey,
			"details_uri": "/environments?diagnosis_task_id=" + strconv.FormatInt(e.DiagnosisTaskID, 10),
		})
	}
	return map[string]any{"items": items, "total": len(all), "next_after": next,
		"scope": "Independently reviewed environments, not new task approvals. Read details before selecting environment_diagnosis_task_id; the consumer must pass ordinary source admission and reproduce the exact environment."}, http.StatusOK
}

func autoEnvironmentSelectionPrompt(a *autoRecord) string {
	var b strings.Builder
	b.WriteString("Reviewed environment discovery: GET /environments gives a paginated index; follow next_after and details_uri. An ordinary new, continuation or repair proposal may set environment_diagnosis_task_id to select a previously verified environment. This never selects its source code or grants source/repair eligibility: keep the ordinary source selector and audits. The consumer must reproduce the pinned environment before launch, and its own reviewer must still verify the actual task.\n")
	if a.State == nil {
		return b.String()
	}
	seen := map[int64]bool{}
	for _, p := range a.State.Items {
		id := p.EnvironmentDiagnosisTaskID
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		e := a.EnvironmentPins[id]
		if e == nil || e.Request == nil || e.Receipt == nil {
			continue
		}
		fmt.Fprintf(&b, "Pinned environment for both independent plan audits (prior capability evidence, not this task's approval): %s. Read /environments?diagnosis_task_id=%d for the exact source/admission and independent-use receipts.\n", store.J(map[string]any{"diagnosis_task_id": id, "origin_task_id": e.OriginTaskID, "input_key": e.Receipt.InputKey, "bundle_key": e.Receipt.BundleKey, "runtime_digest": e.Receipt.RuntimeDigest, "requirements": e.Request.Requirements, "imports": e.Request.Imports}), id)
	}
	return b.String()
}
