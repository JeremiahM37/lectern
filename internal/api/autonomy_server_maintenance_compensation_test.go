package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMaintenanceCompensationBeforeApplyReceiptSurvivesReload(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	ledger := &autoMaintenanceLedger{}
	o, err := autoReserveMaintenance(ledger, a, r, g, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); err != nil {
		t.Fatal(err)
	}
	// Controller died after intent; helper installed, failed health, then restored.
	raw, _ := json.Marshal(ledger)
	ledger = &autoMaintenanceLedger{}
	if err = json.Unmarshal(raw, ledger); err != nil {
		t.Fatal(err)
	}
	o = ledger.Operations[o.ID]
	autoRequestMaintenanceStop(o)
	receipt := autoMaintenanceCompensationReceipt{o.ID, autoSHA([]byte("actual restored journal")), a.BeforeSHA, a.CandidateSHA, a.BeforeSHA, true}
	if err = autoRecordMaintenanceCompensation(o, receipt); err != nil {
		t.Fatal(err)
	}
	if o.State != "restored" || o.ApplyReceiptSHA != "" || o.AppliedInvocation != "" || o.AppliedStateSHA != "" || o.HealthReceiptSHA != "" {
		t.Fatalf("invented successful deployment: %+v", o)
	}
	if err = autoRecordMaintenanceCompensation(o, receipt); err != nil {
		t.Fatal("duplicate", err)
	}
	if err = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); err == nil {
		t.Fatal("reapplied failed candidate")
	}
	receipt.ReceiptSHA = autoSHA([]byte("replacement history"))
	if autoRecordMaintenanceCompensation(o, receipt) == nil {
		t.Fatal("replaced evidence")
	}
}

func TestMaintenanceCompensationRejectsWrongOrUnownedEvidence(t *testing.T) {
	for _, bad := range []string{"operation", "candidate", "before", "restored", "unhealthy", "reserved", "verified", "conflict"} {
		t.Run(bad, func(t *testing.T) {
			a, r, g, now := maintenanceFixture()
			o, err := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); err != nil {
				t.Fatal(err)
			}
			receipt := autoMaintenanceCompensationReceipt{o.ID, autoSHA([]byte("journal")), a.BeforeSHA, a.CandidateSHA, a.BeforeSHA, true}
			switch bad {
			case "operation":
				receipt.OperationID = autoSHA([]byte("other"))
			case "candidate":
				receipt.CandidateSHA = autoSHA([]byte("other"))
			case "before":
				receipt.BeforeSHA = autoSHA([]byte("other"))
			case "restored":
				receipt.RestoredSHA = autoSHA([]byte("other"))
			case "unhealthy":
				receipt.Healthy = false
			default:
				o.State = bad
			}
			before, _ := json.Marshal(o)
			if autoRecordMaintenanceCompensation(o, receipt) == nil {
				t.Fatal("accepted bad proof")
			}
			after, _ := json.Marshal(o)
			if string(before) != string(after) {
				t.Fatal("failed proof changed ledger")
			}
		})
	}
}

func TestMaintenanceOffPreservesCommittedHistoricalResult(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	o, err := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); err != nil {
		t.Fatal(err)
	}
	applied := maintenanceApplied(o)
	if err = autoRecordMaintenanceApply(o, applied); err != nil {
		t.Fatal(err)
	}
	health := autoMaintenanceHealthReceipt{o.ID, autoSHA([]byte("healthy")), a.CandidateSHA, applied.InvocationID, true, true, true}
	if err = autoFinishMaintenanceHealth(o, health); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(o)
	autoRequestMaintenanceStop(o)
	after, _ := json.Marshal(o)
	if string(before) != string(after) || o.State != "verified" {
		t.Fatal("OFF altered committed verified change")
	}
}

func TestMaintenanceCancelledRenewalRetainsAuthorityAndRequiresFreshGate(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	o, err := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
	if err != nil {
		t.Fatal(err)
	}
	autoRequestMaintenanceStop(o)
	authority, _ := json.Marshal(o.Authority)
	id := o.ID
	receipt := autoSHA([]byte("confirmed preeffect cancellation"))
	if err = autoCancelMaintenanceBeforeEffects(o, receipt, true); err != nil {
		t.Fatal(err)
	}
	off := g
	off.Enabled = false
	if autoResumeMaintenanceCancelled(o, receipt, r, off, now) == nil {
		t.Fatal("renewed while OFF")
	}
	stale := g
	stale.QuotaObservedAt = now.Add(-7 * time.Minute)
	if autoResumeMaintenanceCancelled(o, receipt, r, stale, now) == nil {
		t.Fatal("renewed stale quota")
	}
	if err = autoResumeMaintenanceCancelled(o, receipt, r, g, now); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(o.Authority)
	if o.ID != id || string(authority) != string(after) || o.State != "reserved" || o.StopRequested || len(o.CancellationHistory) != 1 {
		t.Fatal("renewal lost original ownership")
	}
	autoRequestMaintenanceStop(o)
	if autoResumeMaintenanceCancelled(o, receipt, r, g, now) == nil {
		t.Fatal("reused prior cancellation proof")
	}
	o.AppliedStateSHA = autoSHA([]byte("effect"))
	if autoResumeMaintenanceCancelled(o, autoSHA([]byte("new cancellation")), r, g, now) == nil {
		t.Fatal("renewed after effect")
	}
}

func TestMaintenancePendingIntentRequiresNoEffectProofToCancel(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	o, err := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); err != nil {
		t.Fatal(err)
	}
	autoRequestMaintenanceStop(o)
	receipt := autoSHA([]byte("launch locked no journal proof"))
	if autoCancelMaintenanceBeforeEffects(o, receipt, false) == nil || o.State != "apply_pending" {
		t.Fatal("stopped process treated as no effects")
	}
	if autoResumeMaintenanceCancelled(o, receipt, r, g, now) == nil {
		t.Fatal("renewed ambiguous pending intent")
	}
	if err = autoCancelMaintenanceBeforeEffects(o, receipt, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(o)
	reloaded := &autoMaintenanceOperation{}
	if err = json.Unmarshal(raw, reloaded); err != nil {
		t.Fatal(err)
	}
	if err = autoResumeMaintenanceCancelled(reloaded, receipt, r, g, now); err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != o.ID || len(reloaded.CancellationHistory) != 1 {
		t.Fatal("lost cancellation provenance")
	}
}
