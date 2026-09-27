package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maintenanceControllerFixture(t *testing.T) (*autoRecord, *autoMaintenanceTransaction, autoMaintenanceRegistry) {
	t.Helper()
	_, a, j := maintenanceReviewedFixture(t)
	auth, reg, _, _ := maintenanceFixture()
	pin := j.MaintenanceAdmission.Pin
	reg.ResourceID = pin.TargetID + "/" + pin.ServiceID
	reg.Digest = pin.RegistrySHA
	reg.Min = autoMaintenanceLimits{50, 256 << 20, 64}
	reg.Max = autoMaintenanceLimits{200, 2 << 30, 512}
	reg.MemoryHeadroomBytes = 0
	j.MaintenanceAdmission.Audits = auth.Audits
	j.MaintenanceAdmission.Audits[0].JobID = "44444444-4444-4444-8444-444444444444"
	for i := range j.MaintenanceAdmission.Audits {
		j.MaintenanceAdmission.Audits[i].BindingSHA = autoMaintenanceBinding(autoMaintenancePinAuthority(pin))
	}
	a.Jobs = append(a.Jobs, &autoJob{ID: j.MaintenanceAdmission.JobID, TaskID: j.MaintenanceAdmission.TaskID, Role: "builder", Status: "done", Provider: "codex", MaintenanceAdmission: j.MaintenanceAdmission})
	if _, e := autoRecordMaintenanceReviewedCandidate(a, j, maintenanceApproval, autoSHA([]byte("review archive"))); e != nil {
		t.Fatal(e)
	}
	j.Status = "done"
	if e := autoQueueReviewedMaintenance(a); e != nil {
		t.Fatal(e)
	}
	if len(a.MaintenanceTransactions) != 1 {
		t.Fatal("missing transaction")
	}
	for _, v := range a.MaintenanceTransactions {
		return a, v, reg
	}
	panic("missing")
}
func controllerReceipt(t *testing.T, tx *autoMaintenanceTransaction, state string) []byte {
	b := tx.Binding
	inner := map[string]any{"state": state, "before_sha256": b.BeforeSHA}
	switch state {
	case "offbox_restored_verified":
		for _, k := range []string{"snapshot_id", "repository_id", "repository_sha256", "profile_sha256", "restored_manifest_sha256", "restore_proof_sha256", "offbox_receipt_sha256"} {
			inner[k] = autoSHA([]byte(k))
		}
	case "applied", "rolled_back", "rollback_conflict":
		inner["operation_id"] = b.OperationID
		inner["request_sha256"] = b.SemanticRequestSHA
		inner["authority_sha256"] = b.AuthoritySHA
		inner["candidate_sha256"] = b.CandidateSHA
		inner["generation"] = b.Generation
		if state == "applied" {
			inner["applied_state_sha256"] = autoSHA([]byte("applied state"))
			inner["invocation_id"] = "new invocation"
		} else if state == "rolled_back" {
			inner["restored_sha256"] = b.BeforeSHA
		}
		obs := []any{}
		for i := 0; i < 3; i++ {
			obs = append(obs, map[string]any{"healthy": true, "observed_at": float64(100 + i), "response_sha256": autoSHA([]byte("response")), "metrics_sha256": autoSHA([]byte("metrics"))})
		}
		inner["observations"] = obs
	}
	out := map[string]any{"schema_version": 1, "operation_id": b.OperationID, "owner_job": b.OwnerJob, "owner_task": b.OwnerTask, "pin_sha256": b.PinSHA, "phase": b.Phase, "generation": b.Generation, "request_sha256": b.RequestSHA, "state": state}
	if state == "offbox_restored_verified" || state == "applied" || state == "rolled_back" || state == "rollback_conflict" {
		out["result"] = json.RawMessage(executionSeal(t, inner))
	}
	return executionSeal(t, out)
}
func TestMaintenanceControllerReviewedBackupApplyPersistsAndCommits(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	ctx := context.Background()
	saved := []byte(nil)
	calls := []string{}
	writes := 0
	io := autoMaintenanceControllerIO{Save: func() error { saved, _ = json.Marshal(a); return nil }, Write: func(v *autoMaintenanceTransaction) error {
		if !strings.Contains(string(saved), v.Binding.RequestSHA) {
			t.Fatal("write before durable request")
		}
		writes++
		return nil
	}, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		return &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "old invocation", RSSBytes: 8 << 20, CurrentTasks: 2, CapturedAt: time.Now()}, nil
	}}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		if !strings.Contains(string(saved), tx.Binding.RequestSHA) {
			t.Fatal("runner before durable request")
		}
		if args[0] == "server-maintenance-backup" {
			return controllerReceipt(t, tx, "offbox_restored_verified"), nil
		}
		if args[0] == "server-maintenance-apply" {
			if a.Maintenance.Operations[tx.ID].State != "apply_pending" {
				t.Fatal("no durable apply intent")
			}
			return controllerReceipt(t, tx, "applied"), nil
		}
		t.Fatal(args)
		return nil, nil
	}
	for i := 0; i < 3; i++ {
		if e := io.Save(); e != nil {
			t.Fatal(e)
		}
		if e := autoAdvanceMaintenance(ctx, a, tx, reg, true, time.Now(), io); e != nil {
			t.Fatal(e)
		}
		// Restart after every boundary must retain the same semantic operation.
		var restored autoRecord
		if e := json.Unmarshal(saved, &restored); e != nil {
			t.Fatal(e)
		}
		a = &restored
		tx = a.MaintenanceTransactions[tx.ID]
	}
	if tx.State != "verified" || a.Maintenance.Operations[tx.ID].State != "verified" || writes != 2 || len(calls) != 2 {
		t.Fatalf("not committed once: %s %v writes%d", tx.State, calls, writes)
	}
	before := len(calls)
	if e := autoAdvanceMaintenance(ctx, a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if len(calls) != before {
		t.Fatal("OFF revoked historical verified operation")
	}
}
func TestMaintenanceControllerOFFBeforeApplyRenewsSameReservation(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	ctx := context.Background()
	saved := []byte(nil)
	io := autoMaintenanceControllerIO{Save: func() error { saved, _ = json.Marshal(a); return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		return &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "before", CurrentTasks: 2, CapturedAt: time.Now()}, nil
	}}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "server-maintenance-backup":
			return controllerReceipt(t, tx, "offbox_restored_verified"), nil
		case "server-maintenance-stop":
			if !strings.Contains(string(saved), `"stop_requested":true`) {
				t.Fatal("stop intent not durable")
			}
			raw, e := json.Marshal(map[string]any{"state": "stopped", "no_effects": true, "operation_id": tx.ID, "generation": tx.Generation})
			return append(raw, '\n'), e
		default:
			t.Fatal("unexpected effect", args)
			return nil, nil
		}
	}
	if e := autoAdvanceMaintenance(ctx, a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if e := autoAdvanceMaintenance(ctx, a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	} // durable apply intent, no launch
	id := tx.ID
	authority := a.Maintenance.Operations[id].Authority
	generation := tx.Generation
	if e := autoAdvanceMaintenance(ctx, a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if tx.State != "cancelled" || !tx.StopConfirmed {
		t.Fatal("missing no-effect cancellation")
	}
	var reloaded autoRecord
	if e := json.Unmarshal(saved, &reloaded); e != nil {
		t.Fatal(e)
	}
	a = &reloaded
	tx = a.MaintenanceTransactions[id]
	if e := autoAdvanceMaintenance(ctx, a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if tx.ID != id || tx.Generation != generation+1 || tx.Phase != "backup" || len(tx.History) != 1 || a.Maintenance.Operations[id].State != "reserved" {
		t.Fatal("resume lost reservation")
	}
	old, _ := json.Marshal(authority)
	newA, _ := json.Marshal(a.Maintenance.Operations[id].Authority)
	if string(old) != string(newA) {
		t.Fatal("resume changed authority")
	}
}
func TestMaintenanceControllerOFFReadsTerminalCompensationBeforeReconcile(t *testing.T) {
	for _, terminal := range []string{"rolled_back", "rollback_conflict"} {
		t.Run(terminal, func(t *testing.T) {
			a, tx, reg := maintenanceControllerFixture(t)
			auth, _, gate, now := maintenanceFixture()
			auth, _ = autoReviewedMaintenanceAuthority(&tx.Candidate.Admission, *tx.Candidate.Validation, tx.Candidate.Review, auth.Backup)
			// Use actual candidate's before proof.
			backup := autoMaintenanceBackup{BeforeSHA: tx.Candidate.Admission.Pin.BeforeSHA, ReceiptSHA: autoSHA([]byte("backup")), RestoreProofSHA: autoSHA([]byte("restore")), OffboxReceiptSHA: autoSHA([]byte("offbox")), Verified: true}
			auth, e := autoReviewedMaintenanceAuthority(&tx.Candidate.Admission, *tx.Candidate.Validation, tx.Candidate.Review, backup)
			if e != nil {
				t.Fatal(e)
			}
			a.Maintenance = &autoMaintenanceLedger{}
			op, e := autoReserveMaintenance(a.Maintenance, auth, reg, gate, now)
			if e != nil {
				t.Fatal(e)
			}
			op.State = "apply_pending"
			req, ar, b, e := autoBuildMaintenanceExecution(&tx.Candidate.Admission, *tx.Candidate.Validation, tx.Candidate.Review, backup, tx.Generation)
			if e != nil {
				t.Fatal(e)
			}
			tx.Request = string(req)
			tx.Authority = string(ar)
			tx.Binding = b
			tx.Phase = "apply"
			tx.Started = true
			calls := []string{}
			io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(*autoMaintenanceTransaction) error { t.Fatal("must not launch for terminal journal"); return nil }, Call: func(_ context.Context, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == "server-maintenance-stop" {
					return json.Marshal(map[string]any{"state": "reconciliation_required", "operation_id": tx.ID, "generation": tx.Generation, "journal_state": terminal})
				}
				if args[0] != "server-maintenance-status" {
					t.Fatal(args)
				}
				return controllerReceipt(t, tx, terminal), nil
			}}
			if e = autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
				t.Fatal(e)
			}
			want := "restored"
			if terminal == "rollback_conflict" {
				want = "conflict"
			}
			if tx.State != want || op.State != want || len(calls) != 2 || op.ApplyReceiptSHA != "" {
				t.Fatalf("bad terminal reconciliation %s %s %v", tx.State, op.State, calls)
			}
		})
	}
}
func TestMaintenanceControllerDurableFilePublicationAndRejectForeign(t *testing.T) {
	_, tx, _ := maintenanceControllerFixture(t)
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, tx.Binding.OwnerJob), 0700); e != nil {
		t.Fatal(e)
	}
	if e := autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal(e)
	}
	if e := autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal(e)
	}
	name := filepath.Join(root, tx.Binding.OwnerJob, "server-maintenance", tx.ID+".backup.json")
	raw, e := os.ReadFile(name)
	if e != nil || string(raw) != tx.Request {
		t.Fatal(e)
	}
	tx.Request += " "
	tx.Binding.RequestSHA = autoSHA([]byte(tx.Request))
	if e = autoWriteMaintenanceTransaction(root, tx); e == nil {
		t.Fatal("accepted changed immutable request")
	}
}
func TestMaintenanceControllerFailedBackupCannotApply(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	calls := []string{}
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Call: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		if args[0] == "server-maintenance-stop" {
			return json.Marshal(map[string]any{"state": "stopped", "no_effects": true, "operation_id": tx.ID, "generation": tx.Generation})
		}
		return controllerReceipt(t, tx, "unavailable"), nil
	}, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		return nil, errors.New("must not observe")
	}}
	for i := 0; i < 3; i++ {
		if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
			t.Fatal(e)
		}
	}
	if tx.State != "cancelled" || len(calls) != 2 || a.Maintenance != nil || tx.Generation != 1 {
		t.Fatal("failed backup bypassed no-effect proof/backoff", calls, tx.State)
	}
	tx.RetryAt = time.Now().Add(-time.Second)
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if tx.Generation != 2 || tx.State != "retry_observe" || len(tx.History) != 1 || len(calls) != 2 {
		t.Fatal("temporary backup failure permanently stranded or launched without saved generation")
	}
}

func TestMaintenanceControllerWaitingReloadReusesSameExecution(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	calls := []string{}
	replies := []string{"running", "waiting", "offbox_restored_verified"}
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Call: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		state := replies[0]
		replies = replies[1:]
		return controllerReceipt(t, tx, state), nil
	}}
	id, request := tx.ID, tx.Request
	for i := 0; i < 3; i++ {
		if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(a)
		var copy autoRecord
		if e := json.Unmarshal(raw, &copy); e != nil {
			t.Fatal(e)
		}
		a = &copy
		tx = a.MaintenanceTransactions[id]
	}
	if strings.Join(calls, ",") != "server-maintenance-backup,server-maintenance-status,server-maintenance-backup" || tx.ID != id || tx.Request != request || tx.State != "observe" {
		t.Fatal("inactive helper stranded or changed identity", calls, tx.State)
	}
}
func TestMaintenanceControllerNewerReviewSuppressesOldApproval(t *testing.T) {
	a, tx, _ := maintenanceControllerFixture(t)
	a.MaintenanceTransactions = nil
	latest := autoFindJob(a, tx.Candidate.Review.TaskID)
	latest.Status = "running"
	if e := autoQueueReviewedMaintenance(a); e != nil || len(a.MaintenanceTransactions) != 0 {
		t.Fatal("queued before authoritative completion", e)
	}
	latest.Status = "done"
	later := *latest
	later.ID = "99999999-9999-4999-8999-999999999999"
	later.Rejected = true
	a.Jobs = append(a.Jobs, &later)
	if e := autoQueueReviewedMaintenance(a); e != nil || len(a.MaintenanceTransactions) != 0 {
		t.Fatal("old approval bypassed newer process", e)
	}
	a.MaintenanceTransactions = map[string]*autoMaintenanceTransaction{tx.ID: tx}
	if e := autoQueueReviewedMaintenance(a); e != nil || a.MaintenanceTransactions[tx.ID] != tx {
		t.Fatal("replaced already reserved authority", e)
	}
}
func TestMaintenanceControllerCancellationPublishesEveryPhaseGeneration(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, tx.Binding.OwnerJob), 0700)
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(v *autoMaintenanceTransaction) error { return autoWriteMaintenanceTransaction(root, v) }, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		return &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "old", CurrentTasks: 1, CapturedAt: time.Now()}, nil
	}}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "server-maintenance-stop" {
			return json.Marshal(map[string]any{"state": "stopped", "no_effects": true, "operation_id": tx.ID, "generation": tx.Generation})
		}
		return controllerReceipt(t, tx, "offbox_restored_verified"), nil
	}
	for i := 0; i < 2; i++ {
		if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
			t.Fatal(e)
		}
	}
	// Simulate crash after publishing apply inputs but BEFORE runner launch.
	if e := autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal(e)
	}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	} // renewed target observation
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	} // second backup
	if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
		t.Fatal(e)
	} // second apply intent
	if e := autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal(e)
	}
	for _, phase := range []string{"backup", "apply"} {
		raw, e := os.ReadFile(filepath.Join(root, tx.Binding.OwnerJob, "server-maintenance", tx.ID+"."+phase+".json"))
		if e != nil {
			t.Fatal(e)
		}
		var req map[string]any
		json.Unmarshal(raw, &req)
		if req["generation"] != float64(2) {
			t.Fatal("old phase pointer stranded renewal", phase)
		}
	}
}
func TestMaintenanceControllerStaleStateAndLowHeadroomNeverApply(t *testing.T) {
	for _, kind := range []string{"configuration", "headroom"} {
		t.Run(kind, func(t *testing.T) {
			a, tx, reg := maintenanceControllerFixture(t)
			calls := 0
			io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Call: func(context.Context, ...string) ([]byte, error) {
				calls++
				return controllerReceipt(t, tx, "offbox_restored_verified"), nil
			}, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
				o := &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "old", CurrentTasks: 1, CapturedAt: time.Now()}
				if kind == "configuration" {
					o.ConfigurationSHA = autoSHA([]byte("external"))
				} else {
					o.RSSBytes = 256 << 20
				}
				return o, nil
			}}
			if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
				t.Fatal(e)
			}
			if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e == nil {
				t.Fatal("unsafe beforestate admitted")
			}
			if calls != 1 || tx.Phase == "apply" {
				t.Fatal("apply despite stale/headroom failure")
			}
		})
	}
}

func TestMaintenanceControllerFailedObservationRetiresBeforeFreshRetry(t *testing.T) {
	s := autoTestServer(t)
	a, tx, _ := maintenanceControllerFixture(t)
	root := t.TempDir()
	ids := []string{}
	stopped := false
	call := func(_ context.Context, args ...string) ([]byte, error) {
		r := tx.Observation
		if args[0] == "server-observe-stop" {
			stopped = true
			return json.Marshal(map[string]any{"state": "stopped", "owner_job": r.OwnerJob, "request_id": r.RequestID})
		}
		if args[0] != "server-observe" {
			t.Fatal(args)
		}
		ids = append(ids, r.OwnerJob)
		raw, _ := json.Marshal(r)
		return json.Marshal(map[string]any{"schema_version": 1, "request_id": r.RequestID, "owner_job": r.OwnerJob, "owner_task": r.OwnerTask, "target_id": r.TargetID, "registry_sha256": r.RegistrySHA, "request_sha256": autoSHA(raw), "mutation_performed": false, "state": "unavailable"})
	}
	if _, e := s.autoMaintenanceObserve(context.Background(), root, a, tx, call); e != nil {
		t.Fatal(e)
	}
	if !stopped || tx.Observation != nil || len(tx.ObservationHistory) != 1 || len(tx.ObservationHistory[0].Receipt) == 0 {
		t.Fatal("failed collector not retired with evidence")
	}
	if _, e := s.autoMaintenanceObserve(context.Background(), root, a, tx, call); e != nil {
		t.Fatal(e)
	}
	if len(ids) != 1 {
		t.Fatal("retry ignored cooldown")
	}
	tx.ObservationRetryAt = time.Now().Add(-time.Second)
	if _, e := s.autoMaintenanceObserve(context.Background(), root, a, tx, call); e != nil {
		t.Fatal(e)
	}
	if len(ids) != 2 || ids[0] == ids[1] || ids[0] == tx.Candidate.Admission.JobID {
		t.Fatal("reused permanently tombstoned observer UUID")
	}
}
func TestMaintenanceControllerOFFStopsObservationBeforeRenewal(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	req, e := autoNewServerObservation(&autoJob{ID: "88888888-8888-4888-8888-888888888888", TaskID: tx.Candidate.Admission.TaskID}, tx.Candidate.Admission.Pin.TargetID, reg.Digest, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	tx.Observation = &req
	tx.ObservationStarted = true
	calls := []string{}
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Call: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		if args[0] == "server-observe-stop" {
			return json.Marshal(map[string]any{"state": "stopped", "owner_job": req.OwnerJob, "request_id": req.RequestID})
		}
		return json.Marshal(map[string]any{"state": "stopped", "no_effects": true, "operation_id": tx.ID, "generation": tx.Generation})
	}}
	if e = autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
		t.Fatal(e)
	}
	if strings.Join(calls, ",") != "server-observe-stop,server-maintenance-stop" || !tx.ObservationStopped || !tx.StopConfirmed {
		t.Fatal("observer left unowned", calls)
	}
}

func TestMaintenanceControllerSealedPreeffectJournalRenewsWithoutFakeStop(t *testing.T) {
	for _, failedState := range []string{"unavailable", "cancelled"} {
		t.Run(failedState, func(t *testing.T) {
			a, tx, reg := maintenanceControllerFixture(t)
			saved := []byte(nil)
			io := autoMaintenanceControllerIO{Save: func() error { saved, _ = json.Marshal(a); return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
				return &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "before", CurrentTasks: 1, CapturedAt: time.Now()}, nil
			}}
			io.Call = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "server-maintenance-backup" {
					return controllerReceipt(t, tx, "offbox_restored_verified"), nil
				}
				if args[0] == "server-maintenance-stop" {
					return json.Marshal(map[string]any{"state": "reconciliation_required", "no_effects": false, "journal_state": failedState, "operation_id": tx.ID, "generation": tx.Generation})
				}
				if args[0] != "server-maintenance-apply" {
					t.Fatal("unexpected restart", args)
				}
				var m map[string]any
				json.Unmarshal(controllerReceipt(t, tx, "applied"), &m)
				inner := m["result"].(map[string]any)
				inner["state"] = failedState
				inner["no_effects"] = true
				inner["effects_started"] = false
				delete(inner, "applied_state_sha256")
				delete(inner, "invocation_id")
				delete(inner, "observations")
				m["state"] = failedState
				m["result"] = json.RawMessage(executionSeal(t, inner))
				return executionSeal(t, m), nil
			}
			for i := 0; i < 3; i++ {
				if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
					t.Fatal(e)
				}
			}
			if tx.State != "cancelled" || tx.StopConfirmed || len(tx.PreeffectReceipt) == 0 {
				t.Fatal("no-effect journal not retained")
			}
			id := tx.ID
			var restored autoRecord
			if e := json.Unmarshal(saved, &restored); e != nil {
				t.Fatal(e)
			}
			a = &restored
			tx = a.MaintenanceTransactions[id]
			if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
				t.Fatal(e)
			}
			if !tx.StopConfirmed || tx.Generation != 1 {
				t.Fatal("OFF renewed or lost terminal journal")
			}
			tx.RetryAt = time.Now().Add(-time.Second)
			if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
				t.Fatal(e)
			}
			if tx.ID != id || tx.Generation != 2 || a.Maintenance.Operations[id].State != "reserved" || len(tx.History[0].PreeffectReceipt) == 0 {
				t.Fatal("same reservation renewal lost journal")
			}
		})
	}
}

func TestMaintenanceControllerOFFAfterAppliedLaunchesPendingCompensation(t *testing.T) {
	a, tx, reg := maintenanceControllerFixture(t)
	calls := []string{}
	io := autoMaintenanceControllerIO{Save: func() error { return nil }, Write: func(*autoMaintenanceTransaction) error { return nil }, Observe: func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		return &autoMaintenanceObservation{RegistrySHA: reg.Digest, ConfigurationSHA: tx.Candidate.Admission.Pin.BeforeSHA, InvocationID: "before", CurrentTasks: 1, CapturedAt: time.Now()}, nil
	}}
	io.Call = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		switch args[0] {
		case "server-maintenance-backup":
			return controllerReceipt(t, tx, "offbox_restored_verified"), nil
		case "server-maintenance-stop":
			return json.Marshal(map[string]any{"state": "reconciliation_required", "no_effects": false, "journal_state": "applied", "operation_id": tx.ID, "generation": tx.Generation})
		case "server-maintenance-status":
			if tx.Phase != "apply" {
				t.Fatal("polled nonexistent reconcile stage")
			}
			return controllerReceipt(t, tx, "applied"), nil
		case "server-maintenance-reconcile":
			return controllerReceipt(t, tx, "rolled_back"), nil
		}
		t.Fatal(args)
		return nil, nil
	}
	for i := 0; i < 2; i++ {
		if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, true, time.Now(), io); e != nil {
			t.Fatal(e)
		}
	}
	// The helper completed its effect after launch; controller crashed before
	// receiving it. OFF must inspect that receipt and preserve a new reconcile
	// launch across another tick/reload, despite repeated stop replies.
	tx.Started = true
	for i := 0; i < 2; i++ {
		if e := autoAdvanceMaintenance(context.Background(), a, tx, reg, false, time.Now(), io); e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(a)
		var loaded autoRecord
		json.Unmarshal(raw, &loaded)
		a = &loaded
		tx = a.MaintenanceTransactions[tx.ID]
	}
	if tx.State != "restored" || a.Maintenance.Operations[tx.ID].State != "restored" || calls[len(calls)-1] != "server-maintenance-reconcile" {
		t.Fatal("OFF compensation stranded", tx.State, calls)
	}
}

func TestMaintenanceControllerSkippedPhaseGenerationRetainsExactPointer(t *testing.T) {
	_, tx, _ := maintenanceControllerFixture(t)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, tx.Binding.OwnerJob), 0700)
	backup := autoMaintenanceBackup{BeforeSHA: tx.Candidate.Admission.Pin.BeforeSHA, ReceiptSHA: autoSHA([]byte("backup")), RestoreProofSHA: autoSHA([]byte("restore")), OffboxReceiptSHA: autoSHA([]byte("offbox")), Verified: true}
	raw, authority, b, e := autoBuildMaintenanceExecution(&tx.Candidate.Admission, *tx.Candidate.Validation, tx.Candidate.Review, backup, 1)
	if e != nil {
		t.Fatal(e)
	}
	first := string(raw)
	tx.Phase = "apply"
	tx.Request = first
	tx.Authority = string(authority)
	tx.Binding = b
	if e = autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal(e)
	}
	tx.History = []autoMaintenanceTransactionGeneration{{Generation: 1, PhaseRequests: map[string]string{"apply": first}, Authority: string(authority)}, {Generation: 2, PhaseRequests: map[string]string{"backup": "retained generation2 backup"}}}
	raw, authority, b, e = autoBuildMaintenanceExecution(&tx.Candidate.Admission, *tx.Candidate.Validation, tx.Candidate.Review, backup, 3)
	if e != nil {
		t.Fatal(e)
	}
	tx.Generation = 3
	tx.Request = string(raw)
	tx.Authority = string(authority)
	tx.Binding = b
	if e = autoWriteMaintenanceTransaction(root, tx); e != nil {
		t.Fatal("cancelled generation without apply stranded older apply pointer", e)
	}
}
