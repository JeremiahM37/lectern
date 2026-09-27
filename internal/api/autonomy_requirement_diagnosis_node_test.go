package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func nodeDiagnosisRequirement() autonomy.Requirement {
	return autonomy.Requirement{SchemaVersion: 1, Capability: "node_packages", Condition: "offline_node_available", Requirements: []string{"debug@4.4.3"}, Modules: []string{"debug"}, Evidence: []string{"exact immutable source and registry metadata"}}
}
func nodeDiagnosisReports(a *autoRecord, rs []autonomy.Requirement) []byte {
	a.State.Reports[900] = json.RawMessage(store.J(autonomy.BuildReport{Outcome: "ready_for_review", Summary: "diagnosed", Evidence: []string{"retained source"}, Requirements: rs}))
	yes := true
	return []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "independent evidence", Requirements: rs}))
}
func TestNodeDiagnosisReviewedReplacementPreservesSourceAndRequiresVerification(t *testing.T) {
	s, a, p, target, reviewer := diagnosisReviewFixture(t)
	target.NodeRequest = &autoNodeRequest{SchemaVersion: 1, Kind: "node_packages", Requirements: []string{"debug@4.4.2", "ms@2.1.3"}, Modules: []string{"mistyped_probe"}, SourceJob: target.ID, SourceSHA: strings.Repeat("a", 64), AdmissionSHA: autoDiagnosisAdmission(target)}
	before := store.J(target.NodeRequest)
	admission := store.J(target.Admission)
	raw := nodeDiagnosisReports(a, []autonomy.Requirement{nodeDiagnosisRequirement()})
	yes := true
	wrong := nodeDiagnosisRequirement()
	wrong.Requirements = []string{"debug@4.4.2"}
	var reportErr *autoReportError
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Requirements: []autonomy.Requirement{wrong}}))); !errors.As(err, &reportErr) {
		t.Fatal("mismatched reviewer released remedy", err)
	}
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
		t.Fatal(err)
	}
	d := autoSelectedDiagnosis(a, p)
	n := target.PendingNodeRequest
	if target.RequirementHold || n == nil || target.NodeRecovery != nil || d.AttemptedNodeRequest == nil {
		t.Fatal("no real provisioning gate", target)
	}
	if store.J(n.Requirements) != store.J([]string{"debug@4.4.3", "ms@2.1.3"}) || store.J(n.Modules) != store.J([]string{"debug"}) {
		t.Fatal("reviewed replacement lost unrelated pins or retained refuted probe", n)
	}
	if n.SourceJob != d.TargetEvidenceJob || n.SourceSHA != d.TargetArchiveSHA || n.AdmissionSHA != d.TargetAdmissionSHA || store.J(target.NodeRequest) != before || store.J(target.Admission) != admission {
		t.Fatal("original provenance modified")
	}
	clone := *target
	autoPrepareNodeResume(target, &clone)
	if clone.TaskID != target.TaskID || clone.NodeRequest == nil || clone.NodeRecovery != nil || !clone.NodeNeedsChange {
		t.Fatal("resume bypassed real verification")
	}
	clone.ID = "new-provisioner"
	clone.Status = "deferred"
	clone.RequirementHold = true
	clone.NodeRecovery = &autoNodeReceipt{State: "unavailable", Capability: "node_packages", Generation: 1, Unsupported: true, SourceJob: n.SourceJob, SourceSHA: n.SourceSHA, AdmissionSHA: n.AdmissionSHA, Reason: "no compatible runtime"}
	a.Jobs = append(a.Jobs, &clone)
	autoRefuteDiagnosisRemedy(a, &clone)
	if d.Outcome != "remedy_failed" || len(d.Failures) != 1 || d.Failures[0].NodeReceipt == nil || len(d.Reviews) != 1 {
		t.Fatal("trusted failure lost bounded correction and historical approval")
	}
	d.Failures[0].NodeReceipt.Reason = "mutated"
	if _, ok := autoDiagnosisRefutedCheckpoint(a, 900); ok {
		t.Fatal("mutated failure accepted")
	}
}
func TestNodeDiagnosisMixedCapabilitiesAndUnresolvedSiblings(t *testing.T) {
	for _, state := range []string{"pending", "unavailable", "unsupported"} {
		t.Run(state, func(t *testing.T) {
			s, a, _, target, reviewer := diagnosisReviewFixture(t)
			py := pythonRequirement()
			key := autoRequirementKey(py)
			target.RequirementIDs = append(target.RequirementIDs, key)
			a.Requirements[key] = &autoRequirement{Key: key, Request: py, State: state}
			raw := nodeDiagnosisReports(a, []autonomy.Requirement{nodeDiagnosisRequirement()})
			if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
				t.Fatal(err)
			}
			if state == "pending" {
				if target.RequirementHold || target.PendingPythonRequest == nil || target.PendingNodeRequest == nil {
					t.Fatal("mixed supported requests not retained")
				}
			} else if !target.RequirementHold || target.PendingNodeRequest != nil || target.PendingPythonRequest != nil {
				t.Fatal("unreviewed failed sibling released")
			}
		})
	}
	s, a, _, target, reviewer := diagnosisReviewFixture(t)
	raw := nodeDiagnosisReports(a, []autonomy.Requirement{pythonRequirement(), nodeDiagnosisRequirement()})
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil || target.PendingNodeRequest == nil || target.PendingPythonRequest == nil {
		t.Fatal("mixed reviewed recipe", err)
	}
}
func TestNodeDiagnosisLockedCorrectionAndConflictingReviewedRecipes(t *testing.T) {
	s, a, _, target, reviewer := diagnosisReviewFixture(t)
	target.NodeRequest = &autoNodeRequest{SchemaVersion: 1, Kind: "node_packages", PackageJSON: "wrong/package.json", PackageLock: "wrong/package-lock.json", Modules: []string{"old"}}
	before := store.J(target.NodeRequest)
	r := nodeDiagnosisRequirement()
	r.Requirements = nil
	r.PackageJSON = "tool/package.json"
	r.PackageLock = "tool/package-lock.json"
	raw := nodeDiagnosisReports(a, []autonomy.Requirement{r})
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
		t.Fatal(err)
	}
	if target.PendingNodeRequest == nil || target.PendingNodeRequest.PackageLock != r.PackageLock || store.J(target.NodeRequest) != before {
		t.Fatal("audited archived selector replacement failed")
	}
	other := r
	other.PackageJSON = "other/package.json"
	other.PackageLock = "other/package-lock.json"
	if _, _, err := autoDiagnosisRecipeFor([]autonomy.Requirement{r, other}); err == nil {
		t.Fatal("ambiguous reviewed lock recipes accepted")
	}
}
func TestNodeDiagnosisPreflightEvidenceIsOriginalArchivedProducer(t *testing.T) {
	s, a, p, source := requirementDiagnosisFixture(t)
	r := nodeDiagnosisRequirement()
	key := autoRequirementKey(r)
	p.DiagnoseRequirement = key
	a.State.Items = []autonomy.Proposal{p}
	source.RequirementIDs = []string{key}
	owner := *source
	owner.ID = "88888888-8888-4888-8888-888888888888"
	owner.NodeRequest = &autoNodeRequest{SourceJob: source.ID, SourceSHA: strings.Repeat("a", 64), AdmissionSHA: autoDiagnosisAdmission(source)}
	owner.NodeRecovery = &autoNodeReceipt{State: "unavailable", Capability: "node_packages", Generation: 1, Unsupported: true, SourceJob: source.ID, SourceSHA: owner.NodeRequest.SourceSHA, AdmissionSHA: owner.NodeRequest.AdmissionSHA}
	a.Requirements = map[string]*autoRequirement{key: {Key: key, Request: r, State: "unavailable", Occurrences: []autoRequirementOccurrence{{TaskID: owner.TaskID, ProjectID: p.ProjectID, JobID: owner.ID, EvidenceJob: source.ID, ArchiveSHA: owner.NodeRequest.SourceSHA}}}}
	a.Jobs = append(a.Jobs, &owner)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	d := autoSelectedDiagnosis(a, p)
	if d.TargetJob != owner.ID || d.TargetEvidenceJob != source.ID {
		t.Fatal("preflight owner conflated with evidence")
	}
	owner.NodeRecovery.SourceSHA = strings.Repeat("b", 64)
	if _, ok := autoDiagnosisOccurrence(a.Requirements[key], &owner); ok {
		t.Fatal("forged original source accepted")
	}
}

func nodeDiagnosisEnvironmentFixture(t *testing.T) (*autoRecord, *autoJob, *autoJob, autonomy.Proposal) {
	a, b, r, p := diagnosisEnvironmentFixture(t)
	n, err := autoNodeInputs(nodeDiagnosisRequirement())
	if err != nil {
		t.Fatal(err)
	}
	n.SourceJob = "source"
	n.SourceSHA = strings.Repeat("a", 64)
	n.AdmissionSHA = strings.Repeat("b", 64)
	receipt := &autoNodeReceipt{State: "verified", Capability: "node_packages", Generation: 1, InputKey: strings.Repeat("c", 64), BundleKey: strings.Repeat("d", 64), RuntimeDigest: strings.Repeat("e", 64), LockSHA: strings.Repeat("f", 64), ProbeSHA: strings.Repeat("1", 64), SourceJob: n.SourceJob, SourceSHA: n.SourceSHA, AdmissionSHA: n.AdmissionSHA}
	for _, j := range []*autoJob{b, r} {
		j.PythonRequest = nil
		j.PythonRecovery = nil
		j.PythonUsedBundle = ""
		j.NodeRequest = n
		j.NodeRecovery = receipt
		j.NodeUsedBundle = receipt.BundleKey
	}
	e, err := autoHistoricalVerifiedEnvironment(&autoRequirementDiagnosis{OriginTaskID: 300}, b, r, []autonomy.Requirement{nodeDiagnosisRequirement()})
	if err != nil {
		t.Fatal(err)
	}
	a.RequirementDiagnoses["origin"].VerifiedEnvironment = e
	return a, b, r, p
}
func TestNodeDiagnosisHistoricalEnvironmentRequiresExactActualUse(t *testing.T) {
	for _, bad := range []string{"unused", "lock", "generation", "source", "module", "pending"} {
		t.Run(bad, func(t *testing.T) {
			a, _, r, _ := nodeDiagnosisEnvironmentFixture(t)
			if len(autoVerifiedDiagnosisEnvironments(a)) != 1 {
				t.Fatal("valid actual environment absent")
			}
			receipt := *r.NodeRecovery
			r.NodeRecovery = &receipt
			request := *r.NodeRequest
			r.NodeRequest = &request
			switch bad {
			case "unused":
				r.NodeUsedBundle = ""
			case "lock":
				r.NodeRecovery.LockSHA = strings.Repeat("2", 64)
			case "generation":
				r.NodeRecovery.Generation++
			case "source":
				r.NodeRequest.SourceSHA = strings.Repeat("3", 64)
			case "module":
				r.NodeRequest.Modules = []string{"other"}
			case "pending":
				r.Status = "running"
			}
			if len(autoVerifiedDiagnosisEnvironments(a)) != 0 {
				t.Fatal("unproven environment advertised")
			}
		})
	}
}
func TestNodeDiagnosisEnvironmentSelectionReverifiesAndPreservesLaterRemedy(t *testing.T) {
	a, _, _, p := nodeDiagnosisEnvironmentFixture(t)
	if err := pinAutoDiagnosisEnvironments(a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	a.State.Phase = autonomy.Build
	yes := true
	a.State.Audits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes}, "auditor_b": {Approve: &yes}}
	j := &autoJob{Role: "builder", Status: "prepared"}
	if err := autoInheritPythonRequest(a, j); err != nil {
		t.Fatal(err)
	}
	if j.NodeRecovery != nil || j.NodeExpectedBundle == "" || j.NodeExpectedLock == "" || j.PythonRequest != nil {
		t.Fatal("selection forged actual runtime or omitted frozen identity")
	}
	raw := store.J(j.NodeRequest)
	if err := autoInheritPythonRequest(a, j); err != nil || store.J(j.NodeRequest) != raw {
		t.Fatal("nonidempotent selection", err)
	}
	changed := *j.NodeRequest
	changed.Requirements = []string{"debug@4.4.4"}
	j.NodeRequest = &changed
	j.NodeExpectedBundle = ""
	j.NodeExpectedInput = ""
	j.NodeExpectedLock = ""
	if err := autoInheritPythonRequest(a, j); err != nil || store.J(j.NodeRequest) != store.J(&changed) || j.NodeExpectedBundle != "" {
		t.Fatal("selected environment erased later explicit remedy", err)
	}
}

func TestNodeDiagnosisDoesNotReprovisionUnrelatedVerifiedPython(t *testing.T) {
	s, a, _, target, reviewer := diagnosisReviewFixture(t)
	target.PythonRequest = &autoPythonRequest{SchemaVersion: 1, Kind: "python_wheels", Requirements: []string{"pytest==9.1.1"}, Imports: []string{"pytest"}}
	target.PythonRecovery = &autoPythonReceipt{State: "verified", BundleKey: strings.Repeat("a", 64)}
	target.PythonUsedBundle = target.PythonRecovery.BundleKey
	before := store.J(target.PythonRequest)
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, nodeDiagnosisReports(a, []autonomy.Requirement{nodeDiagnosisRequirement()})); err != nil {
		t.Fatal(err)
	}
	if target.PendingPythonRequest != nil || target.PendingNodeRequest == nil || target.RequirementHold || store.J(target.PythonRequest) != before {
		t.Fatal("unrelated verified environment forced through changed-bundle gate")
	}
}

func TestNodeDiagnosisMixedHistoricalGateAndApproval(t *testing.T) {
	a, b, r, _ := nodeDiagnosisEnvironmentFixture(t)
	rs := []autonomy.Requirement{nodeDiagnosisRequirement(), pythonRequirement()}
	b.DiagnosisReservation = "origin"
	a.RequirementDiagnoses["origin"].EvidenceOnly = true
	if !autoHistoricalProvisioningGate(a, b, "ready_for_review", rs) {
		t.Fatal("missing Python half skipped real provisioning")
	}
	if _, err := autoHistoricalVerifiedEnvironment(a.RequirementDiagnoses["origin"], b, r, rs); err == nil {
		t.Fatal("partial mixed runtime advertised")
	}
	py, _, err := autoDiagnosisRecipeFor([]autonomy.Requirement{pythonRequirement()})
	if err != nil {
		t.Fatal(err)
	}
	py.Python.SourceJob = "source"
	py.Python.SourceSHA = strings.Repeat("a", 64)
	py.Python.AdmissionSHA = strings.Repeat("b", 64)
	receipt := &autoPythonReceipt{State: "verified", Capability: "python_wheels", InputKey: strings.Repeat("c", 64), BundleKey: strings.Repeat("d", 64), RuntimeDigest: strings.Repeat("e", 64), SourceJob: py.Python.SourceJob, SourceSHA: py.Python.SourceSHA, AdmissionSHA: py.Python.AdmissionSHA}
	for _, j := range []*autoJob{b, r} {
		j.PythonRequest = py.Python
		j.PythonRecovery = receipt
		j.PythonUsedBundle = receipt.BundleKey
	}
	e, err := autoHistoricalVerifiedEnvironment(a.RequirementDiagnoses["origin"], b, r, rs)
	if err != nil || e.Request == nil || e.NodeRequest == nil || autoHistoricalProvisioningGate(a, b, "ready_for_review", rs) {
		t.Fatal("complete independently used mixed environment unavailable", err)
	}
	a.RequirementDiagnoses["origin"].VerifiedEnvironment = e
	if len(autoVerifiedDiagnosisEnvironments(a)) != 1 {
		t.Fatal("mixed catalog omitted actual usage")
	}
}
