package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func nodeRequirement() autonomy.Requirement {
	return autonomy.Requirement{Capability: "node_packages", SchemaVersion: 1, Condition: "offline_node_available", Requirements: []string{"@playwright/mcp@0.0.80"}, Binaries: []string{"playwright-mcp"}, Evidence: []string{"retained source invokes exact MCP package"}}
}
func nodeVerified(j *autoJob) autoNodeReceipt {
	return autoNodeReceipt{Generation: autoNodeGeneration(j), State: "verified", Capability: "node_packages", SourceJob: j.NodeRequest.SourceJob, SourceSHA: j.NodeRequest.SourceSHA, AdmissionSHA: j.NodeRequest.AdmissionSHA, InputKey: autoSHA([]byte("input")), BundleKey: autoSHA([]byte("bundle")), RuntimeDigest: autoSHA([]byte("runtime")), LockSHA: autoSHA([]byte("lock")), ProbeSHA: autoSHA([]byte("probe"))}
}
func TestNodeInputsExactPinsPathsAndSelectors(t *testing.T) {
	good := nodeRequirement()
	n, e := autoNodeInputs(good)
	if e != nil || n.Requirements[0] != "@playwright/mcp@0.0.80" {
		t.Fatal(n, e)
	}
	for _, pin := range []string{"@playwright/mcp@latest", "@playwright/mcp@^0.0.80", "node:fs", "-g@1.2.3", "x@file:/tmp/x", "x@https://example.com/x", "x@npm:y@1.2.3", "X@1.2.3", "x@01.2.3"} {
		r := good
		r.Requirements = []string{pin}
		if _, e := autoNodeInputs(r); e == nil {
			t.Fatal("accepted", pin)
		}
	}
	for _, module := range []string{"../x", "/tmp/x", "a/../../x", "node:fs", "http://host/x", "@scope", "a\\b", "a/--eval"} {
		r := good
		r.Modules = []string{module}
		if _, e := autoNodeInputs(r); e == nil {
			t.Fatal("module", module)
		}
	}
	for _, p := range []string{"../package.json", "/work/package.json", "x/../package.json", "x\\package.json", "x/not-package.json"} {
		r := good
		r.Requirements = nil
		r.PackageJSON = p
		r.PackageLock = "package-lock.json"
		if _, e := autoNodeInputs(r); e == nil {
			t.Fatal("path", p)
		}
	}
	r := good
	r.Requirements = nil
	r.PackageJSON = "frontend/package.json"
	r.PackageLock = "frontend/package-lock.json"
	if _, e := autoNodeInputs(r); e != nil {
		t.Fatal(e)
	}
	r.PackageLock = "other/package-lock.json"
	if _, e := autoNodeInputs(r); e == nil {
		t.Fatal("different source directories")
	}
	r = good
	r.Requirements = append(r.Requirements, "@playwright/mcp@0.0.81")
	if _, e := autoNodeInputs(r); e == nil {
		t.Fatal("conflicting exact pins")
	}
	r = good
	r.Requirements = append(r.Requirements, r.Requirements...)
	r.Evidence = []string{"new prose"}
	if autoRequirementKey(r) != autoRequirementKey(good) {
		t.Fatal("cosmetic evidence/ordering changes key")
	}
}
func TestNodeMixedBlockedReportPreservesAssignmentAndSeparatesEvidence(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	j := autoFindJob(a, p.DocumentationTaskID)
	j.Status = "exporting"
	j.Rejected = false
	state := a.State
	admission := j.Admission
	report, _ := json.Marshal(autonomy.BuildReport{Outcome: "blocked", Summary: "missing isolated runtimes", Evidence: []string{"real subprocess failure"}, Requirements: []autonomy.Requirement{pythonRequirement(), nodeRequirement()}})
	ok, e := s.recordAutoRequirements(context.Background(), a, j, report)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	if a.State != state || j.RequirementHold || j.Status != "stopped" || j.PendingPythonRequest == nil || j.PendingNodeRequest == nil || j.Admission != admission || j.Rejected {
		t.Fatal("mixed requirement lost admission or false hold")
	}
	clone := *j
	clone.ID = "new-process"
	autoPreparePythonResume(j, &clone)
	autoPrepareNodeResume(j, &clone)
	clone.Status = "prepared"
	if clone.TaskID != j.TaskID || clone.NodeRequest.SourceJob != j.ID || clone.NodeRequest.SourceSHA != strings.Repeat("a", 64) || clone.RepairAttemptTaskID != j.RepairAttemptTaskID {
		t.Fatal("same assignment identity lost")
	}
	py := autoPythonReceipt{State: "verified", Capability: "python_wheels", InputKey: autoSHA([]byte("pyinput")), BundleKey: autoSHA([]byte("pybundle")), RuntimeDigest: autoSHA([]byte("pyruntime")), SourceJob: clone.PythonRequest.SourceJob, SourceSHA: clone.PythonRequest.SourceSHA, AdmissionSHA: clone.PythonRequest.AdmissionSHA}
	raw, _ := json.Marshal(py)
	if ok, e = autoApplyPythonReceipt(a, &clone, raw); e != nil || !ok {
		t.Fatal(e)
	}
	for _, entry := range a.Requirements {
		if entry.Request.Capability == "node_packages" && entry.State == "verified" {
			t.Fatal("Python forged Node verification")
		}
	}
	clone.Recovery = &autoRecoveryReceipt{State: "not_applicable"}
	if autoDeferredReady(&clone) {
		t.Fatal("resumed without Node")
	}
	nr := nodeVerified(&clone)
	raw, _ = json.Marshal(nr)
	if ok, e = autoApplyNodeReceipt(a, &clone, raw); e != nil || !ok {
		t.Fatal(e)
	}
	if !autoDeferredReady(&clone) {
		t.Fatal("all verified prerequisites did not release retained assignment")
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	loaded, e := s.loadAuto()
	if e != nil || len(loaded.Requirements) != 2 {
		t.Fatal("durability", e)
	}
	for _, entry := range loaded.Requirements {
		if len(entry.Occurrences) != 1 || entry.Occurrences[0].ReportSHA != autoSHA(report) {
			t.Fatal("original report provenance lost")
		}
	}
}
func TestNodeUnsupportedSiblingCannotPartiallyRelaunch(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	j := autoFindJob(a, p.DocumentationTaskID)
	j.Status = "exporting"
	r := nodeRequirement()
	r.Requirements = []string{"unlocked@latest"}
	report, _ := json.Marshal(autonomy.BuildReport{Outcome: "blocked", Requirements: []autonomy.Requirement{pythonRequirement(), r}})
	if ok, e := s.recordAutoRequirements(context.Background(), a, j, report); e != nil || !ok {
		t.Fatal(e)
	}
	if !j.RequirementHold || j.PendingPythonRequest != nil || j.PendingNodeRequest != nil || j.Status != "deferred" {
		t.Fatal("partial recipe authorized relaunch")
	}
}
func TestNodeReceiptForeignUnchangedAndInheritedLockRejected(t *testing.T) {
	req, _ := autoNodeInputs(nodeRequirement())
	req.SourceJob = "source"
	req.SourceSHA = autoSHA([]byte("archive"))
	req.AdmissionSHA = autoSHA([]byte("admission"))
	j := &autoJob{NodeRequest: req, NodeNeedsChange: true}
	a := &autoRecord{}
	r := nodeVerified(j)
	r.SourceJob = "foreign"
	raw, _ := json.Marshal(r)
	if _, e := autoApplyNodeReceipt(a, j, raw); e == nil || j.NodeRecovery != nil {
		t.Fatal("foreign mutated state")
	}
	r = nodeVerified(j)
	j.NodePreviousBundle = r.BundleKey
	raw, _ = json.Marshal(r)
	if ok, e := autoApplyNodeReceipt(a, j, raw); e != nil || ok || !j.RequirementHold {
		t.Fatal("unchanged environment resumed", e)
	}
	j.RequirementHold = false
	j.NodeNeedsChange = false
	j.NodeExpectedLock = autoSHA([]byte("other lock"))
	if ok, e := autoApplyNodeReceipt(a, j, raw); e != nil || ok || !j.RequirementHold {
		t.Fatal("changed frozen lock accepted", e)
	}
	j.NodeExpectedLock = ""
	j.NodeRequest.PackageJSON = "package.json"
	j.NodeRequest.PackageLock = "package-lock.json"
	if _, e := autoApplyNodeReceipt(a, j, raw); e == nil {
		t.Fatal("lock mode lacked source byte hashes")
	}
}
func TestNodeRequestAtomicReplayAndResumeInheritance(t *testing.T) {
	root := t.TempDir()
	req, _ := autoNodeInputs(nodeRequirement())
	req.SourceJob = "source"
	req.SourceSHA = autoSHA([]byte("archive"))
	req.AdmissionSHA = autoSHA([]byte("admission"))
	j := &autoJob{ID: "job", NodeRequest: req}
	os.Mkdir(filepath.Join(root, j.ID), 0700)
	if e := autoWriteNodeRequestAt(root, j); e != nil {
		t.Fatal(e)
	}
	if e := autoWriteNodeRequestAt(root, j); e != nil {
		t.Fatal(e)
	}
	source := *j
	r := nodeVerified(j)
	source.NodeRecovery = &r
	dest := &autoJob{}
	autoInheritNodeFrom(dest, &source)
	if dest.NodeExpectedLock != r.LockSHA || dest.NodeExpectedBundle != r.BundleKey || dest.NodeRequest.SourceJob != "source" {
		t.Fatal("inheritance drift")
	}
	j.NodeExpectedLock = r.LockSHA
	if e := autoWriteNodeRequestAt(root, j); e == nil {
		t.Fatal("rewrote sealed request")
	}
	clone := source
	autoPrepareNodeResume(&source, &clone)
	if clone.NodeExpectedLock != r.LockSHA {
		t.Fatal("restart changed lock")
	}
}
func TestNodePendingRecoveryHeldOverflowEligibility(t *testing.T) {
	j := &autoJob{Status: "deferred", PendingNodeRequest: &autoNodeRequest{}, Recovery: &autoRecoveryReceipt{State: "not_applicable"}}
	if !autoDeferredReady(j) {
		t.Fatal("pending node recipe cannot restore same assignment")
	}
	j.PendingNodeRequest = nil
	j.NodeRequest = &autoNodeRequest{}
	j.NodeRecovery = &autoNodeReceipt{State: "recovering"}
	if autoDeferredReady(j) {
		t.Fatal("unverified ready")
	}
	j.RequirementHold = true
	j.NodeRecovery.State = "verified"
	if autoDeferredReady(j) {
		t.Fatal("unsupported sibling ignored")
	}
	j.RequirementHold = false
	if !autoDeferredReady(j) {
		t.Fatal("verified held task stranded")
	}
	j.RecoveryCheckAt = time.Now()
	j.NodeStopped = false
	if !autoNodePending(j) {
		t.Fatal("OFF ownership missing")
	}
	j.NodeStopped = true
	if autoNodePending(j) {
		t.Fatal("confirmed stop retained unnecessarily")
	}
}
func TestNodeCancellationRetriesAcrossDisabledReload(t *testing.T) {
	s, a, _ := documentationFixture(t)
	response := documentationRunner(t)
	script := "#!/bin/sh\nif [ ! -f \"$LECTERN_DOC_RESPONSE.ok\" ]; then echo '{\"state\":\"stopping\",\"job\":\"node-owner\",\"generation\":1}'; exit 0; fi\necho '{\"state\":\"stopped\",\"job\":\"node-owner\",\"generation\":1}'\n"
	if e := os.WriteFile(filepath.Join(filepath.Dir(response), "sudo"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	a.Config.Enabled = false
	a.Jobs = []*autoJob{{ID: "node-owner", Status: "prepared", NodeRequest: &autoNodeRequest{Kind: "node_packages"}}}
	s.stopAutoJobs(context.Background(), a, "off")
	if a.Status != "error" || !autoNodePending(a.Jobs[0]) {
		t.Fatal("draining helper lost ownership")
	}
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(response+".ok", nil, 0600)
	s.RunAutonomyTick(context.Background())
	loaded, e := s.loadAuto()
	if e != nil || loaded.Status != "off" || autoNodePending(loaded.Jobs[0]) {
		t.Fatal("OFF restart did not confirm owned stop", e)
	}
}
func TestNodePrerequisiteEndpointBoundToSocketOwner(t *testing.T) {
	s, a, _ := documentationFixture(t)
	r, _ := autoNodeInputs(nodeRequirement())
	r.SourceJob = "source"
	r.SourceSHA = autoSHA([]byte("archive"))
	r.AdmissionSHA = autoSHA([]byte("admission"))
	j := &autoJob{ID: "owner", NodeRequest: r, Recovery: &autoRecoveryReceipt{State: "not_applicable"}}
	receipt := nodeVerified(j)
	j.NodeRecovery = &receipt
	a.Jobs = append(a.Jobs, j)
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	s.autoJobReadBridge(j.ID)(w, httptest.NewRequest("GET", "/prerequisite?job_id=foreign", nil))
	var got struct {
		State string           `json:"state"`
		Node  *autoNodeReceipt `json:"node"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Node == nil || got.Node.BundleKey != receipt.BundleKey || got.State != "not_applicable" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.autoJobReadBridge("foreign")(w, httptest.NewRequest("GET", "/prerequisite?job_id=owner", nil))
	if w.Code != 404 {
		t.Fatal("foreign bridge read receipt", w.Code)
	}
}
func TestNodeReviewerInheritsAlongsideExistingPython(t *testing.T) {
	_, a, _ := documentationFixture(t)
	source := a.Jobs[0]
	r, _ := autoNodeInputs(nodeRequirement())
	r.SourceJob = source.ID
	r.SourceSHA = autoSHA([]byte("archive"))
	r.AdmissionSHA = autoSHA([]byte("admission"))
	source.NodeRequest = r
	receipt := nodeVerified(source)
	source.NodeRecovery = &receipt
	a.State.Assignments = []autonomy.Assignment{{TaskID: source.TaskID, Role: "builder", Item: a.State.Item, Completed: true}}
	reviewer := &autoJob{Role: "reviewer", Status: "prepared", PythonRequest: &autoPythonRequest{Kind: "python_wheels"}}
	if e := autoInheritPythonRequest(a, reviewer); e != nil {
		t.Fatal(e)
	}
	if reviewer.NodeRequest == nil || reviewer.NodeExpectedLock != receipt.LockSHA {
		t.Fatal("Python presence suppressed Node inheritance")
	}
	verified := nodeVerified(reviewer)
	reviewer.NodeRecovery = &verified
	if e := autoInheritPythonRequest(a, reviewer); e != nil {
		t.Fatal(e)
	}
	if reviewer.NodeRecovery == nil {
		t.Fatal("poll reset existing verification")
	}
}
func TestNodeLateVerifiedReceiptCannotCrossCancellationGeneration(t *testing.T) {
	req, _ := autoNodeInputs(nodeRequirement())
	req.SourceJob = "source"
	req.SourceSHA = autoSHA([]byte("source"))
	req.AdmissionSHA = autoSHA([]byte("admission"))
	j := &autoJob{NodeRequest: req, NodeGeneration: 1}
	late := nodeVerified(j)
	// The same immutable request can be retried only after confirmed OFF stop.
	// A late result from the revoked execution cannot authorize the new launch.
	j.NodeGeneration = 2
	raw, _ := json.Marshal(late)
	if ready, e := autoApplyNodeReceipt(&autoRecord{}, j, raw); e == nil || ready || j.NodeRecovery != nil {
		t.Fatal("revoked result accepted")
	}
	late.Generation = 2
	raw, _ = json.Marshal(late)
	if ready, e := autoApplyNodeReceipt(&autoRecord{}, j, raw); e != nil || !ready {
		t.Fatal("fresh bound cache revalidation refused", e)
	}
}
func TestNodeReviewedRemedyResumesExactHeldReviewerAfterReload(t *testing.T) {
	active, _ := autonomy.NewState("2026-09-26")
	retained, _ := autonomy.NewState("2026-09-25")
	retained.Phase = autonomy.Review
	retained.Item = 0
	retained.Step = 2
	retained.Revision = 1
	retained.Items = []autonomy.Proposal{{ProjectID: 1, Title: "same reviewed repair", Acceptance: []string{"original criterion"}}}
	retained.Assignments = []autonomy.Assignment{{TaskID: 9000, Role: "reviewer", Item: 0, Step: 2, Round: 1}}
	req, _ := autoNodeInputs(nodeRequirement())
	req.SourceJob = "original"
	req.SourceSHA = autoSHA([]byte("source"))
	req.AdmissionSHA = autoSHA([]byte("admission"))
	job := &autoJob{ID: "held", TaskID: 9000, Role: "reviewer", Status: "deferred", PendingNodeRequest: req, RepairAttemptTaskID: 8000, ReportRepairs: 1}
	a := &autoRecord{State: active, HeldRuns: []*autonomy.State{retained}, Jobs: []*autoJob{job}}
	raw, _ := json.Marshal(a)
	var restored autoRecord
	if e := json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if !autoResumeRecovered(&restored) {
		t.Fatal("Node remedy stranded in held history")
	}
	got := autoFindJob(&restored, 9000)
	if got.Status != "stopped" || got.RepairAttemptTaskID != 8000 || got.ReportRepairs != 1 || restored.State.Phase != autonomy.Review || restored.State.Step != 2 || restored.State.Revision != 1 || len(restored.HeldRuns) != 0 || restored.State.Items[0].Acceptance[0] != "original criterion" {
		t.Fatal("retained review admission or counters changed")
	}
	if autoResumeRecovered(&restored) {
		t.Fatal("duplicate restoration")
	}
}
