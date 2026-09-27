package api

import (
	"encoding/json"
	"errors"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"time"
)

// These records are controller-only: callers must resolve admitted jobs/audits and
// authenticate helper receipts. Decoding a worker body into them grants nothing.
// Persist each intent transition before executing its corresponding host effect.
type autoMaintenanceLimits = autonomy.MaintenanceLimits

type autoMaintenanceRegistry struct {
	ResourceID          string
	Digest              string
	Protected           bool
	Stateless           bool
	Min, Max            autoMaintenanceLimits
	MemoryHeadroomBytes int64
}
type autoMaintenanceEvidence struct {
	ReceiptSHA string
	BindingSHA string
	TaskID     int64
	JobID      string
	Successful bool
}
type autoMaintenanceBackup struct {
	ReceiptSHA       string
	BeforeSHA        string
	RestoreProofSHA  string
	OffboxReceiptSHA string
	Verified         bool
}
type autoMaintenanceAuthority struct {
	PredecessorOperationID string `json:"predecessor_operation_id,omitempty"`
	SupersessionSHA        string `json:"supersession_receipt_sha256,omitempty"`
	ExecutedValidationSHA  string
	CandidateReviewSHA     string
	PlanSHA                string
	TaskID                 int64
	JobID                  string
	ResourceID             string
	RegistrySHA            string
	BeforeSHA              string
	CandidateSHA           string
	Limits                 autoMaintenanceLimits
	Audits                 [2]autoMaintenanceEvidence
	Validation             autoMaintenanceEvidence
	Backup                 autoMaintenanceBackup
}
type autoMaintenanceGate struct {
	Config          autonomy.Config
	Enabled         bool
	QuotaObservedAt time.Time
	// Minimum remaining across all applicable session and weekly quotas.
	RemainingPercent float64
}
type autoMaintenanceObservation struct {
	CurrentTasks                  int
	RegistrySHA, ConfigurationSHA string
	InvocationID                  string
	RSSBytes                      int64
	CapturedAt                    time.Time
}
type autoMaintenanceOperation struct {
	ConflictReceiptSHA                                    string                                `json:"conflict_receipt_sha256,omitempty"`
	ExternalResolution                                    *autoMaintenanceExternalHealthReceipt `json:"external_resolution,omitempty"`
	CancellationReceiptSHA                                string
	CancellationHistory                                   []string
	AppliedStateSHA                                       string
	ID                                                    string
	Authority                                             autoMaintenanceAuthority
	State                                                 string
	StopRequested                                         bool
	AppliedInvocation                                     string
	ApplyReceiptSHA, HealthReceiptSHA, RollbackReceiptSHA string
	Reason                                                string
	CreatedAt                                             time.Time
}

// NoEffects must come from the privileged launch-lock/journal check. A stopped
// process may already have applied or rolled back a failed candidate.
func autoCancelMaintenanceBeforeEffects(o *autoMaintenanceOperation, receiptSHA string, noEffects bool) error {
	if o == nil || !noEffects || !autoHash256(receiptSHA) || !o.StopRequested || o.ApplyReceiptSHA != "" || o.AppliedStateSHA != "" || o.AppliedInvocation != "" || o.HealthReceiptSHA != "" || o.RollbackReceiptSHA != "" {
		return errors.New("maintenance cancellation has no authenticated no-effect proof")
	}
	if o.State != "reserved" && o.State != "apply_pending" && o.State != "cancelled" {
		return errors.New("maintenance cancellation is not pre-effect")
	}
	if o.CancellationReceiptSHA != "" && o.CancellationReceiptSHA != receiptSHA {
		return errors.New("maintenance cancellation evidence immutable")
	}
	o.CancellationReceiptSHA = receiptSHA
	o.State = "cancelled"
	return nil
}

// The caller authenticates a terminal pre-effect cancellation receipt and owns
// generation renewal. Retain the same semantic operation and exact authority;
// compensation or a failed applied candidate never becomes another attempt.
func autoResumeMaintenanceCancelled(o *autoMaintenanceOperation, receiptSHA string, r autoMaintenanceRegistry, g autoMaintenanceGate, now time.Time) error {
	if o == nil || o.State != "cancelled" || !o.StopRequested || !autoHash256(receiptSHA) || o.CancellationReceiptSHA != receiptSHA || o.ApplyReceiptSHA != "" || o.AppliedStateSHA != "" || o.AppliedInvocation != "" || o.HealthReceiptSHA != "" || o.RollbackReceiptSHA != "" {
		return errors.New("maintenance is not confirmed pre-effect cancellation")
	}
	if r.ResourceID != o.Authority.ResourceID || r.Digest != o.Authority.RegistrySHA || r.Protected || !r.Stateless {
		return errors.New("maintenance registry changed before renewal")
	}
	if err := autoMaintenanceQuota(g, now); err != nil {
		return err
	}
	for _, prior := range o.CancellationHistory {
		if prior == receiptSHA {
			return errors.New("maintenance cancellation receipt already consumed")
		}
	}
	o.CancellationHistory = append(o.CancellationHistory, receiptSHA)
	o.CancellationReceiptSHA = ""
	o.StopRequested = false
	o.State = "reserved"
	return nil
}

type autoMaintenanceLedger struct {
	Operations map[string]*autoMaintenanceOperation
	Owners     map[string]string
}
type autoMaintenanceApplyReceipt struct {
	AppliedStateSHA                                                string
	OperationID, ReceiptSHA, BeforeSHA, CandidateSHA, InvocationID string
}
type autoMaintenanceHealthReceipt struct {
	OperationID, ReceiptSHA, CandidateSHA, InvocationID string
	Healthy, FreshSamples, ValidMetrics                 bool
}
type autoMaintenanceRollbackReceipt struct {
	OperationID, ReceiptSHA, RestoredSHA string
	Healthy                              bool
}

// A privileged apply may compensate its own failed change before any successful
// apply receipt exists. This is authenticated journal evidence, not a worker's
// assertion and not evidence of an intermediate successful deployment.
type autoMaintenanceCompensationReceipt struct {
	OperationID, ReceiptSHA, BeforeSHA, CandidateSHA, RestoredSHA string
	Healthy                                                       bool
}

func autoRecordMaintenanceCompensation(o *autoMaintenanceOperation, r autoMaintenanceCompensationReceipt) error {
	if o == nil || r.OperationID != o.ID || !autoHash256(r.ReceiptSHA) || r.BeforeSHA != o.Authority.BeforeSHA || r.CandidateSHA != o.Authority.CandidateSHA || r.RestoredSHA != o.Authority.BeforeSHA || !r.Healthy {
		return errors.New("maintenance compensation evidence mismatch")
	}
	if o.RollbackReceiptSHA != "" {
		if o.State == "restored" && o.RollbackReceiptSHA == r.ReceiptSHA {
			return nil
		}
		return errors.New("maintenance compensation evidence immutable")
	}
	switch o.State {
	case "apply_pending", "rollback_pending", "rollback_applying":
	default:
		return errors.New("maintenance compensation has no owned effect intent")
	}
	o.RollbackReceiptSHA = r.ReceiptSHA
	o.State = "restored"
	return nil
}

func autoMaintenanceBinding(a autoMaintenanceAuthority) string {
	b, _ := json.Marshal(struct {
		Plan, Resource, Registry, Before, Candidate string
		Limits                                      autoMaintenanceLimits
		Predecessor                                 string `json:"predecessor_operation_id,omitempty"`
		Supersession                                string `json:"supersession_receipt_sha256,omitempty"`
	}{a.PlanSHA, a.ResourceID, a.RegistrySHA, a.BeforeSHA, a.CandidateSHA, a.Limits, a.PredecessorOperationID, a.SupersessionSHA})
	return autoSHA(b)
}

func autoMaintenanceCandidate(l autoMaintenanceLimits) string {
	b, _ := json.Marshal(l)
	return autoSHA(b)
}
func autoMaintenanceQuota(g autoMaintenanceGate, now time.Time) error {
	if g.Config.Validate() != nil || !g.Enabled || g.QuotaObservedAt.IsZero() || g.QuotaObservedAt.After(now) || now.Sub(g.QuotaObservedAt) > 6*time.Minute || !(g.RemainingPercent > g.Config.ReservePercent+g.Config.MarginPercent && g.RemainingPercent <= 100) {
		return errors.New("maintenance requires enabled mode and fresh quota above reserve")
	}
	return nil
}
func autoReserveMaintenance(l *autoMaintenanceLedger, a autoMaintenanceAuthority, r autoMaintenanceRegistry, g autoMaintenanceGate, now time.Time) (*autoMaintenanceOperation, error) {
	if l == nil {
		return nil, errors.New("maintenance ledger missing")
	}
	if !autoHash256(a.ExecutedValidationSHA) || !autoHash256(a.CandidateReviewSHA) || !autoHash256(a.PlanSHA) || a.TaskID <= 0 || !autoExpertJobID.MatchString(a.JobID) || a.ResourceID == "" || a.ResourceID != r.ResourceID || a.RegistrySHA != r.Digest || !autoHash256(r.Digest) || r.Protected || !r.Stateless || !autoHash256(a.BeforeSHA) || a.CandidateSHA != autoMaintenanceCandidate(a.Limits) {
		return nil, errors.New("maintenance authority or registry mismatch")
	}
	v := a.Limits
	if r.Min.CPUPercent <= 0 || r.Min.MemoryBytes <= 0 || r.Min.Tasks <= 0 || r.MemoryHeadroomBytes < 0 || v.CPUPercent < r.Min.CPUPercent || v.CPUPercent > r.Max.CPUPercent || v.MemoryBytes < r.Min.MemoryBytes || v.MemoryBytes > r.Max.MemoryBytes || v.Tasks < r.Min.Tasks || v.Tasks > r.Max.Tasks {
		return nil, errors.New("maintenance limits outside registered bounds")
	}
	binding := autoMaintenanceBinding(a)
	for _, e := range a.Audits {
		if !e.Successful || !autoHash256(e.ReceiptSHA) || e.BindingSHA != binding || e.TaskID <= 0 || e.TaskID == a.TaskID || !autoExpertJobID.MatchString(e.JobID) || e.JobID == a.JobID {
			return nil, errors.New("maintenance needs exact independent admitted audits")
		}
	}
	if a.Audits[0].TaskID == a.Audits[1].TaskID || a.Audits[0].JobID == a.Audits[1].JobID || a.Audits[0].ReceiptSHA == a.Audits[1].ReceiptSHA {
		return nil, errors.New("maintenance auditors must be distinct")
	}
	e := a.Validation
	if !e.Successful || e.BindingSHA != binding || !autoHash256(e.ReceiptSHA) || e.TaskID <= 0 || e.TaskID == a.TaskID || !autoExpertJobID.MatchString(e.JobID) || e.JobID == a.JobID {
		return nil, errors.New("maintenance independent validation missing")
	}
	b := a.Backup
	if !b.Verified || b.BeforeSHA != a.BeforeSHA || !autoHash256(b.ReceiptSHA) || !autoHash256(b.RestoreProofSHA) || !autoHash256(b.OffboxReceiptSHA) {
		return nil, errors.New("maintenance verified restorable backup missing")
	}
	id := autoMaintenanceAuthorityID(a)
	if (a.PredecessorOperationID == "") != (a.SupersessionSHA == "") {
		return nil, errors.New("maintenance successor binding incomplete")
	}
	if old := l.Operations[id]; old != nil {
		raw, _ := json.Marshal(old.Authority)
		next, _ := json.Marshal(a)
		if string(raw) != string(next) {
			return nil, errors.New("maintenance semantic operation already reserved under different authority")
		}
		return old, nil
	}
	if a.PredecessorOperationID != "" {
		if err := autoValidateMaintenanceSuccessor(l, a); err != nil {
			return nil, err
		}
	}
	if err := autoMaintenanceQuota(g, now); err != nil {
		return nil, err
	}
	if l.Operations == nil {
		l.Operations = map[string]*autoMaintenanceOperation{}
	}
	if l.Owners == nil {
		l.Owners = map[string]string{}
	}
	if owner := l.Owners[a.ResourceID]; owner != "" {
		old := l.Operations[owner]
		if old == nil || old.State != "verified" && old.State != "restored" && old.State != "cancelled" && old.State != "stale" && old.State != "superseded" {
			return nil, errors.New("maintenance resource already owned or unresolved")
		}
	}
	op := &autoMaintenanceOperation{ID: id, Authority: a, State: "reserved", CreatedAt: now}
	l.Operations[id] = op
	l.Owners[a.ResourceID] = id
	return op, nil
}
func autoBeginMaintenance(o *autoMaintenanceOperation, obs autoMaintenanceObservation, r autoMaintenanceRegistry, g autoMaintenanceGate, now time.Time) error {
	if o == nil || o.State != "reserved" || o.StopRequested {
		return errors.New("maintenance apply not permitted")
	}
	if err := autoMaintenanceQuota(g, now); err != nil {
		return err
	}
	if r.Protected || !r.Stateless || r.ResourceID != o.Authority.ResourceID || r.Digest != o.Authority.RegistrySHA || obs.RegistrySHA != r.Digest || obs.ConfigurationSHA != o.Authority.BeforeSHA {
		o.State = "stale"
		o.Reason = "configuration or registry changed"
		return errors.New(o.Reason)
	}
	if obs.CapturedAt.IsZero() || obs.CapturedAt.After(now) || now.Sub(obs.CapturedAt) > 30*time.Second || obs.InvocationID == "" || obs.CurrentTasks <= 0 || obs.CurrentTasks > o.Authority.Limits.Tasks/2 || obs.RSSBytes < 0 || obs.RSSBytes > o.Authority.Limits.MemoryBytes/2 || r.MemoryHeadroomBytes < 0 || obs.RSSBytes > o.Authority.Limits.MemoryBytes || r.MemoryHeadroomBytes > o.Authority.Limits.MemoryBytes-obs.RSSBytes {
		return errors.New("maintenance observation stale or insufficient memory headroom")
	}
	o.State = "apply_pending"
	return nil
}
func autoRecordMaintenanceApply(o *autoMaintenanceOperation, r autoMaintenanceApplyReceipt) error {
	if o == nil || r.OperationID != o.ID || !autoHash256(r.ReceiptSHA) || r.BeforeSHA != o.Authority.BeforeSHA || r.CandidateSHA != o.Authority.CandidateSHA || r.InvocationID == "" || !autoHash256(r.AppliedStateSHA) {
		return errors.New("maintenance apply receipt mismatch")
	}
	if o.ApplyReceiptSHA != "" {
		if o.ApplyReceiptSHA == r.ReceiptSHA && o.AppliedInvocation == r.InvocationID && o.AppliedStateSHA == r.AppliedStateSHA {
			return nil
		}
		return errors.New("maintenance apply evidence immutable")
	}
	if o.State != "apply_pending" {
		return errors.New("maintenance apply intent absent")
	}
	o.ApplyReceiptSHA = r.ReceiptSHA
	o.AppliedInvocation = r.InvocationID
	o.AppliedStateSHA = r.AppliedStateSHA
	o.State = "applied"
	if o.StopRequested {
		o.State = "rollback_pending"
	}
	return nil
}
func autoFinishMaintenanceHealth(o *autoMaintenanceOperation, r autoMaintenanceHealthReceipt) error {
	if o == nil || r.OperationID != o.ID || !autoHash256(r.ReceiptSHA) || r.CandidateSHA != o.Authority.CandidateSHA || r.InvocationID != o.AppliedInvocation || o.ApplyReceiptSHA == "" {
		return errors.New("maintenance health receipt mismatch")
	}
	if o.HealthReceiptSHA != "" {
		if o.HealthReceiptSHA == r.ReceiptSHA {
			return nil
		}
		return errors.New("maintenance health evidence immutable")
	}
	if o.State != "applied" && o.State != "rollback_pending" {
		return errors.New("maintenance health phase mismatch")
	}
	o.HealthReceiptSHA = r.ReceiptSHA
	if r.Healthy && r.FreshSamples && r.ValidMetrics && !o.StopRequested {
		o.State = "verified"
	} else {
		o.State = "rollback_pending"
	}
	return nil
}
func autoRequestMaintenanceStop(o *autoMaintenanceOperation) {
	if o == nil {
		return
	}
	// A committed result is historical evidence, not an unfinished owned effect.
	// In particular OFF must not revoke a previously verified deployment.
	switch o.State {
	case "verified", "restored", "cancelled", "stale", "superseded":
		return
	}
	o.StopRequested = true
	switch o.State {
	case "reserved":
		o.State = "cancelled"
	case "applied":
		o.State = "rollback_pending"
	}
}

// No quota gate here: owned compensation must finish even with autonomous mode OFF.
// A pending apply must first reconcile its privileged journal; never assume it did not happen.
func autoBeginMaintenanceRollback(o *autoMaintenanceOperation, obs autoMaintenanceObservation) error {
	if o == nil || o.State != "rollback_pending" {
		return errors.New("maintenance rollback not pending")
	}
	if obs.RegistrySHA != o.Authority.RegistrySHA || obs.ConfigurationSHA != o.AppliedStateSHA || obs.InvocationID != o.AppliedInvocation {
		o.State = "conflict"
		o.Reason = "external change prevents owned rollback"
		return errors.New(o.Reason)
	}
	o.State = "rollback_applying"
	return nil
}
func autoRecordMaintenanceRollback(o *autoMaintenanceOperation, r autoMaintenanceRollbackReceipt) error {
	if o == nil || r.OperationID != o.ID || !autoHash256(r.ReceiptSHA) || r.RestoredSHA != o.Authority.BeforeSHA || !r.Healthy {
		return errors.New("maintenance restoration not verified")
	}
	if o.RollbackReceiptSHA != "" {
		if o.RollbackReceiptSHA == r.ReceiptSHA {
			return nil
		}
		return errors.New("maintenance rollback evidence immutable")
	}
	if o.State != "rollback_applying" {
		return errors.New("maintenance rollback intent missing")
	}
	o.RollbackReceiptSHA = r.ReceiptSHA
	o.State = "restored"
	return nil
}

// Reconciliation records an independently observed external generation without
// claiming rollback. The helper must authenticate this receipt and health proof;
// no writes occur. A later fresh plan may acquire the resource again.
func autoResolveMaintenanceConflict(o *autoMaintenanceOperation, r autoMaintenanceExternalHealthReceipt, now time.Time) error {
	if o == nil || o.State != "conflict" || r.OperationID != o.ID || !autoHash256(o.ConflictReceiptSHA) || r.ConflictReceiptSHA != o.ConflictReceiptSHA || !autoHash256(r.ReceiptSHA) || r.RegistrySHA != o.Authority.RegistrySHA || !autoHash256(r.CurrentStateSHA) || r.PostStateSHA != r.CurrentStateSHA || r.InvocationID == "" || r.PostInvocationID != r.InvocationID || !r.NoMutation || !r.OwnershipChecked || r.OwnedCandidate || !r.Healthy || r.Profile != autoMaintenanceExternalProfile || r.CompletedAt.IsZero() || r.ObservedAt.IsZero() || r.CompletedAt.Before(r.ObservedAt) || r.CompletedAt.Sub(r.ObservedAt) > time.Minute || r.CompletedAt.After(now) || now.Sub(r.CompletedAt) > 30*time.Second || (r.CurrentStateSHA == o.AppliedStateSHA && r.InvocationID == o.AppliedInvocation) {
		return errors.New("external maintenance generation not freshly and independently verified")
	}
	o.ExternalResolution = &r
	o.State = "superseded"
	o.Reason += "; external healthy generation retained; no restoration or deployment claimed"
	return nil
}
