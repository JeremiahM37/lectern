package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func historicalFixture(t *testing.T, role string) (*Server, *autoRecord, autonomy.Proposal, *autoJob, string) {
	t.Helper()
	s, project, _ := sourceFixture(t)
	sourceTask, err := s.DB.InsertTask(&store.Task{ProjectID: project.ID, Title: "historical report", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := autonomy.NewState("2026-09-26")
	state.Phase = autonomy.Plan
	source := &autoJob{TaskID: sourceTask.ID, ID: "11111111-1111-4111-8111-111111111111", Role: role, Status: "done", Rejected: role == "builder", ReviewReason: "old outcome must remain"}
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state, Jobs: []*autoJob{source}, ProjectID: project.ID}
	a.Config.Enabled = true
	p := autonomy.Proposal{ProjectID: project.ID, DiagnoseTaskID: source.TaskID, Title: "investigate archived prerequisite", Why: "test a supported isolated remedy", Acceptance: []string{"actually test proposed environment and independently reproduce result"}}
	a.State.Items = []autonomy.Proposal{p}
	dir := t.TempDir()
	base := filepath.Join(dir, "archive")
	report := filepath.Join(dir, "report")
	t.Setenv("LECTERN_DOC_RESPONSE", base)
	t.Setenv("LECTERN_HIST_REPORT", report)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	script := "#!/bin/sh\ncase \"$3\" in\narchive-report) cat \"$LECTERN_HIST_REPORT\";;\nreport) echo 'mutable work report must never be read' >&2; exit 1;;\n*) cat \"$LECTERN_DOC_RESPONSE\";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	docResponse(t, base, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	raw := "{\n  \"summary\": \"Historical dependency claim; no exact package inferred\",\n  \"outcome\": \"blocked\"\n}\n"
	docResponse(t, report, map[string]string{"state": "ready", "archive_sha256": strings.Repeat("a", 64), "report_sha256": autoSHA([]byte(raw)), "report": raw})
	return s, a, p, source, report
}
func admitHistorical(t *testing.T, s *Server, a *autoRecord, p autonomy.Proposal) (*autoJob, autonomy.Proposal) {
	t.Helper()
	items := []autonomy.Proposal{p}
	if err := s.pinAutoSources(context.Background(), a, items); err != nil {
		t.Fatal(err)
	}
	p = items[0]
	a.State.Items = items
	task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "new isolated diagnosis", Status: "backlog", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	j := &autoJob{TaskID: task.ID, ID: "22222222-2222-4222-8222-222222222222", Role: "builder", Status: "exporting"}
	a.State.Phase = autonomy.Build
	if err := s.reserveAutoRequirementDiagnosis(a, j); err == nil {
		t.Fatal("diagnosis skipped plan audits")
	}
	yes := true
	a.State.Audits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes}, "auditor_b": {Approve: &yes}}
	j.Admission = autoNewAdmission(a, j)
	if err := s.reserveAutoRequirementDiagnosis(a, j); err != nil {
		t.Fatal(err)
	}
	a.Jobs = append(a.Jobs, j)
	return j, p
}

func TestHistoricalDiagnosisExactArchiveAndCanonicalRoot(t *testing.T) {
	s, a, p, source, report := historicalFixture(t, "builder")
	before := store.J(source)
	builder, p := admitHistorical(t, s, a, p)
	d := a.RequirementDiagnoses[builder.DiagnosisReservation]
	if !d.EvidenceOnly || d.OriginTaskID != source.TaskID || d.OriginRootTaskID != source.TaskID || d.OriginReportSHA != autoSHA([]byte(d.OriginReport)) || !strings.Contains(d.OriginReport, "\n") || len(a.Requirements[d.Key].Request.Requirements) != 0 {
		t.Fatal("historical report relabeled or package inferred", d)
	}
	for _, role := range []string{"auditor_a", "auditor_b"} {
		copies, err := autoDiagnosisCopies(a, role)
		if err != nil || len(copies) != 1 || copies[0].SourceJob != source.ID {
			t.Fatal(copies, err)
		}
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	persisted := loaded.RequirementDiagnoses[builder.DiagnosisReservation]
	if persisted.OriginReport != d.OriginReport || persisted.OriginReportSHA != autoSHA([]byte(persisted.OriginReport)) {
		t.Fatal("original report bytes changed on persistence")
	}
	// A different checkpoint from the same old repair root cannot mint a second allowance.
	task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "old repair checkpoint", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	alternate := *source
	alternate.ID = "33333333-3333-4333-8333-333333333333"
	alternate.TaskID = task.ID
	alternate.RepairSourceTaskID = source.TaskID
	a.Jobs = append(a.Jobs, &alternate)
	a.State.Cycle++
	a.State.Phase = autonomy.Plan
	other := p
	other.DiagnoseTaskID = alternate.TaskID
	other.DiagnoseRequirement = ""
	if err := s.pinAutoSources(context.Background(), a, []autonomy.Proposal{other}); err == nil {
		t.Fatal("alternate checkpoint reset historical allowance")
	}
	if store.J(source) != before {
		t.Fatal("historical task changed")
	}
	// No live work report can replace the archived observation.
	bad := map[string]string{"state": "ready", "archive_sha256": strings.Repeat("b", 64), "report_sha256": autoSHA([]byte("{}")), "report": "{}"}
	docResponse(t, report, bad)
	if _, err := s.autoHistoricalReport(context.Background(), a, source, strings.Repeat("a", 64)); err == nil {
		t.Fatal("report bound to wrong archive")
	}
}

func TestHistoricalPlannerUsesActualSnapshotEligibleProject(t *testing.T) {
	s, a, p, source, _ := historicalFixture(t, "planner")
	if _, err := s.autoSourceProject(p.ProjectID); err != nil {
		t.Fatal("planner diagnosis not snapshot eligible", err)
	}
	wrong := p
	wrong.ProjectID++
	if err := s.observeAutoHistoricalDiagnoses(context.Background(), a, []autonomy.Proposal{wrong}); err == nil {
		t.Fatal("planner backlog project invented")
	}
	_, p = admitHistorical(t, s, a, p)
	if p.ProjectID != a.ProjectID || autoSelectedDiagnosis(a, p).OriginTaskID != source.TaskID {
		t.Fatal("planner evidence project changed")
	}
}

func TestHistoricalReportPendingRetainsOwnershipAndOffStopsIt(t *testing.T) {
	s, a, p, source, report := historicalFixture(t, "planner")
	docResponse(t, report, map[string]string{"state": "waiting"})
	err := s.observeAutoHistoricalDiagnoses(context.Background(), a, []autonomy.Proposal{p})
	if !errors.Is(err, errAutoArtifactPending) || !a.HistoricalReportPending[source.ID] {
		t.Fatal("pending read lost source ownership", err)
	}
	loaded, err := s.loadAuto()
	if err != nil || !loaded.HistoricalReportPending[source.ID] {
		t.Fatal("reader intent not durable", err)
	}
	a.Config.Enabled = false
	s.stopAutoJobs(context.Background(), a, "off")
	if len(a.HistoricalReportPending) != 0 || source.Status != "done" {
		t.Fatal("OFF failed reader stop or changed historical worker")
	}
}

func TestHistoricalReadyRecipeActuallyProvisionsThenIndependentReviewAndConsumer(t *testing.T) {
	s, a, p, source, _ := historicalFixture(t, "builder")
	original := store.J(source)
	builder, p := admitHistorical(t, s, a, p)
	admission := store.J(builder.Admission)
	root := a.RequirementDiagnoses[builder.DiagnosisReservation].RootTaskID
	report := []byte(store.J(autonomy.BuildReport{Outcome: "ready_for_review", Summary: "proposed typed remedy", Evidence: []string{"inspect exact archived source"}, Requirements: []autonomy.Requirement{pythonRequirement()}}))
	state := a.State
	intercepted, err := s.recordAutoRequirements(context.Background(), a, builder, report)
	if err != nil || !intercepted || builder.PendingPythonRequest == nil || builder.Status != "stopped" || a.State != state {
		t.Fatal("pins-only diagnosis advanced before actual capability", intercepted, err)
	}
	next := *builder
	autoPreparePythonResume(builder, &next)
	next.ID = "44444444-4444-4444-8444-444444444444"
	next.Status = "prepared"
	a.Jobs = append(a.Jobs, &next)
	receipt := autoPythonReceipt{State: "verified", Capability: "python_wheels", InputKey: strings.Repeat("c", 64), BundleKey: strings.Repeat("d", 64), RuntimeDigest: strings.Repeat("e", 64), SourceJob: next.PythonRequest.SourceJob, SourceSHA: next.PythonRequest.SourceSHA, AdmissionSHA: next.PythonRequest.AdmissionSHA}
	if ready, err := autoApplyPythonReceipt(a, &next, []byte(store.J(receipt))); err != nil || !ready {
		t.Fatal(ready, err)
	}
	next.PythonUsedBundle = receipt.BundleKey
	next.Status = "done"
	if intercepted, err = s.recordAutoRequirements(context.Background(), a, &next, report); err != nil || intercepted {
		t.Fatal("verified diagnosis cannot reach review", intercepted, err)
	}
	a.State.Phase = autonomy.Review
	a.State.Reports[next.TaskID] = json.RawMessage(report)
	a.State.Assignments = []autonomy.Assignment{{TaskID: next.TaskID, Role: "builder", Completed: true}}
	rt, err := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "independent runtime review", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := &autoJob{TaskID: rt.ID, ID: "55555555-5555-4555-8555-555555555555", Role: "reviewer", Status: "done"}
	if err := autoInheritPythonRequest(a, reviewer); err != nil {
		t.Fatal(err)
	}
	yes := true
	verdict := []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "independently reproduced actual runtime tests", Requirements: []autonomy.Requirement{pythonRequirement()}}))
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, verdict); err == nil {
		t.Fatal("unused reviewer environment accepted")
	}
	if ready, err := autoApplyPythonReceipt(a, reviewer, []byte(store.J(receipt))); err != nil || !ready {
		t.Fatal(ready, err)
	}
	reviewer.PythonUsedBundle = receipt.BundleKey
	a.Jobs = append(a.Jobs, reviewer)
	if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, verdict); err != nil {
		t.Fatal(err)
	}
	// Normal final-review completion publishes these fields; before that, catalog stays closed.
	if len(autoVerifiedDiagnosisEnvironments(a)) != 0 {
		t.Fatal("environment visible before normal review admission")
	}
	next.Approved = true
	next.ReviewTaskID = reviewer.TaskID
	next.ReviewOutcome = "completed"
	if len(autoVerifiedDiagnosisEnvironments(a)) != 1 {
		t.Fatal("verified diagnosis orphaned from consumer catalog")
	}
	if store.J(source) != original || store.J(next.Admission) != admission || a.RequirementDiagnoses[next.DiagnosisReservation].RootTaskID != root {
		t.Fatal("historical outcome or diagnosis admission changed")
	}
	consumerProposal := autonomy.Proposal{ProjectID: p.ProjectID, EnvironmentDiagnosisTaskID: next.TaskID, SourceRevision: p.SourceRevision, Title: "separately audited eligible work", Why: "use actual verified environment", Acceptance: []string{"test this distinct admitted source"}}
	a.State.Phase = autonomy.Plan
	a.State.Items = []autonomy.Proposal{consumerProposal}
	if err := pinAutoDiagnosisEnvironments(a, a.State.Items); err != nil {
		t.Fatal(err)
	}
	a.State.Phase = autonomy.Build
	consumer := &autoJob{Role: "builder", TaskID: 999, ID: "consumer"}
	if err := autoInheritPythonRequest(a, consumer); err != nil {
		t.Fatal(err)
	}
	if consumer.PythonRecovery != nil || consumer.PythonExpectedInput != receipt.InputKey || consumer.PythonExpectedBundle != receipt.BundleKey || store.J(consumer.PythonRequest) != store.J(next.PythonRequest) {
		t.Fatal("consumer skipped exact environment re-verification")
	}
	// Reusing the environment cannot turn the original rejected task into an approved continuation.
	consumerProposal.ContinueTaskID = source.TaskID
	consumerProposal.SourceRevision = ""
	if err := s.validateAutoSources(a, []autonomy.Proposal{consumerProposal}); err == nil {
		t.Fatal("environment laundered rejected implementation")
	}
}

func TestHistoricalBlockedRefinementReplacesCompleteRecipe(t *testing.T) {
	s, a, p, _, _ := historicalFixture(t, "builder")
	builder, _ := admitHistorical(t, s, a, p)
	builder.PythonRequest = &autoPythonRequest{Requirements: []string{"django==4.2.0"}, Imports: []string{"obsolete_module"}}
	raw := []byte(store.J(autonomy.BuildReport{Outcome: "blocked", Summary: "older test recipe incompatible", Evidence: []string{"actual isolated import error"}, Requirements: []autonomy.Requirement{pythonRequirement()}}))
	if intercepted, err := s.recordAutoRequirements(context.Background(), a, builder, raw); err != nil || !intercepted || builder.RequirementHold || builder.PendingPythonRequest == nil {
		t.Fatal("supported refinement became unsupported", intercepted, err)
	}
	if store.J(builder.PendingPythonRequest.Requirements) != store.J(pythonRequirement().Requirements) || store.J(builder.PendingPythonRequest.Imports) != store.J([]string{"django"}) {
		t.Fatal("obsolete pins/imports poisoned refined recipe")
	}
}

func TestHistoricalTerminalFailedMalformedReportIsEvidenceOnly(t *testing.T) {
	s, a, p, source, path := historicalFixture(t, "builder")
	source.Status = "failed"
	source.ReportRepairs = 2
	source.ReportError = "exhausted report schema correction"
	malformed := "{broken report after two attempts\n"
	docResponse(t, path, map[string]string{"state": "ready", "archive_sha256": strings.Repeat("a", 64), "report_sha256": autoSHA([]byte(malformed)), "report": malformed})
	before := store.J(source)
	builder, p := admitHistorical(t, s, a, p)
	d := a.RequirementDiagnoses[builder.DiagnosisReservation]
	if !d.EvidenceOnly || d.OriginReportFormat != "malformed_text" || d.OriginReport != malformed || source.Status != "failed" || store.J(source) != before {
		t.Fatal("failed history relabeled", d)
	}
	if d.RootTaskID == source.TaskID || d.OriginTaskID != source.TaskID {
		t.Fatal("old task resurrected")
	}
}

func TestHistoricalDoesNotAdmitActiveOrSupersededFailedOwner(t *testing.T) {
	s, a, p, source, _ := historicalFixture(t, "builder")
	source.Status = "failed"
	source.ReportRepairs = 2
	a.State.Phase = autonomy.Paused
	a.State.Assignments = []autonomy.Assignment{{TaskID: source.TaskID, Role: "builder"}}
	if err := s.observeAutoHistoricalDiagnoses(context.Background(), a, []autonomy.Proposal{p}); err == nil {
		t.Fatal("active failed task treated as historical")
	}
	a.State.Phase = autonomy.Complete
	if !autoHistoricalEligible(a, source) {
		t.Fatal("abandoned failed task unavailable")
	}
	newer := *source
	newer.ID = "77777777-7777-4777-8777-777777777777"
	newer.Status = "running"
	a.Jobs = append(a.Jobs, &newer)
	if autoHistoricalEligible(a, source) {
		t.Fatal("inactive old UUID admitted while same task active")
	}
}

func TestHistoricalTypedPlannerObservationCanReachDiagnosis(t *testing.T) {
	s, a, p, source, path := historicalFixture(t, "planner")
	raw := []byte(store.J(map[string]any{"items": []any{}, "requirements": []autonomy.Requirement{pythonRequirement()}}))
	docResponse(t, path, map[string]string{"state": "ready", "archive_sha256": strings.Repeat("a", 64), "report_sha256": autoSHA(raw), "report": string(raw)})
	if intercepted, err := s.recordAutoRequirements(context.Background(), a, source, raw); err != nil || intercepted {
		t.Fatal(intercepted, err)
	}
	p.DiagnoseTaskID = 0
	p.DiagnoseRequirement = autoRequirementKey(pythonRequirement())
	builder, _ := admitHistorical(t, s, a, p)
	d := a.RequirementDiagnoses[builder.DiagnosisReservation]
	if !d.EvidenceOnly || d.OriginTaskID != source.TaskID || d.OriginReportSHA != autoSHA(raw) {
		t.Fatal("typed planner observation still inert")
	}
}

func TestHistoricalDiagnosisPromptDoesNotPromiseOriginalResume(t *testing.T) {
	s, a, p, _, _ := historicalFixture(t, "builder")
	builder, _ := admitHistorical(t, s, a, p)
	prompt := autoDiagnosisPrompt(a)
	if strings.Contains(prompt, "original retained task only after this review") || !strings.Contains(prompt, "historical source task is never resumed") || !strings.Contains(prompt, "before independent review") {
		t.Fatal("evidence-only prompt contradicts its lifecycle", prompt)
	}
	a.RequirementDiagnoses[builder.DiagnosisReservation].EvidenceOnly = false
	prompt = autoDiagnosisPrompt(a)
	if !strings.Contains(prompt, "original retained task only after this review") {
		t.Fatal("retained task lifecycle guidance missing", prompt)
	}
}
