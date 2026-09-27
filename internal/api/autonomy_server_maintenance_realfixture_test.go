package api

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Opt-in root-run disposable adapter only. Fixture registry has no production
// targets. Audit/review authority is explicitly synthetic; all execution is real.
func TestMaintenanceRealControllerFixture(t *testing.T) {
	base := os.Getenv("LECTERN_MAINTENANCE_REAL_FIXTURE")
	if base == "" {
		t.Skip("disposable fixture not supplied")
	}
	if os.Geteuid() != 0 {
		t.Fatal("fixture must run as root")
	}
	var cfg struct{ RegistrySHA, BeforeSHA, Adapter string }
	raw, e := os.ReadFile(filepath.Join(base, "controller-config.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &cfg); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	call := func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/python3", append([]string{"-I", "-S", cfg.Adapter}, args...)...)
		return cmd.Output()
	}
	observe := func(ctx context.Context, _ *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
		raw, e := call(ctx, "observe")
		if e != nil {
			return nil, e
		}
		var o autoMaintenanceObservation
		e = json.Unmarshal(raw, &o)
		return &o, e
	}
	limits := autoMaintenanceLimits{CPUPercent: 50, MemoryBytes: 256 << 20, Tasks: 64}
	pin := autoMaintenancePlanPin{ProjectID: 1, TargetID: "fixture", ServiceID: "sensor", RegistrySHA: cfg.RegistrySHA, BeforeSHA: cfg.BeforeSHA, CandidateSHA: autoMaintenanceCandidate(limits), Limits: limits, Acceptance: []string{"disposable sensor remains healthy under caps"}, CapturedAt: time.Now()}
	pin.Key = autoMaintenancePinHash(pin)
	ad := autoMaintenanceAdmitted{Pin: pin, TaskID: 1, JobID: "11111111-1111-4111-8111-111111111111"}
	for i, id := range []string{"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"} {
		ad.Audits[i] = autoMaintenanceEvidence{TaskID: int64(i + 3), JobID: id, ReceiptSHA: autoSHA([]byte(id)), BindingSHA: autoMaintenanceBinding(autoMaintenancePinAuthority(pin)), Successful: true}
	}
	a := &autoRecord{Config: autonomy.DefaultConfig()}
	a.Config.Enabled = true
	expertQuota(a, time.Now())
	reviewer := &autoJob{ID: "22222222-2222-4222-8222-222222222222", TaskID: 2, Role: "reviewer", Status: "running", MaintenanceAdmission: &ad, MaintenancePin: pin.Key}
	lease, e := autoReserveMaintenanceValidation(a, reviewer)
	if e != nil {
		t.Fatal(e)
	}
	jobs := filepath.Join(base, "jobs")
	for _, id := range []string{ad.JobID, reviewer.ID} {
		if e = os.MkdirAll(filepath.Join(jobs, id), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e = autoWriteMaintenanceValidation(jobs, lease); e != nil {
		t.Fatal(e)
	}
	args := []string{"server-maintenance-validate", "--job", lease.OwnerJob, "--operation-id", lease.OperationID, "--generation", "1"}
	var validation *autoMaintenanceValidationReceipt
	for {
		raw, e = call(ctx, args...)
		if e != nil {
			t.Fatal(e)
		}
		state, v, err := autoDecodeMaintenanceValidation(raw, lease)
		if err != nil {
			t.Fatal(err, string(raw))
		}
		if state == "validated" {
			validation = v
			lease.Validation = v
			lease.ReceiptRaw = raw
			lease.State = state
			break
		}
		if state != "running" && state != "waiting" {
			t.Fatal(state, string(raw))
		}
		args[0] = "server-maintenance-status"
		if len(args) == 7 {
			args = append(args, "--phase", "validate")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	review := autoMaintenanceFinalReview{TaskID: 2, JobID: reviewer.ID, PinSHA: pin.Key, ValidationSHA: validation.ReceiptSHA, ArchiveSHA: autoSHA([]byte("synthetic reviewed archive")), ReportSHA: autoSHA([]byte("synthetic independent approval")), Approved: true}
	reviewer.Status = "done"
	a.Jobs = []*autoJob{{ID: ad.JobID, TaskID: ad.TaskID, Role: "builder", Status: "done", Provider: "codex"}, reviewer}
	a.MaintenanceReviews = map[string]*autoMaintenanceReviewedCandidate{"synthetic": {ID: "synthetic", State: "candidate_reviewed", Admission: ad, Review: review, Report: "synthetic independent approval", Validation: validation, ValidationLease: lease}}
	if e = autoQueueReviewedMaintenance(a); e != nil {
		t.Fatal(e)
	}
	tx := a.MaintenanceTransactions[autoMaintenanceOperationID(pin)]
	if tx == nil {
		t.Fatal("queue missing")
	}
	b := tx.Binding
	jobs = filepath.Join(base, "jobs")
	registry := autoMaintenanceRegistry{ResourceID: "fixture/sensor", Digest: cfg.RegistrySHA, Stateless: true, Min: autoMaintenanceLimits{CPUPercent: 50, MemoryBytes: 256 << 20, Tasks: 64}, Max: autoMaintenanceLimits{CPUPercent: 200, MemoryBytes: 2 << 30, Tasks: 512}, MemoryHeadroomBytes: 32 << 20}
	statePath := filepath.Join(base, "controller-state.json")
	save := func() error {
		raw, e := json.Marshal(a)
		if e != nil {
			return e
		}
		return os.WriteFile(statePath, raw, 0600)
	}
	reload := func() {
		raw, e := os.ReadFile(statePath)
		if e != nil {
			t.Fatal(e)
		}
		var next autoRecord
		if e = json.Unmarshal(raw, &next); e != nil {
			t.Fatal(e)
		}
		a = &next
		tx = a.MaintenanceTransactions[b.OperationID]
	}
	io := autoMaintenanceControllerIO{Save: save, Call: call, Observe: observe, Write: func(tx *autoMaintenanceTransaction) error { return autoWriteMaintenanceTransaction(jobs, tx) }}
	advance := func(enabled bool) {
		t.Helper()
		expertQuota(a, time.Now())
		if e := autoAdvanceMaintenance(ctx, a, tx, registry, enabled, time.Now(), io); e != nil {
			t.Fatal(e, tx.State, tx.Phase)
		}
		if e = save(); e != nil {
			t.Fatal(e)
		}
		reload()
	}
	if e = save(); e != nil {
		t.Fatal(e)
	}
	until := func(predicate func() bool, enabled bool) {
		t.Helper()
		for !predicate() {
			advance(enabled)
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	until(func() bool { return tx.Phase == "apply" && !tx.Started }, true)
	firstBackupSHA := tx.Backup.ReceiptSHA
	// Persisted apply intent, but no runner effect: OFF must retain one reservation.
	advance(false)
	if tx.State != "cancelled" || !tx.StopConfirmed {
		t.Fatal("pre-effect OFF not confirmed", tx.State)
	}
	advance(true)
	if tx.Generation != 2 {
		t.Fatal("same reservation did not renew", tx.Generation)
	}
	until(func() bool { return tx.Phase == "apply" && !tx.Started }, true)
	if tx.Backup.ReceiptSHA != firstBackupSHA {
		t.Fatal("renewal changed immutable backup authority")
	}
	advance(true)
	// Do not let controller ingest success yet: simulate crash after real effect.
	for {
		raw, e = call(ctx, "server-maintenance-status", "--job", tx.Binding.OwnerJob, "--operation-id", tx.ID, "--phase", "apply", "--generation", "2")
		if e != nil {
			t.Fatal(e)
		}
		r, err := autoDecodeMaintenanceExecution(raw, tx.Binding)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == "applied" {
			break
		}
		if r.State != "running" && r.State != "waiting" {
			t.Fatal(r.State, string(raw))
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	reload()
	until(func() bool { return tx.State == "restored" }, false)
	if len(tx.History) != 1 || a.Maintenance.Operations[tx.ID].State != "restored" {
		t.Fatal("history or restoration lost")
	}
	current, e := observe(ctx, tx)
	if e != nil || current.ConfigurationSHA != cfg.BeforeSHA {
		t.Fatal("before state not restored", e)
	}
	proof := map[string]any{"state": "PASS", "scope": "real Go controller serializer/decoder/reload + actual bounded runner, disposable systemd target and non-expiring offbox restore; synthetic admission/audits/review, not live authority", "operation_id": tx.ID, "generation": tx.Generation, "state_file": statePath, "final_state": tx.State, "validation_receipt": validation.ReceiptSHA, "receipts": tx.Receipts}
	raw, _ = json.MarshalIndent(proof, "", "  ")
	if e = os.WriteFile(filepath.Join(base, "controller-proof.json"), raw, 0644); e != nil {
		t.Fatal(e)
	}
}
