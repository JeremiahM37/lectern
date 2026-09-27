package api

import (
	"encoding/json"
	"errors"
)

// Fixed execution adapters select only a bundle actually delivered to their
// admitted owner. A package request or cached catalog entry is not delivery.
func autoSelectNodeTestRuntime(j *autoJob, r *autoExpertProbeRuntime) error {
	if j.NodeUsedBundle == "" {
		if j.NodeRequest != nil {
			return errors.New("selected Node environment has not been delivered to this worker")
		}
		return nil
	}
	n := j.NodeRecovery
	if n == nil || n.State != "verified" || n.Generation != autoNodeGeneration(j) || !autoNodeBound(j.NodeRequest, *n) || n.BundleKey != j.NodeUsedBundle {
		return errors.New("Node executed runtime identity unavailable")
	}
	for _, h := range []string{n.BundleKey, n.InputKey, n.LockSHA, n.RuntimeDigest, n.ProbeSHA} {
		if !autoHash256(h) {
			return errors.New("Node executed runtime identity incomplete")
		}
	}
	if j.NodeRequest.PackageLock != "" && (!autoHash256(n.SourceManifestSHA) || !autoHash256(n.SourceLockSHA)) {
		return errors.New("Node source lock identity unavailable")
	}
	r.NodeBundle = n.BundleKey
	r.NodeInput = n.InputKey
	r.NodeLock = n.LockSHA
	r.NodeRuntime = n.RuntimeDigest
	return nil
}
func autoBindExpertNodeRuntime(lease *autoExpertProbeLease, raw []byte) error {
	if lease == nil || autoSHA(raw) != lease.RequestKey {
		return errors.New("expert runtime request binding mismatch")
	}
	var r autoExpertProbeRequest
	if json.Unmarshal(raw, &r) != nil {
		return errors.New("invalid expert runtime request")
	}
	if lease.Runtime != nil && *lease.Runtime != r.Runtime {
		return errors.New("expert runtime reservation changed")
	}
	copy := r.Runtime
	lease.Runtime = &copy
	return nil
}
func autoExpertNodeReceiptRuntime(lease *autoExpertProbeLease, r autoExpertProbeReceipt) error {
	executed := r.State == "exited" || r.Executed != nil && *r.Executed
	if !executed {
		return nil
	}
	if lease.Runtime == nil {
		if r.Runtime != nil && (r.Runtime.NodeBundle != "" || r.Runtime.NodeInput != "" || r.Runtime.NodeLock != "" || r.Runtime.NodeRuntime != "") {
			return errors.New("unreserved Node runtime in legacy expert probe")
		}
		return nil
	}
	if lease.Runtime.NodeBundle == "" {
		if r.Runtime != nil && (r.Runtime.NodeBundle != "" || r.Runtime.NodeInput != "" || r.Runtime.NodeLock != "" || r.Runtime.NodeRuntime != "") {
			return errors.New("unexpected Node runtime in expert probe")
		}
		return nil
	}
	if r.Runtime == nil || *r.Runtime != *lease.Runtime {
		return errors.New("expert probe executed Node runtime differs from reserved environment")
	}
	return nil
}

// Captured during source pinning, before plan audits. The source archive is the
// retained executed output, while its runtime receipt may correctly refer to an
// earlier input archive; those are distinct provenance identities.
type autoExpertNodeSource struct {
	Job        string                 `json:"job"`
	TaskID     int64                  `json:"task_id"`
	ArchiveSHA string                 `json:"archive_sha256"`
	ReceiptSHA string                 `json:"receipt_sha256"`
	Runtime    autoExpertProbeRuntime `json:"runtime"`
}

func autoNodeReceiptIdentity(r *autoNodeReceipt) string {
	raw, _ := json.Marshal(r)
	return autoSHA(raw)
}
func autoPinExpertNodeSource(pin *autoExpertRecoveryPin, j *autoJob) error {
	if j.NodeUsedBundle == "" {
		return nil
	}
	if j.ID != pin.SourceJob || j.TaskID != pin.SourceTaskID || !autoHash256(pin.SourceSHA) {
		return errors.New("Node source does not match pinned archive owner")
	}
	var runtime autoExpertProbeRuntime
	if e := autoSelectNodeTestRuntime(j, &runtime); e != nil {
		return e
	}
	pin.NodeSource = &autoExpertNodeSource{Job: j.ID, TaskID: j.TaskID, ArchiveSHA: pin.SourceSHA, ReceiptSHA: autoNodeReceiptIdentity(j.NodeRecovery), Runtime: runtime}
	return nil
}
func autoUseExpertNodeSource(a *autoRecord, pin *autoExpertRecoveryPin, runtime *autoExpertProbeRuntime) error {
	var source *autoJob
	for _, j := range a.Jobs {
		if j.ID == pin.SourceJob && j.TaskID == pin.SourceTaskID {
			source = j
			break
		}
	}
	if pin.NodeSource == nil {
		if source != nil && source.NodeUsedBundle != "" {
			return errors.New("expert source Node environment was not pinned; obtain a fresh source pin")
		}
		return nil
	}
	n := pin.NodeSource
	if source == nil || n.Job != pin.SourceJob || n.TaskID != pin.SourceTaskID || n.ArchiveSHA != pin.SourceSHA || n.ReceiptSHA != autoNodeReceiptIdentity(source.NodeRecovery) {
		return errors.New("expert Node source receipt/archive changed")
	}
	var selected autoExpertProbeRuntime
	if e := autoSelectNodeTestRuntime(source, &selected); e != nil {
		return e
	}
	if selected != n.Runtime {
		return errors.New("expert source Node use differs from pinned environment")
	}
	runtime.NodeBundle = selected.NodeBundle
	runtime.NodeInput = selected.NodeInput
	runtime.NodeLock = selected.NodeLock
	runtime.NodeRuntime = selected.NodeRuntime
	return nil
}
