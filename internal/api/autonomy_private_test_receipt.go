package api

import (
	"encoding/json"
	"errors"
)

func autoDecodeBoundPrivateTestReceipt(raw []byte, integration string, lease *autoPrivateTestLease, observationGeneration int) (autoPrivateTestReceipt, error) {
	r, err := autoDecodePrivateTestReceipt(raw)
	if err != nil {
		return r, err
	}
	var evidence struct {
		Scope     string                  `json:"evidence_scope"`
		Execution *int                    `json:"execution_generation"`
		Revoked   int                     `json:"revoked_through_generation"`
		Runtime   *autoExpertProbeRuntime `json:"runtime"`
	}
	if json.Unmarshal(raw, &evidence) != nil || lease == nil || r.IntegrationID != integration || r.CheckID != lease.ID || r.RequestSHA != lease.RequestSHA || r.OwnerJob != lease.OwnerJob || r.Generation != observationGeneration || !autoHash256(r.ReceiptSHA) {
		return r, errors.New("private test receipt authority mismatch")
	}
	if evidence.Scope == "revoked_missing_request" {
		if r.State != "cancelled" || r.Reason != "cancelled_missing_request" || r.Executed || r.ChargedMilliseconds != 0 || r.ExitCode != nil || evidence.Execution != nil || lease.Generation < 1 || evidence.Revoked < lease.Generation || r.OwnerTask != 0 || r.CandidateSHA != "" {
			return r, errors.New("invalid revoked missing-request diagnostic")
		}
		// These fields come from the durable reservation, not a claimed launch.
		// Retain the exact root diagnostic so that provenance remains inspectable.
		r.OwnerTask = lease.OwnerTask
		r.CandidateSHA = lease.CandidateSHA
		r.Generation = lease.Generation
		r.ReceiptEvidence = append(json.RawMessage(nil), raw...)
		if lease.Receipt != nil && lease.Receipt.ReceiptSHA == r.ReceiptSHA {
			r.ReceiptEvidence = append(json.RawMessage(nil), lease.Receipt.ReceiptEvidence...)
		}
		return r, nil
	}
	if evidence.Scope != "" || evidence.Execution == nil || *evidence.Execution != lease.Generation || *evidence.Execution < 1 || r.OwnerTask != lease.OwnerTask || r.CandidateSHA != lease.CandidateSHA {
		return r, errors.New("private test execution lease mismatch")
	}
	r.Generation = *evidence.Execution
	if r.Executed {
		var request autoPrivateTestRequest
		if len(lease.RequestRaw) == 0 || json.Unmarshal(lease.RequestRaw, &request) != nil || evidence.Runtime == nil || request.Runtime != *evidence.Runtime {
			return r, errors.New("private test executed runtime does not match reserved request")
		}
		r.GoDependencyKey = evidence.Runtime.GoDependency
		r.GoBundleDigest = evidence.Runtime.GoBundle
		r.GoToolchainDigest = evidence.Runtime.GoToolchain
		r.NodeBundleKey = evidence.Runtime.NodeBundle
		r.NodeInputKey = evidence.Runtime.NodeInput
		r.NodeLockSHA = evidence.Runtime.NodeLock
		r.NodeRuntimeDigest = evidence.Runtime.NodeRuntime
		r.PythonBundleKey = evidence.Runtime.PythonBundle
		r.PythonInputKey = evidence.Runtime.PythonInput
		r.BrowserKey = evidence.Runtime.Browser
		r.PythonTestKey = evidence.Runtime.PythonTest
		r.ReceiptEvidence = append(json.RawMessage(nil), raw...)
		if lease.Receipt != nil && lease.Receipt.ReceiptSHA == r.ReceiptSHA {
			r.ReceiptEvidence = append(json.RawMessage(nil), lease.Receipt.ReceiptEvidence...)
		}
	}
	return r, nil
}
