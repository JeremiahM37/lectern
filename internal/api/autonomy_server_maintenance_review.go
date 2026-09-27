package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// A reviewed candidate is evidence, never apply permission. Backup/restore,
// fresh before-state CAS, quota and exclusive operation ownership remain later
// gates. Execution ownership is retained independently of report-correction UUIDs.
type autoMaintenanceReviewedCandidate struct {
	ID              string                            `json:"id"`
	State           string                            `json:"state"`
	Admission       autoMaintenanceAdmitted           `json:"admission"`
	Review          autoMaintenanceFinalReview        `json:"review"`
	Report          string                            `json:"report"`
	Validation      *autoMaintenanceValidationReceipt `json:"validation,omitempty"`
	ValidationLease *autoMaintenanceValidationLease   `json:"validation_lease,omitempty"`
	RecordedAt      time.Time                         `json:"recorded_at"`
}

func autoMaintenanceReviewSelected(a *autoRecord, j *autoJob) bool {
	if j == nil || j.Role != "reviewer" {
		return false
	}
	if j.MaintenancePin != "" || j.MaintenanceAdmission != nil {
		return true
	}
	return a != nil && a.State != nil && a.State.Item >= 0 && a.State.Item < len(a.State.Items) && a.State.Items[a.State.Item].Maintenance != nil
}
func autoMaintenanceReviewAdmission(a *autoRecord, j *autoJob) (*autoMaintenanceAdmitted, error) {
	if a == nil || j == nil || j.Role != "reviewer" || j.TaskID <= 0 || !autoExpertJobID.MatchString(j.ID) || j.MaintenanceAdmission == nil {
		return nil, errors.New("maintenance reviewer admission missing")
	}
	ad := j.MaintenanceAdmission
	if ad.Pin.Key != autoMaintenancePinHash(ad.Pin) || j.MaintenancePin != ad.Pin.Key || ad.TaskID <= 0 || !autoExpertJobID.MatchString(ad.JobID) || j.TaskID == ad.TaskID || j.ID == ad.JobID {
		return nil, errors.New("maintenance reviewer admission binding invalid")
	}
	latest := autoFindJob(a, j.TaskID)
	if latest == nil || latest.ID != j.ID || latest.Role != "reviewer" {
		return nil, errors.New("maintenance report is not from latest reviewer process")
	}
	return ad, nil
}
func autoMaintenanceReviewValidation(a *autoRecord, j *autoJob) (*autoMaintenanceValidationLease, *autoMaintenanceValidationReceipt, error) {
	ad, err := autoMaintenanceReviewAdmission(a, j)
	if err != nil {
		return nil, nil, err
	}
	id := autoSHA([]byte(ad.Pin.Key + ":" + strconv.FormatInt(j.TaskID, 10)))
	l := a.MaintenanceValidations[id]
	if l == nil || l.ID != id || l.OwnerTask != j.TaskID || l.PinSHA != ad.Pin.Key || l.OperationID != autoMaintenanceOperationID(ad.Pin) || l.Generation < 1 || l.State != "validated" || l.Validation == nil || len(l.ReceiptRaw) == 0 || l.RequestSHA != autoSHA(l.RequestRaw) || !autoExpertJobID.MatchString(l.OwnerJob) || l.OwnerJob == ad.JobID {
		return nil, nil, errors.New("approving maintenance review requires persisted executed candidate validation")
	}
	var request autoMaintenanceValidationRequest
	if json.Unmarshal(l.RequestRaw, &request) != nil || request.SchemaVersion != 1 || request.OperationID != l.OperationID || request.OwnerJob != l.OwnerJob || request.OwnerTask != j.TaskID || request.PinSHA != ad.Pin.Key || request.Generation != l.Generation || request.TargetID != ad.Pin.TargetID || request.ServiceID != ad.Pin.ServiceID || request.Action != "service_resource_limits" || request.RegistrySHA != ad.Pin.RegistrySHA || request.BeforeSHA != ad.Pin.BeforeSHA || request.Limits != ad.Pin.Limits {
		return nil, nil, errors.New("maintenance validation request differs from admitted candidate")
	}
	state, validation, err := autoDecodeMaintenanceValidation(l.ReceiptRaw, l)
	if err != nil || state != "validated" || validation == nil {
		return nil, nil, errors.New("maintenance persisted validation receipt invalid")
	}
	expected, _ := json.Marshal(validation)
	cached, _ := json.Marshal(l.Validation)
	if !bytes.Equal(expected, cached) {
		return nil, nil, errors.New("maintenance validation projection differs from sealed receipt")
	}
	return l, validation, nil
}
func autoValidateMaintenanceReviewReport(a *autoRecord, j *autoJob, report []byte) error {
	if !autoMaintenanceReviewSelected(a, j) {
		return nil
	}
	if _, err := autoMaintenanceReviewAdmission(a, j); err != nil {
		return err
	}
	var verdict autonomy.Verdict
	if json.Unmarshal(report, &verdict) != nil || verdict.Approve == nil {
		return errors.New("maintenance reviewer verdict required")
	}
	// An honest rejection needs no successful execution and never reserves apply.
	if !verdict.AcceptsWork() {
		return nil
	}
	_, _, err := autoMaintenanceReviewValidation(a, j)
	return err
}

func autoRecordMaintenanceReviewedCandidate(a *autoRecord, j *autoJob, report []byte, archiveSHA string) (*autoMaintenanceReviewedCandidate, error) {
	if !autoMaintenanceReviewSelected(a, j) {
		return nil, nil
	}
	if err := autoValidateMaintenanceReviewReport(a, j, report); err != nil {
		return nil, err
	}
	if !autoHash256(archiveSHA) {
		return nil, errors.New("maintenance final reviewer archive identity missing")
	}
	var verdict autonomy.Verdict
	if err := json.Unmarshal(report, &verdict); err != nil {
		return nil, err
	}
	ad := j.MaintenanceAdmission
	value := autoMaintenanceReviewedCandidate{ID: autoSHA([]byte(ad.Pin.Key + ":" + strconv.FormatInt(j.TaskID, 10) + ":" + j.ID)), State: "rejected", Admission: *ad, Report: string(report), Review: autoMaintenanceFinalReview{TaskID: j.TaskID, JobID: j.ID, PinSHA: ad.Pin.Key, ArchiveSHA: archiveSHA, ReportSHA: autoSHA(report), Approved: verdict.AcceptsWork()}}
	if verdict.AcceptsWork() {
		lease, validation, err := autoMaintenanceReviewValidation(a, j)
		if err != nil {
			return nil, err
		}
		value.State = "candidate_reviewed"
		value.Validation = validation
		value.ValidationLease = lease
		value.Review.ValidationSHA = validation.ReceiptSHA
	}
	// Detach every nested slice/map from live job and lease state before persistence.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var detached autoMaintenanceReviewedCandidate
	if err = json.Unmarshal(raw, &detached); err != nil {
		return nil, err
	}
	if old := a.MaintenanceReviews[value.ID]; old != nil {
		compare := *old
		compare.RecordedAt = time.Time{}
		before, _ := json.Marshal(compare)
		if !bytes.Equal(before, raw) {
			return nil, errors.New("immutable maintenance final review already differs")
		}
		return old, nil
	}
	detached.RecordedAt = time.Now().UTC()
	if a.MaintenanceReviews == nil {
		a.MaintenanceReviews = map[string]*autoMaintenanceReviewedCandidate{}
	}
	a.MaintenanceReviews[value.ID] = &detached
	return &detached, nil
}

// Invoke after snapshotAutoJob succeeds and before advancing the report. The
// existing archive reader has durable cancellation ownership and never starts a
// model; exact archived report text must match the transport being admitted.
func (s *Server) finishAutoMaintenanceReview(ctx context.Context, a *autoRecord, j *autoJob, report []byte) error {
	if !autoMaintenanceReviewSelected(a, j) {
		return nil
	}
	archive, err := s.autoArchiveIdentity(ctx, j)
	if err != nil {
		return err
	}
	archived, err := s.autoHistoricalReport(ctx, a, j, archive)
	if err != nil {
		return err
	}
	if !bytes.Equal(archived, report) {
		return fmt.Errorf("maintenance archived reviewer report differs from submitted transport")
	}
	if _, err = autoRecordMaintenanceReviewedCandidate(a, j, report, archive); err != nil {
		return err
	}
	return s.saveAuto(a)
}
