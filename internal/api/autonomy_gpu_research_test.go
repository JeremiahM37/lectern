package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func gpuAPIContext() (*autoRecord, *autoJob, *autoJob, autoGPUQualifiedRuntime) {
	admission := &autoAdmission{TaskID: 7, Proposal: autonomy.Proposal{ProjectID: 3, Acceptance: []string{"actual result checked"}}}
	source := &autoJob{ID: "11111111-1111-1111-1111-111111111111", TaskID: 7, Role: "builder", Status: "stopped", Admission: admission}
	owner := &autoJob{ID: "22222222-2222-2222-2222-222222222222", TaskID: 7, Role: "builder", Status: "running", Admission: admission}
	a := &autoRecord{Jobs: []*autoJob{source, owner}}
	return a, owner, source, autoGPUQualifiedRuntime{RuntimeKey: strings.Repeat("e", 64), ReceiptSHA: strings.Repeat("f", 64)}
}
func TestGPUAdmissionRootSurvivesProcessAndReportCorrection(t *testing.T) {
	a, j, source, q := gpuAPIContext()
	first, err := autoGPUAdmittedRequest(a, j, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(42)"})
	if err != nil {
		t.Fatal(err)
	}
	retry := *j
	retry.ID = "33333333-3333-3333-3333-333333333333"
	retry.TaskID = 8
	second, err := autoGPUAdmittedRequest(a, &retry, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(42)"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExperimentRoot != second.ExperimentRoot || first.AcceptanceSHA != second.AcceptanceSHA || first.AdmissionSHA != second.AdmissionSHA || second.OwnerTask != 8 || second.Argv == nil {
		t.Fatal("retained lineage changed or wire not normalized")
	}
	source.Status = "running"
	if _, err = autoGPUAdmittedRequest(a, j, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(42)"}); err == nil {
		t.Fatal("mutable source accepted")
	}
}
func TestGPUAdmissionRefusesForeignSourceAndUnadmittedRole(t *testing.T) {
	a, j, source, q := gpuAPIContext()
	source.TaskID = 999
	source.Admission = nil
	if _, err := autoGPUAdmittedRequest(a, j, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(42)"}); err == nil {
		t.Fatal("foreign source accepted")
	}
	a, j, source, q = gpuAPIContext()
	j.Role = "auditor_a"
	if _, err := autoGPUAdmittedRequest(a, j, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(42)"}); err == nil {
		t.Fatal("unadmitted audit accepted")
	}
}
func TestGPUReceiptRequiresExplicitBoundAccounting(t *testing.T) {
	a, j, source, q := gpuAPIContext()
	r, err := autoGPUAdmittedRequest(a, j, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(42)"})
	if err != nil {
		t.Fatal(err)
	}
	ledger := autonomy.GPUResearchLedger{}
	lease, err := ledger.Reserve(r, q.RuntimeKey, q.ReceiptSHA, time.Now(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]any{"owner_job": j.ID, "run_id": lease.ID, "request_sha256": lease.RequestSHA, "state": "cancelled", "executed": false, "charged_ms": 0, "cleanup_confirmed": true, "receipt_sha256": strings.Repeat("9", 64)}
	raw, _ := json.Marshal(value)
	if _, err = autoGPUDecodeReceipt(raw, lease); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"owner_job", "executed", "charged_ms", "cleanup_confirmed"} {
		saved := value[field]
		delete(value, field)
		raw, _ = json.Marshal(value)
		if _, err = autoGPUDecodeReceipt(raw, lease); err == nil {
			t.Fatalf("missing %s accepted", field)
		}
		value[field] = saved
	}
	value["owner_job"] = source.ID
	raw, _ = json.Marshal(value)
	if _, err = autoGPUDecodeReceipt(raw, lease); err == nil {
		t.Fatal("foreign owner accepted")
	}
}
func TestGPUCurrentCandidateRequiresBoundCaptureReceipt(t *testing.T) {
	a, j, _, q := gpuAPIContext()
	input := autoGPUExperimentInput{Script: "print('new fix')"}
	id := strings.Repeat("1", 64)
	capture := autoGPUCapturedSource{State: "ready", SnapshotID: id, OwnerJob: j.ID, InputSHA: autoGPUInputSHA(input, q), ArchiveSHA: strings.Repeat("2", 64), TreeSHA: strings.Repeat("3", 64), ReceiptSHA: strings.Repeat("4", 64), StartedNS: 1, FinishedNS: 2}
	request, err := autoGPUCapturedRequest(a, j, id, capture, q, input)
	if err != nil {
		t.Fatal(err)
	}
	if request.SourceSnapshotID != id || request.SourceJob != j.ID || request.SourceArchiveSHA != capture.ArchiveSHA || j.Status != "running" {
		t.Fatal("capture not selected or live owner changed")
	}
	input.Script = "changed experiment"
	if _, err = autoGPUCapturedRequest(a, j, id, capture, q, input); err == nil {
		t.Fatal("foreign input capture accepted")
	}
}
