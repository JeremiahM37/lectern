package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func diagnosisEnvironmentFixture(t *testing.T) (*autoRecord, *autoJob, *autoJob, autonomy.Proposal) {
	t.Helper()
	state, err := autonomy.NewState("2026-09-26")
	if err != nil {
		t.Fatal(err)
	}
	request := &autoPythonRequest{SchemaVersion: 1, Kind: "python_wheels", Requirements: []string{"django==5.2.12"}, Imports: []string{"django"}, SourceJob: "origin-evidence", SourceSHA: strings.Repeat("a", 64), AdmissionSHA: strings.Repeat("b", 64)}
	receipt := &autoPythonReceipt{State: "verified", Capability: "python_wheels", InputKey: strings.Repeat("c", 64), BundleKey: strings.Repeat("d", 64), RuntimeDigest: strings.Repeat("e", 64), SourceJob: request.SourceJob, SourceSHA: request.SourceSHA, AdmissionSHA: request.AdmissionSHA}
	b := &autoJob{ID: "diagnosis-builder", TaskID: 901, Role: "builder", Status: "done", Approved: true, ReviewTaskID: 902, ReviewOutcome: "completed", PythonRequest: request, PythonRecovery: receipt, PythonUsedBundle: receipt.BundleKey}
	r := &autoJob{ID: "diagnosis-reviewer", TaskID: 902, Role: "reviewer", Status: "done", PythonRequest: request, PythonRecovery: receipt, PythonUsedBundle: receipt.BundleKey}
	e := &autoVerifiedDiagnosisEnvironment{DiagnosisTaskID: 901, OriginTaskID: 300, BuilderJob: b.ID, ReviewerJob: r.ID, Request: request, Receipt: receipt, ReviewerReceipt: receipt}
	a := &autoRecord{State: state, Jobs: []*autoJob{b, r}, RequirementDiagnoses: map[string]*autoRequirementDiagnosis{"origin": {TaskID: 901, ReviewTaskID: 902, Outcome: "completed", VerifiedEnvironment: e}}}
	p := autonomy.Proposal{ProjectID: 86, Title: "eligible fresh work", Why: "actually needs reviewed Django environment", Acceptance: []string{"test current source"}, EnvironmentDiagnosisTaskID: 901}
	a.State.Items = []autonomy.Proposal{p}
	return a, b, r, p
}

func TestDiagnosisEnvironmentRequiresActualIndependentUse(t *testing.T) {
	for _, which := range []string{"builder-unused", "reviewer-unused", "different-bundle", "rejected", "not-final", "request-mismatch", "no-runtime"} {
		t.Run(which, func(t *testing.T) {
			a, b, r, p := diagnosisEnvironmentFixture(t)
			if len(autoVerifiedDiagnosisEnvironments(a)) != 1 {
				t.Fatal("valid proof missing")
			}
			switch which {
			case "builder-unused":
				b.PythonUsedBundle = ""
			case "reviewer-unused":
				r.PythonUsedBundle = ""
			case "different-bundle":
				copy := *r.PythonRecovery
				copy.BundleKey = strings.Repeat("f", 64)
				r.PythonRecovery = &copy
				r.PythonUsedBundle = copy.BundleKey
			case "rejected":
				b.Rejected = true
			case "not-final":
				r.Status = "running"
			case "request-mismatch":
				copy := *r.PythonRequest
				copy.Imports = []string{"other"}
				r.PythonRequest = &copy
			case "no-runtime":
				b.PythonRecovery.RuntimeDigest = ""
			}
			if len(autoVerifiedDiagnosisEnvironments(a)) != 0 || autoValidateDiagnosisEnvironments(a, []autonomy.Proposal{p}) == nil {
				t.Fatal("unverified environment was selectable")
			}
		})
	}
}
func TestDiagnosisEnvironmentPinnedAuditedAndReverifiedForConsumer(t *testing.T) {
	a, _, _, p := diagnosisEnvironmentFixture(t)
	if err := pinAutoDiagnosisEnvironments(a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	frozen := store.J(a.EnvironmentPins[p.EnvironmentDiagnosisTaskID])
	// Persistence keeps a deep copy rather than aliasing mutable source reports.
	var restored autoRecord
	if err := json.Unmarshal([]byte(store.J(a)), &restored); err != nil {
		t.Fatal(err)
	}
	a = &restored
	a.State.Phase = autonomy.Build
	consumer := &autoJob{ID: "consumer", TaskID: 1001, Role: "builder", Status: "prepared"}
	if err := autoInheritPythonRequest(a, consumer); err == nil {
		t.Fatal("missing plan audits accepted")
	}
	yes := true
	a.State.Audits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes}, "auditor_b": {Approve: &yes}}
	if err := autoInheritPythonRequest(a, consumer); err != nil {
		t.Fatal(err)
	}
	if consumer.PythonRecovery != nil || consumer.PythonUsedBundle != "" || consumer.PythonExpectedInput != strings.Repeat("c", 64) || consumer.PythonExpectedBundle != strings.Repeat("d", 64) {
		t.Fatal("copied verification instead of requiring real provisioning")
	}
	receipt := *a.EnvironmentPins[p.EnvironmentDiagnosisTaskID].Receipt
	raw := []byte(store.J(receipt))
	if ready, err := autoApplyPythonReceipt(a, consumer, raw); err != nil || !ready {
		t.Fatal("bound environment did not verify", err)
	}
	a.Jobs = append(a.Jobs, consumer)
	a.State.Assignments = []autonomy.Assignment{{TaskID: 1001, Role: "builder", Item: 0, Completed: true}}
	reviewer := &autoJob{ID: "consumer-reviewer", TaskID: 1002, Role: "reviewer", Status: "prepared"}
	if err := autoInheritPythonRequest(a, reviewer); err != nil || reviewer.PythonExpectedBundle != consumer.PythonExpectedBundle {
		t.Fatal("consumer reviewer did not inherit exact environment", err)
	}
	if store.J(a.EnvironmentPins[p.EnvironmentDiagnosisTaskID]) != frozen {
		t.Fatal("consumer changed frozen evidence")
	}
}
func TestDiagnosisEnvironmentCannotChangeAfterAuditPin(t *testing.T) {
	a, b, _, p := diagnosisEnvironmentFixture(t)
	if err := pinAutoDiagnosisEnvironments(a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	b.PythonRecovery.Reason = "changed after audit"
	if err := autoValidateDiagnosisEnvironments(a, []autonomy.Proposal{p}); err == nil {
		t.Fatal("mutated environment passed old pin")
	}
}
func TestDiagnosisEnvironmentDoesNotGrantSourceAdmission(t *testing.T) {
	s, a, project, rejected := repairFixture(t)
	env, _, _, p := diagnosisEnvironmentFixture(t)
	a.RequirementDiagnoses = env.RequirementDiagnoses
	a.Jobs = append(a.Jobs, env.Jobs...)
	a.State.Phase = autonomy.Plan
	if err := pinAutoDiagnosisEnvironments(a, []autonomy.Proposal{p}); err != nil {
		t.Fatal(err)
	}
	p.ProjectID = project
	p.ContinueTaskID = rejected
	if err := s.validateAutoSources(a, []autonomy.Proposal{p}); err == nil {
		t.Fatal("environment resurrected rejected source as approved continuation")
	}
}

func TestDiagnosisEnvironmentSelectionDoesNotReplaceLaterRemediation(t *testing.T) {
	a, _, _, p := diagnosisEnvironmentFixture(t)
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
	// An actual later dependency discovery resumes the same assignment with a
	// new explicit request; repeated preflight must not overwrite it with the
	// originally selected environment.
	request := *j.PythonRequest
	request.Requirements = append(append([]string(nil), request.Requirements...), "extra==1.0")
	j.PythonRequest = &request
	j.PythonExpectedInput = ""
	j.PythonExpectedBundle = ""
	before := store.J(j.PythonRequest)
	if err := autoInheritPythonRequest(a, j); err != nil || store.J(j.PythonRequest) != before || j.PythonExpectedInput != "" {
		t.Fatal("initial environment selector overrode retained remediation", err)
	}
}
