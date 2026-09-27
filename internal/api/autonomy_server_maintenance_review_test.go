package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func maintenanceReviewedFixture(t *testing.T) (*Server, *autoRecord, *autoJob) {
	t.Helper()
	s, _, j, root, _ := maintenanceHTTPFixture(t)
	w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", `{"pin_sha256":"`+j.MaintenancePin+`"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	j = autoFindJob(a, j.TaskID)
	a.State.Items = []autonomy.Proposal{{ProjectID: 1, Acceptance: append([]string(nil), j.MaintenanceAdmission.Pin.Acceptance...), Maintenance: &autonomy.MaintenanceProposal{TargetID: j.MaintenanceAdmission.Pin.TargetID, ServiceID: j.MaintenanceAdmission.Pin.ServiceID, Limits: j.MaintenanceAdmission.Pin.Limits, Pin: j.MaintenancePin}}}
	return s, a, j
}

var maintenanceApproval = []byte("{\"outcome\":\"completed\",\"approve\":true,\"reason\":\"independently checked fixed profile and scoped limits\"}\n")
var maintenanceRejection = []byte("{\"outcome\":\"incomplete\",\"approve\":false,\"reason\":\"acceptance unmet\"}\n")

func TestMaintenanceReviewRequiresPersistedAuthenticatedExecution(t *testing.T) {
	for _, which := range []string{"valid", "missing_lease", "missing_raw", "cached_only", "tamper", "wrong_request", "foreign_task", "wrong_pin", "failed", "missing_admission", "old_reviewer"} {
		t.Run(which, func(t *testing.T) {
			_, a, j := maintenanceReviewedFixture(t)
			var lease *autoMaintenanceValidationLease
			for _, l := range a.MaintenanceValidations {
				lease = l
			}
			switch which {
			case "missing_lease":
				a.MaintenanceValidations = nil
			case "missing_raw":
				lease.ReceiptRaw = nil
			case "cached_only":
				lease.Validation.ReceiptSHA = strings.Repeat("a", 64)
			case "tamper":
				lease.ReceiptRaw = append(lease.ReceiptRaw, []byte(`{"foreign":true}`)...)
			case "wrong_request":
				var req autoMaintenanceValidationRequest
				json.Unmarshal(lease.RequestRaw, &req)
				req.ServiceID = "other"
				lease.RequestRaw, _ = json.Marshal(req)
				lease.RequestSHA = autoSHA(lease.RequestRaw)
			case "foreign_task":
				lease.OwnerTask++
			case "wrong_pin":
				lease.PinSHA = strings.Repeat("b", 64)
			case "failed":
				lease.State = "validation_failed"
			case "missing_admission":
				j.MaintenanceAdmission = nil
			case "old_reviewer":
				next := *j
				next.ID = "33333333-3333-4333-8333-333333333333"
				a.Jobs = append(a.Jobs, &next)
			}
			err := autoValidateMaintenanceReviewReport(a, j, maintenanceApproval)
			if which == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unverified approval accepted", which)
			}
			if a.Maintenance != nil || len(a.MaintenanceReviews) != 0 {
				t.Fatal("validation reserved host effects")
			}
		})
	}
}
func TestMaintenanceReviewCorrectionPreservesExecutionOwnerAndDetachedEvidence(t *testing.T) {
	_, a, j := maintenanceReviewedFixture(t)
	executed := j.ID
	clone := *j
	clone.ID = "33333333-3333-4333-8333-333333333333"
	a.Jobs = append(a.Jobs, &clone)
	record, err := autoRecordMaintenanceReviewedCandidate(a, &clone, maintenanceApproval, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != "candidate_reviewed" || record.Review.JobID != clone.ID || record.Validation.OwnerJob != executed || record.Review.TaskID != j.TaskID || record.Review.ReportSHA != autoSHA(maintenanceApproval) {
		t.Fatal("report correction lost original execution ownership", record)
	}
	again, err := autoRecordMaintenanceReviewedCandidate(a, &clone, maintenanceApproval, strings.Repeat("c", 64))
	if err != nil || again != record || len(a.MaintenanceReviews) != 1 {
		t.Fatal("exact receipt replay was not idempotent", err)
	}
	original := record.Admission.Pin.Acceptance[0]
	clone.MaintenanceAdmission.Pin.Acceptance[0] = "mutated"
	for _, lease := range a.MaintenanceValidations {
		lease.ReceiptRaw[0] = 'x'
	}
	if record.Admission.Pin.Acceptance[0] != original || record.ValidationLease.ReceiptRaw[0] == 'x' {
		t.Fatal("record aliases live state")
	}
	// Restore syntactic JSON after the alias adversary before exercising persistence.
	for _, lease := range a.MaintenanceValidations {
		lease.ReceiptRaw[0] = '{'
	}
	raw, marshalErr := json.Marshal(a)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var restored autoRecord
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.MaintenanceReviews[record.ID].Report != string(maintenanceApproval) || autoSHA([]byte(restored.MaintenanceReviews[record.ID].Report)) != record.Review.ReportSHA {
		t.Fatal("archived report exact bytes lost after persistence")
	}
	if restored.MaintenanceReviews[record.ID].Validation.OwnerJob != executed {
		t.Fatal("restart lost execution owner")
	}
	if a.Maintenance != nil {
		t.Fatal("review approval prematurely reserved host operation")
	}
}
func TestMaintenanceRejectedReviewNeedsNoPassAndCannotBecomeApplyAuthority(t *testing.T) {
	_, a, j := maintenanceReviewedFixture(t)
	a.MaintenanceValidations = nil
	record, err := autoRecordMaintenanceReviewedCandidate(a, j, maintenanceRejection, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != "rejected" || record.Validation != nil || record.ValidationLease != nil || record.Review.Approved || a.Maintenance != nil {
		t.Fatal("rejection acquired execution or host authority")
	}
	if _, err = autoRecordMaintenanceReviewedCandidate(a, j, maintenanceApproval, strings.Repeat("d", 64)); err == nil {
		t.Fatal("rejected record silently promoted")
	}
}
func TestMaintenanceReviewFinishBindsExactImmutableArchiveReport(t *testing.T) {
	for _, which := range []string{"exact", "different", "pending", "wrong_archive"} {
		t.Run(which, func(t *testing.T) {
			s, a, j := maintenanceReviewedFixture(t)
			root := t.TempDir()
			t.Setenv("PATH", root+":/usr/bin:/bin")
			t.Setenv("MAINT_REVIEW_PROOF", root)
			sha := strings.Repeat("e", 64)
			report := string(maintenanceApproval)
			if which == "different" {
				report = string(maintenanceRejection)
			}
			receipt := map[string]any{"state": "ready", "archive_sha256": sha, "report_sha256": autoSHA([]byte(report)), "report": report}
			if which == "pending" {
				receipt["state"] = "exporting"
			}
			if which == "wrong_archive" {
				receipt["archive_sha256"] = strings.Repeat("f", 64)
			}
			raw, _ := json.Marshal(receipt)
			os.WriteFile(filepath.Join(root, "report.json"), raw, 0600)
			script := "#!/bin/sh\ncase \"$3\" in\narchive-identity) echo '{\"state\":\"ready\",\"sha256\":\"" + sha + "\"}';;\narchive-report) cat \"$MAINT_REVIEW_PROOF/report.json\";;\n*) exit 93;;\nesac\n"
			if err := os.WriteFile(filepath.Join(root, "sudo"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			err := s.finishAutoMaintenanceReview(context.Background(), a, j, maintenanceApproval)
			if which == "exact" {
				if err != nil || len(a.MaintenanceReviews) != 1 {
					t.Fatal("matching archived evidence denied", err)
				}
			} else {
				if err == nil || len(a.MaintenanceReviews) != 0 {
					t.Fatal("unbound archived report persisted", err)
				}
				if which == "pending" && !errors.Is(err, errAutoArtifactPending) {
					t.Fatal("pending reader was treated as report failure", err)
				}
			}
			if a.Maintenance != nil {
				t.Fatal("archive read triggered host reservation")
			}
		})
	}
}

func TestMaintenanceReviewGateDoesNotTreatIncompleteApproveAsAcceptance(t *testing.T) {
	_, a, j := maintenanceReviewedFixture(t)
	a.MaintenanceValidations = nil
	contradictory := []byte(`{"outcome":"incomplete","approve":true,"reason":"still blocked"}`)
	var verdict autonomy.Verdict
	if err := json.Unmarshal(contradictory, &verdict); err != nil {
		t.Fatal(err)
	}
	if verdict.AcceptsWork() {
		t.Fatal("incomplete outcome accepted")
	}
	if _, err := autoValidateWorkerReport(a, j, contradictory); err == nil {
		t.Fatal("invalid approval passed full worker schema gate")
	}
	if a.Maintenance != nil || len(a.MaintenanceReviews) != 0 {
		t.Fatal("invalid report reserved host action")
	}
}

func TestMaintenanceReviewCrashAfterEvidencePreservesBothFinalProcessRecords(t *testing.T) {
	_, a, j := maintenanceReviewedFixture(t)
	first, err := autoRecordMaintenanceReviewedCandidate(a, j, maintenanceApproval, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	// Evidence committed, but original process was not yet marked done. A genuine
	// operational correction keeps the task and validation, receives a fresh UUID.
	clone := *j
	clone.ID = "33333333-3333-4333-8333-333333333333"
	a.Jobs = append(a.Jobs, &clone)
	corrected := []byte("{\"outcome\":\"completed\",\"approve\":true,\"reason\":\"same candidate; recovered report delivery\"}\n")
	second, err := autoRecordMaintenanceReviewedCandidate(a, &clone, corrected, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(a.MaintenanceReviews) != 2 || first.Report != string(maintenanceApproval) || second.Report != string(corrected) {
		t.Fatal("new final process overwrote immutable review")
	}
	if second.Validation.OwnerJob != j.ID || first.Validation.ReceiptSHA != second.Validation.ReceiptSHA {
		t.Fatal("correction changed execution owner")
	}
	again, err := autoRecordMaintenanceReviewedCandidate(a, &clone, corrected, strings.Repeat("d", 64))
	if err != nil || again != second || len(a.MaintenanceReviews) != 2 {
		t.Fatal("normal finish retry needed new model approval", err)
	}
	if a.Maintenance != nil {
		t.Fatal("record history duplicated a host operation")
	}
}
