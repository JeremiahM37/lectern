package api

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Only populated by the controller's authenticated observer receipt decoder.
// Configuration completeness and exact registered service identity are mandatory;
// unavailable facts or catalog discovery cannot substitute for this receipt.
type autoMaintenanceObservedResource struct {
	TargetID, ServiceID, ObservationID        string
	RegistrySHA, ConfigurationSHA, ReceiptSHA string
	Complete                                  bool
	CapturedAt                                time.Time
}
type autoMaintenancePlanPin struct {
	PredecessorOperationID                                      string `json:"predecessor_operation_id,omitempty"`
	SupersessionSHA                                             string `json:"supersession_receipt_sha256,omitempty"`
	Key                                                         string
	ProjectID                                                   int64
	TargetID, ServiceID, ObservationID                          string
	RegistrySHA, BeforeSHA, CandidateSHA, ObservationReceiptSHA string
	Limits                                                      autoMaintenanceLimits
	Acceptance                                                  []string
	CapturedAt                                                  time.Time
}
type autoMaintenanceAdmitted struct {
	Pin    autoMaintenancePlanPin
	TaskID int64
	JobID  string
	Audits [2]autoMaintenanceEvidence
}

// This is a trusted executed-profile receipt, never an assertion from a report.
// The registered profile verifies syntax and a disposable bounded workload; it
// does not purport to prove the live hardware/service will be healthy after apply.
type autoMaintenanceValidationReceipt struct {
	PinSHA, CandidateSHA, ReceiptSHA, ProfileSHA, OutputSHA string
	OwnerTask                                               int64
	OwnerJob                                                string
	Executed                                                bool
	ExitCode                                                *int
	Profile                                                 string
}
type autoMaintenanceFinalReview struct {
	TaskID                                       int64
	JobID                                        string
	PinSHA, ValidationSHA, ArchiveSHA, ReportSHA string
	Approved                                     bool
}

func autoMaintenancePinHash(p autoMaintenancePlanPin) string {
	p.Key = ""
	b, _ := json.Marshal(p)
	return autoSHA(b)
}
func autoPinMaintenanceProposal(projectID int64, p *autonomy.Proposal, obs autoMaintenanceObservedResource, registry autoMaintenanceRegistry, now time.Time) (*autoMaintenancePlanPin, error) {
	if p == nil || p.ProjectID != projectID || projectID <= 0 || p.Maintenance == nil || !autonomy.ValidMaintenanceProposal(*p) {
		return nil, errors.New("invalid maintenance proposal")
	}
	m := p.Maintenance
	if !obs.Complete || obs.TargetID != m.TargetID || obs.ServiceID != m.ServiceID || obs.ObservationID != m.ObservationID || !autoHash256(obs.ReceiptSHA) || !autoHash256(obs.ConfigurationSHA) || obs.RegistrySHA != registry.Digest || !autoHash256(registry.Digest) || registry.ResourceID != obs.TargetID+"/"+obs.ServiceID || registry.Protected || !registry.Stateless || obs.CapturedAt.IsZero() || obs.CapturedAt.After(now) || now.Sub(obs.CapturedAt) > 5*time.Minute {
		return nil, errors.New("maintenance requires complete fresh registered observation")
	}
	if m.Limits.CPUPercent < registry.Min.CPUPercent || m.Limits.CPUPercent > registry.Max.CPUPercent || m.Limits.MemoryBytes < registry.Min.MemoryBytes || m.Limits.MemoryBytes > registry.Max.MemoryBytes || m.Limits.Tasks < registry.Min.Tasks || m.Limits.Tasks > registry.Max.Tasks || len(p.Acceptance) == 0 {
		return nil, errors.New("maintenance limits or acceptance outside policy")
	}
	pin := &autoMaintenancePlanPin{ProjectID: projectID, TargetID: m.TargetID, ServiceID: m.ServiceID, ObservationID: obs.ObservationID, RegistrySHA: obs.RegistrySHA, BeforeSHA: obs.ConfigurationSHA, CandidateSHA: autoMaintenanceCandidate(m.Limits), ObservationReceiptSHA: obs.ReceiptSHA, Limits: m.Limits, Acceptance: append([]string(nil), p.Acceptance...), CapturedAt: obs.CapturedAt}
	pin.Key = autoMaintenancePinHash(*pin)
	if m.Pin != "" && m.Pin != pin.Key {
		return nil, errors.New("maintenance supplied pin differs from authenticated observation")
	}
	m.Pin = pin.Key
	return pin, nil
}
func autoMaintenancePinAuthority(pin autoMaintenancePlanPin) autoMaintenanceAuthority {
	return autoMaintenanceAuthority{PredecessorOperationID: pin.PredecessorOperationID, SupersessionSHA: pin.SupersessionSHA, PlanSHA: pin.Key, ResourceID: pin.TargetID + "/" + pin.ServiceID, RegistrySHA: pin.RegistrySHA, BeforeSHA: pin.BeforeSHA, CandidateSHA: pin.CandidateSHA, Limits: pin.Limits}
}
func autoValidateMaintenancePin(pin *autoMaintenancePlanPin, p autonomy.Proposal) error {
	if pin == nil || !autoHash256(pin.Key) || pin.Key != autoMaintenancePinHash(*pin) || p.Maintenance == nil || !autonomy.ValidMaintenanceProposal(p) || p.Maintenance.Pin != pin.Key || p.ProjectID != pin.ProjectID || p.Maintenance.TargetID != pin.TargetID || p.Maintenance.ServiceID != pin.ServiceID || p.Maintenance.ObservationID != pin.ObservationID || p.Maintenance.Limits != pin.Limits {
		return errors.New("maintenance immutable pin changed")
	}
	before, _ := json.Marshal(pin.Acceptance)
	after, _ := json.Marshal(p.Acceptance)
	if string(before) != string(after) {
		return errors.New("maintenance acceptance changed after pin")
	}
	return nil
}

// The caller resolves each audit from the actual completed auditor assignment and
// its exact archived report. Audit binding is stable before the builder exists.
func autoAdmitMaintenance(pin *autoMaintenancePlanPin, p autonomy.Proposal, j *autoJob, audits [2]autoMaintenanceEvidence) (*autoMaintenanceAdmitted, error) {
	if err := autoValidateMaintenancePin(pin, p); err != nil {
		return nil, err
	}
	if j == nil || j.Role != "builder" || j.TaskID <= 0 || !autoExpertJobID.MatchString(j.ID) {
		return nil, errors.New("maintenance builder admission missing")
	}
	binding := autoMaintenanceBinding(autoMaintenancePinAuthority(*pin))
	for _, a := range audits {
		if !a.Successful || a.BindingSHA != binding || !autoHash256(a.ReceiptSHA) || a.TaskID <= 0 || a.TaskID == j.TaskID || !autoExpertJobID.MatchString(a.JobID) || a.JobID == j.ID {
			return nil, errors.New("maintenance audit not bound to this pin")
		}
	}
	if audits[0].TaskID == audits[1].TaskID || audits[0].JobID == audits[1].JobID || audits[0].ReceiptSHA == audits[1].ReceiptSHA {
		return nil, errors.New("maintenance independent audits required")
	}
	frozen := *pin
	frozen.Acceptance = append([]string(nil), pin.Acceptance...)
	return &autoMaintenanceAdmitted{Pin: frozen, TaskID: j.TaskID, JobID: j.ID, Audits: audits}, nil
}
func autoValidateMaintenanceCandidateReview(admission *autoMaintenanceAdmitted, validation autoMaintenanceValidationReceipt, review autoMaintenanceFinalReview) error {
	if admission == nil || admission.Pin.Key != autoMaintenancePinHash(admission.Pin) {
		return errors.New("maintenance admission invalid")
	}
	p := admission.Pin
	if validation.PinSHA != p.Key || validation.CandidateSHA != p.CandidateSHA || !autoHash256(validation.ReceiptSHA) || !autoHash256(validation.ProfileSHA) || !autoHash256(validation.OutputSHA) || validation.Profile != "service_resource_limits_v1" || !validation.Executed || validation.ExitCode == nil || *validation.ExitCode != 0 {
		return errors.New("maintenance executed registered candidate validation missing")
	}
	if !review.Approved || review.TaskID <= 0 || review.TaskID == admission.TaskID || !autoExpertJobID.MatchString(review.JobID) || review.JobID == admission.JobID || review.PinSHA != p.Key || review.ValidationSHA != validation.ReceiptSHA || !autoHash256(review.ArchiveSHA) || !autoHash256(review.ReportSHA) || validation.OwnerTask != review.TaskID || !autoExpertJobID.MatchString(validation.OwnerJob) || validation.OwnerJob == admission.JobID {
		return errors.New("maintenance independent reviewer/runtime binding missing")
	}
	return nil
}

func autoReviewedMaintenanceAuthority(admission *autoMaintenanceAdmitted, validation autoMaintenanceValidationReceipt, review autoMaintenanceFinalReview, backup autoMaintenanceBackup) (autoMaintenanceAuthority, error) {
	empty := autoMaintenanceAuthority{}
	if err := autoValidateMaintenanceCandidateReview(admission, validation, review); err != nil {
		return empty, err
	}
	p := admission.Pin
	if !backup.Verified || backup.BeforeSHA != p.BeforeSHA || !autoHash256(backup.ReceiptSHA) || !autoHash256(backup.RestoreProofSHA) || !autoHash256(backup.OffboxReceiptSHA) {
		return empty, errors.New("maintenance restorable backup evidence missing")
	}
	a := autoMaintenancePinAuthority(p)
	a.TaskID = admission.TaskID
	a.JobID = admission.JobID
	a.Audits = admission.Audits
	a.Backup = backup
	// Bind both actual executed validation and independent archived review together.
	combined, _ := json.Marshal(struct {
		Validation autoMaintenanceValidationReceipt
		Review     autoMaintenanceFinalReview
	}{validation, review})
	a.ExecutedValidationSHA = validation.ReceiptSHA
	a.CandidateReviewSHA = review.ReportSHA
	a.Validation = autoMaintenanceEvidence{ReceiptSHA: autoSHA(combined), BindingSHA: autoMaintenanceBinding(a), TaskID: review.TaskID, JobID: review.JobID, Successful: true}
	return a, nil
}
