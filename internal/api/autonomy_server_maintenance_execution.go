package api

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Controller-owned transport identity, persisted with the exact request before effects.
type autoMaintenanceExecutionBinding struct {
	OperationID, OwnerJob, PinSHA, Phase, RequestSHA, SemanticRequestSHA, AuthoritySHA string
	BeforeSHA, CandidateSHA                                                            string
	OwnerTask                                                                          int64
	Generation                                                                         int
}
type autoMaintenanceExecutionResult struct {
	NoEffects                 bool
	InnerReceiptSHA           string
	Compensation              *autoMaintenanceCompensationReceipt
	State, ReceiptSHA, Reason string
	Raw                       json.RawMessage
	Backup                    *autoMaintenanceBackup
	Apply                     *autoMaintenanceApplyReceipt
	Health                    *autoMaintenanceHealthReceipt
	Restoration               *autoMaintenanceRollbackReceipt
}

func autoMaintenanceJSON(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var normalized any
	if e = d.Decode(&normalized); e != nil {
		return nil, e
	}
	return autoMaintenanceCanonical(normalized)
}
func autoMaintenanceExecutionRequest(a *autoMaintenanceAdmitted, generation int) (map[string]any, error) {
	if a == nil || a.Pin.Key != autoMaintenancePinHash(a.Pin) || !autoHash256(a.Pin.Key) || a.TaskID <= 0 || !autoExpertJobID.MatchString(a.JobID) || generation < 1 {
		return nil, errors.New("maintenance execution admission invalid")
	}
	p := a.Pin
	return map[string]any{"schema_version": 1, "operation_id": autoMaintenanceOperationID(p), "owner_job": a.JobID, "owner_task": a.TaskID, "pin_sha256": p.Key, "target_id": p.TargetID, "service_id": p.ServiceID, "action": "service_resource_limits", "registry_sha256": p.RegistrySHA, "expected_state_sha256": p.BeforeSHA, "limits": p.Limits, "generation": generation}, nil
}
func autoMaintenanceExecutionBind(a *autoMaintenanceAdmitted, generation int, phase string, raw []byte) autoMaintenanceExecutionBinding {
	return autoMaintenanceExecutionBinding{OperationID: autoMaintenanceOperationID(a.Pin), OwnerJob: a.JobID, OwnerTask: a.TaskID, PinSHA: a.Pin.Key, Generation: generation, Phase: phase, RequestSHA: autoSHA(raw), BeforeSHA: a.Pin.BeforeSHA, CandidateSHA: a.Pin.CandidateSHA}
}
func autoBuildMaintenanceBackup(a *autoMaintenanceAdmitted, generation int) ([]byte, autoMaintenanceExecutionBinding, error) {
	var empty autoMaintenanceExecutionBinding
	r, e := autoMaintenanceExecutionRequest(a, generation)
	if e != nil {
		return nil, empty, e
	}
	raw, e := autoMaintenanceJSON(r)
	if e != nil {
		return nil, empty, e
	}
	return raw, autoMaintenanceExecutionBind(a, generation, "backup", raw), nil
}

// The original validation execution UUID may differ from the latest archived
// reviewer UUID after report correction. Neither is replaced with the builder.
func autoBuildMaintenanceExecution(a *autoMaintenanceAdmitted, v autoMaintenanceValidationReceipt, review autoMaintenanceFinalReview, backup autoMaintenanceBackup, generation int) ([]byte, []byte, autoMaintenanceExecutionBinding, error) {
	var empty autoMaintenanceExecutionBinding
	auth, e := autoReviewedMaintenanceAuthority(a, v, review, backup)
	if e != nil {
		return nil, nil, empty, e
	}
	request, e := autoMaintenanceExecutionRequest(a, generation)
	if e != nil {
		return nil, nil, empty, e
	}
	semantic := map[string]any{}
	for k, val := range request {
		if k != "generation" {
			semantic[k] = val
		}
	}
	raw, e := autoMaintenanceJSON(semantic)
	if e != nil {
		return nil, nil, empty, e
	}
	semanticSHA := autoSHA(raw)
	authority := map[string]any{}
	for _, k := range []string{"operation_id", "target_id", "service_id", "registry_sha256", "expected_state_sha256", "pin_sha256", "owner_job", "owner_task", "action"} {
		authority[k] = request[k]
	}
	authority["request_sha256"] = semanticSHA
	authority["audit_receipt_sha256"] = map[string]string{"auditor_a": auth.Audits[0].ReceiptSHA, "auditor_b": auth.Audits[1].ReceiptSHA}
	authority["candidate_review_sha256"] = review.ReportSHA
	authority["validation_receipt_sha256"] = v.ReceiptSHA
	authority["validation_owner_job"] = v.OwnerJob
	authority["validation_owner_task"] = v.OwnerTask
	authority["backup_receipt_sha256"] = backup.ReceiptSHA
	authorityRaw, e := autoMaintenanceJSON(authority)
	if e != nil {
		return nil, nil, empty, e
	}
	request["authority_sha256"] = autoSHA(authorityRaw)
	requestRaw, e := autoMaintenanceJSON(request)
	if e != nil {
		return nil, nil, empty, e
	}
	binding := autoMaintenanceExecutionBind(a, generation, "apply", requestRaw)
	binding.AuthoritySHA = autoSHA(authorityRaw)
	binding.SemanticRequestSHA = semanticSHA
	return requestRaw, authorityRaw, binding, nil
}
func autoDecodeMaintenanceExecution(raw []byte, b autoMaintenanceExecutionBinding) (*autoMaintenanceExecutionResult, error) {
	fail := func() (*autoMaintenanceExecutionResult, error) {
		return nil, errors.New("maintenance execution receipt binding invalid")
	}
	if len(raw) == 0 || len(raw) > 256<<10 || !autoHash256(b.RequestSHA) || !autoHash256(b.OperationID) || !autoHash256(b.PinSHA) || !autoHash256(b.BeforeSHA) || !autoHash256(b.CandidateSHA) || b.Generation < 1 || b.OwnerTask <= 0 || !autoExpertJobID.MatchString(b.OwnerJob) {
		return fail()
	}
	if b.Phase != "backup" && b.Phase != "apply" && b.Phase != "reconcile" {
		return fail()
	}
	if e := autoVerifyMaintenanceSeal(raw); e != nil {
		return nil, e
	}
	var out struct {
		Schema     int             `json:"schema_version"`
		Operation  string          `json:"operation_id"`
		Owner      string          `json:"owner_job"`
		Task       int64           `json:"owner_task"`
		Pin        string          `json:"pin_sha256"`
		Generation int             `json:"generation"`
		Request    string          `json:"request_sha256"`
		Phase      string          `json:"phase"`
		State      string          `json:"state"`
		SHA        string          `json:"receipt_sha256"`
		Reason     string          `json:"reason"`
		Result     json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Schema != 1 || out.Operation != b.OperationID || out.Owner != b.OwnerJob || out.Task != b.OwnerTask || out.Pin != b.PinSHA || out.Generation != b.Generation || out.Request != b.RequestSHA || out.Phase != b.Phase {
		return fail()
	}
	r := &autoMaintenanceExecutionResult{State: out.State, ReceiptSHA: out.SHA, Reason: out.Reason, Raw: append(json.RawMessage(nil), raw...)}
	switch out.State {
	case "running", "waiting", "cancelled", "unavailable", "conflict", "reconciliation_required":
	case "offbox_restored_verified":
		if b.Phase != "backup" {
			return fail()
		}
	case "applied", "rolled_back", "rollback_conflict":
		if b.Phase == "backup" {
			return fail()
		}
	default:
		return fail()
	}
	if len(out.Result) == 0 {
		if out.State == "offbox_restored_verified" || out.State == "applied" || out.State == "rolled_back" || out.State == "rollback_conflict" {
			return fail()
		}
		return r, nil
	}
	if e := autoVerifyMaintenanceSeal(out.Result); e != nil {
		return nil, e
	}
	var inner struct {
		State          string `json:"state"`
		NoEffects      *bool  `json:"no_effects"`
		EffectsStarted *bool  `json:"effects_started"`
		Reason         string `json:"reason"`
		SHA            string `json:"receipt_sha256"`
		Before         string `json:"before_sha256"`
		Operation      string `json:"operation_id"`
		Request        string `json:"request_sha256"`
		Authority      string `json:"authority_sha256"`
		Candidate      string `json:"candidate_sha256"`
		Generation     int    `json:"generation"`
		Applied        string `json:"applied_state_sha256"`
		Restored       string `json:"restored_sha256"`
		Invocation     string `json:"invocation_id"`
		Snapshot       string `json:"snapshot_id"`
		Repository     string `json:"repository_id"`
		RepositorySHA  string `json:"repository_sha256"`
		Profile        string `json:"profile_sha256"`
		Manifest       string `json:"restored_manifest_sha256"`
		RestoreProof   string `json:"restore_proof_sha256"`
		Offbox         string `json:"offbox_receipt_sha256"`
		Observations   []struct {
			Healthy  *bool    `json:"healthy"`
			At       *float64 `json:"observed_at"`
			Response string   `json:"response_sha256"`
			Metrics  string   `json:"metrics_sha256"`
		} `json:"observations"`
	}
	if json.Unmarshal(out.Result, &inner) != nil || inner.State != out.State || inner.Before != b.BeforeSHA {
		return fail()
	}
	if inner.Reason != "" {
		r.Reason = inner.Reason
	}
	if b.Phase == "backup" {
		if out.State != "offbox_restored_verified" {
			return fail()
		}
		for _, h := range []string{inner.Snapshot, inner.Repository, inner.RepositorySHA, inner.Profile, inner.Manifest, inner.RestoreProof, inner.Offbox} {
			if !autoHash256(h) {
				return fail()
			}
		}
		r.Backup = &autoMaintenanceBackup{ReceiptSHA: inner.SHA, BeforeSHA: inner.Before, RestoreProofSHA: inner.RestoreProof, OffboxReceiptSHA: inner.Offbox, Verified: true}
		return r, nil
	}
	if !autoHash256(b.AuthoritySHA) || !autoHash256(b.SemanticRequestSHA) || inner.Operation != b.OperationID || inner.Request != b.SemanticRequestSHA || inner.Authority != b.AuthoritySHA || inner.Candidate != b.CandidateSHA || inner.Generation != b.Generation {
		return fail()
	}
	r.InnerReceiptSHA = inner.SHA
	if inner.NoEffects != nil && *inner.NoEffects {
		if inner.EffectsStarted == nil || *inner.EffectsStarted || (out.State != "cancelled" && out.State != "unavailable" && out.State != "conflict") {
			return fail()
		}
		r.NoEffects = true
	}
	if out.State == "applied" || out.State == "rolled_back" {
		minimum := 1
		if out.State == "applied" {
			minimum = 3
		}
		if len(inner.Observations) < minimum || len(inner.Observations) > 16 {
			return fail()
		}
		previous := float64(0)
		for _, o := range inner.Observations {
			if o.Healthy == nil || !*o.Healthy || o.At == nil || *o.At <= previous || !autoHash256(o.Response) || !autoHash256(o.Metrics) {
				return fail()
			}
			previous = *o.At
		}
		if out.State == "applied" {
			if !autoHash256(inner.Applied) || inner.Invocation == "" {
				return fail()
			}
			r.Apply = &autoMaintenanceApplyReceipt{OperationID: b.OperationID, ReceiptSHA: inner.SHA, BeforeSHA: inner.Before, CandidateSHA: inner.Candidate, InvocationID: inner.Invocation, AppliedStateSHA: inner.Applied}
			r.Health = &autoMaintenanceHealthReceipt{OperationID: b.OperationID, ReceiptSHA: inner.SHA, CandidateSHA: inner.Candidate, InvocationID: inner.Invocation, Healthy: true, FreshSamples: true, ValidMetrics: true}
		} else {
			if inner.Restored != b.BeforeSHA {
				return fail()
			}
			r.Compensation = &autoMaintenanceCompensationReceipt{OperationID: b.OperationID, ReceiptSHA: inner.SHA, BeforeSHA: inner.Before, CandidateSHA: inner.Candidate, RestoredSHA: inner.Restored, Healthy: true}
			r.Restoration = &autoMaintenanceRollbackReceipt{OperationID: b.OperationID, ReceiptSHA: inner.SHA, RestoredSHA: inner.Restored, Healthy: true}
		}
	}
	return r, nil
}

// Stop acknowledgements come directly from the fixed privileged runner. A
// reconciliation_required response is not a completed cancellation.
func autoDecodeMaintenanceExecutionStop(raw []byte, operation string, generation int) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 || !autoHash256(operation) || generation < 1 {
		return "", errors.New("invalid maintenance stop binding")
	}
	var body struct {
		Operation  string `json:"operation_id"`
		Generation int    `json:"generation"`
		State      string `json:"state"`
		NoEffects  *bool  `json:"no_effects"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Operation != operation || body.Generation != generation {
		return "", errors.New("maintenance stop acknowledgement differs")
	}
	switch body.State {
	case "stopped":
		if body.NoEffects == nil || !*body.NoEffects {
			return "", errors.New("maintenance stop does not prove absence of effects")
		}
		return body.State, nil
	case "stopping", "reconciliation_required":
		return body.State, nil
	}
	return "", errors.New("unknown maintenance stop acknowledgement")
}
