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

func requirementDiagnosisFixture(t *testing.T) (*Server, *autoRecord, autonomy.Proposal, *autoJob) {
	t.Helper()
	s, a, project, _ := repairFixture(t)
	task, err := s.DB.InsertTask(&store.Task{ProjectID: project, Title: "retained blocked builder", Status: "backlog", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	target := &autoJob{ID: "77777777-7777-4777-8777-777777777777", TaskID: task.ID, Role: "builder", Status: "deferred", RequirementHold: true, RepairAttemptTaskID: 123, Admission: &autoAdmission{TaskID: task.ID, Proposal: autonomy.Proposal{ProjectID: project, Acceptance: []string{"unchanged original acceptance"}}}}
	r := autonomy.Requirement{SchemaVersion: 1, Capability: "unknown_python_environment", Condition: "original_tests_execute", Evidence: []string{"retained import failed"}}
	key := autoRequirementKey(r)
	target.RequirementIDs = []string{key}
	a.Requirements = map[string]*autoRequirement{key: {Key: key, Request: r, State: "unsupported", Occurrences: []autoRequirementOccurrence{{TaskID: task.ID, ProjectID: project, JobID: target.ID, ArchiveSHA: strings.Repeat("a", 64)}}}}
	a.Jobs = append(a.Jobs, target)
	held, _ := autonomy.NewState("2026-09-24")
	held.Phase = autonomy.Build
	held.Items = []autonomy.Proposal{target.Admission.Proposal}
	held.Assignments = []autonomy.Assignment{{TaskID: task.ID, Role: "builder"}}
	a.DeferredRuns = []*autonomy.State{held}
	p := autonomy.Proposal{ProjectID: project, DiagnoseRequirement: key, Title: "diagnose exact missing prerequisite", Why: "supported alternative may exist", Acceptance: []string{"identify actual cause and evidence supported remedy"}, SourceRevision: strings.Repeat("b", 40)}
	a.State.Items = []autonomy.Proposal{p}
	a.State.Cycle = 44
	return s, a, p, target
}

func TestRequirementDiagnosisAdmissionEvidenceAndOneAllowance(t *testing.T) {
	s, a, p, target := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"auditor_a", "auditor_b", "builder"} {
		copies, err := autoDiagnosisCopies(a, role)
		if err != nil || len(copies) != 1 || copies[0].SourceJob != target.ID || copies[0].SHA != strings.Repeat("a", 64) {
			t.Fatal(role, copies, err)
		}
	}
	j := &autoJob{TaskID: 900, ID: "diagnosis", Role: "builder", Status: "prepared"}
	audits := a.State.Audits
	a.State.Audits = nil
	if err := s.reserveAutoRequirementDiagnosis(a, j); err == nil {
		t.Fatal("unaudited diagnosis admitted")
	}
	a.State.Audits = audits
	if err := s.reserveAutoRequirementDiagnosis(a, j); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveAutoRequirementDiagnosis(a, j); err != nil {
		t.Fatal("idempotent reservation", err)
	}
	other := *j
	other.TaskID++
	if err := s.reserveAutoRequirementDiagnosis(a, &other); err == nil {
		t.Fatal("second builder spent same allowance")
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	loaded.State.Cycle++
	if _, _, err = autoDiagnosisTarget(loaded, p); err == nil {
		t.Fatal("reload minted new diagnosis")
	}
	if autoSelectedDiagnosis(loaded, p).RootTaskID != 900 || !target.RequirementHold || target.PendingPythonRequest != nil {
		t.Fatal("reservation granted recovery")
	}
}

func TestRequirementDiagnosisRejectsInvalidAndUnboundSources(t *testing.T) {
	s, a, p, _ := requirementDiagnosisFixture(t)
	if err := autoValidateRequirementDiagnoses(a, []autonomy.Proposal{p, p}); err == nil {
		t.Fatal("duplicate allowance")
	}
	for _, change := range []func(*autonomy.Proposal){func(p *autonomy.Proposal) { p.ProjectID++ }, func(p *autonomy.Proposal) { p.DiagnoseRequirement = "bad" }, func(p *autonomy.Proposal) { p.RepairTaskID = 1 }, func(p *autonomy.Proposal) { p.ContinueTaskID = 1 }, func(p *autonomy.Proposal) { p.DocumentationTaskID = 1 }} {
		other := p
		change(&other)
		if _, _, err := autoDiagnosisTarget(a, other); err == nil {
			t.Fatal("invalid selection", other)
		}
	}
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("c", 64)})
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err == nil {
		t.Fatal("changed original evidence pinned")
	}
}

func diagnosisReviewFixture(t *testing.T) (*Server, *autoRecord, autonomy.Proposal, *autoJob, *autoJob) {
	s, a, p, target := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	builder := &autoJob{TaskID: 900, ID: "builder", Role: "builder", Status: "done"}
	if err := s.reserveAutoRequirementDiagnosis(a, builder); err != nil {
		t.Fatal(err)
	}
	a.Jobs = append(a.Jobs, builder)
	a.State.Phase = autonomy.Review
	a.State.Reports[900] = json.RawMessage(store.J(autonomy.BuildReport{Outcome: "ready_for_review", Summary: "cause identified", Evidence: []string{"exact source and dependency metadata"}, Requirements: []autonomy.Requirement{pythonRequirement()}}))
	reviewer := &autoJob{TaskID: 901, ID: "reviewer", Role: "reviewer", Status: "exporting"}
	return s, a, p, target, reviewer
}

func TestRequirementDiagnosisReviewedRemedyResumesOriginalOnlyThroughProvisioning(t *testing.T) {
	s, a, p, target, reviewer := diagnosisReviewFixture(t)
	originalAdmission := store.J(target.Admission)
	originalHeld := store.J(a.DeferredRuns[0])
	originalRejected := store.J(a.Jobs[0])
	yes := true
	missing := []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "verified diagnosis"}))
	var reportErr *autoReportError
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, missing); !errors.As(err, &reportErr) {
		t.Fatal("missing reviewer remedy confirmation was not correctable", err)
	}
	if !target.RequirementHold || target.PendingPythonRequest != nil {
		t.Fatal("unconfirmed recovery")
	}
	valid := []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "independently inspected source and exact metadata", Requirements: []autonomy.Requirement{pythonRequirement()}}))
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, valid); err != nil {
		t.Fatal(err)
	}
	d := autoSelectedDiagnosis(a, p)
	if d.Outcome != "completed" || d.ReviewReportSHA != autoSHA(valid) || d.ReviewSHA == "" {
		t.Fatal("missing durable review proof")
	}
	if target.RequirementHold || target.PendingPythonRequest == nil || target.PythonRecovery != nil || target.Status != "deferred" || a.Requirements[d.Key].State != "pending" {
		t.Fatal("review fabricated verification or lost pending remediation")
	}
	if target.PendingPythonRequest.SourceJob != target.ID || target.PendingPythonRequest.SourceSHA != d.TargetArchiveSHA || store.J(target.Admission) != originalAdmission || target.RepairAttemptTaskID != 123 || store.J(a.DeferredRuns[0]) != originalHeld || store.J(a.Jobs[0]) != originalRejected {
		t.Fatal("diagnosis changed original scope/lineage/history")
	}
	if !autoResumeRecovered(a) || target.Status != "stopped" || a.State.Assignments[0].TaskID != target.TaskID {
		t.Fatal("reviewed remedy failed to reselect retained assignment")
	}
	clone := *target
	autoPreparePythonResume(target, &clone)
	if clone.TaskID != target.TaskID || clone.PythonRequest == nil || clone.PythonRecovery != nil || !clone.PythonNeedsChange {
		t.Fatal("new worker skipped changed-environment verification")
	}
}

func TestRequirementDiagnosisRejectAndSiblingBlockersRemainHeld(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		s, a, p, target, reviewer := diagnosisReviewFixture(t)
		no := false
		raw := []byte(store.J(autonomy.Verdict{Outcome: "incomplete", Approve: &no, Reason: "unsupported conclusion"}))
		if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
			t.Fatal(err)
		}
		if !target.RequirementHold || target.PendingPythonRequest != nil || autoSelectedDiagnosis(a, p).Outcome != "rejected" {
			t.Fatal("rejected diagnosis released work")
		}
	})
	t.Run("sibling", func(t *testing.T) {
		s, a, _, target, reviewer := diagnosisReviewFixture(t)
		other := autonomy.Requirement{SchemaVersion: 1, Capability: "external_fixture", Condition: "fixture_available", Evidence: []string{"missing independent fixture"}}
		key := autoRequirementKey(other)
		a.Requirements[key] = &autoRequirement{Key: key, Request: other, State: "unsupported"}
		target.RequirementIDs = append(target.RequirementIDs, key)
		yes := true
		raw := []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "one dependency understood", Requirements: []autonomy.Requirement{pythonRequirement()}}))
		if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
			t.Fatal(err)
		}
		if !target.RequirementHold || target.PendingPythonRequest != nil {
			t.Fatal("one diagnosis erased sibling blocker")
		}
	})
}

func TestRequirementDiagnosisPreparedOwnerUsesOriginalEvidence(t *testing.T) {
	s, a, p, source := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	owner := *source
	owner.ID = "88888888-8888-4888-8888-888888888888"
	owner.PythonRequest = &autoPythonRequest{SourceJob: source.ID, SourceSHA: strings.Repeat("a", 64), AdmissionSHA: autoDiagnosisAdmission(source)}
	owner.PythonRecovery = &autoPythonReceipt{State: "unavailable", Capability: "python_wheels", Unsupported: true, SourceJob: owner.PythonRequest.SourceJob, SourceSHA: owner.PythonRequest.SourceSHA, AdmissionSHA: owner.PythonRequest.AdmissionSHA}
	a.Jobs = append(a.Jobs, &owner)
	sealed := store.J(owner.PythonRequest)
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	d := autoSelectedDiagnosis(a, p)
	if d.TargetJob != owner.ID || d.TargetEvidenceJob != source.ID {
		t.Fatal("owner/evidence conflated", d)
	}
	copies, err := autoDiagnosisCopies(a, "auditor_a")
	if err != nil || copies[0].SourceJob != source.ID {
		t.Fatal(copies, err)
	}
	p.Acceptance[0] = "mutated after pin"
	if d.Proposal.Acceptance[0] == p.Acceptance[0] {
		t.Fatal("audited acceptance aliased")
	}
	if store.J(owner.PythonRequest) != sealed {
		t.Fatal("sealed request mutated")
	}
}

func TestRequirementDiagnosisBoundedRepairKeepsReservationAndHistory(t *testing.T) {
	s, a, p, target := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	original := store.J(target)
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	var prior *autoJob
	var firstReview string
	var root int64
	for attempt := 0; attempt <= a.Config.MaxRevisionRounds; attempt++ {
		if attempt > 0 {
			p.RepairTaskID = prior.TaskID
			p.SourceRevision = ""
			stripped := p
			stripped.DiagnoseRequirement = ""
			if err := s.validateAutoSources(a, []autonomy.Proposal{stripped}); err == nil {
				t.Fatal("stripped diagnosis")
			}
			if err := s.validateAutoSources(a, []autonomy.Proposal{p}); err != nil {
				t.Fatal(attempt, err)
			}
			if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
				t.Fatal(err)
			}
		}
		a.State.Items = []autonomy.Proposal{p}
		a.State.Phase = autonomy.Build
		task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "diagnose", Status: "backlog", CreatedBy: autoOwner})
		if err != nil {
			t.Fatal(err)
		}
		builder := &autoJob{TaskID: task.ID, ID: task.Title + string(rune('a'+attempt)), Role: "builder", Status: "done", RepairSourceTaskID: p.RepairTaskID}
		if p.RepairTaskID > 0 {
			builder.RepairAttemptTaskID = task.ID
		}
		if err := s.reserveAutoRequirementDiagnosis(a, builder); err != nil {
			t.Fatal(attempt, err)
		}
		a.Jobs = append(a.Jobs, builder)
		d := autoSelectedDiagnosis(a, p)
		if attempt == 0 {
			root = d.RootTaskID
		}
		if d.RootTaskID != root || autoRepairRoot(a, builder.TaskID) != root {
			t.Fatal("reset root")
		}
		a.State.Phase = autonomy.Review
		a.State.Reports[builder.TaskID] = json.RawMessage(store.J(autonomy.BuildReport{Outcome: "ready_for_review", Summary: "diagnosis", Evidence: []string{"evidence"}}))
		rt, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "review", Status: "done", CreatedBy: autoOwner})
		if err != nil {
			t.Fatal(err)
		}
		reviewer := &autoJob{TaskID: rt.ID, ID: "review" + string(rune('a'+attempt)), Role: "reviewer", Status: "done"}
		a.Jobs = append(a.Jobs, reviewer)
		no := false
		raw := []byte(store.J(autonomy.Verdict{Outcome: "incomplete", Approve: &no, Reason: "diagnostic implementation error"}))
		if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
			t.Fatal(err)
		}
		builder.Rejected = true
		builder.ReviewTaskID = reviewer.TaskID
		builder.ReviewReason = "diagnostic implementation error"
		if attempt == 0 {
			firstReview = store.J(d.Reviews[0])
		}
		if len(d.Reviews) != attempt+1 || store.J(d.Reviews[0]) != firstReview {
			t.Fatal("review evidence overwritten")
		}
		prior = builder
		a.State.Cycle++
	}
	p.RepairTaskID = prior.TaskID
	if err := s.validateAutoSources(a, []autonomy.Proposal{p}); err == nil {
		t.Fatal("repair cap reset")
	}
	prior.Approved = true
	if autoCheckpointApproved(a, prior.TaskID) {
		t.Fatal("diagnosis promoted to implementation")
	}
	if store.J(target) != original {
		t.Fatal("original retained assignment changed")
	}
}

func TestRequirementDiagnosisAllowanceScopedToRetainedAssignment(t *testing.T) {
	s, a, p, first := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveAutoRequirementDiagnosis(a, &autoJob{TaskID: 900, Role: "builder"}); err != nil {
		t.Fatal(err)
	}
	original := store.J(a.RequirementDiagnoses[autoDiagnosisID(p.DiagnoseRequirement, first.TaskID)])
	// Another assignment with the identical grouped requirement gets its own
	// reservation even within the same project, without resetting the first.
	task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "other retained task", Status: "backlog", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	second := *first
	second.TaskID = task.ID
	second.ID = "99999999-9999-4999-8999-999999999999"
	a.Jobs = append(a.Jobs, &second)
	r := a.Requirements[p.DiagnoseRequirement]
	r.Occurrences = append(r.Occurrences, autoRequirementOccurrence{TaskID: task.ID, ProjectID: p.ProjectID, JobID: second.ID, ArchiveSHA: strings.Repeat("a", 64)})
	a.State.Cycle++
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveAutoRequirementDiagnosis(a, &autoJob{TaskID: 901, Role: "builder"}); err != nil {
		t.Fatal(err)
	}
	if len(autoRequirementDiagnoses(a, p.DiagnoseRequirement)) != 2 || store.J(a.RequirementDiagnoses[autoDiagnosisID(p.DiagnoseRequirement, first.TaskID)]) != original {
		t.Fatal("target reservation conflated")
	}
	a.State.Cycle++
	if _, _, err := autoDiagnosisTarget(a, p); err == nil {
		t.Fatal("consumed target allowance reset")
	}
}

func TestRequirementDiagnosisReviewedPinReplacementPreservesOldRequest(t *testing.T) {
	_, a, p, target := requirementDiagnosisFixture(t)
	target.PythonRequest = &autoPythonRequest{Requirements: []string{"django==5.2.12", "asgiref==3.9.0"}, Imports: []string{"django"}}
	original := store.J(target.PythonRequest)
	a.RequirementDiagnoses = map[string]*autoRequirementDiagnosis{autoDiagnosisID(p.DiagnoseRequirement, target.TaskID): {Key: p.DiagnoseRequirement, TargetTaskID: target.TaskID, TargetJob: target.ID, Outcome: "completed", Remedy: &autoPythonRequest{Requirements: []string{"django==5.2.13"}, Imports: []string{"django"}, SourceJob: target.ID}}}
	request, ok := autoDiagnosisCombinedRequest(a, target)
	if !ok || store.J(request.Requirements) != store.J([]string{"asgiref==3.9.0", "django==5.2.13"}) || store.J(target.PythonRequest) != original {
		t.Fatal(request, ok, "replacement lost old provenance or unrelated pin")
	}
}

func TestRequirementDiagnosisCompletedWorkerPrefersItsOwnOccurrence(t *testing.T) {
	s, a, p, current := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	current.PythonRequest = &autoPythonRequest{SourceJob: "older-producer", SourceSHA: strings.Repeat("b", 64), AdmissionSHA: strings.Repeat("c", 64)}
	current.PythonRecovery = &autoPythonReceipt{State: "verified", Capability: "python_wheels"}
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	d := autoSelectedDiagnosis(a, p)
	if d.TargetEvidenceJob != current.ID || d.TargetArchiveSHA != strings.Repeat("a", 64) || d.TargetAdmissionSHA != autoSHA([]byte(store.J(current.Admission))) {
		t.Fatal("completed worker evidence replaced with inherited environment source", d)
	}
	// Missing current evidence cannot be laundered into the previous producer.
	r := a.Requirements[p.DiagnoseRequirement]
	r.Occurrences[0].JobID = "older-producer"
	r.Occurrences[0].ArchiveSHA = strings.Repeat("b", 64)
	if _, _, err := autoDiagnosisTarget(a, p); err == nil {
		t.Fatal("launched worker borrowed unrelated old evidence")
	}
}

func TestRequirementDiagnosisInheritedConsumerUsesTrustedPreflightOccurrence(t *testing.T) {
	s, a, p, source := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "inherited reviewer", Status: "backlog", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	consumer := *source
	consumer.TaskID = task.ID
	consumer.ID = "88888888-8888-4888-8888-888888888888"
	consumer.Role = "reviewer"
	consumer.Admission = nil
	consumer.RequirementIDs = nil
	consumer.PythonRequest = &autoPythonRequest{Requirements: []string{"django==5.2.13"}, Imports: []string{"django"}, SourceJob: source.ID, SourceSHA: strings.Repeat("a", 64), AdmissionSHA: autoDiagnosisAdmission(source)}
	consumer.PythonRecovery = &autoPythonReceipt{State: "unavailable", Unsupported: true, Capability: "python_wheels", SourceJob: source.ID, SourceSHA: consumer.PythonRequest.SourceSHA, AdmissionSHA: consumer.PythonRequest.AdmissionSHA}
	a.Jobs = append(a.Jobs, &consumer)
	if err := s.recordAutoProvisionerRequirement(a, &consumer); err != nil {
		t.Fatal(err)
	}
	p.DiagnoseRequirement = consumer.RequirementIDs[0]
	a.State.Items = []autonomy.Proposal{p}
	r := a.Requirements[p.DiagnoseRequirement]
	if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	d := autoSelectedDiagnosis(a, p)
	if d.TargetTaskID != consumer.TaskID || d.TargetJob != consumer.ID || d.TargetEvidenceJob != source.ID {
		t.Fatal("consumer lost exact source binding", d)
	}
	consumer.PythonRecovery.SourceSHA = strings.Repeat("b", 64)
	if _, ok := autoDiagnosisOccurrence(r, &consumer); ok {
		t.Fatal("unbound preflight accepted borrowed evidence")
	}
}

func TestRequirementDiagnosisFailedProvisioningKeepsApprovalAndBoundedCorrection(t *testing.T) {
	s, a, p, target := requirementDiagnosisFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	originalAdmission := store.J(target.Admission)
	originalRootAttempt := target.RepairAttemptTaskID
	var root int64
	var firstReview string
	var previous *autoJob
	for attempt := 0; attempt <= a.Config.MaxRevisionRounds; attempt++ {
		a.State.Phase = autonomy.Build
		a.State.Cycle++
		if previous != nil {
			p.RepairTaskID = previous.TaskID
			p.SourceRevision = ""
		}
		a.State.Items = []autonomy.Proposal{p}
		if err := s.validateAutoSources(a, []autonomy.Proposal{p}); err != nil {
			t.Fatal(attempt, err)
		}
		if err := s.pinAutoRequirementDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err != nil {
			t.Fatal(err)
		}
		if attempt > 0 {
			for _, role := range []string{"auditor_a", "auditor_b"} {
				copies, err := autoDiagnosisCopies(a, role)
				if err != nil || len(copies) != 3 {
					t.Fatal("repair audit snapshots incomplete", role, copies, err)
				}
			}
		}
		task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "diagnosis", Status: "done", CreatedBy: autoOwner})
		if err != nil {
			t.Fatal(err)
		}
		builder := &autoJob{TaskID: task.ID, ID: "diagnosis" + string(rune('a'+attempt)), Role: "builder", Status: "done", RepairSourceTaskID: p.RepairTaskID}
		if p.RepairTaskID > 0 {
			builder.RepairAttemptTaskID = task.ID
		}
		if err := s.reserveAutoRequirementDiagnosis(a, builder); err != nil {
			t.Fatal(err)
		}
		a.Jobs = append(a.Jobs, builder)
		d := autoSelectedDiagnosis(a, p)
		if attempt == 0 {
			root = d.RootTaskID
		}
		if d.RootTaskID != root {
			t.Fatal("reset root")
		}
		a.State.Phase = autonomy.Review
		a.State.Reports[builder.TaskID] = json.RawMessage(store.J(autonomy.BuildReport{Outcome: "ready_for_review", Summary: "proposed remedy", Evidence: []string{"investigation"}, Requirements: []autonomy.Requirement{pythonRequirement()}}))
		rt, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "approved diagnostic review", Status: "done", CreatedBy: autoOwner})
		if err != nil {
			t.Fatal(err)
		}
		reviewer := &autoJob{TaskID: rt.ID, ID: "review" + string(rune('a'+attempt)), Role: "reviewer", Status: "done"}
		a.Jobs = append(a.Jobs, reviewer)
		yes := true
		raw := []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "independently supported proposal", Requirements: []autonomy.Requirement{pythonRequirement()}}))
		if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, raw); err != nil {
			t.Fatal(err)
		}
		builder.Approved = true
		builder.ReviewTaskID = reviewer.TaskID
		builder.ReviewOutcome = "completed"
		historicalBuilder := store.J(builder)
		if attempt == 0 {
			firstReview = store.J(d.Reviews[0])
		}
		if d.AttemptedRequest == nil || target.PendingPythonRequest == nil {
			t.Fatal("release lacked attempted binding")
		}
		next := *target
		autoPreparePythonResume(target, &next)
		next.ID = "preflight" + string(rune('a'+attempt))
		next.Status = "deferred"
		next.PythonRecovery = &autoPythonReceipt{State: "unavailable", Capability: "python_wheels", Unsupported: true, Reason: "actual registry has no compatible wheel", SourceJob: next.PythonRequest.SourceJob, SourceSHA: next.PythonRequest.SourceSHA, AdmissionSHA: next.PythonRequest.AdmissionSHA}
		next.RequirementHold = true
		a.Jobs = append(a.Jobs, &next)
		// Unrelated and previously verified attempts cannot refute this remedy.
		unrelated := next
		request := *next.PythonRequest
		request.Requirements = []string{"other==1.0"}
		unrelated.PythonRequest = &request
		autoRefuteDiagnosisRemedy(a, &unrelated)
		verified := next
		verified.PythonNeedsChange = false
		autoRefuteDiagnosisRemedy(a, &verified)
		if d.Outcome != "completed" {
			t.Fatal("unrelated attempt refuted diagnosis")
		}
		if err := s.recordAutoProvisionerRequirement(a, &next); err != nil {
			t.Fatal(err)
		}
		if d.Outcome != "remedy_failed" || len(d.Failures) != attempt+1 || d.RootTaskID != root || d.TargetJob != next.ID {
			t.Fatal("failed provisioner lost correction binding", d)
		}
		if store.J(builder) != historicalBuilder || builder.Rejected || store.J(d.Reviews[0]) != firstReview {
			t.Fatal("historical approval rewritten")
		}
		if _, _, rejected := autoRejectedCheckpoint(a, builder.TaskID); rejected {
			t.Fatal("invented reviewer rejection")
		}
		if _, ok := autoDiagnosisRefutedCheckpoint(a, builder.TaskID); !ok {
			t.Fatal("trusted refutation unavailable")
		}
		saved := d.Failures[len(d.Failures)-1]
		d.Failures[len(d.Failures)-1].Receipt.Reason = "changed receipt"
		if _, ok := autoDiagnosisRefutedCheckpoint(a, builder.TaskID); ok {
			t.Fatal("altered failure proof admitted")
		}
		d.Failures[len(d.Failures)-1] = saved
		if store.J(target.Admission) != originalAdmission || next.RepairAttemptTaskID != originalRootAttempt {
			t.Fatal("original admission/repair budget changed")
		}
		target = &next
		previous = builder
	}
	p.RepairTaskID = previous.TaskID
	p.SourceRevision = ""
	a.State.Cycle++
	a.State.Phase = autonomy.Build
	a.State.Items = []autonomy.Proposal{p}
	if err := s.validateAutoSources(a, []autonomy.Proposal{p}); err == nil {
		t.Fatal("failed remedies reset repair cap")
	}
}
