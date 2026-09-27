package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Reservation precedes every external preparation effect. An interrupted
// preparation reuses its UUID, independently of the currently available catalog.
func (s *Server) reserveAutoProgress(ctx context.Context, a *autoRecord, role, id string) (string, *autoExpertRecoveryAttempt, error) {
	if role == "reviewer" {
		for i := len(a.State.Assignments) - 1; i >= 0; i-- {
			as := a.State.Assignments[i]
			if as.Role != "builder" || as.Item != a.State.Item || as.Step != a.State.Step || as.Round != a.State.Revision {
				continue
			}
			b := autoFindJob(a, as.TaskID)
			if b == nil || b.ExpertRecoveryAttempt == 0 {
				break
			}
			v, err := autoExpertAttempt(a, b)
			if err != nil {
				return "", nil, err
			}
			if v.ReviewerJob == "" {
				v.ReviewerJob = id
			}
			if err = s.saveAuto(a); err != nil {
				return "", nil, err
			}
			return v.ReviewerJob, v, nil
		}
	}
	if role != "builder" || a.State.Item >= len(a.State.Items) {
		return id, nil, nil
	}
	p := a.State.Items[a.State.Item]
	if p.ExpertRecoveryTaskID == 0 {
		return id, nil, nil
	}
	if err := s.validateAutoExpertSources(ctx, a, []autonomy.Proposal{p}); err != nil {
		return "", nil, err
	}
	pin, err := autoExpertPin(a, p)
	if err != nil {
		return "", nil, err
	}
	for _, v := range a.ExpertRecovery.Attempts[pin.RootTaskID] {
		if v.Cycle == a.State.Cycle && v.Revision == a.State.Revision && v.Item == a.State.Item && store.J(v.Proposal) == store.J(p) && !autoExpertTerminal(v.Status) {
			id = v.JobID
			break
		}
	}
	provider, _, err := s.autoRoute(a, role, time.Now())
	if err != nil {
		return "", nil, err
	}
	v, err := autoReserveExpertRecovery(a, p, id, []string{provider}, time.Now())
	if err != nil {
		return "", nil, err
	}
	if err = s.saveAuto(a); err != nil {
		return "", nil, err
	}
	return id, v, nil
}

func autoProgressCopies(a *autoRecord, role string) ([]autoDocumentationCopy, error) {
	var copies []autoDocumentationCopy
	if role != "auditor_a" && role != "auditor_b" && role != "builder" {
		return copies, nil
	}
	for i, p := range a.State.Items {
		if p.ExpertRecoveryTaskID == 0 || (role == "builder" && i != a.State.Item) {
			continue
		}
		pin, err := autoExpertPin(a, p)
		if err != nil {
			return nil, err
		}
		command := "copy-archive-review"
		if role == "builder" {
			command = "copy-archive-work"
		}
		copies = append(copies, autoDocumentationCopy{Command: command, SourceJob: pin.SourceJob, SHA: pin.SourceSHA}, autoDocumentationCopy{Command: "copy-archive-review", SourceJob: pin.ReviewJob, SHA: pin.ReviewSHA})
	}
	return copies, nil
}

func autoProgressBinding(a *autoRecord, j *autoJob, attempt *autoExpertRecoveryAttempt) error {
	if attempt != nil {
		j.ExpertRecoveryRoot = attempt.RootTaskID
		j.ExpertRecoveryAttempt = attempt.Number
		if j.Role == "builder" {
			attempt.TaskID = j.TaskID
		} else if j.Role == "reviewer" {
			attempt.ReviewerTaskID = j.TaskID
		}
	}
	if j.Role == "builder" && attempt == nil && a.State.Item < len(a.State.Items) {
		if prior := autoFindJob(a, a.State.Items[a.State.Item].ContinueTaskID); prior != nil {
			j.ExpertRecoveryRoot = prior.ExpertRecoveryRoot
		}
	}
	if j.Role == "reviewer" {
		for i := len(a.State.Assignments) - 1; i >= 0; i-- {
			as := a.State.Assignments[i]
			if as.Role != "builder" || as.Item != a.State.Item || as.Step != a.State.Step || as.Round != a.State.Revision {
				continue
			}
			b := autoFindJob(a, as.TaskID)
			if b == nil {
				return errors.New("review builder unavailable")
			}
			j.ExpertRecoveryRoot = b.ExpertRecoveryRoot
			j.ExpertRecoveryAttempt = b.ExpertRecoveryAttempt
			if b.ExpertRecoveryAttempt > 0 {
				v, err := autoExpertAttempt(a, b)
				if err != nil {
					return err
				}
				v.ReviewerTaskID = j.TaskID
			}
			break
		}
	}
	return nil
}

func autoProgressCharge(a *autoRecord, j *autoJob, raw []byte) error {
	privateErr := autoPrivateCharge(a, j, raw)
	if j.ExpertRecoveryAttempt == 0 {
		return privateErr
	}
	var status struct {
		Elapsed *int64 `json:"elapsed_milliseconds"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Elapsed == nil {
		return errors.New("expert execution accounting unavailable")
	}
	v, e := autoExpertAttempt(a, j)
	if e != nil {
		return e
	}
	if j.Role == "reviewer" {
		e = autoChargeExpertReview(v, j.ID, *status.Elapsed)
	} else {
		e = autoChargeExpertRecovery(v, j.ID, *status.Elapsed)
	}
	if e != nil {
		return e
	}
	return privateErr
}

func autoProgressRemaining(a *autoRecord, j *autoJob) (time.Duration, error) {
	left := 30 * time.Minute
	if j.PrivateIntegrationAttempt > 0 {
		v, e := autoPrivateAttempt(a, j)
		if e != nil {
			return 0, e
		}
		left = autoPrivateRoleRemaining(v, j.Role == "reviewer")
	}
	if j.ExpertRecoveryAttempt > 0 {
		v, e := autoExpertAttempt(a, j)
		if e != nil {
			return 0, e
		}
		remaining := autoExpertRemaining(v)
		if j.Role == "reviewer" {
			remaining = autoExpertReviewRemaining(v)
		}
		if remaining < left {
			left = remaining
		}
	}
	return left, nil
}

func autoProgressPrompt(a *autoRecord, role string) string {
	out := ""
	for _, p := range a.State.Items {
		if p.ExpertRecoveryTaskID > 0 {
			pin, err := autoExpertPin(a, p)
			if err == nil {
				out += fmt.Sprintf("Expert recovery immutable source and acceptance: %s. Original and selected acceptance both remain binding. Builder has 30 minutes cumulative execution across retries; independent reviewer has a separate cumulative 30 minutes. No new decision rounds or ordinary repair allowance. The source archive remains immutable; its root autonomy-report.json is preserved under .lectern-reports/<source_job> as historical transport, and this process must write its own new root report.\n", store.J(pin))
			}
		}
	}
	return out
}

// Exhaustion preserves evidence before ending this lease. It does not invent a
// reviewer verdict, approve artifacts, or change original repair counters.
func (s *Server) endAutoProgressBudget(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.PrivateIntegrationAttempt > 0 {
		return s.endAutoPrivateBudget(ctx, a, j)
	}
	if j.ExpertRecoveryAttempt == 0 {
		return false, nil
	}
	remaining, err := autoProgressRemaining(a, j)
	if err != nil {
		return false, err
	}
	if remaining >= time.Second {
		return false, nil
	}
	if j.Status == "running" || j.Status == "starting" {
		if _, err = s.runAutoCommand(ctx, "stop", "--job", j.ID); err != nil {
			return true, err
		}
	}
	if err = s.snapshotAutoJob(ctx, j); err != nil {
		return true, err
	}
	v, err := autoExpertAttempt(a, j)
	if err != nil {
		return true, err
	}
	reason := "Expert cumulative " + j.Role + " execution budget exhausted; retained evidence, no artifact approval"
	if err = autoFinishExpertRecovery(v, "budget_exhausted", 0, reason, time.Now()); err != nil {
		return true, err
	}
	j.Status = "failed"
	j.Summary = reason
	for i := range a.State.Assignments {
		if a.State.Assignments[i].TaskID == j.TaskID {
			a.State.Assignments[i].Completed = true
		}
	}
	a.State.Phase = autonomy.Complete
	a.State.Reason = reason
	a.Reason = reason
	s.closeAutoBridge(j.ID)
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "failed"})
	return true, s.saveAuto(a)
}

func autoProgressPendingProbes(a *autoRecord) bool {
	if a.ExpertRecovery != nil {
		for _, p := range a.ExpertRecovery.Probes {
			if p != nil && !p.StopConfirmed {
				return true
			}
		}
	}
	return false
}

func autoConfirmCopyStop(j *autoJob, raw []byte) error {
	var receipt struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return errors.New("invalid evidence copy stop receipt")
	}
	if receipt.State == "stopping" {
		return errors.New("evidence copy helpers are still stopping")
	}
	if receipt.State != "stopped" {
		return errors.New("unconfirmed evidence copy stop")
	}
	j.DocumentationStopped = true
	return nil
}

// Publish the next owner before preparing/copying any bytes. The queue is the
// restart and cancellation authority; a retry polls this UUID rather than
// repeatedly allocating invisible workspaces.
func (s *Server) resumeAutoProgress(ctx context.Context, a *autoRecord, old *autoJob) error {
	if err := s.snapshotAutoJob(ctx, old); err != nil {
		return fmt.Errorf("%w: preserving expert retry: %v", errAutoArtifactPending, err)
	}
	sha, err := s.autoArchiveIdentity(ctx, old)
	if err != nil {
		return err
	}
	j, err := autoQueueProgressResume(a, old, autoUUID(), sha)
	if err != nil {
		return err
	}
	if err = s.saveAuto(a); err != nil {
		return err
	}
	return s.launchAutoJob(ctx, a, j)
}

func autoQueueProgressResume(a *autoRecord, old *autoJob, id, sha string) (*autoJob, error) {
	if !autoPlanJobID.MatchString(id) || !autoHash256(sha) {
		return nil, errors.New("invalid expert retry identity")
	}
	attempt, err := autoExpertAttempt(a, old)
	if err != nil {
		return nil, err
	}
	// A caller recovering after a failed response must not duplicate a queued owner.
	current := attempt.JobID
	if old.Role == "reviewer" {
		current = attempt.ReviewerJob
	}
	if current != "" && current != old.ID {
		for _, j := range a.Jobs {
			if j.ID == current && j.TaskID == old.TaskID {
				return j, nil
			}
		}
		return nil, errors.New("expert retry owner unavailable")
	}
	j := *old
	j.ID = id
	j.Status = "prepared"
	j.StartedAt = time.Now()
	j.ArtifactPath = filepath.Join(autoRoot, id, "work")
	j.LaunchRetryPaid = false
	j.DocumentationStopped = false
	autoPreparePythonResume(old, &j)
	autoPrepareNodeResume(old, &j)
	j.DocumentationCopies = []autoDocumentationCopy{{Command: "copy-archive-resume", SourceJob: old.ID, SHA: sha}}
	if old.ReportError != "" {
		j.ReportRepairs++
		j.ReportRetryAt = time.Time{}
	}
	if old.Admission != nil {
		receipt := *old.Admission
		receipt.JobID = id
		j.Admission = &receipt
	}
	if old.Role == "reviewer" {
		attempt.ReviewerJob = id
	} else {
		attempt.JobID = id
	}
	a.Jobs = append(a.Jobs, &j)
	return &j, nil
}

// This bounded controller-owned prompt is published only after the asynchronous
// copy verifies its archive. Persist the queue until this succeeds, so crashes
// cannot launch a copied workspace without its original assignment.
func (s *Server) autoProgressResumePrompt(j *autoJob) error {
	task, err := s.DB.Task(j.TaskID)
	if err != nil {
		return err
	}
	prompt := autoResumePrompt(task.Prompt, j.ReportError)
	if j.PrivateIntegrationAttempt > 0 && j.Role == "reviewer" && j.ReportError != "" {
		prompt = append(prompt, []byte("\nPRIVATE REVIEW CORRECTION: You may execute missing independent /integration-tests against the same sealed candidate to establish required evidence, then cite its exact receipt_sha256 in reason. Preserve the candidate, all original acceptance, prior checks and cumulative budgets. Do not change production files to make a report pass. If acceptance is not established, reject honestly.\n")...)
	}
	return os.WriteFile(filepath.Join(autoRoot, j.ID, "prompt.txt"), prompt, 0600)
}

func autoProgressCopy(copy autoDocumentationCopy) bool {
	return copy.Command == "copy-archive-work" || copy.Command == "copy-archive-resume"
}
func (s *Server) stopAutoProgressCopies(ctx context.Context, a *autoRecord, j *autoJob) error {
	generation := 0
	for i := range j.DocumentationCopies {
		copy := &j.DocumentationCopies[i]
		if autoProgressCopy(*copy) {
			if copy.Generation == 0 {
				copy.Generation = 1
			}
			copy.CancelRequested = true
			if copy.Generation > generation {
				generation = copy.Generation
			}
		}
	}
	if err := s.saveAuto(a); err != nil {
		return err
	}
	args := []string{"completion-stop", "--job", j.ID}
	if generation > 0 {
		args = append(args, "--copy-generation", fmt.Sprint(generation))
	}
	raw, err := s.runAutoCommand(ctx, args...)
	if err != nil {
		return err
	}
	return autoConfirmCopyStop(j, raw)
}
