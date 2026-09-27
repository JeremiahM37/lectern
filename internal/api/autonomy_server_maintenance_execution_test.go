package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func executionSeal(t *testing.T, m map[string]any) []byte {
	t.Helper()
	delete(m, "receipt_sha256")
	raw, e := autoMaintenanceJSON(m)
	if e != nil {
		t.Fatal(e)
	}
	m["receipt_sha256"] = autoSHA(raw)
	raw, e = autoMaintenanceJSON(m)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func executionBinding(t *testing.T, request []byte, phase string) autoMaintenanceExecutionBinding {
	t.Helper()
	var m map[string]any
	if e := json.Unmarshal(request, &m); e != nil {
		t.Fatal(e)
	}
	b := autoMaintenanceExecutionBinding{OperationID: m["operation_id"].(string), OwnerJob: m["owner_job"].(string), OwnerTask: int64(m["owner_task"].(float64)), PinSHA: m["pin_sha256"].(string), Phase: phase, Generation: int(m["generation"].(float64)), BeforeSHA: m["expected_state_sha256"].(string), RequestSHA: autoSHA(request)}
	limits, _ := autoMaintenanceJSON(m["limits"])
	b.CandidateSHA = autoSHA(limits)
	if h, ok := m["authority_sha256"].(string); ok {
		b.AuthoritySHA = h
	}
	delete(m, "authority_sha256")
	delete(m, "generation")
	raw, _ := autoMaintenanceJSON(m)
	b.SemanticRequestSHA = autoSHA(raw)
	return b
}
func TestMaintenanceExecutionCapturedWrapper(t *testing.T) {
	root := os.Getenv("LECTERN_MAINTENANCE_CAPTURED")
	if root == "" {
		t.Skip("explicit disposable captured proof directory required")
	}
	for _, phase := range []string{"backup", "apply", "reconcile"} {
		t.Run(phase, func(t *testing.T) {
			req, e := os.ReadFile(filepath.Join(root, phase+"-request.json"))
			if e != nil {
				t.Fatal(e)
			}
			raw, e := os.ReadFile(filepath.Join(root, phase+"-receipt.json"))
			if e != nil {
				t.Fatal(e)
			}
			b := executionBinding(t, req, phase)
			r, e := autoDecodeMaintenanceExecution(raw, b)
			if e != nil {
				t.Fatal(e)
			}
			if phase == "backup" && r.Backup == nil {
				t.Fatal("backup lost")
			}
			if phase == "apply" && (r.Apply == nil || r.Health == nil) {
				t.Fatal("applied health lost")
			}
			if phase == "reconcile" {
				if r.Compensation == nil || r.Apply != nil {
					t.Fatal("invented apply or lost restoration")
				}
				o := &autoMaintenanceOperation{ID: b.OperationID, Authority: autoMaintenanceAuthority{BeforeSHA: b.BeforeSHA, CandidateSHA: b.CandidateSHA}, State: "apply_pending"}
				if e = autoRecordMaintenanceCompensation(o, *r.Compensation); e != nil || o.State != "restored" {
					t.Fatal(e, o.State)
				}
			}
			for _, field := range []string{"owner_task", "generation", "phase", "request_sha256"} {
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				switch field {
				case "owner_task", "generation":
					m[field] = 999
				default:
					m[field] = "wrong"
				}
				if _, e = autoDecodeMaintenanceExecution(executionSeal(t, m), b); e == nil {
					t.Fatal("accepted foreign", field)
				}
			}
		})
	}
}
func TestMaintenanceExecutionAuthorityKeepsOriginalValidationOwner(t *testing.T) {
	old, _, _, now := maintenanceFixture()
	p := autoMaintenancePlanPin{ProjectID: 1, TargetID: "local", ServiceID: "temp-api", RegistrySHA: old.RegistrySHA, BeforeSHA: old.BeforeSHA, CandidateSHA: old.CandidateSHA, Limits: old.Limits, Acceptance: []string{"healthy"}, CapturedAt: now}
	p.Key = autoMaintenancePinHash(p)
	a := &autoMaintenanceAdmitted{Pin: p, TaskID: 50, JobID: "55555555-5555-4555-8555-555555555555", Audits: old.Audits}
	zero := 0
	v := autoMaintenanceValidationReceipt{PinSHA: p.Key, CandidateSHA: p.CandidateSHA, ReceiptSHA: autoSHA([]byte("validation")), ProfileSHA: autoSHA([]byte("profile")), OutputSHA: autoSHA([]byte("output")), OwnerTask: 60, OwnerJob: "66666666-6666-4666-8666-666666666666", Executed: true, ExitCode: &zero, Profile: "service_resource_limits_v1"}
	rev := autoMaintenanceFinalReview{TaskID: 60, JobID: "77777777-7777-4777-8777-777777777777", PinSHA: p.Key, ValidationSHA: v.ReceiptSHA, ArchiveSHA: autoSHA([]byte("archive")), ReportSHA: autoSHA([]byte("review")), Approved: true}
	req, auth, b, e := autoBuildMaintenanceExecution(a, v, rev, old.Backup, 1)
	if e != nil {
		t.Fatal(e)
	}
	var am, rm map[string]any
	_ = json.Unmarshal(auth, &am)
	_ = json.Unmarshal(req, &rm)
	if am["validation_owner_job"] != v.OwnerJob || am["owner_job"] != a.JobID || am["candidate_review_sha256"] != rev.ReportSHA || rm["authority_sha256"] != autoSHA(auth) {
		t.Fatal("authority identity substitution")
	}
	_, auth2, b2, e := autoBuildMaintenanceExecution(a, v, rev, old.Backup, 2)
	if e != nil || string(auth) != string(auth2) || b.SemanticRequestSHA != b2.SemanticRequestSHA || b.RequestSHA == b2.RequestSHA {
		t.Fatal("generation changed semantic reservation", e)
	}
	rev.Approved = false
	if _, _, _, e = autoBuildMaintenanceExecution(a, v, rev, old.Backup, 1); e == nil {
		t.Fatal("rejected review authorized")
	}
}
func TestMaintenanceExecutionPendingAndRestorationBoundaries(t *testing.T) {
	h := autoSHA([]byte("h"))
	b := autoMaintenanceExecutionBinding{OperationID: h, OwnerJob: "55555555-5555-4555-8555-555555555555", OwnerTask: 50, PinSHA: h, Generation: 1, Phase: "apply", RequestSHA: h, SemanticRequestSHA: h, AuthoritySHA: h, BeforeSHA: h, CandidateSHA: h}
	out := map[string]any{"schema_version": 1, "operation_id": h, "owner_job": b.OwnerJob, "owner_task": 50, "pin_sha256": h, "generation": 1, "phase": "apply", "request_sha256": h, "state": "waiting"}
	if r, e := autoDecodeMaintenanceExecution(executionSeal(t, out), b); e != nil || r.Apply != nil {
		t.Fatal(e)
	}
	out["state"] = "rolled_back"
	if _, e := autoDecodeMaintenanceExecution(executionSeal(t, out), b); e == nil {
		t.Fatal("restored without proof")
	}
	inner := map[string]any{"state": "rolled_back", "operation_id": h, "before_sha256": h, "candidate_sha256": h, "generation": 1, "request_sha256": h, "authority_sha256": h, "restored_sha256": h, "observations": []any{map[string]any{"healthy": true, "observed_at": 123, "response_sha256": h, "metrics_sha256": h}}}
	out["result"] = json.RawMessage(executionSeal(t, inner))
	r, e := autoDecodeMaintenanceExecution(executionSeal(t, out), b)
	if e != nil || r.Compensation == nil || r.Apply != nil {
		t.Fatal(e)
	}
	inner["restored_sha256"] = autoSHA([]byte("foreign"))
	out["result"] = json.RawMessage(executionSeal(t, inner))
	if _, e = autoDecodeMaintenanceExecution(executionSeal(t, out), b); e == nil {
		t.Fatal("foreign restoration accepted")
	}
}
func TestMaintenanceExecutionStopRetainsReconciliation(t *testing.T) {
	h := autoSHA([]byte("operation"))
	for _, state := range []string{"stopped", "stopping", "reconciliation_required"} {
		raw, _ := json.Marshal(map[string]any{"operation_id": h, "generation": 2, "state": state, "no_effects": state == "stopped"})
		got, e := autoDecodeMaintenanceExecutionStop(raw, h, 2)
		if e != nil || got != state {
			t.Fatal(e, got)
		}
		if _, e = autoDecodeMaintenanceExecutionStop(raw, h, 3); e == nil {
			t.Fatal("old stop acknowledged new generation")
		}
	}
}

func TestMaintenanceExecutionStopNeedsNoEffectsProof(t *testing.T) {
	h := autoSHA([]byte("operation"))
	for _, proof := range []any{nil, false} {
		m := map[string]any{"operation_id": h, "generation": 1, "state": "stopped"}
		if proof != nil {
			m["no_effects"] = proof
		}
		raw, _ := json.Marshal(m)
		if _, e := autoDecodeMaintenanceExecutionStop(raw, h, 1); e == nil {
			t.Fatal("unproven stop renewed candidate")
		}
	}
}
func TestMaintenanceExecutionPreeffectJournalProof(t *testing.T) {
	h := autoSHA([]byte("h"))
	b := autoMaintenanceExecutionBinding{OperationID: h, OwnerJob: "55555555-5555-4555-8555-555555555555", OwnerTask: 50, PinSHA: h, Generation: 1, Phase: "apply", RequestSHA: h, SemanticRequestSHA: h, AuthoritySHA: h, BeforeSHA: h, CandidateSHA: h}
	inner := map[string]any{"state": "unavailable", "operation_id": h, "before_sha256": h, "candidate_sha256": h, "generation": 1, "request_sha256": h, "authority_sha256": h, "no_effects": true, "effects_started": false}
	outer := map[string]any{"schema_version": 1, "operation_id": h, "owner_job": b.OwnerJob, "owner_task": 50, "pin_sha256": h, "generation": 1, "phase": "apply", "request_sha256": h, "state": "unavailable"}
	for _, effects := range []any{false, true, nil} {
		if effects == nil {
			delete(inner, "effects_started")
		} else {
			inner["effects_started"] = effects
		}
		outer["result"] = json.RawMessage(executionSeal(t, inner))
		r, e := autoDecodeMaintenanceExecution(executionSeal(t, outer), b)
		if effects == false {
			if e != nil || !r.NoEffects || r.InnerReceiptSHA == "" {
				t.Fatal(e)
			}
		} else if e == nil {
			t.Fatal("accepted ambiguous effect proof")
		}
	}
}
