package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pythonRequirement() autonomy.Requirement {
	return autonomy.Requirement{Capability: "python_wheels", SchemaVersion: 1, Requirements: []string{"django==5.2.12"}, Imports: []string{"django"}, Condition: "offline_imports_available", Evidence: []string{"isolated import failed; source requirements inspected"}}
}
func TestPythonRequirementInputRestrictionsAndStableKey(t *testing.T) {
	for _, pin := range []string{"https://pypi.org/x", "-r requirements.txt", "django>=5", "django @ https://example.com/x", "./project", "django[extra]==5"} {
		if _, _, err := autoPythonInputs([]string{pin}, []string{"django"}); err == nil {
			t.Fatal(pin)
		}
	}
	r := pythonRequirement()
	key := autoRequirementKey(r)
	for _, name := range []string{"Django==5.2.12", "DJANGO==5.2.12", "dJaNgO==5.2.12"} {
		r.Requirements = []string{name}
		if autoRequirementKey(r) != key {
			t.Fatal("package-name case changed requirement identity")
		}
	}
	r.Evidence = []string{"different report, same actual input"}
	if autoRequirementKey(r) != key {
		t.Fatal("evidence churn minted remediation")
	}
	pins, _, err := autoPythonInputs([]string{"some_pkg==1.0", "some-pkg==1.0"}, []string{"package"})
	if err != nil || len(pins) != 1 {
		t.Fatal(pins, err)
	}
	if _, _, err = autoPythonInputs([]string{"django==5", "django==4"}, []string{"django"}); err == nil {
		t.Fatal("version conflict accepted")
	}
}
func TestBlockedRequirementsRetainTaskAdmissionAndDeduplicate(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	j := autoFindJob(a, p.DocumentationTaskID)
	j.Status = "exporting"
	j.Rejected = false
	originalState := a.State
	admission := j.Admission
	raw, _ := json.Marshal(autonomy.BuildReport{Outcome: "blocked", Summary: "dependency missing", Evidence: []string{"actual import failure"}, Requirements: []autonomy.Requirement{pythonRequirement()}})
	stopped, err := s.recordAutoRequirements(context.Background(), a, j, raw)
	if err != nil || !stopped {
		t.Fatal(stopped, err)
	}
	if a.State != originalState || j.Status != "stopped" || j.PendingPythonRequest == nil || j.Admission != admission || j.Rejected {
		t.Fatal("blocked report advanced or discarded retained task")
	}
	if _, err = s.recordAutoRequirements(context.Background(), a, j, raw); err != nil {
		t.Fatal(err)
	}
	if len(a.Requirements) != 1 {
		t.Fatal("duplicate requirement")
	}
	for _, r := range a.Requirements {
		if len(r.Occurrences) != 1 {
			t.Fatal("duplicate occurrence")
		}
	}
	clone := *j
	clone.ID = "fresh"
	autoPreparePythonResume(j, &clone)
	if clone.TaskID != j.TaskID || clone.Admission != j.Admission || clone.RepairAttemptTaskID != j.RepairAttemptTaskID || clone.PythonRequest == nil || clone.PendingPythonRequest != nil || !clone.PythonNeedsChange || j.PendingPythonRequest == nil {
		t.Fatal("fresh process lost or consumed original provenance")
	}
	loaded, err := s.loadAuto()
	if err != nil || len(loaded.Requirements) != 1 {
		t.Fatal("ledger not durable", err)
	}
}
func TestUnsupportedRequirementDefersWithoutFalseReadiness(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	j := autoFindJob(a, p.DocumentationTaskID)
	r := pythonRequirement()
	r.Capability = "missing_browser_fixture"
	raw, _ := json.Marshal(autonomy.BuildReport{Outcome: "blocked", Summary: "fixture missing", Evidence: []string{"observed missing input"}, Requirements: []autonomy.Requirement{r}})
	original := a.State
	if intercepted, err := s.recordAutoRequirements(context.Background(), a, j, raw); err != nil || !intercepted {
		t.Fatal(err)
	}
	if !j.RequirementHold || j.Status != "deferred" || len(a.HeldRuns) != 1 || a.HeldRuns[0] != original || autoDeferredReady(j) {
		t.Fatal("unsupported capability falsely completed")
	}
	entry := a.Requirements[j.RequirementIDs[0]]
	if entry.State != "unsupported" || strings.Contains(entry.Reason, "requires human") {
		t.Fatal(entry)
	}
}
func TestSubstantiveReviewRejectionNotIntercepted(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	j := autoFindJob(a, p.DocumentationTaskID)
	j.Role = "reviewer"
	no := false
	raw, _ := json.Marshal(autonomy.Verdict{Outcome: "incomplete", Approve: &no, Reason: "real defect plus missing dependency", Requirements: []autonomy.Requirement{pythonRequirement()}})
	if intercepted, err := s.recordAutoRequirements(context.Background(), a, j, raw); err != nil || intercepted {
		t.Fatal("substantive rejection evaded", err)
	}
}
func TestPythonReceiptRequiresBindingAndChangedVerifiedEnvironment(t *testing.T) {
	req := &autoPythonRequest{SourceJob: "source", SourceSHA: strings.Repeat("a", 64), AdmissionSHA: strings.Repeat("b", 64)}
	j := &autoJob{PythonRequest: req, PythonPreviousBundle: strings.Repeat("c", 64), PythonNeedsChange: true, Status: "prepared"}
	a := &autoRecord{}
	receipt := autoPythonReceipt{State: "verified", Capability: "python_wheels", SourceJob: req.SourceJob, SourceSHA: req.SourceSHA, AdmissionSHA: req.AdmissionSHA, InputKey: strings.Repeat("d", 64), BundleKey: j.PythonPreviousBundle, RuntimeDigest: strings.Repeat("e", 64)}
	raw, _ := json.Marshal(receipt)
	if ready, err := autoApplyPythonReceipt(a, j, raw); err != nil || ready || !j.PythonNeedsChange {
		t.Fatal("unchanged environment relaunched", err)
	}
	receipt.BundleKey = strings.Repeat("f", 64)
	receipt.SourceJob = "other"
	raw, _ = json.Marshal(receipt)
	if _, err := autoApplyPythonReceipt(a, j, raw); err == nil {
		t.Fatal("different assignment proof accepted")
	}
	// A reviewed diagnosis must explicitly release the unchanged-input hold.
	j.RequirementHold = false
	receipt.SourceJob = req.SourceJob
	raw, _ = json.Marshal(receipt)
	if ready, err := autoApplyPythonReceipt(a, j, raw); err != nil || !ready || j.PythonNeedsChange {
		t.Fatal("verified changed environment not ready", err)
	}
	j.Status = "deferred"
	j.Recovery = &autoRecoveryReceipt{State: "not_applicable"}
	if !autoDeferredReady(j) {
		t.Fatal("verified retained assignment not ready")
	}
	j.RequirementHold = true
	if autoDeferredReady(j) {
		t.Fatal("diagnostic hold bypassed")
	}
}
func TestPythonRequestAtomicPublicationAndImmutableRetry(t *testing.T) {
	root := t.TempDir()
	j := &autoJob{ID: "job", PythonRequest: &autoPythonRequest{Kind: "python_wheels", SchemaVersion: 1, Requirements: []string{"django==5.2.12"}}}
	os.Mkdir(filepath.Join(root, j.ID), 0700)
	if err := autoWritePythonRequestAt(root, j); err != nil {
		t.Fatal(err)
	}
	if err := autoWritePythonRequestAt(root, j); err != nil {
		t.Fatal(err)
	}
	j.PythonRequest.Requirements = []string{"django==5.1"}
	if err := autoWritePythonRequestAt(root, j); err == nil {
		t.Fatal("published request replaced")
	}
}
func TestDiagnosedRequirementResumesOriginalStoppedAssignment(t *testing.T) {
	_, a, _ := documentationFixture(t)
	a.State.Phase = autonomy.Build
	a.State.Assignments = []autonomy.Assignment{{TaskID: 987, Role: "builder", Item: 0, Round: a.State.Revision, Step: a.State.Step}}
	j := &autoJob{TaskID: 987, Status: "deferred", PendingPythonRequest: &autoPythonRequest{Kind: "python_wheels"}}
	a.Jobs = append(a.Jobs, j)
	retained := a.State
	a.DeferredRuns = []*autonomy.State{retained}
	a.State = nil
	if !autoResumeRecovered(a) || a.State != retained || j.Status != "stopped" {
		t.Fatal("diagnosis did not restore retained task before fresh provisioning")
	}
}

func TestPythonInheritancePinsExactVerifiedEnvironment(t *testing.T) {
	_, a, _ := documentationFixture(t)
	source := a.Jobs[0]
	source.PythonRequest = &autoPythonRequest{Kind: "python_wheels", Requirements: []string{"django==5.2.12"}, Imports: []string{"django"}, SourceJob: "original", SourceSHA: strings.Repeat("a", 64), AdmissionSHA: strings.Repeat("b", 64)}
	source.PythonRecovery = &autoPythonReceipt{State: "verified", InputKey: strings.Repeat("c", 64), BundleKey: strings.Repeat("d", 64)}
	a.State.Assignments = []autonomy.Assignment{{TaskID: source.TaskID, Role: "builder", Item: a.State.Item, Completed: true}}
	reviewer := &autoJob{Role: "reviewer", Status: "prepared"}
	autoInheritPythonRequest(a, reviewer)
	if reviewer.PythonExpectedInput != source.PythonRecovery.InputKey || reviewer.PythonExpectedBundle != source.PythonRecovery.BundleKey || reviewer.PythonRequest == source.PythonRequest {
		t.Fatal("reviewer did not freeze source environment")
	}
	receipt := autoPythonReceipt{State: "verified", Capability: "python_wheels", SourceJob: "original", SourceSHA: strings.Repeat("a", 64), AdmissionSHA: strings.Repeat("b", 64), InputKey: strings.Repeat("c", 64), BundleKey: strings.Repeat("f", 64), RuntimeDigest: strings.Repeat("e", 64)}
	raw, _ := json.Marshal(receipt)
	if ready, err := autoApplyPythonReceipt(a, reviewer, raw); err != nil || ready {
		t.Fatal("reviewer silently changed tested environment", err)
	}
}
func TestPythonCancellationRetriesWhileDisabled(t *testing.T) {
	s, a, _ := documentationFixture(t)
	response := documentationRunner(t)
	script := "#!/bin/sh\nif [ ! -f \"$LECTERN_DOC_RESPONSE.ok\" ]; then exit 1; fi\necho '{}'\n"
	os.WriteFile(filepath.Join(filepath.Dir(response), "sudo"), []byte(script), 0700)
	a.Config.Enabled = false
	a.Jobs = []*autoJob{{ID: "pending", Status: "prepared", PythonRequest: &autoPythonRequest{Kind: "python_wheels"}}}
	s.stopAutoJobs(context.Background(), a, "off")
	if a.Status != "error" || !autoPythonPending(a.Jobs[0]) {
		t.Fatal("failed stop lost Python owner")
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(response+".ok", nil, 0600)
	s.RunAutonomyTick(context.Background())
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "off" || autoPythonPending(loaded.Jobs[0]) {
		t.Fatal("disabled tick did not retry Python stop")
	}
}

func TestRequirementOverflowKeepsPlanningAndResumesExactHeldState(t *testing.T) {
	s, a, _ := documentationFixture(t)
	for i := 0; i < 16; i++ {
		state, _ := autonomy.NewState("2026-09-26")
		state.Phase = autonomy.Build
		state.Cycle = i + 1
		state.Assignments = []autonomy.Assignment{{TaskID: int64(1000 + i), Role: "builder"}}
		a.DeferredRuns = append(a.DeferredRuns, state)
	}
	held := a.State
	held.Phase = autonomy.Build
	held.Assignments = []autonomy.Assignment{{TaskID: 9000, Role: "builder", Item: held.Item, Round: held.Revision, Step: held.Step}}
	j := &autoJob{TaskID: 9000, Role: "builder", Status: "exporting", RequirementHold: true}
	a.Jobs = append(a.Jobs, j)
	autoDeferRequirements(a, j, time.Now())
	if a.State.Phase != autonomy.Plan || len(a.DeferredRuns) != 16 || len(a.HeldRuns) != 1 || a.HeldRuns[0] != held {
		t.Fatal("overflow stopped planning or discarded held state")
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	restored, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	job := autoFindJob(restored, 9000)
	job.RequirementHold = false
	job.PendingPythonRequest = &autoPythonRequest{Kind: "python_wheels"}
	if !autoResumeRecovered(restored) || restored.State.ActiveTaskIDs()[0] != 9000 || job.Status != "stopped" || len(restored.HeldRuns) != 0 || len(restored.DeferredRuns) != 16 {
		t.Fatal("reviewed remedy failed to recover exact overflow assignment")
	}
}
func TestPythonSupportedInputsRespectRunnerEnvelopeLimits(t *testing.T) {
	if _, _, err := autoPythonInputs([]string{strings.Repeat("a", 129) + "==1"}, []string{"django"}); err == nil {
		t.Fatal("oversized registry name")
	}
	if _, _, err := autoPythonInputs([]string{"django==" + strings.Repeat("1", 129)}, []string{"django"}); err == nil {
		t.Fatal("oversized version")
	}
	if _, _, err := autoPythonInputs([]string{"django==5.2.12"}, []string{strings.Repeat("a", 513)}); err == nil {
		t.Fatal("oversized runner import")
	}
}

func TestTemporarilyUnavailableOverflowReentersBoundedPolling(t *testing.T) {
	_, a, _ := documentationFixture(t)
	a.DeferredRuns = nil
	for i := 0; i < 16; i++ {
		state, _ := autonomy.NewState("2026-09-26")
		state.Phase = autonomy.Build
		state.Assignments = []autonomy.Assignment{{TaskID: int64(1000 + i), Role: "builder"}}
		a.DeferredRuns = append(a.DeferredRuns, state)
		a.Jobs = append(a.Jobs, &autoJob{TaskID: int64(1000 + i), Status: "deferred"})
	}
	held, _ := autonomy.NewState("2026-09-26")
	held.Phase = autonomy.Review
	held.Assignments = []autonomy.Assignment{{TaskID: 9000, Role: "reviewer"}}
	j := &autoJob{TaskID: 9000, Status: "deferred", PythonRequest: &autoPythonRequest{Kind: "python_wheels"}, PythonRecovery: &autoPythonReceipt{State: "unavailable"}, Recovery: &autoRecoveryReceipt{State: "not_applicable"}}
	a.Jobs = append(a.Jobs, j)
	a.HeldRuns = []*autonomy.State{held}
	autoPromoteHeldPrerequisites(a)
	if len(a.HeldRuns) != 1 || len(a.DeferredRuns) != 16 {
		t.Fatal("polling cap exceeded")
	}
	// One unsupported entry vacates its polling slot without losing its state.
	autoFindJob(a, 1000).RequirementHold = true
	autoPromoteHeldPrerequisites(a)
	if len(a.DeferredRuns) != 16 || a.DeferredRuns[15] != held || len(a.HeldRuns) != 1 {
		t.Fatal("temporary overflow was stranded")
	}
	j.PythonRecovery.State = "verified"
	if !autoResumeRecovered(a) || a.State != held || j.Status != "prepared" {
		t.Fatal("changed prerequisite did not restore original review")
	}
}

func TestColdPrerequisiteOverflowGetsFairPollingSlot(t *testing.T) {
	_, a, _ := documentationFixture(t)
	a.DeferredRuns = nil
	for i := 0; i < 16; i++ {
		state, _ := autonomy.NewState("2026-09-26")
		state.Phase = autonomy.Build
		state.Assignments = []autonomy.Assignment{{TaskID: int64(1000 + i), Role: "builder"}}
		a.DeferredRuns = append(a.DeferredRuns, state)
		a.Jobs = append(a.Jobs, &autoJob{TaskID: int64(1000 + i), Status: "deferred", Recovery: &autoRecoveryReceipt{State: "waiting"}})
	}
	held, _ := autonomy.NewState("2026-09-26")
	held.Phase = autonomy.Build
	held.Assignments = []autonomy.Assignment{{TaskID: 9000, Role: "builder"}}
	a.HeldRuns = []*autonomy.State{held}
	a.Jobs = append(a.Jobs, &autoJob{TaskID: 9000, Status: "deferred", Recovery: &autoRecoveryReceipt{State: "waiting"}})
	cold := a.DeferredRuns[0]
	autoRotateColdPrerequisite(a, cold, autoFindJob(a, 1000))
	if len(a.DeferredRuns) != 16 || a.DeferredRuns[15] != held || len(a.HeldRuns) != 1 || a.HeldRuns[0] != cold {
		t.Fatal("cold full queue starved overflow")
	}
}
