package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"testing"
	"time"
)

func maintenanceFixture() (autoMaintenanceAuthority, autoMaintenanceRegistry, autoMaintenanceGate, time.Time) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	r := autoMaintenanceRegistry{ResourceID: "local/temp-api", Digest: autoSHA([]byte("registry")), Stateless: true, Min: autoMaintenanceLimits{10, 128 << 20, 16}, Max: autoMaintenanceLimits{200, 2 << 30, 512}, MemoryHeadroomBytes: 64 << 20}
	a := autoMaintenanceAuthority{ExecutedValidationSHA: autoSHA([]byte("executed validation")), CandidateReviewSHA: autoSHA([]byte("candidate review")), PlanSHA: autoSHA([]byte("audited plan")), TaskID: 1, JobID: "11111111-1111-4111-8111-111111111111", ResourceID: r.ResourceID, RegistrySHA: r.Digest, BeforeSHA: autoSHA([]byte("before")), Limits: autoMaintenanceLimits{50, 256 << 20, 64}}
	a.CandidateSHA = autoMaintenanceCandidate(a.Limits)
	b := autoMaintenanceBinding(a)
	a.Audits = [2]autoMaintenanceEvidence{{autoSHA([]byte("auditA")), b, 2, "22222222-2222-4222-8222-222222222222", true}, {autoSHA([]byte("auditB")), b, 3, "33333333-3333-4333-8333-333333333333", true}}
	a.Validation = autoMaintenanceEvidence{autoSHA([]byte("validation")), b, 4, "44444444-4444-4444-8444-444444444444", true}
	a.Backup = autoMaintenanceBackup{autoSHA([]byte("backup")), a.BeforeSHA, autoSHA([]byte("restored")), autoSHA([]byte("offbox")), true}
	return a, r, autoMaintenanceGate{Config: autonomy.DefaultConfig(), Enabled: true, QuotaObservedAt: now, RemainingPercent: 50}, now
}
func maintenanceObservation(a autoMaintenanceAuthority, now time.Time) autoMaintenanceObservation {
	return autoMaintenanceObservation{RegistrySHA: a.RegistrySHA, ConfigurationSHA: a.BeforeSHA, InvocationID: "before-invocation", RSSBytes: 32 << 20, CurrentTasks: 8, CapturedAt: now}
}
func maintenanceApplied(o *autoMaintenanceOperation) autoMaintenanceApplyReceipt {
	return autoMaintenanceApplyReceipt{AppliedStateSHA: autoSHA([]byte("installed config")), OperationID: o.ID, ReceiptSHA: autoSHA([]byte("applied")), BeforeSHA: o.Authority.BeforeSHA, CandidateSHA: o.Authority.CandidateSHA, InvocationID: "new-invocation"}
}
func TestMaintenanceDurableApplyOffCompensationAndDuplicate(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	l := &autoMaintenanceLedger{}
	o, e := autoReserveMaintenance(l, a, r, g, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); e != nil {
		t.Fatal(e)
	}
	// Crash after persisted intent, before knowing whether the helper committed.
	raw, _ := json.Marshal(l)
	l = &autoMaintenanceLedger{}
	if e = json.Unmarshal(raw, l); e != nil {
		t.Fatal(e)
	}
	o = l.Operations[o.ID]
	autoRequestMaintenanceStop(o)
	if o.State != "apply_pending" {
		t.Fatal("OFF guessed that pending host effect did not occur")
	}
	applied := maintenanceApplied(o)
	if e = autoRecordMaintenanceApply(o, applied); e != nil {
		t.Fatal(e)
	}
	if o.State != "rollback_pending" {
		t.Fatal(o.State)
	}
	if e = autoRecordMaintenanceApply(o, applied); e != nil {
		t.Fatal("duplicate effect receipt", e)
	}
	obs := maintenanceObservation(a, now)
	obs.ConfigurationSHA = applied.AppliedStateSHA
	obs.InvocationID = applied.InvocationID
	if e = autoBeginMaintenanceRollback(o, obs); e != nil {
		t.Fatal(e)
	}
	rollback := autoMaintenanceRollbackReceipt{o.ID, autoSHA([]byte("rollback")), a.BeforeSHA, true}
	if e = autoRecordMaintenanceRollback(o, rollback); e != nil {
		t.Fatal(e)
	}
	g.Enabled = false
	again, e := autoReserveMaintenance(l, a, r, g, now)
	if e != nil || again != o || o.State != "restored" {
		t.Fatal("duplicate did not reconcile retained operation", e)
	}
	if autoBeginMaintenance(o, obs, r, g, now) == nil {
		t.Fatal("reapplied terminal operation")
	}
}
func TestMaintenanceRejectsUntrustedOrIncompleteAuthority(t *testing.T) {
	for _, which := range []string{"same_auditor", "wrong_binding", "backup_unverified", "backup_no_restore", "no_offbox", "own_validation", "protected", "bounds", "quota", "stale_quota"} {
		t.Run(which, func(t *testing.T) {
			a, r, g, now := maintenanceFixture()
			switch which {
			case "same_auditor":
				a.Audits[1] = a.Audits[0]
			case "wrong_binding":
				a.Audits[0].BindingSHA = autoSHA([]byte("different"))
			case "backup_unverified":
				a.Backup.Verified = false
			case "backup_no_restore":
				a.Backup.RestoreProofSHA = ""
			case "no_offbox":
				a.Backup.OffboxReceiptSHA = ""
			case "own_validation":
				a.Validation.TaskID = a.TaskID
			case "protected":
				r.Protected = true
			case "bounds":
				r.Max.MemoryBytes = 1
			case "quota":
				g.RemainingPercent = 10
			case "stale_quota":
				g.QuotaObservedAt = now.Add(-7 * time.Minute)
			}
			if _, e := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now); e == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
}
func TestMaintenanceConflictsPreserveExternalWorkAndExclusiveOwner(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	l := &autoMaintenanceLedger{}
	o, e := autoReserveMaintenance(l, a, r, g, now)
	if e != nil {
		t.Fatal(e)
	}
	obs := maintenanceObservation(a, now)
	obs.ConfigurationSHA = autoSHA([]byte("human change"))
	if autoBeginMaintenance(o, obs, r, g, now) == nil || o.State != "stale" {
		t.Fatal("overwrote stale before state")
	}
	// A pre-effect stale reservation is terminal and may be replaced by a fresh audited plan.
	a.BeforeSHA = obs.ConfigurationSHA
	a.Backup.BeforeSHA = a.BeforeSHA
	b := autoMaintenanceBinding(a)
	for i := range a.Audits {
		a.Audits[i].BindingSHA = b
	}
	a.Validation.BindingSHA = b
	if _, e = autoReserveMaintenance(l, a, r, g, now); e != nil {
		t.Fatal("stale pre-effect operation stranded resource", e)
	}
	a, r, g, now = maintenanceFixture()
	o, e = autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); e != nil {
		t.Fatal(e)
	}
	applied := maintenanceApplied(o)
	if e = autoRecordMaintenanceApply(o, applied); e != nil {
		t.Fatal(e)
	}
	health := autoMaintenanceHealthReceipt{o.ID, autoSHA([]byte("health")), a.CandidateSHA, applied.InvocationID, true, false, true}
	if e = autoFinishMaintenanceHealth(o, health); e != nil || o.State != "rollback_pending" {
		t.Fatal("HTTP healthy hid stale health samples", e)
	}
	obs = maintenanceObservation(a, now)
	obs.ConfigurationSHA = autoSHA([]byte("newer external config"))
	obs.InvocationID = applied.InvocationID
	if autoBeginMaintenanceRollback(o, obs) == nil || o.State != "conflict" {
		t.Fatal("rollback would clobber external change")
	}
}
func TestMaintenanceMetricsDoNotInvalidateCASButHeadroomAndFreshnessMatter(t *testing.T) {
	for _, bad := range []string{"none", "rss", "tasks", "stale", "off"} {
		t.Run(bad, func(t *testing.T) {
			a, r, g, now := maintenanceFixture()
			o, e := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
			if e != nil {
				t.Fatal(e)
			}
			obs := maintenanceObservation(a, now)
			obs.RSSBytes = 48 << 20
			switch bad {
			case "tasks":
				obs.CurrentTasks = 33
			case "rss":
				obs.RSSBytes = 250 << 20
			case "stale":
				obs.CapturedAt = now.Add(-time.Minute)
			case "off":
				g.Enabled = false
			}
			e = autoBeginMaintenance(o, obs, r, g, now)
			if (e == nil) != (bad == "none") {
				t.Fatalf("unexpected begin result %v", e)
			}
		})
	}
}

func TestMaintenanceExternalConflictRequiresEvidenceAndDoesNotClaimRestoration(t *testing.T) {
	a, r, g, now := maintenanceFixture()
	o, e := autoReserveMaintenance(&autoMaintenanceLedger{}, a, r, g, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = autoBeginMaintenance(o, maintenanceObservation(a, now), r, g, now); e != nil {
		t.Fatal(e)
	}
	applied := maintenanceApplied(o)
	if e = autoRecordMaintenanceApply(o, applied); e != nil {
		t.Fatal(e)
	}
	autoRequestMaintenanceStop(o)
	external := maintenanceObservation(a, now)
	external.ConfigurationSHA = autoSHA([]byte("external"))
	external.InvocationID = "external-invocation"
	if autoBeginMaintenanceRollback(o, external) == nil {
		t.Fatal("external edit not detected")
	}
	o.ConflictReceiptSHA = autoSHA([]byte("conflict"))
	proof := autoMaintenanceExternalHealthReceipt{ConflictReceiptSHA: o.ConflictReceiptSHA, OwnershipChecked: true, OperationID: o.ID, ReceiptSHA: autoSHA([]byte("proof")), RegistrySHA: r.Digest, CurrentStateSHA: external.ConfigurationSHA, PostStateSHA: external.ConfigurationSHA, InvocationID: external.InvocationID, PostInvocationID: external.InvocationID, ObservedAt: now, CompletedAt: now, Profile: autoMaintenanceExternalProfile, NoMutation: true}
	if autoResolveMaintenanceConflict(o, proof, now) == nil {
		t.Fatal("unhealthy external state released ownership")
	}
	proof.Healthy = true
	if e = autoResolveMaintenanceConflict(o, proof, now); e != nil {
		t.Fatal(e)
	}
	if o.State != "superseded" || o.RollbackReceiptSHA != "" {
		t.Fatal("fabricated restoration")
	}
}

func TestMaintenanceQuotaUsesConfiguredReserveAndMargin(t *testing.T) {
	_, _, g, now := maintenanceFixture()
	for _, pct := range []float64{10, 14.99, 15, 15.01} {
		g.RemainingPercent = pct
		if (autoMaintenanceQuota(g, now) == nil) != (pct > 15) {
			t.Fatalf("wrong margin at %v", pct)
		}
	}
	g.Config.ReservePercent = 20
	g.Config.MarginPercent = 7
	g.RemainingPercent = 26
	if autoMaintenanceQuota(g, now) == nil {
		t.Fatal("ignored stricter configured threshold")
	}
	g.RemainingPercent = 28
	if e := autoMaintenanceQuota(g, now); e != nil {
		t.Fatal(e)
	}
}
