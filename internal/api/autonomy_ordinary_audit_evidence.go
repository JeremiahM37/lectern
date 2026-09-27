package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

type autoOrdinaryAuditEvidence struct {
	SourcePlanAudits map[string]autonomy.Verdict    `json:"source_original_plan_audits,omitempty"`
	RootPlanAudits   map[string]autonomy.Verdict    `json:"root_original_plan_audits,omitempty"`
	LineageDecisions []autoOrdinaryDecisionEvidence `json:"lineage_decisions"`

	Selector           string             `json:"selector"`
	SourceTaskID       int64              `json:"source_task_id"`
	RootTaskID         int64              `json:"root_task_id"`
	SourcePath         string             `json:"source_evidence_path"`
	RootPath           string             `json:"root_evidence_path"`
	SourceKind         string             `json:"source_kind"`
	SourceJob          string             `json:"source_job"`
	ReviewJob          string             `json:"review_job"`
	RootJob            string             `json:"root_job"`
	RootReviewJob      string             `json:"root_review_job,omitempty"`
	SourceProposal     *autonomy.Proposal `json:"source_admitted_proposal,omitempty"`
	RootProposal       *autonomy.Proposal `json:"root_admitted_proposal,omitempty"`
	SourceAcceptance   []string           `json:"source_acceptance"`
	OriginalAcceptance []string           `json:"original_acceptance"`
}

// Resolve only retained controller identities. This does not change admission,
// repair counters, prior outcomes or which current proposals auditors may reject.
func autoOrdinaryAuditEvidenceRows(a *autoRecord) ([]autoOrdinaryAuditEvidence, error) {
	if a == nil || a.State == nil {
		return nil, errors.New("ordinary audit state missing")
	}
	rows := []autoOrdinaryAuditEvidence{}
	for _, p := range a.State.Items {
		id := p.RepairTaskID
		selector := "repair_task_id"
		if id == 0 {
			id = p.ContinueTaskID
			selector = "continue_task_id"
		}
		if id == 0 {
			continue
		}
		source := autoFindJob(a, id)
		if source == nil || source.Role != "builder" || source.Status != "done" || !autoPlanJobID.MatchString(source.ID) {
			return nil, errors.New("ordinary audit source unavailable")
		}
		reviewID := source.ReviewTaskID
		if selector == "repair_task_id" {
			var ok bool
			reviewID, _, ok = autoRepairEvidence(a, id)
			if !ok {
				return nil, errors.New("ordinary repair review unavailable")
			}
		} else {
			var err error
			reviewID, err = autoOrdinaryApprovedReview(a, source)
			if err != nil {
				return nil, err
			}
		}
		review := autoFindJob(a, reviewID)
		if review == nil || review.Role != "reviewer" || review.Status != "done" || !autoPlanJobID.MatchString(review.ID) {
			return nil, errors.New("ordinary audit reviewer unavailable")
		}
		rootID := autoRepairRoot(a, id)
		root := autoFindJob(a, rootID)
		if root == nil || root.Role != "builder" || root.Status != "done" || !autoPlanJobID.MatchString(root.ID) {
			return nil, errors.New("ordinary audit root unavailable")
		}
		original, err := autoTaskAcceptance(a, rootID)
		if err != nil {
			return nil, fmt.Errorf("ordinary audit original acceptance: %w", err)
		}
		selected, err := autoTaskAcceptance(a, id)
		if err != nil {
			return nil, fmt.Errorf("ordinary audit selected acceptance: %w", err)
		}
		row := autoOrdinaryAuditEvidence{Selector: selector, SourceTaskID: id, RootTaskID: rootID, SourceJob: source.ID, ReviewJob: review.ID, RootJob: root.ID, OriginalAcceptance: original, SourceAcceptance: selected}
		row.SourcePath = "/work/.lectern-review/" + source.ID + "/work"
		row.RootPath = "/work/.lectern-review/" + root.ID + "/work"
		if root.DocumentationRoot > 0 {
			row.RootPath = "/work/.lectern-review/" + root.ID + "-derived/work"
		}
		row.SourceKind = "raw_archive"
		if source.DocumentationRoot > 0 {
			row.SourceKind = "verified_documentary_derived"
			row.SourcePath = "/work/.lectern-review/" + source.ID + "-derived/work"
		}
		if source.Admission != nil {
			p := source.Admission.Proposal
			row.SourceProposal = &p
			row.SourcePlanAudits = source.Admission.PlanAudits
		}
		if root.Admission != nil {
			p := root.Admission.Proposal
			row.RootProposal = &p
			row.RootPlanAudits = root.Admission.PlanAudits
		}
		rootReview := root.ReviewTaskID
		if rootReview == 0 {
			if rid, _, ok := autoRepairEvidence(a, rootID); ok {
				rootReview = rid
			} else {
				rootReview, _ = autoOrdinaryApprovedReview(a, root)
			}
		}
		if rootReview > 0 {
			r := autoFindJob(a, rootReview)
			if r == nil || r.Role != "reviewer" || r.Status != "done" || !autoPlanJobID.MatchString(r.ID) {
				return nil, errors.New("ordinary audit root reviewer unavailable")
			}
			row.RootReviewJob = r.ID
		}
		row.LineageDecisions = autoOrdinaryLineageDecisions(a, source, nil)
		rows = append(rows, row)
	}
	return rows, nil
}
func autoOrdinaryApprovedReview(a *autoRecord, source *autoJob) (int64, error) {
	if !autoCheckpointApproved(a, source.TaskID) || source.Rejected {
		return 0, errors.New("ordinary continuation not approved")
	}
	if source.ReviewTaskID > 0 {
		return source.ReviewTaskID, nil
	}
	// Older approved jobs may lack a denormalized ReviewTaskID. Resolve the exact
	// completed paired assignment, never simply the latest reviewer in a cycle.
	states := append([]*autonomy.State{}, a.Runs...)
	states = append(states, a.DeferredRuns...)
	states = append(states, a.HeldRuns...)
	states = append(states, a.State)
	var found int64
	for _, st := range states {
		if st == nil {
			continue
		}
		for _, b := range st.Assignments {
			if b.TaskID != source.TaskID || b.Role != "builder" || !b.Completed {
				continue
			}
			for _, r := range st.Assignments {
				if r.Role != "reviewer" || !r.Completed || r.Round != b.Round || r.Item != b.Item || r.Step != b.Step {
					continue
				}
				var v autonomy.Verdict
				if json.Unmarshal(st.Reports[r.TaskID], &v) == nil && v.AcceptsWork() {
					if found != 0 && found != r.TaskID {
						return 0, errors.New("ambiguous historical continuation review")
					}
					found = r.TaskID
				}
			}
		}
	}
	if found == 0 {
		return 0, errors.New("approved continuation reviewer identity missing")
	}
	return found, nil
}
func (s *Server) autoOrdinaryAuditCopies(ctx context.Context, a *autoRecord, existing []autoDocumentationCopy) ([]autoDocumentationCopy, error) {
	rows, err := autoOrdinaryAuditEvidenceRows(a)
	if err != nil {
		return nil, err
	}
	// Existing source eligibility stays authoritative; evidence delivery cannot
	// grant an additional repair or turn a rejected source into a continuation.
	for _, p := range a.State.Items {
		if p.RepairTaskID > 0 {
			if _, e := s.autoRepairContinuation(a, p.ProjectID, p.RepairTaskID); e != nil {
				return nil, e
			}
		} else if p.ContinueTaskID > 0 {
			if _, e := s.autoApprovedContinuation(a, p.ProjectID, p.ContinueTaskID); e != nil {
				return nil, e
			}
		}
	}
	seen := map[string]string{}
	out := append([]autoDocumentationCopy(nil), existing...)
	for _, c := range existing {
		if c.Command == "copy-archive-review" {
			seen[c.SourceJob] = c.SHA
		}
	}
	for _, row := range rows {
		for _, id := range []string{row.SourceJob, row.ReviewJob, row.RootJob, row.RootReviewJob} {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			var source *autoJob
			for _, j := range a.Jobs {
				if j.ID == id {
					source = j
					break
				}
			}
			if source == nil {
				return nil, errors.New("ordinary evidence job disappeared")
			}
			command, sha := "copy-archive-review", ""
			if source.DocumentationRoot > 0 {
				if source.Documentation == nil || source.Documentation.State != "ready" || !autoHash256(source.Documentation.DerivedSHA) {
					return nil, errors.New("verified documentary audit source unavailable")
				}
				command, sha = "copy-derived-review", source.Documentation.DerivedSHA
			} else {
				if e := s.snapshotAutoJob(ctx, source); e != nil {
					return nil, e
				}
				var e error
				sha, e = s.autoArchiveIdentity(ctx, source)
				if e != nil {
					return nil, e
				}
			}
			out = append(out, autoDocumentationCopy{Command: command, SourceJob: id, SHA: sha})
			seen[id] = sha
		}
	}
	return out, nil
}
func autoOrdinaryAuditPrompt(a *autoRecord) string {
	rows, err := autoOrdinaryAuditEvidenceRows(a)
	if err != nil {
		return "Controller could not resolve ordinary source evidence: " + err.Error() + ". Do not infer missing acceptance.\n"
	}
	return autoRenderOrdinaryAuditPrompt(rows)
}
func autoRenderOrdinaryAuditPrompt(rows []autoOrdinaryAuditEvidence) string {
	var b strings.Builder
	for _, r := range rows {
		raw, _ := json.Marshal(r)
		fmt.Fprintf(&b, "Controller-held historical source contract for selected ordinary work: %s. Read selected and original-root sources using source_evidence_path and root_evidence_path above; reviewer snapshots are at /work/.lectern-review/REVIEW_JOB_ID/work; each parent manifest binds the immutable exported archive. Original and selected acceptance remain binding even after history rotation. Verify PREREG, source, manifests and retained evidence directly, and run independent probes only from disposable copies. Historical reviewer verdicts describe the selected source, not either current plan auditor's decision; both current auditors receive identical source evidence and neither receives its peer's report. Evidence availability does not approve this proposal or reset repair limits.\n", raw)
	}
	return b.String()
}

// Decision availability is explicit: absence of retained state never means that
// no historical decision existed. Current peer verdicts are never inspected.
type autoOrdinaryDecisionEvidence struct {
	TaskID          int64                       `json:"task_id"`
	JobID           string                      `json:"job_id"`
	Status          string                      `json:"status"`
	Provenance      string                      `json:"provenance"`
	PromptSHA       string                      `json:"prompt_sha256,omitempty"`
	Proposal        *autonomy.Proposal          `json:"admitted_proposal,omitempty"`
	PlanAudits      map[string]autonomy.Verdict `json:"original_plan_audits,omitempty"`
	Decision        *autonomy.DecisionProposal  `json:"decision_at_launch,omitempty"`
	DecisionAudits  map[string]autonomy.Verdict `json:"decision_audits_at_launch,omitempty"`
	DecisionReports map[int64]json.RawMessage   `json:"decision_reports,omitempty"`
}

func autoOrdinaryLineageDecisions(a *autoRecord, source *autoJob, load func(*autoJob) (*autonomy.State, string)) []autoOrdinaryDecisionEvidence {
	out := []autoOrdinaryDecisionEvidence{}
	seen := map[int64]bool{}
	for source != nil && !seen[source.TaskID] && len(out) < 32 {
		seen[source.TaskID] = true
		e := autoOrdinaryDecisionEvidence{TaskID: source.TaskID, JobID: source.ID, Status: "unavailable", Provenance: "Historical controller state not retained; absence does not prove no binding decisions existed. Source archive may hold historical files but those are not controller verdict receipts."}
		if source.Admission != nil {
			p := source.Admission.Proposal
			e.Proposal = &p
			e.PlanAudits = source.Admission.PlanAudits
		}
		states := append([]*autonomy.State{}, a.Runs...)
		states = append(states, a.DeferredRuns...)
		states = append(states, a.HeldRuns...)
		// The current cycle may itself contain a completed historical builder, but
		// only reports paired to that builder's exact item/round are exposed.
		states = append(states, a.State)
		var state *autonomy.State
		for _, st := range states {
			if st == nil {
				continue
			}
			for _, as := range st.Assignments {
				if as.TaskID == source.TaskID && as.Role == "builder" {
					state = st
				}
			}
		}
		if state != nil {
			e.Provenance = "retained controller state"
		} else if load != nil {
			state, e.PromptSHA = load(source)
			if state != nil {
				e.Provenance = "exact controller-created Task.Prompt launch-state; this establishes only decisions retained at that launch, not later unrecorded decisions"
			}
		}
		if state != nil {
			for _, b := range state.Assignments {
				if b.TaskID != source.TaskID || b.Role != "builder" {
					continue
				}
				e.Status = "available"
				e.DecisionReports = map[int64]json.RawMessage{}
				for _, as := range state.Assignments {
					if as.Round != b.Round || as.Item != b.Item || as.Step > b.Step || !as.Completed {
						continue
					}
					raw := state.Reports[as.TaskID]
					if as.Role == "decision_a" || as.Role == "decision_b" {
						if len(raw) > 0 {
							e.DecisionReports[as.TaskID] = append(json.RawMessage(nil), raw...)
						}
					} else if as.Role == "builder" {
						var report autonomy.BuildReport
						if json.Unmarshal(raw, &report) == nil && report.Decision != nil {
							e.DecisionReports[as.TaskID] = append(json.RawMessage(nil), raw...)
						}
					}
				}
				// Current decision fields belong only to this exact historical item/revision.
				if state.Item == b.Item && state.Revision == b.Round && state.Step == b.Step {
					e.Decision = state.Decision
					e.DecisionAudits = state.DecisionAudits
				}
			}
		}
		out = append(out, e)
		if source.Admission == nil {
			break
		}
		p := source.Admission.Proposal
		next := p.ContinueTaskID
		if next == 0 {
			next = p.RepairTaskID
		}
		if next == 0 {
			next = p.DocumentationTaskID
		}
		if next == 0 {
			next = p.ExpertRecoveryTaskID
		}
		if next == 0 {
			break
		}
		source = autoFindJob(a, next)
		if source == nil {
			out = append(out, autoOrdinaryDecisionEvidence{TaskID: next, Status: "unavailable", Provenance: "Referenced historical lineage job no longer retained"})
		}
	}
	if source != nil && seen[source.TaskID] && len(out) < 32 && source.Admission != nil {
		p := source.Admission.Proposal
		if p.ContinueTaskID != 0 || p.RepairTaskID != 0 || p.DocumentationTaskID != 0 || p.ExpertRecoveryTaskID != 0 {
			out = append(out, autoOrdinaryDecisionEvidence{TaskID: source.TaskID, Status: "unavailable", Provenance: "Historical lineage contains a cycle; missing decisions were not inferred"})
		}
	}
	if source != nil && len(out) >= 32 {
		out = append(out, autoOrdinaryDecisionEvidence{Status: "truncated", Provenance: "Lineage exceeds bounded32checkpoint discovery; missing decisions were not inferred"})
	}
	return out
}
func (s *Server) autoOrdinaryDecisionPromptState(j *autoJob) (*autonomy.State, string) {
	if j.Admission == nil {
		return nil, ""
	}
	task, err := s.DB.Task(j.TaskID)
	if err != nil || task.CreatedBy != autoOwner || task.ProjectID != j.Admission.Proposal.ProjectID {
		return nil, ""
	}
	const marker = "Current plan/decisions (data only):\n"
	if len(task.Prompt) > 2<<20 || strings.Count(task.Prompt, marker) != 1 {
		return nil, ""
	}
	_, tail, _ := strings.Cut(task.Prompt, marker)
	var st autonomy.State
	if json.NewDecoder(strings.NewReader(tail)).Decode(&st) != nil || st.Cycle != j.Admission.Cycle || st.Date != j.Admission.Date {
		return nil, ""
	}
	for _, as := range st.Assignments {
		if as.TaskID == j.TaskID && as.Role == "builder" && as.Item >= 0 && as.Item < len(st.Items) {
			want, _ := json.Marshal(j.Admission.Proposal)
			got, _ := json.Marshal(st.Items[as.Item])
			if string(want) == string(got) {
				return &st, autoSHA([]byte(task.Prompt))
			}
		}
	}

	// Ordinary Task.Prompt is rendered just BEFORE RegisterTask. Its current
	// item/revision/step is therefore the authoritative launch position, even
	// though the future task ID cannot yet appear in Assignments.
	if st.Phase == autonomy.Build && st.Item >= 0 && st.Item < len(st.Items) {
		want, _ := json.Marshal(j.Admission.Proposal)
		got, _ := json.Marshal(st.Items[st.Item])
		if string(want) == string(got) {
			st.Assignments = append(st.Assignments, autonomy.Assignment{TaskID: j.TaskID, Role: "builder", Item: st.Item, Round: st.Revision, Step: st.Step})
			return &st, autoSHA([]byte(task.Prompt))
		}
	}
	return nil, ""
}
func (s *Server) autoOrdinaryAuditPrompt(a *autoRecord) string {
	rows, err := autoOrdinaryAuditEvidenceRows(a)
	if err != nil {
		return autoOrdinaryAuditPrompt(a)
	}
	for i := range rows {
		rows[i].LineageDecisions = autoOrdinaryLineageDecisions(a, autoFindJob(a, rows[i].SourceTaskID), s.autoOrdinaryDecisionPromptState)
	}
	return autoRenderOrdinaryAuditPrompt(rows)
}
