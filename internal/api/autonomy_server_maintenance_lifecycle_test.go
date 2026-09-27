package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"testing"
)

func TestMaintenancePreAuditPinSurvivesFutureBuilderAdmissionAndReviewedExecution(t *testing.T) {
	old, r, g, now := maintenanceFixture()
	r.ResourceID = "local/temp-api"
	p := autonomy.Proposal{ProjectID: 1, Title: "resource caps", Why: "measured need", Acceptance: []string{"sensor freshness"}, Maintenance: &autonomy.MaintenanceProposal{TargetID: "local", ServiceID: "temp-api", ObservationID: autoSHA([]byte("observation")), Limits: old.Limits}}
	obs := autoMaintenanceObservedResource{TargetID: "local", ServiceID: "temp-api", ObservationID: p.Maintenance.ObservationID, RegistrySHA: r.Digest, ConfigurationSHA: old.BeforeSHA, ReceiptSHA: autoSHA([]byte("observed")), Complete: true, CapturedAt: now}
	pin, e := autoPinMaintenanceProposal(1, &p, obs, r, now)
	if e != nil {
		t.Fatal(e)
	}
	// Both auditors sign a stable pin BEFORE any builder ID exists.
	audits := old.Audits
	binding := autoMaintenanceBinding(autoMaintenancePinAuthority(*pin))
	for i := range audits {
		audits[i].BindingSHA = binding
	}
	j := &autoJob{ID: "55555555-5555-4555-8555-555555555555", TaskID: 50, Role: "builder"}
	admitted, e := autoAdmitMaintenance(pin, p, j, audits)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(admitted)
	admitted = &autoMaintenanceAdmitted{}
	if e = json.Unmarshal(raw, admitted); e != nil {
		t.Fatal(e)
	}
	zero := 0
	validation := autoMaintenanceValidationReceipt{PinSHA: pin.Key, CandidateSHA: pin.CandidateSHA, ReceiptSHA: autoSHA([]byte("validation")), ProfileSHA: autoSHA([]byte("profile")), OutputSHA: autoSHA([]byte("output")), OwnerTask: 60, OwnerJob: "66666666-6666-4666-8666-666666666666", Executed: true, ExitCode: &zero, Profile: "service_resource_limits_v1"}
	review := autoMaintenanceFinalReview{TaskID: validation.OwnerTask, JobID: validation.OwnerJob, PinSHA: pin.Key, ValidationSHA: validation.ReceiptSHA, ArchiveSHA: autoSHA([]byte("archive")), ReportSHA: autoSHA([]byte("review")), Approved: true}
	authority, e := autoReviewedMaintenanceAuthority(admitted, validation, review, old.Backup)
	if e != nil {
		t.Fatal(e)
	}
	if autoMaintenanceBinding(authority) != binding {
		t.Fatal("builder admission changed what auditors assessed")
	}
	if _, e = autoReserveMaintenance(&autoMaintenanceLedger{}, authority, r, g, now); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"not_executed", "wrong_candidate", "wrong_reviewer", "rejected"} {
		v := validation
		rev := review
		switch bad {
		case "not_executed":
			v.Executed = false
		case "wrong_candidate":
			v.CandidateSHA = old.BeforeSHA
		case "wrong_reviewer":
			rev.TaskID++
		case "rejected":
			rev.Approved = false
		}
		if _, e = autoReviewedMaintenanceAuthority(admitted, v, rev, old.Backup); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	// Detached authority must not follow later planner edits.
	p.Acceptance[0] = "new behavior"
	if autoValidateMaintenancePin(pin, p) == nil {
		t.Fatal("acceptance mutation allowed")
	}
}
func TestMaintenancePinRejectsIncompleteOrForeignObservation(t *testing.T) {
	a, r, _, now := maintenanceFixture()
	r.ResourceID = "local/temp-api"
	p := autonomy.Proposal{ProjectID: 1, Acceptance: []string{"healthy"}, Maintenance: &autonomy.MaintenanceProposal{TargetID: "local", ServiceID: "temp-api", ObservationID: autoSHA([]byte("id")), Limits: a.Limits}}
	obs := autoMaintenanceObservedResource{TargetID: "local", ServiceID: "temp-api", ObservationID: p.Maintenance.ObservationID, RegistrySHA: r.Digest, ConfigurationSHA: a.BeforeSHA, ReceiptSHA: autoSHA([]byte("receipt")), Complete: false, CapturedAt: now}
	if _, e := autoPinMaintenanceProposal(1, &p, obs, r, now); e == nil {
		t.Fatal("partial observation pinned")
	}
	obs.Complete = true
	obs.ServiceID = "lectern"
	if _, e := autoPinMaintenanceProposal(1, &p, obs, r, now); e == nil {
		t.Fatal("foreign service pinned")
	}
}
