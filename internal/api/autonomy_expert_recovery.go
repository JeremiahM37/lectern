package api

// Progress-gated recovery policy. This file does not execute probes or launch
// workers. The runner adapter must authenticate receipts before recording them;
// semantic causal relevance remains the responsibility of both plan auditors.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const autoExpertBuildBudget = 30 * time.Minute
const autoExpertReviewBudget = 30 * time.Minute
const autoExpertProbeBudget = 180 * time.Second
const autoExpertCooldown = time.Hour
const autoExpertWindow = 24 * time.Hour
const autoExpertWindowAttempts = 2
const autoExpertWindowProbes = 12

type autoExpertRecoveryLedger struct {
	Pins     map[string]*autoExpertRecoveryPin      `json:"pins"`
	Attempts map[int64][]*autoExpertRecoveryAttempt `json:"attempts"`
	Probes   map[string]*autoExpertProbeLease       `json:"probes"`
}
type autoExpertRecoveryPin struct {
	Key                 string   `json:"key"`
	RootTaskID          int64    `json:"root_task_id"`
	ProjectID           int64    `json:"project_id"`
	SourceTaskID        int64    `json:"source_task_id"`
	ReviewTaskID        int64    `json:"review_task_id"`
	SourceJob           string   `json:"source_job"`
	ReviewJob           string   `json:"review_job"`
	SourceSHA           string   `json:"source_archive_sha256"`
	ReviewSHA           string   `json:"review_archive_sha256"`
	Acceptance          []string `json:"original_acceptance"`
	SourceAcceptance    []string `json:"source_acceptance"`
	AcceptanceSHA       string   `json:"acceptance_sha256"`
	SourceAcceptanceSHA string   `json:"source_acceptance_sha256"`
}

// This is a controller-normalized projection of the root-owned runner receipt,
// not an additional worker report format. ReceiptSHA authenticates the full
// runner envelope (including its mount policy and resource limits).
type autoExpertProbeReceipt struct {
	Reason              string    `json:"reason,omitempty"`
	Executed            *bool     `json:"executed,omitempty"`
	RequestKey          string    `json:"request_key"`
	Profile             string    `json:"profile"`
	ReceiptSHA          string    `json:"receipt_sha256"`
	ProbeID             string    `json:"probe_id"`
	ProgressKey         string    `json:"progress_key"`
	SourceSHA           string    `json:"source_archive_sha256"`
	RootAcceptanceSHA   string    `json:"root_acceptance_sha256"`
	SourceAcceptanceSHA string    `json:"source_acceptance_sha256"`
	SourceTreeSHA       string    `json:"source_tree_sha256"`
	ScriptSHA           string    `json:"script_sha256"`
	FixturesSHA         string    `json:"fixtures_sha256"`
	ArgvSHA             string    `json:"argv_sha256"`
	RuntimeSHA          string    `json:"runtime_digest"`
	PolicySHA           string    `json:"launch_policy_sha256"`
	OutputSHA           string    `json:"output_manifest_sha256"`
	OwnerJob            string    `json:"owner_job"`
	OwnerTask           int64     `json:"owner_task"`
	Role                string    `json:"role"`
	StartedAt           time.Time `json:"started_at"`
	EndedAt             time.Time `json:"ended_at"`
	ExitCode            *int      `json:"exit_code"`
	State               string    `json:"state"`
	Truncated           bool      `json:"truncated"`
}
type autoExpertProbeLease struct {
	StopAttempts        int                     `json:"stop_attempts,omitempty"`
	StopRequestedAt     time.Time               `json:"stop_requested_at,omitempty"`
	StopConfirmed       bool                    `json:"stop_confirmed,omitempty"`
	StopError           string                  `json:"stop_error,omitempty"`
	Diagnostic          *autoExpertProbeReceipt `json:"diagnostic,omitempty"`
	ChargedMilliseconds int64                   `json:"charged_milliseconds"`
	ID                  string                  `json:"id"`
	ProgressKey         string                  `json:"progress_key"`
	RootTaskID          int64                   `json:"root_task_id"`
	OwnerJob            string                  `json:"owner_job"`
	OwnerTask           int64                   `json:"owner_task"`
	Role                string                  `json:"role"`
	Cycle               int                     `json:"cycle"`
	Revision            int                     `json:"revision"`
	RequestKey          string                  `json:"request_key"`
	CreatedAt           time.Time               `json:"created_at"`
	Receipt             *autoExpertProbeReceipt `json:"receipt,omitempty"`
}
type autoExpertRecoveryAttempt struct {
	ReviewerJob               string            `json:"reviewer_job,omitempty"`
	ReviewerTaskID            int64             `json:"reviewer_task_id,omitempty"`
	ReviewChargedMilliseconds int64             `json:"review_charged_milliseconds"`
	ReviewUsage               map[string]int64  `json:"review_usage,omitempty"`
	RootTaskID                int64             `json:"root_task_id"`
	Number                    int               `json:"number"`
	LeaseKey                  string            `json:"lease_key"`
	ProgressKey               string            `json:"progress_key"`
	Proposal                  autonomy.Proposal `json:"proposal"`
	Cycle                     int               `json:"cycle"`
	Revision                  int               `json:"revision"`
	Item                      int               `json:"item"`
	TaskID                    int64             `json:"task_id"`
	JobID                     string            `json:"job_id"`
	Status                    string            `json:"status"`
	CreatedAt                 time.Time         `json:"created_at"`
	FinishedAt                time.Time         `json:"finished_at,omitempty"`
	ChargedMilliseconds       int64             `json:"charged_milliseconds"`
	// Usage is cumulative per operational UUID; replayed observations are no-ops.
	Usage         map[string]int64                        `json:"usage"`
	FailureFamily string                                  `json:"failure_family"`
	EvidenceKeys  []string                                `json:"evidence_keys"`
	Audits        map[string]autonomy.ExpertRecoveryAudit `json:"audits"`
	ProbeReceipts []autoExpertProbeReceipt                `json:"probe_receipts"`
	ReviewTaskID  int64                                   `json:"review_task_id,omitempty"`
	ReviewReason  string                                  `json:"review_reason,omitempty"`
}

func autoExpertLedger(a *autoRecord) *autoExpertRecoveryLedger {
	if a.ExpertRecovery == nil {
		a.ExpertRecovery = &autoExpertRecoveryLedger{}
	}
	l := a.ExpertRecovery
	if l.Pins == nil {
		l.Pins = map[string]*autoExpertRecoveryPin{}
	}
	if l.Attempts == nil {
		l.Attempts = map[int64][]*autoExpertRecoveryAttempt{}
	}
	if l.Probes == nil {
		l.Probes = map[string]*autoExpertProbeLease{}
	}
	return l
}
func autoExpertRoot(a *autoRecord, id int64) int64 {
	j := autoFindJob(a, id)
	if j == nil {
		return 0
	}
	if j.ExpertRecoveryRoot > 0 {
		return j.ExpertRecoveryRoot
	}
	return autoRepairRoot(a, id)
}
func autoExpertTerminal(status string) bool {
	return status == "approved" || status == "rejected" || status == "budget_exhausted" || status == "unavailable"
}
func autoValidateExpertProposal(p autonomy.Proposal) error {
	if p.ExpertRecoveryTaskID == 0 {
		if p.ExpertProgressKey != "" {
			return errors.New("expert progress requires source task")
		}
		return nil
	}
	if p.ExpertRecoveryTaskID < 0 || !autoHash256(p.ExpertProgressKey) || p.ProjectID <= 0 || p.SourceRevision != "" || p.ContinueTaskID != 0 || p.RepairTaskID != 0 || p.DocumentationTaskID != 0 || p.DiagnoseTaskID != 0 || p.DiagnoseRequirement != "" {
		return errors.New("expert recovery requires an exclusive retained source and trusted progress key")
	}
	return nil
}
func autoExpertPinKey(p *autoExpertRecoveryPin) string {
	v := *p
	v.Key = ""
	return autoSHA([]byte(store.J(v)))
}

// Pinning is read-only evidence admission, available before audits/probes. The
// caller persists the returned ledger before exposing its key in the catalog.
func (s *Server) pinAutoExpertRecovery(ctx context.Context, a *autoRecord, project, source int64) (*autoExpertRecoveryPin, error) {
	j, e := s.autoContinuation(a, project, source)
	if e != nil {
		return nil, e
	}
	if j.DocumentationRoot > 0 || j.DiagnosisRequirement != "" {
		return nil, errors.New("documentary/diagnosis checkpoints cannot create expert implementation lineage")
	}
	root := autoExpertRoot(a, source)
	if root == 0 || autoRepairAttempts(a, root) < a.Config.MaxRevisionRounds {
		return nil, errors.New("ordinary repair lineage is not exhausted")
	}
	rid, _, ok := autoRejectedCheckpoint(a, source)
	if !ok {
		return nil, errors.New("expert recovery requires independent final rejection")
	}
	reviewer := autoFindJob(a, rid)
	if reviewer == nil {
		return nil, errors.New("source reviewer unavailable")
	}
	original, e := autoTaskAcceptance(a, root)
	if e != nil {
		return nil, e
	}
	selected, e := autoTaskAcceptance(a, source)
	if e != nil {
		return nil, e
	}
	sourceSHA, e := s.autoArchiveIdentity(ctx, j)
	if e != nil {
		return nil, e
	}
	reviewSHA, e := s.autoArchiveIdentity(ctx, reviewer)
	if e != nil {
		return nil, e
	}
	p := &autoExpertRecoveryPin{RootTaskID: root, ProjectID: project, SourceTaskID: source, ReviewTaskID: rid, SourceJob: j.ID, ReviewJob: reviewer.ID, SourceSHA: sourceSHA, ReviewSHA: reviewSHA, Acceptance: original, SourceAcceptance: selected, AcceptanceSHA: autoSHA([]byte(store.J(original))), SourceAcceptanceSHA: autoSHA([]byte(store.J(selected)))}
	p.Key = autoExpertPinKey(p)
	l := autoExpertLedger(a)
	if old := l.Pins[p.Key]; old != nil {
		return old, nil
	}
	l.Pins[p.Key] = p
	return p, nil
}
func autoExpertPin(a *autoRecord, p autonomy.Proposal) (*autoExpertRecoveryPin, error) {
	if e := autoValidateExpertProposal(p); e != nil {
		return nil, e
	}
	if a.ExpertRecovery == nil {
		return nil, errors.New("expert evidence not pinned")
	}
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	if pin == nil || pin.Key != autoExpertPinKey(pin) || pin.ProjectID != p.ProjectID || pin.SourceTaskID != p.ExpertRecoveryTaskID || autoExpertRoot(a, pin.SourceTaskID) != pin.RootTaskID {
		return nil, errors.New("expert source/progress identity mismatch")
	}
	original, e := autoTaskAcceptance(a, pin.RootTaskID)
	if e != nil {
		return nil, e
	}
	selected, e := autoTaskAcceptance(a, pin.SourceTaskID)
	if e != nil {
		return nil, e
	}
	if autoSHA([]byte(store.J(original))) != pin.AcceptanceSHA || autoSHA([]byte(store.J(selected))) != pin.SourceAcceptanceSHA {
		return nil, errors.New("expert original/source acceptance changed")
	}
	j := autoFindJob(a, pin.SourceTaskID)
	review := autoFindJob(a, pin.ReviewTaskID)
	if j == nil || review == nil || j.ID != pin.SourceJob || review.ID != pin.ReviewJob || j.Role != "builder" || j.Status != "done" || review.Role != "reviewer" || review.Status != "done" {
		return nil, errors.New("expert source owner changed")
	}
	if rid, _, ok := autoRejectedCheckpoint(a, pin.SourceTaskID); !ok || rid != pin.ReviewTaskID {
		return nil, errors.New("expert source no longer has the pinned independent rejection")
	}
	return pin, nil
}
func autoExpertAuditOwner(a *autoRecord, role string) (*autoJob, error) {
	if a.State == nil || (role != "auditor_a" && role != "auditor_b") {
		return nil, errors.New("independent audit owner required")
	}
	var result *autoJob
	for _, as := range a.State.Assignments {
		if as.Role == role && as.Round == a.State.Revision {
			j := autoFindJob(a, as.TaskID)
			if j == nil || j.Role != role {
				return nil, errors.New("audit job missing")
			}
			if result != nil {
				return nil, errors.New("ambiguous audit owner")
			}
			result = j
		}
	}
	if result == nil {
		return nil, errors.New("audit assignment missing")
	}
	return result, nil
}

// Reserve investigation separately from implementation so awaiting new
// evidence never requires a build allowance just to run its causal experiment.
func autoReserveExpertProbe(a *autoRecord, p autonomy.Proposal, role, requestKey string, required []string, now time.Time) (*autoExpertProbeLease, error) {
	if e := autonomy.QuotaGate(a.Config, a.Quota.Providers, required, now); e != nil {
		return nil, e
	}
	pin, e := autoExpertPin(a, p)
	if e != nil {
		return nil, e
	}
	owner, e := autoExpertAuditOwner(a, role)
	if e != nil {
		return nil, e
	}
	if a.State.Phase != autonomy.Audit || owner.Status != "running" || !autoHash256(requestKey) {
		return nil, errors.New("probe requires running admitted auditor and immutable request digest")
	}
	l := autoExpertLedger(a)
	id := autoSHA([]byte(fmt.Sprintf("%s:%d:%s", pin.Key, owner.TaskID, requestKey)))
	if existing := l.Probes[id]; existing != nil {
		if !existing.StopRequestedAt.IsZero() {
			return nil, errors.New("probe cancellation intent is durable; same execution cannot relaunch")
		}
		return existing, nil
	}
	recent, perOwner := 0, 0
	for _, v := range l.Probes {
		if v.RootTaskID == pin.RootTaskID && v.CreatedAt.After(now.Add(-autoExpertWindow)) {
			recent++
		}
		if v.ProgressKey == pin.Key && v.OwnerTask == owner.TaskID {
			perOwner++
		}
	}
	if recent >= autoExpertWindowProbes || perOwner >= 3 {
		return nil, errors.New("bounded root investigation budget exhausted; choose other work until probe window opens")
	}
	lease := &autoExpertProbeLease{ChargedMilliseconds: autoExpertProbeBudget.Milliseconds(), ID: id, ProgressKey: pin.Key, RootTaskID: pin.RootTaskID, OwnerJob: owner.ID, OwnerTask: owner.TaskID, Role: role, Cycle: a.State.Cycle, Revision: a.State.Revision, RequestKey: requestKey, CreatedAt: now}
	l.Probes[id] = lease
	return lease, nil
}

// Called only by the authenticated root-runner adapter, never by a worker JSON
// report. Structural checks are additional defenses, not receipt authentication.
func autoRecordExpertProbe(a *autoRecord, r autoExpertProbeReceipt) error {
	if a.ExpertRecovery == nil {
		return errors.New("probe was not reserved")
	}
	lease := a.ExpertRecovery.Probes[r.ProbeID]
	if lease == nil {
		return errors.New("probe was not reserved")
	}
	pin := a.ExpertRecovery.Pins[lease.ProgressKey]
	if pin == nil || r.ProgressKey != pin.Key || r.SourceSHA != pin.SourceSHA || r.RootAcceptanceSHA != pin.AcceptanceSHA || r.SourceAcceptanceSHA != pin.SourceAcceptanceSHA || r.OwnerJob != lease.OwnerJob || r.OwnerTask != lease.OwnerTask || r.Role != lease.Role || r.RequestKey != lease.RequestKey || r.Profile != "ordinary180" {
		return errors.New("probe receipt owner/source binding mismatch")
	}
	if r.State != "exited" {
		allowed := map[string]bool{"failed": true, "timeout": true, "output_limit": true, "cancelled": true, "interrupted": true, "unavailable": true}
		if !allowed[r.State] || !autoHash256(r.ReceiptSHA) || strings.TrimSpace(r.Reason) == "" {
			return errors.New("invalid terminal probe diagnostic")
		}
		// A missing execution inventory is honest only when the trusted runner
		// explicitly attests it did not start execution. Unknown interrupted work
		// retains the full reserved budget rather than inventing zero cost.
		complete := true
		for _, h := range []string{r.SourceTreeSHA, r.ScriptSHA, r.FixturesSHA, r.ArgvSHA, r.RuntimeSHA, r.PolicySHA, r.OutputSHA} {
			if !autoHash256(h) {
				complete = false
			}
		}
		if !complete && (r.Executed == nil || *r.Executed) {
			return errors.New("missing probe inventory without explicit nonexecution attestation")
		}
		if lease.Receipt != nil {
			return errors.New("successful probe evidence cannot become a diagnostic")
		}
		if lease.Diagnostic != nil {
			if store.J(lease.Diagnostic) == store.J(r) {
				return nil
			}
			return errors.New("terminal probe diagnostic is immutable")
		}
		copy := r
		lease.Diagnostic = &copy
		lease.ChargedMilliseconds = autoExpertProbeBudget.Milliseconds()
		if r.Executed != nil && !*r.Executed {
			lease.ChargedMilliseconds = 0
		} else if !r.StartedAt.IsZero() && !r.EndedAt.Before(r.StartedAt) && r.EndedAt.Sub(r.StartedAt) <= autoExpertProbeBudget+10*time.Second {
			lease.ChargedMilliseconds = r.EndedAt.Sub(r.StartedAt).Milliseconds()
		}
		return nil
	}
	if lease.Diagnostic != nil {
		return errors.New("terminal probe diagnostic cannot be retried as the same execution")
	}
	for _, h := range []string{r.ReceiptSHA, r.SourceTreeSHA, r.ScriptSHA, r.FixturesSHA, r.ArgvSHA, r.RuntimeSHA, r.PolicySHA, r.OutputSHA} {
		if !autoHash256(h) {
			return errors.New("probe requires immutable source/input/runtime/output identities")
		}
	}
	if r.StartedAt.Before(lease.CreatedAt) || r.EndedAt.Before(r.StartedAt) || r.EndedAt.Sub(r.StartedAt) > autoExpertProbeBudget+10*time.Second || r.ExitCode == nil || r.State != "exited" || r.Executed == nil || !*r.Executed || r.Truncated {
		return errors.New("probe did not finish within its evidence bounds")
	}
	if lease.Receipt != nil {
		if store.J(lease.Receipt) == store.J(r) {
			return nil
		}
		return errors.New("immutable probe receipt changed")
	}
	copy := r
	lease.Receipt = &copy
	lease.ChargedMilliseconds = r.EndedAt.Sub(r.StartedAt).Milliseconds()
	return nil
}
func autoExpertEvidenceKey(r autoExpertProbeReceipt) string {
	// Deliberately excludes source archive, log bytes, timestamps and job UUIDs:
	// changing those alone cannot buy another implementation attempt. New source
	// effects require a materially new causal experiment or verified runtime.
	return autoSHA([]byte(store.J([]string{r.ScriptSHA, r.FixturesSHA, r.ArgvSHA, r.RuntimeSHA})))
}
func autoExpertRoleEvidence(a *autoRecord, p autonomy.Proposal, pin *autoExpertRecoveryPin, prior int, role string, verdict autonomy.Verdict) (autonomy.ExpertRecoveryAudit, []autoExpertProbeReceipt, error) {
	bad := func(reason string) (autonomy.ExpertRecoveryAudit, []autoExpertProbeReceipt, error) {
		return autonomy.ExpertRecoveryAudit{}, nil, errors.New(reason)
	}
	owner, e := autoExpertAuditOwner(a, role)
	if e != nil {
		return bad(e.Error())
	}
	var selected *autonomy.ExpertRecoveryAudit
	for _, v := range verdict.ExpertRecovery {
		if v.SourceTaskID == p.ExpertRecoveryTaskID && v.ProgressKey == pin.Key {
			if selected != nil {
				return bad("duplicate expert attestation")
			}
			copy := v
			selected = &copy
		}
	}
	if selected == nil || !selected.MaterialChange || selected.PriorAttempt != prior || selected.FailureFamily != pin.AcceptanceSHA || strings.TrimSpace(selected.CausalExplanation) == "" || strings.TrimSpace(selected.DifferentStrategy) == "" || strings.TrimSpace(selected.StopCriterion) == "" || len(selected.ProbeIDs) == 0 || len(selected.ProbeIDs) > 3 {
		return bad("expert audit lacks bound causal evidence, changed strategy or stop criterion")
	}
	receipts := []autoExpertProbeReceipt{}
	seen := map[string]bool{}
	for _, id := range selected.ProbeIDs {
		lease := a.ExpertRecovery.Probes[id]
		if lease == nil || lease.Receipt == nil || lease.Diagnostic != nil || lease.ProgressKey != pin.Key || lease.OwnerTask != owner.TaskID || !autoExpertProbeOwnerValid(a, lease, owner) || lease.Role != role || lease.Cycle != a.State.Cycle || lease.Revision != a.State.Revision || seen[id] {
			return bad("expert audit probe is missing, stale, shared or owned by another role")
		}
		seen[id] = true
		receipts = append(receipts, *lease.Receipt)
	}
	return *selected, receipts, nil
}

// Run before ApplyReport for a current plan auditor. This makes malformed
// progress attestations schema-repairable before the build transition, instead
// of discovering them as an operational preparation failure after both audits.
func autoValidateExpertAudit(a *autoRecord, j *autoJob, v autonomy.Verdict) error {
	if j == nil || a.State == nil || a.State.Phase != autonomy.Audit || (j.Role != "auditor_a" && j.Role != "auditor_b") {
		return nil
	}
	if v.Approve == nil || !*v.Approve {
		return nil
	}
	owner, e := autoExpertAuditOwner(a, j.Role)
	if e != nil {
		return e
	}
	if owner.ID != j.ID || owner.TaskID != j.TaskID {
		return errors.New("expert auditor is not current assignment")
	}
	expected := 0
	for _, p := range a.State.Items {
		if p.ExpertRecoveryTaskID == 0 {
			continue
		}
		expected++
		pin, e := autoExpertPin(a, p)
		if e != nil {
			return e
		}
		if _, _, e = autoExpertRoleEvidence(a, p, pin, len(a.ExpertRecovery.Attempts[pin.RootTaskID]), j.Role, v); e != nil {
			return e
		}
	}
	if len(v.ExpertRecovery) != expected {
		return errors.New("expert audit contains missing or unrequested attestations")
	}
	return nil
}
func autoExpertAuditEvidence(a *autoRecord, p autonomy.Proposal, pin *autoExpertRecoveryPin, prior int) (map[string]autonomy.ExpertRecoveryAudit, []autoExpertProbeReceipt, error) {
	if !autoRepairAudited(a) {
		return nil, nil, errors.New("expert recovery requires both independent plan approvals")
	}
	audits := map[string]autonomy.ExpertRecoveryAudit{}
	receipts := []autoExpertProbeReceipt{}
	for _, role := range []string{"auditor_a", "auditor_b"} {
		v, r, e := autoExpertRoleEvidence(a, p, pin, prior, role, a.State.Audits[role])
		if e != nil {
			return nil, nil, e
		}
		audits[role] = v
		receipts = append(receipts, r...)
	}
	return audits, receipts, nil
}
func autoExpertRecoveryEligible(a *autoRecord, p autonomy.Proposal, now time.Time) (*autoExpertRecoveryPin, error) {
	pin, e := autoExpertPin(a, p)
	if e != nil {
		return nil, e
	}
	attempts := a.ExpertRecovery.Attempts[pin.RootTaskID]
	recent := 0
	for _, v := range attempts {
		if v.Status == "approved" && !autoExpertLaterRejectedMilestone(a, pin, v) {
			return nil, errors.New("approved expert scope cannot be retried; require a later independently rejected descendant milestone with different bound acceptance")
		}
		if !autoExpertTerminal(v.Status) {
			return nil, errors.New("root has an active expert recovery owner")
		}
		if v.CreatedAt.After(now.Add(-autoExpertWindow)) {
			recent++
		}
		if v.Status != "approved" && !v.FinishedAt.IsZero() && now.Before(v.FinishedAt.Add(autoExpertCooldown)) {
			return nil, errors.New("expert failure cooldown active")
		}
	}
	if recent >= autoExpertWindowAttempts {
		return nil, errors.New("expert root attempt rate limit; new evidence alone does not bypass budget")
	}
	// A stopped operational clone can still own a retained assignment. Status
	// alone is insufficient: OFF/report repair/deferred recovery must not make
	// its lineage available for a competing build.
	states := append([]*autonomy.State{a.State}, a.DeferredRuns...)
	states = append(states, a.HeldRuns...)
	for _, st := range states {
		if st == nil {
			continue
		}
		for _, as := range st.Assignments {
			if as.Completed || (as.Role != "builder" && as.Role != "reviewer") {
				continue
			}
			root := autoExpertRoot(a, as.TaskID)
			if as.Role == "reviewer" {
				for _, b := range st.Assignments {
					if b.Role == "builder" && b.Item == as.Item && b.Round == as.Round && b.Step == as.Step {
						root = autoExpertRoot(a, b.TaskID)
					}
				}
			}
			if root == pin.RootTaskID {
				return nil, errors.New("retained assignment owns implementation lineage even while stopped")
			}
		}
	}
	// Any running/deferred owner on the root blocks a competing build. Audit
	// tasks themselves are investigations and do not own implementation lineage.
	for _, j := range a.Jobs {
		if j.Role != "builder" && j.Role != "reviewer" {
			continue
		}
		if (j.Status == "running" || j.Status == "starting" || j.Status == "prepared" || j.Status == "exporting" || j.Status == "deferred") && (autoExpertRoot(a, j.TaskID) == pin.RootTaskID || j.ExpertRecoveryRoot == pin.RootTaskID) {
			return nil, errors.New("retained lineage already has an implementation owner")
		}
	}
	return pin, nil
}

// Caller MUST persist mutation before task creation or any runner side effect.
func autoReserveExpertRecovery(a *autoRecord, p autonomy.Proposal, jobID string, required []string, now time.Time) (*autoExpertRecoveryAttempt, error) {
	if e := autonomy.QuotaGate(a.Config, a.Quota.Providers, required, now); e != nil {
		return nil, e
	}
	pin, e := autoExpertPin(a, p)
	if e != nil {
		return nil, e
	}
	if a.State == nil || a.State.Phase != autonomy.Build || a.State.Item < 0 || a.State.Item >= len(a.State.Items) || store.J(a.State.Items[a.State.Item]) != store.J(p) || !autoPlanJobID.MatchString(jobID) {
		return nil, errors.New("expert reservation requires exact current build assignment")
	}
	attempts := a.ExpertRecovery.Attempts[pin.RootTaskID]
	for _, old := range attempts {
		if old.Cycle == a.State.Cycle && old.Revision == a.State.Revision && old.Item == a.State.Item && store.J(old.Proposal) == store.J(p) && !autoExpertTerminal(old.Status) {
			if old.JobID != jobID {
				return nil, errors.New("expert reservation already owns another UUID; resume same lease")
			}
			return old, nil
		}
	}
	if _, e = autoExpertRecoveryEligible(a, p, now); e != nil {
		return nil, e
	}
	audits, receipts, e := autoExpertAuditEvidence(a, p, pin, len(attempts))
	if e != nil {
		return nil, e
	}
	known := map[string]bool{}
	for _, old := range attempts {
		for _, k := range old.EvidenceKeys {
			known[k] = true
		}
	}
	keys := map[string]bool{}
	newPerRole := map[string]bool{}
	for _, r := range receipts {
		k := autoExpertEvidenceKey(r)
		keys[k] = true
		if !known[k] {
			newPerRole[r.Role] = true
		}
	}
	if !newPerRole["auditor_a"] || !newPerRole["auditor_b"] {
		return nil, errors.New("unchanged experiment/runtime cannot renew expert eligibility; source/log/title/clock changes are insufficient")
	}
	keyList := []string{}
	for k := range keys {
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)
	n := len(attempts) + 1
	lease := autoSHA([]byte(fmt.Sprintf("%d:%d:%s:%s", pin.RootTaskID, n, pin.Key, store.J(p))))
	attempt := &autoExpertRecoveryAttempt{RootTaskID: pin.RootTaskID, Number: n, LeaseKey: lease, ProgressKey: pin.Key, Proposal: p, Cycle: a.State.Cycle, Revision: a.State.Revision, Item: a.State.Item, JobID: jobID, Status: "reserved", CreatedAt: now, Usage: map[string]int64{}, FailureFamily: audits["auditor_a"].FailureFamily, EvidenceKeys: keyList, Audits: audits, ProbeReceipts: receipts}
	a.ExpertRecovery.Attempts[pin.RootTaskID] = append(attempts, attempt)
	return attempt, nil
}

// Account monotonic actual execution per operational UUID, across restarts.
// Caller must apply runner timeout <= remaining budget before every launch.
func autoChargeExpertRecovery(v *autoExpertRecoveryAttempt, jobID string, milliseconds int64) error {
	if v == nil || !autoPlanJobID.MatchString(jobID) || milliseconds < 0 {
		return errors.New("invalid expert usage observation")
	}
	if v.Usage == nil {
		v.Usage = map[string]int64{}
	}
	old := v.Usage[jobID]
	if milliseconds < old {
		return errors.New("expert cumulative usage cannot decrease")
	}
	v.Usage[jobID] = milliseconds
	delta := milliseconds - old
	if v.ChargedMilliseconds >= autoExpertBuildBudget.Milliseconds() || delta >= autoExpertBuildBudget.Milliseconds()-v.ChargedMilliseconds {
		v.ChargedMilliseconds = autoExpertBuildBudget.Milliseconds()
	} else {
		v.ChargedMilliseconds += delta
	}
	if v.ChargedMilliseconds >= autoExpertBuildBudget.Milliseconds() {
		return errors.New("expert cumulative implementation budget exhausted")
	}
	return nil
}
func autoFinishExpertRecovery(v *autoExpertRecoveryAttempt, outcome string, reviewID int64, reason string, now time.Time) error {
	if v == nil || !autoExpertTerminal(outcome) || strings.TrimSpace(reason) == "" || now.Before(v.CreatedAt) {
		return errors.New("invalid expert terminal result")
	}
	if (outcome == "approved" || outcome == "rejected") && reviewID <= 0 {
		return errors.New("substantive expert outcome requires independent review identity")
	}
	if autoExpertTerminal(v.Status) {
		if v.Status == outcome && v.ReviewTaskID == reviewID && v.ReviewReason == reason {
			return nil
		}
		return errors.New("expert terminal history is immutable")
	}
	v.Status = outcome
	v.FinishedAt = now
	v.ReviewTaskID = reviewID
	v.ReviewReason = reason
	return nil
}
func autoExpertRecoveryRows(a *autoRecord, now time.Time) []map[string]any {
	rows := []map[string]any{}
	if a.ExpertRecovery == nil {
		return rows
	}
	keys := []string{}
	for k := range a.ExpertRecovery.Pins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := a.ExpertRecovery.Pins[k]
		v := autonomy.Proposal{ProjectID: p.ProjectID, ExpertRecoveryTaskID: p.SourceTaskID, ExpertProgressKey: k}
		_, e := autoExpertRecoveryEligible(a, v, now)
		row := map[string]any{"expert_recovery_task_id": p.SourceTaskID, "expert_progress_key": k, "root_task_id": p.RootTaskID, "project_id": p.ProjectID, "attempts": a.ExpertRecovery.Attempts[p.RootTaskID], "source_archive_sha256": p.SourceSHA, "failure_family": p.AcceptanceSHA, "acceptance_sha256": p.AcceptanceSHA, "prior_attempt": len(a.ExpertRecovery.Attempts[p.RootTaskID]), "original_acceptance": p.Acceptance, "source_acceptance": p.SourceAcceptance, "status": "investigation_available", "note": "A catalog pin does not grant implementation eligibility: both independent auditor probes and progress judgments are required."}
		if e != nil {
			row["status"] = "retained"
			row["reason"] = e.Error()
		}
		rows = append(rows, row)
	}
	return rows
}

// Lightweight archive identity calls only; no hashing or extraction under
// autoMu. Existing runner archive-identity authenticates its published receipt.
func (s *Server) validateAutoExpertSources(ctx context.Context, a *autoRecord, items []autonomy.Proposal) error {
	roots := map[int64]bool{}
	for _, p := range items {
		if p.ExpertRecoveryTaskID == 0 {
			if e := autoValidateExpertProposal(p); e != nil {
				return e
			}
			continue
		}
		pin, e := autoExpertPin(a, p)
		if e != nil {
			return e
		}
		if roots[pin.RootTaskID] {
			return errors.New("duplicate expert root in selected plan")
		}
		roots[pin.RootTaskID] = true
		sourceSHA, e := s.autoArchiveIdentity(ctx, autoFindJob(a, pin.SourceTaskID))
		if e != nil {
			return e
		}
		reviewSHA, e := s.autoArchiveIdentity(ctx, autoFindJob(a, pin.ReviewTaskID))
		if e != nil {
			return e
		}
		if sourceSHA != pin.SourceSHA || reviewSHA != pin.ReviewSHA {
			return errors.New("expert pinned source/reviewer archive changed; fresh audits required")
		}
	}
	return nil
}
func autoExpertAttempt(a *autoRecord, j *autoJob) (*autoExpertRecoveryAttempt, error) {
	if j == nil || j.ExpertRecoveryRoot <= 0 || j.ExpertRecoveryAttempt <= 0 || a.ExpertRecovery == nil {
		return nil, errors.New("expert job has no durable lease")
	}
	attempts := a.ExpertRecovery.Attempts[j.ExpertRecoveryRoot]
	if j.ExpertRecoveryAttempt > len(attempts) {
		return nil, errors.New("expert attempt unavailable")
	}
	v := attempts[j.ExpertRecoveryAttempt-1]
	if v.RootTaskID != j.ExpertRecoveryRoot || v.Number != j.ExpertRecoveryAttempt || v.TaskID <= 0 || (j.Role == "builder" && v.TaskID != j.TaskID) || (j.Role == "reviewer" && v.ReviewerTaskID != j.TaskID) {
		return nil, errors.New("expert attempt task binding mismatch")
	}
	return v, nil
}
func autoExpertRemaining(v *autoExpertRecoveryAttempt) time.Duration {
	if v == nil || autoExpertTerminal(v.Status) || v.ChargedMilliseconds < 0 || v.ChargedMilliseconds >= autoExpertBuildBudget.Milliseconds() {
		return 0
	}
	remaining := autoExpertBuildBudget - time.Duration(v.ChargedMilliseconds)*time.Millisecond
	if remaining < 0 {
		return 0
	}
	return remaining
}

func validateAutoExpertAuditReport(a *autoRecord, role string, raw []byte) error {
	if a.State == nil || a.State.Phase != autonomy.Audit || (role != "auditor_a" && role != "auditor_b") {
		return nil
	}
	var v autonomy.Verdict
	if e := json.Unmarshal(raw, &v); e != nil {
		return e
	}
	if len(v.ExpertRecovery) == 0 {
		selected := false
		for _, p := range a.State.Items {
			if p.ExpertRecoveryTaskID > 0 {
				selected = true
				break
			}
		}
		if !selected {
			return nil
		}
	}
	j, e := autoExpertAuditOwner(a, role)
	if e != nil {
		return e
	}
	return autoValidateExpertAudit(a, j, v)
}
func autoChargeExpertReview(v *autoExpertRecoveryAttempt, jobID string, milliseconds int64) error {
	if v == nil || !autoPlanJobID.MatchString(jobID) || milliseconds < 0 {
		return errors.New("invalid expert review usage observation")
	}
	if v.ReviewUsage == nil {
		v.ReviewUsage = map[string]int64{}
	}
	old := v.ReviewUsage[jobID]
	if milliseconds < old {
		return errors.New("expert cumulative review usage cannot decrease")
	}
	v.ReviewUsage[jobID] = milliseconds
	delta := milliseconds - old
	if v.ReviewChargedMilliseconds >= autoExpertReviewBudget.Milliseconds() || delta >= autoExpertReviewBudget.Milliseconds()-v.ReviewChargedMilliseconds {
		v.ReviewChargedMilliseconds = autoExpertReviewBudget.Milliseconds()
	} else {
		v.ReviewChargedMilliseconds += delta
	}
	if v.ReviewChargedMilliseconds >= autoExpertReviewBudget.Milliseconds() {
		return errors.New("expert cumulative review budget exhausted")
	}
	return nil
}
func autoExpertReviewRemaining(v *autoExpertRecoveryAttempt) time.Duration {
	if v == nil || autoExpertTerminal(v.Status) || v.ReviewChargedMilliseconds < 0 || v.ReviewChargedMilliseconds >= autoExpertReviewBudget.Milliseconds() {
		return 0
	}
	remaining := autoExpertReviewBudget - time.Duration(v.ReviewChargedMilliseconds)*time.Millisecond
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Schema correction and operational retries retain the admitted auditor task.
// Its earlier immutable probe remains attributable to that same assignment,
// while current launch authority belongs only to the latest UUID.
func autoExpertProbeOwnerValid(a *autoRecord, lease *autoExpertProbeLease, owner *autoJob) bool {
	if lease == nil || owner == nil || lease.OwnerTask != owner.TaskID || lease.Role != owner.Role {
		return false
	}
	if lease.OwnerJob == owner.ID {
		return true
	}
	for _, j := range a.Jobs {
		if j.ID == lease.OwnerJob {
			return j.TaskID == owner.TaskID && j.Role == owner.Role && (j.Status == "done" || j.Status == "failed" || j.Status == "stopped")
		}
	}
	return false
}

// Prior success closes its satisfied historical scope, not the entire future
// lineage. A later rejected milestone must actually descend from that success;
// changing a title, choosing an older sibling, or altering catalog keys is not
// lineage. Distinct acceptance is necessary, not semantic novelty proof: both
// auditors still compare scope and materially new executed causal evidence.
func autoExpertLaterRejectedMilestone(a *autoRecord, pin *autoExpertRecoveryPin, approved *autoExpertRecoveryAttempt) bool {
	if approved == nil || approved.TaskID <= 0 || pin.SourceTaskID == approved.TaskID {
		return false
	}
	if pin.SourceAcceptanceSHA == autoSHA([]byte(store.J(approved.Proposal.Acceptance))) {
		return false
	}
	rid, _, ok := autoRejectedCheckpoint(a, pin.SourceTaskID)
	if !ok || rid <= approved.ReviewTaskID {
		return false
	}
	seen := map[int64]bool{}
	id := pin.SourceTaskID
	for id > 0 && !seen[id] {
		seen[id] = true
		j := autoFindJob(a, id)
		if j == nil || j.Admission == nil {
			return false
		}
		p := j.Admission.Proposal
		next := p.ContinueTaskID
		if p.ExpertRecoveryTaskID > 0 {
			next = p.ExpertRecoveryTaskID
		}
		if p.RepairTaskID > 0 {
			next = p.RepairTaskID
		}
		if next == approved.TaskID {
			return true
		}
		id = next
	}
	return false
}
