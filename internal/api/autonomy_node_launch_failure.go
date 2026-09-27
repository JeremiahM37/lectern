package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// The model never started: this is a failed prerequisite, not a malformed
// worker report. Only a terminal process and the exact selected runtime can
// enter this path. The consumed UUID remains in history.
func (s *Server) handleAutoNodeLaunchFailure(ctx context.Context, a *autoRecord, j *autoJob, raw []byte) (bool, error) {
	var st struct {
		State string          `json:"state"`
		Node  json.RawMessage `json:"node_runtime"`
	}
	if json.Unmarshal(raw, &st) != nil || len(st.Node) == 0 || (st.State != "failed" && st.State != "stopped") || j.NodeRequest == nil {
		return false, nil
	}
	var failure struct {
		autoNodeReceipt
		Executed *bool  `json:"executed"`
		Scope    string `json:"scope"`
	}
	if json.Unmarshal(st.Node, &failure) != nil || failure.State != "unavailable" {
		return false, nil
	}
	if !failure.Unsupported || failure.Executed == nil || *failure.Executed || failure.Scope != "full content validation before model execution" || (failure.Diagnostic != "missing" && failure.Diagnostic != "integrity") {
		return true, errors.New("Node failure does not establish pre-execution validation")
	}
	prior := j.NodeRecovery
	if !autoNodeBound(j.NodeRequest, failure.autoNodeReceipt) || failure.Generation != autoNodeGeneration(j) || prior == nil || (prior.State != "verified" && !j.NodeNeedsResume) || prior.InputKey != failure.InputKey || prior.BundleKey != failure.BundleKey || prior.LockSHA != failure.LockSHA || prior.RuntimeDigest != failure.RuntimeDigest || prior.ProbeSHA != failure.ProbeSHA {
		return true, errors.New("Node launch failure differs from selected runtime")
	}
	// Export before changing assignment state. A failed export is retried against
	// the same terminal process; no model or new UUID is launched here.
	if err := s.snapshotAutoJob(ctx, j); err != nil {
		return true, err
	}
	if _, err := autoApplyNodeReceipt(a, j, st.Node); err != nil {
		return true, err
	}
	j.NodeNeedsResume = true
	if err := s.recordAutoNodeProvisionerRequirement(a, j); err != nil {
		return true, err
	}
	j.RecoveryCheckAt = time.Now().Add(5 * time.Minute)
	s.closeAutoBridge(j.ID)
	autoDeferRequirements(a, j, time.Now())
	return true, s.saveAuto(a)
}
