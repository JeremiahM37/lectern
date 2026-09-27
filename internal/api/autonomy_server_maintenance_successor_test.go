package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"testing"
	"time"
)

func maintenanceSuccessorProposal(t *testing.T, l *autoMaintenanceLedger, old *autoMaintenanceOperation, reg autoMaintenanceRegistry, now time.Time) (*autoMaintenancePlanPin, autonomy.Proposal) {
	t.Helper()
	p := autonomy.Proposal{ProjectID: 1, Title: "fresh measured configuration", Why: "current independent observation", Acceptance: []string{"maintain fixed sensor health"}, Maintenance: &autonomy.MaintenanceProposal{TargetID: "local", ServiceID: "temp-api", ObservationID: autoSHA([]byte("fresh observation")), Limits: old.Authority.Limits}}
	obs := autoMaintenanceObservedResource{TargetID: "local", ServiceID: "temp-api", ObservationID: p.Maintenance.ObservationID, RegistrySHA: reg.Digest, ConfigurationSHA: old.Authority.BeforeSHA, ReceiptSHA: autoSHA([]byte("fresh observed receipt")), Complete: true, CapturedAt: now}
	pin, e := autoPinMaintenanceProposal(1, &p, obs, reg, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = autoPinMaintenanceSuccessor(l, pin); e != nil {
		t.Fatal(e)
	}
	p.Maintenance.Pin = pin.Key
	return pin, p
}
func TestMaintenanceSuccessorFreshAuditsOneReleaseOneSuccessor(t *testing.T) {
	original, reg, gate, now := maintenanceFixture()
	ledger := &autoMaintenanceLedger{}
	old, e := autoReserveMaintenance(ledger, original, reg, gate, now)
	if e != nil {
		t.Fatal(e)
	}
	old.State = "conflict"
	old.ConflictReceiptSHA = autoSHA([]byte("root conflict"))
	// E1 released old ownership. A later fresh E2 observation may differ: this
	// historical release is not a claim that yesterday's health describes E2.
	proof := autoMaintenanceExternalHealthReceipt{ConflictReceiptSHA: old.ConflictReceiptSHA, OperationID: old.ID, ReceiptSHA: autoSHA([]byte("release")), RegistrySHA: reg.Digest, CurrentStateSHA: autoSHA([]byte("external E1")), PostStateSHA: autoSHA([]byte("external E1")), InvocationID: "external", PostInvocationID: "external", ObservedAt: now, CompletedAt: now, Profile: autoMaintenanceExternalProfile, Healthy: true, NoMutation: true, OwnershipChecked: true}
	if e = autoResolveMaintenanceConflict(old, proof, now); e != nil {
		t.Fatal(e)
	}
	oldRaw, _ := json.Marshal(old)
	pin, p := maintenanceSuccessorProposal(t, ledger, old, reg, now)
	if pin.PredecessorOperationID != old.ID || pin.SupersessionSHA != proof.ReceiptSHA || autoMaintenanceOperationID(*pin) == old.ID {
		t.Fatal("successor lost actual release identity")
	}
	audits := original.Audits
	j := &autoJob{TaskID: 50, ID: "55555555-5555-4555-8555-555555555555", Role: "builder"}
	if _, e = autoAdmitMaintenance(pin, p, j, audits); e == nil {
		t.Fatal("old audits reused for new observed state")
	}
	binding := autoMaintenanceBinding(autoMaintenancePinAuthority(*pin))
	for i := range audits {
		audits[i].BindingSHA = binding
	}
	admitted, e := autoAdmitMaintenance(pin, p, j, audits)
	if e != nil {
		t.Fatal(e)
	}
	zero := 0
	v := autoMaintenanceValidationReceipt{PinSHA: pin.Key, CandidateSHA: pin.CandidateSHA, ReceiptSHA: autoSHA([]byte("new execution")), ProfileSHA: autoSHA([]byte("profile")), OutputSHA: autoSHA([]byte("output")), OwnerTask: 60, OwnerJob: "66666666-6666-4666-8666-666666666666", Executed: true, ExitCode: &zero, Profile: "service_resource_limits_v1"}
	review := autoMaintenanceFinalReview{TaskID: 60, JobID: v.OwnerJob, PinSHA: pin.Key, ValidationSHA: v.ReceiptSHA, ArchiveSHA: autoSHA([]byte("new archive")), ReportSHA: autoSHA([]byte("new review")), Approved: true}
	auth, e := autoReviewedMaintenanceAuthority(admitted, v, review, original.Backup)
	if e != nil {
		t.Fatal(e)
	}
	next, e := autoReserveMaintenance(ledger, auth, reg, gate, now)
	if e != nil {
		t.Fatal(e)
	}
	if next.ID != autoMaintenanceOperationID(*pin) || next.ID == old.ID {
		t.Fatal("wrong successor")
	}
	again, e := autoReserveMaintenance(ledger, auth, reg, gate, now)
	if e != nil || again != next {
		t.Fatal("idempotent reservation failed", e)
	}
	replay := auth
	replay.PlanSHA = autoSHA([]byte("renamed plan"))
	if _, e = autoReserveMaintenance(ledger, replay, reg, gate, now); e == nil {
		t.Fatal("relabel minted successor attempt")
	}
	next.State = "restored" // Failed applied candidate does not re-release ancestor.
	another := *pin
	another.PredecessorOperationID = ""
	another.SupersessionSHA = ""
	another.Key = autoMaintenancePinHash(another)
	if e = autoPinMaintenanceSuccessor(ledger, &another); e == nil {
		t.Fatal("spent ancestor proof reused after failed successor")
	}
	mismatch := maintenanceObservation(auth, now)
	mismatch.ConfigurationSHA = autoSHA([]byte("E3 after audits"))
	next.State = "reserved"
	if e = autoBeginMaintenance(next, mismatch, reg, gate, now); e == nil {
		t.Fatal("old release substituted for fresh current CAS")
	}
	after, _ := json.Marshal(old)
	if string(after) != string(oldRaw) {
		t.Fatal("predecessor history rewritten")
	}
}
func TestMaintenanceSuccessorRejectsUnauthenticatedRelease(t *testing.T) {
	a, reg, gate, now := maintenanceFixture()
	l := &autoMaintenanceLedger{}
	old, e := autoReserveMaintenance(l, a, reg, gate, now)
	if e != nil {
		t.Fatal(e)
	}
	old.State = "superseded"
	pin := autoMaintenancePlanPin{TargetID: "local", ServiceID: "temp-api", RegistrySHA: reg.Digest, BeforeSHA: a.BeforeSHA, CandidateSHA: a.CandidateSHA, Limits: a.Limits}
	if e = autoPinMaintenanceSuccessor(l, &pin); e == nil {
		t.Fatal("state label alone granted successor")
	}
	old.ConflictReceiptSHA = autoSHA([]byte("conflict"))
	old.ExternalResolution = &autoMaintenanceExternalHealthReceipt{OperationID: old.ID, ReceiptSHA: autoSHA([]byte("receipt")), RegistrySHA: reg.Digest, CurrentStateSHA: a.BeforeSHA, Healthy: true, NoMutation: true, OwnershipChecked: true, ConflictReceiptSHA: autoSHA([]byte("foreign conflict"))}
	if e = autoPinMaintenanceSuccessor(l, &pin); e == nil {
		t.Fatal("foreign historical journal granted successor")
	}
}
func TestMaintenanceSuccessorPreservesLegacyHashes(t *testing.T) {
	a, _, _, _ := maintenanceFixture()
	raw, _ := json.Marshal(struct {
		Plan, Resource, Registry, Before, Candidate string
		Limits                                      autoMaintenanceLimits
	}{a.PlanSHA, a.ResourceID, a.RegistrySHA, a.BeforeSHA, a.CandidateSHA, a.Limits})
	if autoMaintenanceBinding(a) != autoSHA(raw) {
		t.Fatal("legacy audit binding changed")
	}
	if autoMaintenanceAuthorityID(a) != autoSHA([]byte(a.ResourceID+":"+a.RegistrySHA+":"+a.BeforeSHA+":"+a.CandidateSHA)) {
		t.Fatal("legacy operation identity changed")
	}
}
