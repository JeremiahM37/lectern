package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// Runtime silence is not proof of a stall: legitimate tools and reasoning can
// be quiet. Only the runner's authoritative terminal process status permits
// recovery. Existing hard deadlines, quota and expert lifetime budgets remain.
func autoWorkerTerminalFailure(raw []byte) (string, bool) {
	var r struct {
		State  string `json:"state"`
		Exit   *int   `json:"exit_code"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", false
	}
	if r.State != "failed" && r.State != "stopped" {
		return "", false
	}
	if r.State == "failed" && (r.Exit == nil || *r.Exit == 0) {
		return "", false
	}
	if len(r.Reason) > 512 {
		r.Reason = r.Reason[:512]
	}
	if r.Reason == "" {
		r.Reason = "worker process ended without a successful final report"
	}
	return r.Reason, true
}
func autoWorkerBackoff(failures int) time.Duration {
	if failures <= 3 {
		return 30 * time.Minute
	}
	delay := 30 * time.Minute
	for n := 3; n < failures && delay < 6*time.Hour; n++ {
		delay *= 2
	}
	if delay > 6*time.Hour {
		return 6 * time.Hour
	}
	return delay
}
func (s *Server) deferAutoInterruptedWorker(ctx context.Context, a *autoRecord, j *autoJob, raw []byte, now time.Time) (bool, error) {
	reason, ok := autoWorkerTerminalFailure(raw)
	if !ok || j.ExpertRecoveryAttempt > 0 || j.PrivateIntegrationRoot != "" || j.DocumentationRoot > 0 {
		return false, nil
	}
	// One retained process is one attempt, even if archive polling, restarts or
	// retries observe its terminal result repeatedly. Counts never reset by day,
	// cycle, model output, archive/log churn, or allocating a fresh UUID.
	if !j.WorkerFailureRecorded {
		j.WorkerFailures++
		j.WorkerFailureRecorded = true
	}
	j.WorkerRecoveryReason = reason
	if j.WorkerFailures < 3 || !a.Config.Continuous {
		return false, nil
	}
	sha, err := s.autoArchiveIdentity(ctx, j)
	if err != nil {
		return false, err
	}
	if !autoHash256(sha) {
		return false, errors.New("interrupted source archive identity missing")
	}
	// A paired auditor still running must be stopped and exported before the
	// admitted state can leave the active scheduler. Never orphan its process.
	var peers []*autoJob
	for _, id := range a.State.ActiveTaskIDs() {
		peer := autoFindJob(a, id)
		if peer == nil {
			return false, errors.New("missing interrupted-cycle peer")
		}
		if peer.ID == j.ID {
			continue
		}
		if peer.ExpertRecoveryAttempt > 0 || peer.PrivateIntegrationRoot != "" || peer.DocumentationRoot > 0 {
			return false, nil
		}
		if peer.Status == "prepared" {
			// A persisted prepare/copy owner may not have a workspace yet. Its
			// controller status proves no model launch was attempted. Stop only
			// owned preparation helpers and retain that same prepared UUID.
			if len(peer.DocumentationCopies) > 0 {
				if err := s.stopAutoProgressCopies(ctx, a, peer); err != nil {
					return false, err
				}
			}
			if autoNodePending(peer) {
				if err := s.stopAutoNode(ctx, a, peer); err != nil {
					return false, err
				}
			}
			if autoPythonPending(peer) {
				if err := s.stopAutoWorkerPreparation(ctx, "python-dependencies-stop", peer.ID); err != nil {
					return false, err
				}
				peer.PythonStopped = true
			}
			if peer.Recovery != nil {
				if err := s.stopAutoWorkerPreparation(ctx, "dependencies-stop", peer.ID); err != nil {
					return false, err
				}
				peer.Recovery = nil
			}
			peer.WorkerRecoveryPrepared = true
			peers = append(peers, peer)
			continue
		}
		stopped, err := s.runAutoCommand(ctx, "stop", "--job", peer.ID)
		if err != nil {
			return false, err
		}
		var st struct {
			State string `json:"state"`
		}
		if json.Unmarshal(stopped, &st) != nil || (st.State != "stopped" && st.State != "failed" && st.State != "done") {
			return false, errors.New("interrupted-cycle peer termination unconfirmed")
		}
		if err := s.snapshotAutoJob(ctx, peer); err != nil {
			return false, err
		}
		peerSHA, err := s.autoArchiveIdentity(ctx, peer)
		if err != nil {
			return false, err
		}
		if !autoHash256(peerSHA) {
			return false, errors.New("interrupted peer archive missing")
		}
		peer.WorkerRecoveryArchive = peerSHA
		peers = append(peers, peer)
	}
	j.WorkerRecoveryArchive = sha
	if j.WorkerRecoveryAt.IsZero() {
		j.WorkerRecoveryAt = now.Add(autoWorkerBackoff(j.WorkerFailures))
	}
	j.Summary = fmt.Sprintf("Interrupted worker retained after %d process failures; earliest fair retry %s. No acceptance or review outcome inferred.", j.WorkerFailures, j.WorkerRecoveryAt.UTC().Format(time.RFC3339))
	for _, peer := range peers {
		peer.Status = "deferred"
		peer.WorkerRecoveryAt = j.WorkerRecoveryAt
		peer.WorkerRecoveryReason = "paired assignment retained during interrupted worker backoff"
		s.closeAutoBridge(peer.ID)
		_ = s.DB.Update("tasks", peer.TaskID, map[string]any{"status": "backlog"})
	}
	autoDeferRequirements(a, j, now)
	s.closeAutoBridge(j.ID)
	a.Reason = j.Summary
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "backlog"})
	return true, nil
}

// Reuse the proven asynchronous archive-resume transport. Persist the new owner
// and copy intent before any prepare effect; retries recover the same UUID.
func (s *Server) resumeAutoInterruptedWorker(ctx context.Context, a *autoRecord, old *autoJob) error {
	raw, err := s.runAutoCommand(ctx, "status", "--job", old.ID)
	if err != nil {
		return err
	}
	var status struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return errors.New("invalid interrupted source status")
	}
	if status.State != "done" && status.State != "failed" && status.State != "stopped" && status.State != "prepared" {
		return fmt.Errorf("%w: source process has not authoritatively stopped", errAutoArtifactPending)
	}
	if err := s.snapshotAutoJob(ctx, old); err != nil {
		return fmt.Errorf("%w: preserving exact resume source: %v", errAutoArtifactPending, err)
	}
	sha, err := s.autoArchiveIdentity(ctx, old)
	if err != nil {
		return err
	}
	if old.WorkerRecoveryArchive != "" && old.WorkerRecoveryArchive != sha {
		return errors.New("interrupted source archive changed after quarantine")
	}
	// A saved pending owner is authoritative after a lost response.
	if current := autoFindJob(a, old.TaskID); current != nil && current.ID != old.ID {
		return nil
	}
	j := *old
	j.ID = autoUUID()
	j.Status = "prepared"
	j.StartedAt = time.Now()
	j.ArtifactPath = filepath.Join(autoRoot, j.ID, "work")
	j.LaunchRetryPaid = false
	j.DocumentationStopped = false
	j.WorkerFailureRecorded = false
	j.WorkerRecoveryAt = time.Time{}
	j.WorkerRecoveryArchive = ""
	autoPreparePythonResume(old, &j)
	autoPrepareNodeResume(old, &j)
	j.DocumentationCopies = []autoDocumentationCopy{{Command: "copy-archive-resume", SourceJob: old.ID, SHA: sha, Generation: 1}}
	if old.ReportError != "" {
		j.ReportRepairs++
		j.ReportRetryAt = time.Time{}
	}
	if old.Admission != nil {
		receipt := *old.Admission
		receipt.JobID = j.ID
		j.Admission = &receipt
	}
	a.Jobs = append(a.Jobs, &j)
	return s.saveAuto(a)
}

// Paired audits are retained as one admitted state and resumed together only
// after both exact archives exist and the shared backoff has elapsed.
func autoResumePairedWorkerRecovery(a *autoRecord, now time.Time) bool {
	for _, held := range []bool{true, false} {
		states := a.DeferredRuns
		if held {
			states = a.HeldRuns
		}
		for i, state := range states {
			ids := state.ActiveTaskIDs()
			if len(ids) < 2 {
				continue
			}
			ready := true
			for _, id := range ids {
				j := autoFindJob(a, id)
				if j == nil || j.Status != "deferred" || j.WorkerRecoveryAt.IsZero() || now.Before(j.WorkerRecoveryAt) || (!j.WorkerRecoveryPrepared && !autoHash256(j.WorkerRecoveryArchive)) || j.RequirementHold {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			if a.State != nil {
				a.Runs = append(a.Runs, a.State)
			}
			a.State = state
			if held {
				a.HeldRuns = append(a.HeldRuns[:i], a.HeldRuns[i+1:]...)
			} else {
				a.DeferredRuns = append(a.DeferredRuns[:i], a.DeferredRuns[i+1:]...)
			}
			for _, id := range ids {
				j := autoFindJob(a, id)
				j.Status = "stopped"
				if j.WorkerRecoveryPrepared {
					j.Status = "prepared"
					j.WorkerRecoveryPrepared = false
					j.WorkerRecoveryAt = time.Time{}
				}
			}
			a.NextCycleScheduled = false
			a.NextCycleAt = time.Time{}
			a.RetryAt = time.Time{}
			a.Status = string(state.Phase)
			a.Reason = "Resuming exact retained paired assignment after bounded worker backoff"
			return true
		}
	}
	return false
}

func (s *Server) stopAutoWorkerPreparation(ctx context.Context, command, job string) error {
	raw, err := s.runAutoCommand(ctx, command, "--job", job)
	if err != nil {
		return err
	}
	var result struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &result) != nil || result.State != "stopped" {
		return errors.New("paired preparation helper termination unconfirmed")
	}
	return nil
}
