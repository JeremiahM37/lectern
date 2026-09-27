package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type autoDocumentationReceipt struct {
	State          string `json:"state"`
	Policy         string `json:"policy"`
	Reason         string `json:"reason,omitempty"`
	SourceJob      string `json:"source_job"`
	ReviewJob      string `json:"review_job"`
	SourceSHA      string `json:"source_archive_sha256"`
	ReviewSHA      string `json:"review_archive_sha256"`
	BaselineSHA    string `json:"baseline_sha256"`
	DerivedSHA     string `json:"derived_archive_sha256,omitempty"`
	DerivedTreeSHA string `json:"derived_tree_sha256,omitempty"`
}
type autoDocumentationReservation struct {
	RootTaskID          int64                    `json:"root_task_id"`
	SourceTaskID        int64                    `json:"source_task_id"`
	ReviewTaskID        int64                    `json:"review_task_id"`
	ProjectID           int64                    `json:"project_id"`
	TaskID              int64                    `json:"task_id,omitempty"`
	JobID               string                   `json:"job_id"`
	Cycle               int                      `json:"cycle"`
	Revision            int                      `json:"revision"`
	Item                int                      `json:"item"`
	Acceptance          []string                 `json:"original_acceptance"`
	SourceAcceptance    []string                 `json:"source_acceptance"`
	SourceAcceptanceSHA string                   `json:"source_acceptance_sha256"`
	AcceptanceSHA       string                   `json:"acceptance_sha256"`
	Proposal            autonomy.Proposal        `json:"proposal"`
	Binding             autoDocumentationReceipt `json:"binding"`
	Outcome             string                   `json:"outcome"`
}

func autoSHA(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func autoHash256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func autoTaskAcceptance(a *autoRecord, id int64) ([]string, error) {
	if j := autoFindJob(a, id); j != nil && j.Admission != nil && len(j.Admission.Proposal.Acceptance) > 0 {
		return append([]string(nil), j.Admission.Proposal.Acceptance...), nil
	}
	states := append([]*autonomy.State(nil), a.Runs...)
	states = append(states, a.DeferredRuns...)
	states = append(states, a.HeldRuns...)
	states = append(states, a.State)
	var result []string
	for _, st := range states {
		if st == nil {
			continue
		}
		for _, as := range st.Assignments {
			if as.TaskID == id && as.Role == "builder" && as.Item >= 0 && as.Item < len(st.Items) {
				candidate := st.Items[as.Item].Acceptance
				if len(candidate) == 0 {
					continue
				}
				if result != nil && store.J(result) != store.J(candidate) {
					return nil, errors.New("ambiguous original acceptance")
				}
				result = append([]string(nil), candidate...)
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("original acceptance unavailable")
	}
	return result, nil
}
func autoRepairAttempts(a *autoRecord, root int64) int {
	seen := map[int64]bool{}
	for _, j := range a.Jobs {
		if j.RepairSourceTaskID > 0 && autoRepairRoot(a, j.TaskID) == root {
			id := j.RepairAttemptTaskID
			if id == 0 {
				id = j.TaskID
			}
			seen[id] = true
		}
	}
	return len(seen)
}
func (s *Server) autoDocumentationSource(a *autoRecord, p autonomy.Proposal) (*autoJob, *autoJob, int64, []string, error) {
	bad := func(e error) (*autoJob, *autoJob, int64, []string, error) { return nil, nil, 0, nil, e }
	j, e := s.autoContinuation(a, p.ProjectID, p.DocumentationTaskID)
	if e != nil {
		return bad(e)
	}
	if j.PrivateIntegrationAttempt > 0 {
		return bad(errors.New("private integration cannot acquire documentary allowance"))
	}
	if j.ExpertRecoveryRoot > 0 {
		return bad(errors.New("expert recovery lineage cannot acquire documentary allowance"))
	}
	if j.DocumentationRoot > 0 {
		return bad(errors.New("documentary completion cannot complete another completion"))
	}
	if j.DiagnosisRequirement != "" {
		return bad(errors.New("diagnosis cannot acquire a documentary implementation allowance"))
	}
	rid, _, ok := autoRejectedCheckpoint(a, j.TaskID)
	if !ok {
		return bad(errors.New("documentary completion requires explicit final rejection"))
	}
	root := autoRepairRoot(a, j.TaskID)
	if root == 0 || autoRepairAttempts(a, root) < a.Config.MaxRevisionRounds {
		return bad(errors.New("ordinary repair lineage is not exhausted"))
	}
	if a.DocumentationReservations[root] != nil {
		return bad(errors.New("documentary allowance already reserved or consumed"))
	}
	accept, e := autoTaskAcceptance(a, root)
	if e != nil {
		return bad(e)
	}
	reviewer := autoFindJob(a, rid)
	if reviewer == nil || !autoPlanJobID.MatchString(j.ID) || !autoPlanJobID.MatchString(reviewer.ID) {
		return bad(errors.New("documentary source identity unavailable"))
	}
	return j, reviewer, root, accept, nil
}
func (s *Server) autoArchiveIdentity(ctx context.Context, j *autoJob) (string, error) {
	raw, e := s.runAutoCommand(ctx, "archive-identity", "--job", j.ID)
	if e != nil {
		return "", e
	}
	var r struct {
		State string `json:"state"`
		SHA   string `json:"sha256"`
	}
	if json.Unmarshal(raw, &r) != nil || r.State != "ready" || !autoHash256(r.SHA) {
		return "", errors.New("source archive identity unavailable")
	}
	return r.SHA, nil
}
func (s *Server) reserveAutoDocumentation(ctx context.Context, a *autoRecord, p autonomy.Proposal, jobID string) (*autoDocumentationReservation, error) {
	if !autoRepairAudited(a) {
		return nil, errors.New("documentary completion requires both plan audits")
	}
	root := autoRepairRoot(a, p.DocumentationTaskID)
	if r := a.DocumentationReservations[root]; r != nil {
		if r.TaskID == 0 && r.Cycle == a.State.Cycle && r.Revision == a.State.Revision && r.Item == a.State.Item && store.J(r.Proposal) == store.J(p) {
			r.JobID = jobID
			return r, s.saveAuto(a)
		}
		return nil, errors.New("documentary allowance already consumed")
	}
	source, reviewer, root, accept, e := s.autoDocumentationSource(a, p)
	if e != nil {
		return nil, e
	}
	pin := a.DocumentationPins[p.DocumentationTaskID]
	if pin == nil || pin.Binding.SourceJob != source.ID || pin.Binding.ReviewJob != reviewer.ID || pin.AcceptanceSHA != autoSHA([]byte(store.J(accept))) {
		return nil, errors.New("audited documentary source pin unavailable")
	}
	sourceAccept, e := autoTaskAcceptance(a, source.TaskID)
	if e != nil {
		return nil, e
	}
	if pin.SourceAcceptanceSHA != autoSHA([]byte(store.J(sourceAccept))) {
		return nil, errors.New("audited source acceptance changed")
	}
	sourceSHA, e := s.autoArchiveIdentity(ctx, source)
	if e != nil {
		return nil, e
	}
	reviewSHA, e := s.autoArchiveIdentity(ctx, reviewer)
	if e != nil {
		return nil, e
	}
	if sourceSHA != pin.Binding.SourceSHA || reviewSHA != pin.Binding.ReviewSHA {
		return nil, errors.New("audited documentary artifacts changed")
	}
	r := &autoDocumentationReservation{RootTaskID: root, SourceTaskID: source.TaskID, ReviewTaskID: reviewer.TaskID, ProjectID: p.ProjectID, JobID: jobID, Cycle: a.State.Cycle, Revision: a.State.Revision, Item: a.State.Item, Acceptance: accept, SourceAcceptance: sourceAccept, SourceAcceptanceSHA: pin.SourceAcceptanceSHA, AcceptanceSHA: autoSHA([]byte(store.J(accept))), Proposal: p, Outcome: "reserved", Binding: autoDocumentationReceipt{Policy: autonomy.CompletionOverlayPolicy, SourceJob: source.ID, ReviewJob: reviewer.ID, SourceSHA: sourceSHA, ReviewSHA: reviewSHA}}
	if a.DocumentationReservations == nil {
		a.DocumentationReservations = map[int64]*autoDocumentationReservation{}
	}
	a.DocumentationReservations[root] = r
	if e = s.saveAuto(a); e != nil {
		return nil, e
	}
	return r, nil
}
func autoDocumentationBound(r *autoDocumentationReservation, got autoDocumentationReceipt) bool {
	if r == nil {
		return false
	}
	b := r.Binding
	return got.Policy == autonomy.CompletionOverlayPolicy && got.SourceJob == b.SourceJob && got.ReviewJob == b.ReviewJob && got.SourceSHA == b.SourceSHA && got.ReviewSHA == b.ReviewSHA && autoHash256(got.BaselineSHA) && (b.BaselineSHA == "" || b.BaselineSHA == got.BaselineSHA)
}
func (s *Server) prepareAutoDocumentation(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	j.DocumentationStopped = false
	if err := s.saveAuto(a); err != nil {
		return false, err
	}
	r := a.DocumentationReservations[j.DocumentationRoot]
	if r == nil || r.TaskID != j.TaskID {
		return false, errors.New("documentary reservation missing")
	}
	if j.Documentation != nil && j.Documentation.BaselineSHA != "" {
		return true, nil
	}
	raw, e := s.runAutoCommand(ctx, "completion-prepare", "--job", j.ID, "--from-job", r.Binding.SourceJob, "--review-job", r.Binding.ReviewJob)
	if e != nil {
		return false, e
	}
	var got autoDocumentationReceipt
	if json.Unmarshal(raw, &got) != nil {
		return false, errors.New("invalid documentary preparation receipt")
	}
	if got.State == "preparing" || got.State == "running" || got.State == "waiting" {
		a.Status = "preparing_documentation"
		a.Reason = got.Reason
		return false, nil
	}
	if got.State != "ready" || !autoDocumentationBound(r, got) {
		return false, errors.New("documentary preparation binding mismatch")
	}
	r.Binding = got
	j.Documentation = &got
	j.Admission = autoNewAdmission(a, j)
	return true, s.saveAuto(a)
}
func (s *Server) finishAutoDocumentation(ctx context.Context, a *autoRecord, j *autoJob) error {
	j.DocumentationStopped = false
	if err := s.saveAuto(a); err != nil {
		return err
	}
	r := a.DocumentationReservations[j.DocumentationRoot]
	if r == nil || r.TaskID != j.TaskID {
		return errors.New("documentary reservation unavailable")
	}
	raw, e := s.runAutoCommand(ctx, "completion-reconstruct", "--job", j.ID)
	if e != nil {
		return fmt.Errorf("%w: reconstruction unavailable", errAutoArtifactPending)
	}
	var got autoDocumentationReceipt
	if json.Unmarshal(raw, &got) != nil {
		return fmt.Errorf("%w: reconstruction receipt unavailable", errAutoArtifactPending)
	}
	if got.State == "running" || got.State == "waiting" {
		return fmt.Errorf("%w: %s", errAutoArtifactPending, got.Reason)
	}
	if got.State == "rejected" {
		s.rejectAutoDocumentation(ctx, a, j, "Controller documentary validation: "+got.Reason)
		return nil
	}

	if got.State != "ready" || !autoDocumentationBound(r, got) || !autoHash256(got.DerivedSHA) || !autoHash256(got.DerivedTreeSHA) {
		return fmt.Errorf("%w: derived artifact binding mismatch", errAutoArtifactPending)
	}
	if j.Documentation != nil && j.Documentation.DerivedSHA != "" && j.Documentation.DerivedSHA != got.DerivedSHA {
		return errors.New("derived artifact identity changed")
	}
	j.Documentation = &got
	j.ArtifactPath = filepath.Join(autoRoot, j.ID, "completion", "derived", "work")
	r.Outcome = "awaiting_review"
	return s.saveAuto(a)
}

type autoDocumentationCopy struct {
	Generation      int    `json:"copy_generation,omitempty"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
	Command         string `json:"command"`
	SourceJob       string `json:"source_job"`
	SHA             string `json:"sha256"`
}

func (s *Server) copyAutoPromoted(ctx context.Context, dest string, j *autoJob) (*autoDocumentationCopy, error) {
	if j.ExpertRecoveryRoot > 0 {
		sha, err := s.autoArchiveIdentity(ctx, j)
		if err != nil {
			return nil, err
		}
		return &autoDocumentationCopy{Command: "copy-archive-work", SourceJob: j.ID, SHA: sha}, nil
	}
	if j.DocumentationRoot > 0 {
		if j.Documentation == nil || j.Documentation.State != "ready" || !autoHash256(j.Documentation.DerivedSHA) {
			return nil, errors.New("verified documentary artifact unavailable")
		}
		return &autoDocumentationCopy{Command: "copy-derived", SourceJob: j.ID, SHA: j.Documentation.DerivedSHA}, nil
	}
	_, e := s.runAutoCommand(ctx, "copy", "--job", dest, "--from-job", j.ID)
	return nil, e
}
func (s *Server) pollAutoDocumentationCopies(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	for i := range j.DocumentationCopies {
		copy := &j.DocumentationCopies[i]
		if !autoProgressCopy(*copy) {
			continue
		}
		if copy.Generation == 0 {
			copy.Generation = 1
		}
		if copy.CancelRequested && !j.DocumentationStopped {
			if err := s.stopAutoProgressCopies(ctx, a, j); err != nil {
				a.Reason = err.Error()
				return false, nil
			}
		}
		if j.DocumentationStopped {
			copy.Generation++
			copy.CancelRequested = false
		}
	}
	j.DocumentationStopped = false
	if err := s.saveAuto(a); err != nil {
		return false, err
	}
	for len(j.DocumentationCopies) > 0 {
		copy := j.DocumentationCopies[0]
		if copy.Command != "copy-archive-resume" && copy.Command != "copy-archive-work" && copy.Command != "copy-derived" && copy.Command != "copy-archive-review" && copy.Command != "completion-resume" {
			return false, errors.New("invalid documentary copy operation")
		}
		args := []string{copy.Command, "--job", j.ID, "--from-job", copy.SourceJob}
		if autoProgressCopy(copy) {
			args = append(args, "--copy-generation", fmt.Sprint(copy.Generation))
		}
		raw, e := s.runAutoCommand(ctx, args...)
		if e != nil {
			return false, e
		}
		var receipt struct {
			Generation int    `json:"copy_generation"`
			State      string `json:"state"`
			Reason     string `json:"reason"`
			SHA        string `json:"source_archive_sha256"`
			DerivedSHA string `json:"derived_archive_sha256"`
		}
		if json.Unmarshal(raw, &receipt) != nil {
			return false, errors.New("invalid documentary copy receipt")
		}
		if autoProgressCopy(copy) && receipt.Generation != copy.Generation {
			return false, errors.New("expert copy generation mismatch")
		}
		if receipt.State == "copying" || receipt.State == "waiting" || receipt.State == "preparing" {
			a.Status = "copying_documentation_evidence"
			a.Reason = receipt.Reason
			return false, nil
		}
		actual := receipt.SHA
		if copy.Command == "copy-derived" {
			actual = receipt.DerivedSHA
		}
		if copy.Command == "completion-resume" {
			var binding autoDocumentationReceipt
			var identity struct {
				Source string `json:"copy_source_job"`
			}
			if json.Unmarshal(raw, &binding) != nil || json.Unmarshal(raw, &identity) != nil || identity.Source != copy.SourceJob || !autoDocumentationBound(a.DocumentationReservations[j.DocumentationRoot], binding) {
				return false, errors.New("documentary resume identity mismatch")
			}
			actual = copy.SHA
			j.Documentation = &binding
		}
		if receipt.State != "copied" || actual != copy.SHA {
			return false, errors.New("documentary copy identity mismatch")
		}
		if copy.Command == "copy-archive-resume" {
			if err := s.autoProgressResumePrompt(j); err != nil {
				return false, err
			}
		}
		if copy.Command == "copy-archive-work" && j.ExpertRecoveryAttempt > 0 {
			task, err := s.DB.Task(j.TaskID)
			if err != nil {
				return false, err
			}
			if err = os.WriteFile(filepath.Join(autoRoot, j.ID, "prompt.txt"), []byte(task.Prompt), 0600); err != nil {
				return false, err
			}
		}
		j.DocumentationCopies = j.DocumentationCopies[1:]
		if e = s.saveAuto(a); e != nil {
			return false, e
		}
	}
	return true, nil
}

// Pins are captured before plan audits, and shown identically to both auditors.
func (s *Server) pinAutoDocumentation(ctx context.Context, a *autoRecord, items []autonomy.Proposal) error {
	for _, p := range items {
		if p.DocumentationTaskID == 0 {
			continue
		}
		source, reviewer, root, accept, e := s.autoDocumentationSource(a, p)
		if e != nil {
			return e
		}
		selected, e := autoTaskAcceptance(a, source.TaskID)
		if e != nil {
			return e
		}
		sh, e := s.autoArchiveIdentity(ctx, source)
		if e != nil {
			return e
		}
		rh, e := s.autoArchiveIdentity(ctx, reviewer)
		if e != nil {
			return e
		}
		if a.DocumentationPins == nil {
			a.DocumentationPins = map[int64]*autoDocumentationReservation{}
		}
		a.DocumentationPins[p.DocumentationTaskID] = &autoDocumentationReservation{RootTaskID: root, SourceTaskID: source.TaskID, ReviewTaskID: reviewer.TaskID, ProjectID: p.ProjectID, Acceptance: accept, AcceptanceSHA: autoSHA([]byte(store.J(accept))), SourceAcceptance: selected, SourceAcceptanceSHA: autoSHA([]byte(store.J(selected))), Binding: autoDocumentationReceipt{Policy: autonomy.CompletionOverlayPolicy, SourceJob: source.ID, ReviewJob: reviewer.ID, SourceSHA: sh, ReviewSHA: rh}}
	}
	return nil
}
func (s *Server) autoDocumentationCatalog(a *autoRecord) []map[string]any {
	rows := []map[string]any{}
	seen := map[int64]bool{}
	for i := len(a.Jobs) - 1; i >= 0 && len(rows) < 30; i-- {
		j := a.Jobs[i]
		if seen[j.TaskID] {
			continue
		}
		seen[j.TaskID] = true
		task, e := s.DB.Task(j.TaskID)
		if e != nil {
			continue
		}
		p := autonomy.Proposal{ProjectID: task.ProjectID, DocumentationTaskID: j.TaskID}
		_, reviewer, root, accept, e := s.autoDocumentationSource(a, p)
		if e != nil {
			continue
		}
		rows = append(rows, map[string]any{"task_id": j.TaskID, "root_task_id": root, "project_id": task.ProjectID, "review_task_id": reviewer.TaskID, "rejection": j.ReviewReason, "original_acceptance": accept, "policy": autonomy.CompletionOverlayPolicy, "scope": "Eligibility for consideration only. Both audits must establish that only documentary gaps remain. No executable coverage, production or dependency changes; one allowance per exhausted root."})
	}
	return rows
}

func (s *Server) rejectAutoDocumentation(ctx context.Context, a *autoRecord, j *autoJob, reason string) {
	if r := a.DocumentationReservations[j.DocumentationRoot]; r != nil {
		r.Outcome = "rejected"
	}
	if raw, err := s.runAutoCommand(ctx, "report", "--job", j.ID); err == nil && json.Valid(raw) {
		a.State.Reports[j.TaskID] = append(json.RawMessage(nil), raw...)
	}
	j.Status = "done"
	j.Rejected = true
	j.Approved = false
	j.ReviewOutcome = "incomplete"
	j.ReviewReason = reason
	for i := range a.State.Assignments {
		if a.State.Assignments[i].TaskID == j.TaskID {
			a.State.Assignments[i].Completed = true
		}
	}
	a.State.Phase = autonomy.Complete
	a.State.Reason = reason
	a.Status = "complete"
	a.Reason = reason
	s.closeAutoBridge(j.ID)
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "failed"})
}

func autoDocumentationStopPending(j *autoJob) bool {
	return !j.DocumentationStopped && (j.DocumentationRoot > 0 || len(j.DocumentationCopies) > 0) && (j.Status == "prepared" || j.Status == "exporting")
}
