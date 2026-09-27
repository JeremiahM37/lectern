package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maintenanceConflictFixture(t *testing.T) (*autoRecord, *autoMaintenanceTransaction, autoMaintenanceRegistry) {
	t.Helper()
	a, tx, reg := maintenanceControllerFixture(t)
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		return &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "prior", CurrentTasks: 1, CapturedAt: time.Now()}, nil
	}}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "server-maintenance-backup" {
			return controllerReceipt(t, tx, "offbox_restored_verified"), nil
		}
		return controllerReceipt(t, tx, "rollback_conflict"), nil
	}
	for i := 0; i < 3; i++ {
		if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
			t.Fatal(e)
		}
	}
	if tx.State != "conflict" || a.Maintenance.Operations[tx.ID].State != "conflict" {
		t.Fatal("fixture did not reach real controller conflict transition")
	}
	return a, tx, reg
}
func maintenanceInspectionReceipt(t *testing.T, l *autoMaintenanceConflictInspection, changes func(map[string]any, map[string]any)) []byte {
	t.Helper()
	b := l.Binding
	now := time.Now().Add(-time.Second)
	end := float64(now.UnixNano()) / 1e9
	start := end - 3
	var req map[string]any
	json.Unmarshal([]byte(l.Request), &req)
	inner := map[string]any{"conflict_receipt_sha256": l.ConflictReceiptSHA, "schema_version": 1, "state": "external_healthy", "operation_id": b.OperationID, "inspection_id": l.ID, "request_sha256": b.SemanticRequestSHA, "authority_sha256": b.AuthoritySHA, "candidate_sha256": b.CandidateSHA, "before_sha256": b.BeforeSHA, "generation": b.Generation, "registry_sha256": req["registry_sha256"], "current_state_sha256": autoSHA([]byte("foreign config")), "post_state_sha256": autoSHA([]byte("foreign config")), "invocation_id": "external-instance", "post_invocation_id": "external-instance", "observed_at": start, "completed_at": end, "profile": autoMaintenanceExternalProfile, "no_mutation": true, "owned_candidate": false}
	observations := []any{}
	for i := 0; i < 3; i++ {
		observations = append(observations, map[string]any{"healthy": true, "observed_at": start + float64(i), "response_sha256": autoSHA([]byte("sensor")), "metrics_sha256": autoSHA([]byte("metrics"))})
	}
	inner["observations"] = observations
	outer := map[string]any{"conflict_receipt_sha256": l.ConflictReceiptSHA, "schema_version": 1, "state": "external_healthy", "operation_id": b.OperationID, "inspection_id": l.ID, "owner_job": b.OwnerJob, "owner_task": b.OwnerTask, "pin_sha256": b.PinSHA, "phase": "inspect", "generation": b.Generation, "request_sha256": b.RequestSHA}
	if changes != nil {
		changes(outer, inner)
	}
	outer["result"] = json.RawMessage(executionSeal(t, inner))
	return executionSeal(t, outer)
}
func TestMaintenanceConflictReadonlyOFFProofPreservesHistoryAndRequiresNewPlan(t *testing.T) {
	a, tx, reg := maintenanceConflictFixture(t)
	original, _ := json.Marshal(a.Maintenance.Operations[tx.ID].Authority)
	originalReceipts, _ := json.Marshal(tx.Receipts)
	saved := []byte(nil)
	calls := []string{}
	writes := 0
	io := autoMaintenanceControllerIO{Save: func() error { saved, _ = json.Marshal(a); return nil }, InspectWrite: func(v *autoMaintenanceTransaction, l *autoMaintenanceConflictInspection) error {
		if !strings.Contains(string(saved), l.Binding.RequestSHA) {
			t.Fatal("inspection before durable intent")
		}
		writes++
		return nil
	}}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		if args[0] != "server-maintenance-inspect" {
			t.Fatal("unexpected mutating cleanup", args)
		}
		return maintenanceInspectionReceipt(t, tx.Inspections[len(tx.Inspections)-1], nil), nil
	}
	a.Config.Enabled = false
	a.Quota.Providers = nil
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	op := a.Maintenance.Operations[tx.ID]
	if tx.State != "superseded" || op.State != "superseded" || op.ExternalResolution == nil || op.RollbackReceiptSHA != "" || writes != 1 || len(calls) != 1 {
		t.Fatal("read-only supersession failed or restoration fabricated")
	}
	authority, _ := json.Marshal(op.Authority)
	receipts, _ := json.Marshal(tx.Receipts)
	if string(authority) != string(original) || string(receipts) != string(originalReceipts) {
		t.Fatal("prior approved/conflict history changed")
	}
	raw, _ := json.Marshal(a)
	var loaded autoRecord
	json.Unmarshal(raw, &loaded)
	a = &loaded
	tx = a.MaintenanceTransactions[tx.ID]
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if len(calls) != 1 {
		t.Fatal("superseded history launched another operation")
	}
	if e := autoBeginMaintenance(op, autoMaintenanceObservation{}, reg, autoMaintenanceGate{}, time.Now()); e == nil {
		t.Fatal("old authority became new mutation permission")
	}
}
func TestMaintenanceConflictInspectionRejectsForeignAndUnstableEvidence(t *testing.T) {
	_, tx, _ := maintenanceConflictFixture(t)
	l, e := autoNewMaintenanceConflictInspection(tx)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"registry", "post_config", "post_invocation", "mutation", "owned", "missing_owned", "health", "metric", "profile", "request", "inspection", "outside_bracket", "missing_bracket"} {
		t.Run(kind, func(t *testing.T) {
			raw := maintenanceInspectionReceipt(t, l, func(out, in map[string]any) {
				switch kind {
				case "registry":
					in["registry_sha256"] = autoSHA([]byte("foreign"))
				case "post_config":
					in["post_state_sha256"] = autoSHA([]byte("racing"))
				case "post_invocation":
					in["post_invocation_id"] = "racing"
				case "mutation":
					in["no_mutation"] = false
				case "owned":
					in["owned_candidate"] = true
				case "missing_owned":
					delete(in, "owned_candidate")
				case "health":
					in["observations"].([]any)[0].(map[string]any)["healthy"] = false
				case "metric":
					delete(in["observations"].([]any)[0].(map[string]any), "metrics_sha256")
				case "profile":
					in["profile"] = "model asserts healthy"
				case "request":
					out["request_sha256"] = autoSHA([]byte("other"))
				case "inspection":
					out["inspection_id"] = autoSHA([]byte("other"))
				case "outside_bracket":
					in["observations"].([]any)[2].(map[string]any)["observed_at"] = in["completed_at"].(float64) + 1
				case "missing_bracket":
					delete(in, "post_state_sha256")
				}
			})
			if _, _, e := autoDecodeMaintenanceInspection(raw, l, time.Now()); e == nil {
				t.Fatal("accepted", kind)
			}
		})
	}
}
func TestMaintenanceConflictUnavailableRetriesNewIDAndKeepsRevocation(t *testing.T) {
	a, tx, reg := maintenanceConflictFixture(t)
	tx.StopRequested = true
	tx.StopConfirmed = true
	oldGeneration := tx.Generation
	calls := 0
	saved := []byte(nil)
	io := autoMaintenanceControllerIO{Save: func() error { saved, _ = json.Marshal(a); return nil }, InspectWrite: func(*autoMaintenanceTransaction, *autoMaintenanceConflictInspection) error { return nil }, Call: func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if args[0] != "server-maintenance-inspect" {
			t.Fatal(args)
		}
		l := tx.Inspections[len(tx.Inspections)-1]
		return maintenanceInspectionReceipt(t, l, func(out, in map[string]any) { out["state"] = "unavailable"; in["state"] = "unavailable" }), nil
	}}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	first := tx.Inspections[0].ID
	var loaded autoRecord
	json.Unmarshal(saved, &loaded)
	a = &loaded
	tx = a.MaintenanceTransactions[tx.ID]
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("ignored backoff")
	}
	tx.InspectionRetryAt = time.Now().Add(-time.Second)
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if calls != 2 || len(tx.Inspections) != 2 || first == tx.Inspections[1].ID || tx.Generation != oldGeneration || !tx.StopRequested || !tx.StopConfirmed || a.Maintenance.Operations[tx.ID].State != "conflict" {
		t.Fatal("retry reset original effect or lost failure lineage")
	}
}
func TestMaintenanceConflictStaleProofRetainedThenFreshProbe(t *testing.T) {
	a, tx, reg := maintenanceConflictFixture(t)
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, InspectWrite: func(*autoMaintenanceTransaction, *autoMaintenanceConflictInspection) error { return nil }, Call: func(context.Context, ...string) ([]byte, error) {
		l := tx.Inspections[len(tx.Inspections)-1]
		return maintenanceInspectionReceipt(t, l, func(out, in map[string]any) {
			in["observed_at"] = in["observed_at"].(float64) - 300
			in["completed_at"] = in["completed_at"].(float64) - 300
			for _, v := range in["observations"].([]any) {
				m := v.(map[string]any)
				m["observed_at"] = m["observed_at"].(float64) - 300
			}
		}), nil
	}}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e == nil {
		t.Fatal("stale root health released ownership")
	}
	if len(tx.Inspections) != 1 || tx.Inspections[0].State != "proof_rejected" || tx.Inspections[0].Receipt == "" || tx.InspectionRetryAt.IsZero() || tx.State != "conflict" {
		t.Fatal("old immutable receipt stranded retry or lost evidence")
	}
}
func TestMaintenanceConflictRegistryOutageRetainsOwnershipAndNoProbe(t *testing.T) {
	a, tx, _ := maintenanceConflictFixture(t)
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Call: func(context.Context, ...string) ([]byte, error) { t.Fatal("unregistered probe"); return nil, nil }}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, autoMaintenanceRegistry{}, false, time.Now(), io); e == nil {
		t.Fatal("missing registry cleared authority")
	}
	if tx.State != "conflict" || len(tx.Inspections) > 0 {
		t.Fatal("scope widened")
	}
}
func TestMaintenanceConflictInspectionFilePreservesOriginalApplyPointer(t *testing.T) {
	_, tx, _ := maintenanceConflictFixture(t)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, tx.Binding.OwnerJob), 0700)
	if e := autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal(e)
	}
	l, e := autoNewMaintenanceConflictInspection(tx)
	if e != nil {
		t.Fatal(e)
	}
	if e = autoWriteMaintenanceInspection(root, tx, l); e != nil {
		t.Fatal(e)
	}
	if e = autoWriteMaintenanceInspection(root, tx, l); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, tx.Binding.OwnerJob, "server-maintenance", tx.ID+".apply.json"))
	if e != nil || string(raw) != tx.Request {
		t.Fatal("inspector rewrote apply authority", e)
	}
	l.Request += " "
	l.Binding.RequestSHA = autoSHA([]byte(l.Request))
	if e = autoWriteMaintenanceInspection(root, tx, l); e == nil {
		t.Fatal("immutable inspection rewritten")
	}
}

func TestMaintenanceConflictCapturedRealInspection(t *testing.T) {
	prefix := os.Getenv("LECTERN_MAINTENANCE_INSPECTION_FIXTURE")
	if prefix == "" {
		t.Skip("explicit disposable inspection fixture required")
	}
	request, e := os.ReadFile(prefix + "-request.json")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(prefix + "-receipt.json")
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		Inspection string `json:"inspection_id"`
		Result     struct {
			Completed float64 `json:"completed_at"`
			Conflict  string  `json:"conflict_receipt_sha256"`
		} `json:"result"`
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		t.Fatal(e)
	}
	l := &autoMaintenanceConflictInspection{ConflictReceiptSHA: out.Result.Conflict, ID: out.Inspection, Request: string(request), Binding: executionBinding(t, request, "inspect")}
	capturedNow := time.Unix(0, int64(out.Result.Completed*1e9)).Add(time.Second)
	state, proof, e := autoDecodeMaintenanceInspection(raw, l, capturedNow)
	if e != nil {
		t.Fatal(e)
	}
	if state != "external_healthy" || proof == nil {
		t.Fatal("real bracketed health missing")
	}
	op := &autoMaintenanceOperation{ConflictReceiptSHA: proof.ConflictReceiptSHA, ID: l.Binding.OperationID, State: "conflict", Reason: "retained external conflict", Authority: autoMaintenanceAuthority{RegistrySHA: proof.RegistrySHA, BeforeSHA: l.Binding.BeforeSHA, CandidateSHA: l.Binding.CandidateSHA}}
	if e = autoResolveMaintenanceConflict(op, *proof, capturedNow); e != nil {
		t.Fatal(e)
	}
	if op.State != "superseded" || op.RollbackReceiptSHA != "" || op.ApplyReceiptSHA != "" || op.ExternalResolution.ReceiptSHA != proof.ReceiptSHA {
		t.Fatal("real external state manufactured restoration")
	}
	// Exercise the actual captured release receipt through successor admission.
	// The new audits/validation/backup below are synthetic controller fixtures;
	// this does not claim a model autonomously planned a successor or mutate it.
	var requestFields struct {
		Target  string                `json:"target_id"`
		Service string                `json:"service_id"`
		Limits  autoMaintenanceLimits `json:"limits"`
	}
	if e = json.Unmarshal(request, &requestFields); e != nil {
		t.Fatal(e)
	}
	op.Authority.ResourceID = requestFields.Target + "/" + requestFields.Service
	next, reg, gate, _ := maintenanceFixture()
	next.ResourceID = op.Authority.ResourceID
	next.RegistrySHA = proof.RegistrySHA
	next.BeforeSHA = l.Binding.BeforeSHA
	next.Limits = requestFields.Limits
	next.CandidateSHA = l.Binding.CandidateSHA
	next.PredecessorOperationID = op.ID
	next.SupersessionSHA = proof.ReceiptSHA
	next.PlanSHA = autoSHA([]byte("independently audited successor fixture"))
	binding := autoMaintenanceBinding(next)
	for i := range next.Audits {
		next.Audits[i].BindingSHA = binding
	}
	next.Validation.BindingSHA = binding
	next.Backup.BeforeSHA = next.BeforeSHA
	reg.ResourceID = next.ResourceID
	reg.Digest = next.RegistrySHA
	gate.QuotaObservedAt = capturedNow
	ledger := &autoMaintenanceLedger{Operations: map[string]*autoMaintenanceOperation{op.ID: op}, Owners: map[string]string{next.ResourceID: op.ID}}
	before, _ := json.Marshal(op)
	successor, e := autoReserveMaintenance(ledger, next, reg, gate, capturedNow)
	if e != nil {
		t.Fatal("captured release denied correctly bound fresh authority", e)
	}
	replay := next
	replay.PlanSHA = autoSHA([]byte("cosmetically new proposal"))
	changedBinding := autoMaintenanceBinding(replay)
	for i := range replay.Audits {
		replay.Audits[i].BindingSHA = changedBinding
	}
	replay.Validation.BindingSHA = changedBinding
	if _, e = autoReserveMaintenance(ledger, replay, reg, gate, capturedNow); e == nil {
		t.Fatal("captured release minted a second cosmetic successor")
	}
	successor.State = "restored"
	if e = autoValidateMaintenanceSuccessor(ledger, next); e == nil {
		t.Fatal("failed successor reused released ancestor")
	}
	after, _ := json.Marshal(op)
	if string(before) != string(after) {
		t.Fatal("captured predecessor evidence rewritten")
	}
	op.State = "conflict"
	op.ExternalResolution = nil
	if e = autoResolveMaintenanceConflict(op, *proof, capturedNow.Add(time.Minute)); e == nil {
		t.Fatal("expired capture accepted")
	}
}
func TestMaintenanceConflictLostLaunchResponseAndRegistryLossStillPollOwnedID(t *testing.T) {
	a, tx, reg := maintenanceConflictFixture(t)
	saved := []byte(nil)
	calls := []string{}
	io := autoMaintenanceControllerIO{Save: func() error { saved, _ = json.Marshal(a); return nil }, InspectWrite: func(*autoMaintenanceTransaction, *autoMaintenanceConflictInspection) error { return nil }}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		if len(calls) == 1 {
			return nil, context.DeadlineExceeded
		}
		return maintenanceInspectionReceipt(t, tx.Inspections[0], nil), nil
	}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e == nil {
		t.Fatal("lost response missing")
	}
	var loaded autoRecord
	json.Unmarshal(saved, &loaded)
	a = &loaded
	tx = a.MaintenanceTransactions[tx.ID]
	if e := autoAdvanceMaintenance(context.Background(), a, tx, autoMaintenanceRegistry{}, false, time.Now(), io); e == nil {
		t.Fatal("registry loss released ownership")
	}
	if strings.Join(calls, ",") != "server-maintenance-inspect,server-maintenance-inspect-status" || len(tx.Inspections) != 1 || tx.Inspections[0].Receipt == "" || a.Maintenance.Operations[tx.ID].State != "conflict" {
		t.Fatal("lost response orphaned owned inspection or widened authority", calls)
	}
}
