package api

// Private integration authority is separate from historical manual attestations.
// All receipt inputs below must come from the privileged runner adapter, never
// directly from a worker's report. Semantic test adequacy remains peer-reviewed.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"path"
	"sort"
	"strings"
	"time"
)

type autoPrivateIntegrationLedger struct {
	Pins map[string]*autoPrivateIntegrationPin `json:"pins"`
	Runs map[string]*autoPrivateIntegrationRun `json:"runs"`
}
type autoPrivateIntegrationPin struct {
	PriorAttemptID            string   `json:"prior_attempt_id,omitempty"`
	PriorBuilderJob           string   `json:"prior_builder_job,omitempty"`
	PriorBuilderSHA           string   `json:"prior_builder_sha256,omitempty"`
	PriorReviewJob            string   `json:"prior_review_job,omitempty"`
	PriorReviewSHA            string   `json:"prior_review_sha256,omitempty"`
	BaseKind                  string   `json:"base_kind"`
	BasePublicationID         string   `json:"base_publication_id,omitempty"`
	BasePublicationReceiptSHA string   `json:"base_publication_receipt_sha256,omitempty"`
	CanonicalRevision         string   `json:"canonical_revision"`
	CanonicalTree             string   `json:"canonical_tree"`
	RawSourceSHA              string   `json:"raw_source_archive_sha256"`
	SourceArchiveKind         string   `json:"source_archive_kind"`
	SourceReportSHA           string   `json:"source_report_sha256"`
	ReviewReportSHA           string   `json:"review_report_sha256"`
	ExpectedManagedCommit     string   `json:"expected_managed_commit"`
	Key                       string   `json:"key"`
	RootID                    string   `json:"root_id"`
	ProjectID                 int64    `json:"project_id"`
	SourceProjectID           int64    `json:"source_project_id"`
	SourceTaskID              int64    `json:"source_task_id"`
	SourceJob                 string   `json:"source_job"`
	SourceSHA                 string   `json:"source_archive_sha256"`
	ReviewTaskID              int64    `json:"review_task_id"`
	ReviewJob                 string   `json:"review_job"`
	ReviewSHA                 string   `json:"review_archive_sha256"`
	BaseRevision              string   `json:"base_revision"`
	BaseTree                  string   `json:"base_tree"`
	Paths                     []string `json:"paths"`
	Acceptance                []string `json:"acceptance"`
	SourceAcceptance          []string `json:"source_acceptance"`
}
type autoPrivateIntegrationAuthority struct {
	Pin    autoPrivateIntegrationPin   `json:"pin"`
	Audits map[string]autonomy.Verdict `json:"plan_audits"`
	Scope  string                      `json:"scope"`
}
type autoPrivateIntegrationRun struct {
	RootID       string                           `json:"root_id"`
	ProjectID    int64                            `json:"project_id"`
	SourceTaskID int64                            `json:"source_task_id"`
	Attempts     []*autoPrivateIntegrationAttempt `json:"attempts"`
}
type autoPrivateIntegrationAttempt struct {
	ExpertLeaseKey              string                           `json:"expert_lease_key,omitempty"`
	ExpertRootTaskID            int64                            `json:"expert_root_task_id,omitempty"`
	ExpertAttempt               int                              `json:"expert_attempt,omitempty"`
	Reason                      string                           `json:"reason,omitempty"`
	TestLeases                  map[string]*autoPrivateTestLease `json:"test_leases,omitempty"`
	ID                          string                           `json:"id"`
	RootID                      string                           `json:"root_id"`
	Number                      int                              `json:"number"`
	PinKey                      string                           `json:"pin_key"`
	Status                      string                           `json:"status"`
	Cycle                       int                              `json:"cycle"`
	Revision                    int                              `json:"revision"`
	Item                        int                              `json:"item"`
	Proposal                    autonomy.Proposal                `json:"proposal"`
	Authority                   autoPrivateIntegrationAuthority  `json:"authority"`
	JobID                       string                           `json:"job_id"`
	TaskID                      int64                            `json:"task_id"`
	ReviewerJob                 string                           `json:"reviewer_job"`
	ReviewerTaskID              int64                            `json:"reviewer_task_id"`
	CreatedAt                   time.Time                        `json:"created_at"`
	Usage                       map[string]int64                 `json:"usage"`
	ReviewerUsage               map[string]int64                 `json:"reviewer_usage"`
	ChargedMilliseconds         int64                            `json:"charged_milliseconds"`
	ReviewerChargedMilliseconds int64                            `json:"reviewer_charged_milliseconds"`
	Candidate                   *autoPrivateCandidate            `json:"candidate,omitempty"`
	Tests                       []autoPrivateTestReceipt         `json:"tests,omitempty"`
	Review                      *autoPrivateReview               `json:"review,omitempty"`
	Publication                 *autoPrivatePublication          `json:"publication,omitempty"`
	Generation                  int                              `json:"generation"`
	StopRequested               bool                             `json:"stop_requested"`
	StopConfirmed               bool                             `json:"stop_confirmed"`
}
type autoPrivateCandidate struct {
	NoChanges         bool   `json:"no_changes"`
	CandidateCommit   string `json:"candidate_commit"`
	GitTree           string `json:"git_tree"`
	BaseTreeSHA       string `json:"base_tree_sha256"`
	BuilderArchiveSHA string `json:"builder_archive_sha256"`
	PrepareRequestSHA string `json:"prepare_request_sha256"`
	IntegrationID     string `json:"integration_id"`
	PinKey            string `json:"pin_key"`
	BuilderJob        string `json:"builder_job"`
	ArchiveSHA        string `json:"archive_sha256"`
	TreeSHA           string `json:"candidate_tree_sha256"`
	ManifestSHA       string `json:"changed_manifest_sha256"`
	BaseTree          string `json:"base_tree"`
	ReceiptSHA        string `json:"receipt_sha256"`
}
type autoPrivateTestReceipt struct {
	GoDependencyKey     string          `json:"go_dependency_key,omitempty"`
	GoBundleDigest      string          `json:"go_bundle_digest,omitempty"`
	GoToolchainDigest   string          `json:"go_toolchain_digest,omitempty"`
	NodeBundleKey       string          `json:"node_bundle_key,omitempty"`
	NodeInputKey        string          `json:"node_input_key,omitempty"`
	NodeLockSHA         string          `json:"node_lock_sha256,omitempty"`
	NodeRuntimeDigest   string          `json:"node_runtime_digest,omitempty"`
	PythonInputKey      string          `json:"python_input_key,omitempty"`
	PythonTestKey       string          `json:"python_test_key,omitempty"`
	ReceiptEvidence     json.RawMessage `json:"receipt_evidence,omitempty"`
	Reason              string          `json:"reason,omitempty"`
	StartedAt           time.Time       `json:"started_at"`
	EndedAt             time.Time       `json:"ended_at"`
	CheckID             string          `json:"check_id"`
	Generation          int             `json:"generation"`
	ChargedMilliseconds int64           `json:"charged_ms"`
	PythonBundleKey     string          `json:"python_bundle_key,omitempty"`
	BrowserKey          string          `json:"browser_key,omitempty"`
	IntegrationID       string          `json:"integration_id"`
	CandidateSHA        string          `json:"candidate_tree_sha256"`
	OwnerJob            string          `json:"owner_job"`
	OwnerTask           int64           `json:"owner_task"`
	RequestSHA          string          `json:"request_sha256"`
	RuntimeSHA          string          `json:"runtime_digest"`
	OutputSHA           string          `json:"output_manifest_sha256"`
	ReceiptSHA          string          `json:"receipt_sha256"`
	State               string          `json:"state"`
	Executed            bool            `json:"executed"`
	ExitCode            *int            `json:"exit_code"`
	Truncated           bool            `json:"truncated"`
}
type autoPrivateReview struct {
	ArchiveSHA      string   `json:"archive_sha256"`
	JobID           string   `json:"job_id"`
	TaskID          int64    `json:"task_id"`
	CandidateSHA    string   `json:"candidate_tree_sha256"`
	ReportSHA       string   `json:"report_sha256"`
	TestReceiptSHAs []string `json:"test_receipt_sha256"`
	Approved        bool     `json:"approved"`
}
type autoPrivatePublication struct {
	NoChanges        bool   `json:"no_changes"`
	Current          bool   `json:"current"`
	BaseAdvanced     bool   `json:"base_advanced"`
	CanonicalPending string `json:"canonical_pending,omitempty"`
	ConsumerReady    bool   `json:"consumer_ready"`
	CanonicalChanged bool   `json:"canonical_changed"`
	Deployed         bool   `json:"deployed"`
	ID               string `json:"id"`
	RootID           string `json:"root_id"`
	ProjectID        int64  `json:"project_id"`
	PinKey           string `json:"pin_key"`
	CandidateSHA     string `json:"candidate_tree_sha256"`
	Commit           string `json:"commit"`
	GitTree          string `json:"git_tree"`
	Parent           string `json:"parent"`
	Ref              string `json:"ref"`
	ReceiptSHA       string `json:"receipt_sha256"`
	ReviewReportSHA  string `json:"review_report_sha256"`
	State            string `json:"state"`
}

func autoPrivateLedger(a *autoRecord) *autoPrivateIntegrationLedger {
	if a.PrivateIntegration == nil {
		a.PrivateIntegration = &autoPrivateIntegrationLedger{}
	}
	l := a.PrivateIntegration
	if l.Pins == nil {
		l.Pins = map[string]*autoPrivateIntegrationPin{}
	}
	if l.Runs == nil {
		l.Runs = map[string]*autoPrivateIntegrationRun{}
	}
	return l
}
func autoPrivatePaths(in []string) ([]string, error) {
	if len(in) == 0 || len(in) > 64 {
		return nil, errors.New("integration requires 1..64 destination paths")
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	for i, p := range out {
		base := strings.TrimSuffix(p, "/**")
		if len(p) > 512 || base == "." || base == "" || path.IsAbs(base) || path.Clean(base) != base || strings.ContainsAny(base, "\\\x00\r\n*?[") {
			return nil, errors.New("integration paths must be exact relative paths or trailing /**")
		}
		for _, part := range strings.Split(base, "/") {
			if part == ".git" || strings.HasPrefix(part, ".lectern-") || part == "autonomy-report.json" || part == ".." {
				return nil, errors.New("integration path contains reserved transport or Git component")
			}
		}
		for _, old := range out[:i] {
			if p == old || base == strings.TrimSuffix(old, "/**") || (strings.HasSuffix(old, "/**") && strings.HasPrefix(base, strings.TrimSuffix(old, "**"))) {
				return nil, errors.New("overlapping integration paths")
			}
		}
	}
	return out, nil
}
func autoPrivatePinKey(p autoPrivateIntegrationPin) string {
	p.Key = ""
	return autoSHA([]byte(store.J(p)))
}
func (s *Server) pinAutoPrivateIntegrations(ctx context.Context, a *autoRecord, items []autonomy.Proposal) error {
	for i := range items {
		p := &items[i]
		if p.SourceIntegrationID != "" {
			if _, e := autoPrivateIntegrationSource(a, p.SourceIntegrationID, p.ProjectID); e != nil {
				return e
			}
		}
		if p.IntegrationTaskID == 0 {
			continue
		}
		paths, e := autoPrivatePaths(p.IntegrationPaths)
		if e != nil {
			return e
		}
		source := autoFindJob(a, p.IntegrationTaskID)
		if source == nil || source.Status != "done" || source.Role != "builder" || !autoPrivateApprovedSource(a, source.TaskID) {
			return errors.New("integration requires independently approved completed source")
		}
		review := autoPrivateSourceReviewer(a, source)
		if review == nil || review.Status != "done" || review.Role != "reviewer" {
			return errors.New("integration review unavailable")
		}
		project, e := s.autoSourceProject(p.ProjectID)
		if e != nil {
			return e
		}
		task, e := s.DB.Task(source.TaskID)
		if e != nil {
			return e
		}
		rev, e := autoSourceRevision(ctx, project.RepoPath, "HEAD")
		if e != nil {
			return e
		}
		tree, e := autoSourceTree(ctx, project.RepoPath, rev)
		if e != nil {
			return e
		}
		sha, e := s.autoArchiveIdentity(ctx, source)
		if e != nil {
			return e
		}
		rawSHA := sha
		report, e := s.autoHistoricalReport(ctx, a, source, rawSHA)
		if e != nil {
			return e
		}
		kind := "raw"
		if source.DocumentationRoot > 0 {
			if source.Documentation == nil || source.Documentation.State != "ready" || !autoHash256(source.Documentation.DerivedSHA) {
				return errors.New("verified documentary source unavailable")
			}
			kind = "derived"
			sha = source.Documentation.DerivedSHA
		}
		rsha, e := s.autoArchiveIdentity(ctx, review)
		if e != nil {
			return e
		}
		reviewReport, e := s.autoHistoricalReport(ctx, a, review, rsha)
		if e != nil {
			return e
		}
		var verdict autonomy.Verdict
		if json.Unmarshal(reviewReport, &verdict) != nil || !verdict.AcceptsWork() {
			return errors.New("immutable source review does not approve completed output")
		}
		acceptance, e := autoTaskAcceptance(a, source.TaskID)
		if e != nil {
			return e
		}
		// Repairs/documentary/expert versions of one approved output share a root.
		// A separately audited and approved continuation milestone is a new source
		// contribution; semantic auditors must reject cosmetic milestone laundering.
		rootSource := autoRepairRoot(a, source.TaskID)
		if rootSource <= 0 {
			rootSource = source.TaskID
		}
		root := autoSHA([]byte(fmt.Sprintf("private-adaptation-v1:%d:%d", rootSource, p.ProjectID)))
		pin := autoPrivateIntegrationPin{RawSourceSHA: rawSHA, SourceArchiveKind: kind, SourceReportSHA: autoSHA(report), ReviewReportSHA: autoSHA(reviewReport), RootID: root, ProjectID: p.ProjectID, SourceProjectID: task.ProjectID, SourceTaskID: source.TaskID, SourceJob: source.ID, SourceSHA: sha, ReviewTaskID: review.TaskID, ReviewJob: review.ID, ReviewSHA: rsha, BaseRevision: rev, BaseTree: tree, Paths: paths, Acceptance: append([]string(nil), p.Acceptance...), SourceAcceptance: acceptance}
		if run := autoPrivateLedger(a).Runs[root]; run != nil && len(run.Attempts) > 0 {
			prior := run.Attempts[len(run.Attempts)-1]
			if !autoPrivateAttemptTerminal(prior.Status) {
				return errors.New("private integration root already has a live retained owner")
			}
			pin.PriorAttemptID = prior.ID
			if prior.Candidate != nil {
				if !autoPlanJobID.MatchString(prior.Candidate.BuilderJob) || !autoHash256(prior.Candidate.BuilderArchiveSHA) {
					return errors.New("prior private candidate archive unavailable")
				}
				pin.PriorBuilderJob = prior.Candidate.BuilderJob
				pin.PriorBuilderSHA = prior.Candidate.BuilderArchiveSHA
			}
			if prior.Review != nil {
				if !autoPlanJobID.MatchString(prior.Review.JobID) || !autoHash256(prior.Review.ArchiveSHA) {
					return errors.New("prior private review archive unavailable")
				}
				pin.PriorReviewJob = prior.Review.JobID
				pin.PriorReviewSHA = prior.Review.ArchiveSHA
			}
		}
		pin.BaseKind = "canonical"
		pin.CanonicalRevision = rev
		pin.CanonicalTree = tree
		tipRaw, tipErr := s.runAutoCommand(ctx, "integration-tip", "--project-id", fmt.Sprint(p.ProjectID))
		if tipErr != nil {
			return tipErr
		}
		var tip struct {
			State   string `json:"state"`
			ID      string `json:"integration_id"`
			Commit  string `json:"commit"`
			Tree    string `json:"tree_oid"`
			Receipt string `json:"publication_receipt_sha256"`
		}
		if json.Unmarshal(tipRaw, &tip) != nil {
			return errors.New("invalid managed private tip")
		}
		switch tip.State {
		case "absent":
		case "ready":
			published, e := autoPrivateIntegrationSource(a, tip.ID, p.ProjectID)
			if e != nil {
				return e
			}
			if published.Commit != tip.Commit || published.GitTree != tip.Tree || published.ReceiptSHA != tip.Receipt {
				return errors.New("managed tip disagrees with publication ledger")
			}
			pin.BaseKind = "private"
			pin.BasePublicationID = published.ID
			pin.BasePublicationReceiptSHA = published.ReceiptSHA
			pin.BaseRevision = published.Commit
			pin.BaseTree = published.GitTree
			pin.ExpectedManagedCommit = published.Commit
		default:
			return fmt.Errorf("%w: managed private tip is reconciling", errAutoArtifactPending)
		}
		pin.Key = autoPrivatePinKey(pin)
		if p.IntegrationPin != "" && p.IntegrationPin != pin.Key {
			return errors.New("private integration inputs changed; revise before audits")
		}
		autoPrivateLedger(a).Pins[pin.Key] = &pin
		p.IntegrationPin = pin.Key
		p.IntegrationPaths = paths
	}
	return nil
}
func autoPrivatePin(a *autoRecord, p autonomy.Proposal) (*autoPrivateIntegrationPin, error) {
	if a.PrivateIntegration == nil {
		return nil, errors.New("private integration pin unavailable")
	}
	v := a.PrivateIntegration.Pins[p.IntegrationPin]
	if v == nil || autoPrivatePinKey(*v) != v.Key || v.ProjectID != p.ProjectID || v.SourceTaskID != p.IntegrationTaskID || store.J(v.Paths) != store.J(p.IntegrationPaths) || store.J(v.Acceptance) != store.J(p.Acceptance) {
		return nil, errors.New("private integration pin mismatch")
	}
	j := autoFindJob(a, v.SourceTaskID)
	if j == nil || j.ID != v.SourceJob || !autoPrivateApprovedSource(a, j.TaskID) {
		return nil, errors.New("private integration approved source changed")
	}
	r := autoPrivateSourceReviewer(a, j)
	if r == nil || r.TaskID != v.ReviewTaskID || r.ID != v.ReviewJob || r.Status != "done" {
		return nil, errors.New("private integration reviewer changed")
	}
	if err := autoValidatePrivatePrior(a, v); err != nil {
		return nil, err
	}
	return v, nil
}
func autoReservePrivateIntegration(a *autoRecord, p autonomy.Proposal, job string, providers []string, now time.Time) (*autoPrivateIntegrationAttempt, error) {
	if len(providers) == 0 {
		return nil, errors.New("private integration quota providers required")
	}
	if e := autonomy.QuotaGate(a.Config, a.Quota.Providers, providers, now); e != nil {
		return nil, e
	}
	pin, e := autoPrivatePin(a, p)
	if e != nil {
		return nil, e
	}
	if a.State == nil || a.State.Phase != autonomy.Build || a.State.Item < 0 || a.State.Item >= len(a.State.Items) || store.J(a.State.Items[a.State.Item]) != store.J(p) || !autoRepairAudited(a) || !autoPlanJobID.MatchString(job) {
		return nil, errors.New("private integration requires current independently audited assignment")
	}
	l := autoPrivateLedger(a)
	r := l.Runs[pin.RootID]
	if r == nil {
		r = &autoPrivateIntegrationRun{RootID: pin.RootID, ProjectID: p.ProjectID, SourceTaskID: pin.SourceTaskID}
		l.Runs[r.RootID] = r
	}
	for _, v := range r.Attempts {
		if v.Publication != nil && autoPrivatePathsCovered(pin.Paths, v.Authority.Pin.Paths) && (v.Publication.ID == pin.BasePublicationID || v.Publication.GitTree == pin.BaseTree) {
			return nil, errors.New("selected source scope already published on this managed base")
		}
		if v.Cycle == a.State.Cycle && v.Revision == a.State.Revision && v.Item == a.State.Item && store.J(v.Proposal) == store.J(p) && v.Status != "rejected" && v.Status != "published_private" && v.Status != "budget_exhausted" && v.Status != "unavailable" && v.Status != "base_advanced" {
			if v.JobID != job {
				return nil, errors.New("resume existing private integration owner")
			}
			return v, nil
		}
		if v.Status != "rejected" && v.Status != "budget_exhausted" && v.Status != "unavailable" && v.Status != "base_advanced" && !(v.Status == "published_private" && v.Authority.Pin.BaseTree != pin.BaseTree) {
			return nil, errors.New("private integration root already active or published")
		}
	}
	sameBase := 0
	for _, old := range r.Attempts {
		if old.Authority.Pin.BaseTree == pin.BaseTree {
			sameBase++
		}
	}
	if sameBase > a.Config.MaxRevisionRounds {
		return nil, errors.New("private integration correction allowance exhausted; retain root for audited progress recovery")
	}
	audits := map[string]autonomy.Verdict{}
	for k, v := range a.State.Audits {
		audits[k] = v
	}
	n := len(r.Attempts) + 1
	v := &autoPrivateIntegrationAttempt{ID: autoSHA([]byte(fmt.Sprintf("%s:%d:%s", r.RootID, n, pin.Key))), RootID: r.RootID, Number: n, PinKey: pin.Key, Status: "reserved", Cycle: a.State.Cycle, Revision: a.State.Revision, Item: a.State.Item, Proposal: p, JobID: job, CreatedAt: now, Generation: 1, Authority: autoPrivateIntegrationAuthority{Pin: *pin, Audits: audits, Scope: "Adapt and independently review this pinned combined tree; controller may publish only a managed private revision after verified checks. No canonical, deployment, public, or manual-attestation authority."}}
	// Freeze all nested proposal/audit/scope slices; later planner edits cannot mutate admission.
	frozen, _ := json.Marshal(v)
	var detached autoPrivateIntegrationAttempt
	if err := json.Unmarshal(frozen, &detached); err != nil {
		return nil, err
	}
	v = &detached
	r.Attempts = append(r.Attempts, v)
	return v, nil
}
func autoPrivateAttempt(a *autoRecord, j *autoJob) (*autoPrivateIntegrationAttempt, error) {
	if a.PrivateIntegration == nil || j == nil {
		return nil, errors.New("private attempt unavailable")
	}
	r := a.PrivateIntegration.Runs[j.PrivateIntegrationRoot]
	if r == nil || j.PrivateIntegrationAttempt < 1 || j.PrivateIntegrationAttempt > len(r.Attempts) {
		return nil, errors.New("private attempt unavailable")
	}
	v := r.Attempts[j.PrivateIntegrationAttempt-1]
	if (j.Role == "builder" && v.TaskID != j.TaskID) || (j.Role == "reviewer" && v.ReviewerTaskID != j.TaskID) {
		return nil, errors.New("private attempt owner mismatch")
	}
	return v, nil
}

func autoRecordPrivateCandidate(v *autoPrivateIntegrationAttempt, c autoPrivateCandidate) error {
	if v == nil || c.IntegrationID != v.ID || c.PinKey != v.PinKey || c.BuilderJob != v.JobID || c.BaseTree != v.Authority.Pin.BaseTree || !autoSourceHash(c.CandidateCommit) || !autoSourceHash(c.GitTree) || !autoHash256(c.BaseTreeSHA) || !autoHash256(c.BuilderArchiveSHA) || !autoHash256(c.PrepareRequestSHA) || !autoHash256(c.TreeSHA) || !autoHash256(c.ArchiveSHA) || !autoHash256(c.ManifestSHA) || !autoHash256(c.ReceiptSHA) {
		return errors.New("private candidate identity mismatch")
	}
	if c.NoChanges != (c.TreeSHA == c.BaseTreeSHA) {
		return errors.New("private candidate no-change classification mismatch")
	}
	if v.Candidate != nil {
		if store.J(v.Candidate) == store.J(c) {
			return nil
		}
		return errors.New("private candidate is immutable")
	}
	if v.Status != "reserved" && v.Status != "adapting" && v.Status != "freezing" {
		return errors.New("private candidate phase mismatch")
	}
	v.Candidate = &c
	v.Status = "reviewing"
	return nil
}
func autoRecordPrivateTestReceipt(v *autoPrivateIntegrationAttempt, t autoPrivateTestReceipt) error {
	if v == nil || v.Candidate == nil || t.IntegrationID != v.ID || t.CandidateSHA != v.Candidate.TreeSHA || t.OwnerTask != v.ReviewerTaskID || !autoPlanJobID.MatchString(t.OwnerJob) || !autoHash256(t.CheckID) || t.Generation < 1 || t.ChargedMilliseconds < 0 || t.ChargedMilliseconds > 610000 || !autoHash256(t.RequestSHA) || !autoHash256(t.ReceiptSHA) {
		return errors.New("private test identity mismatch")
	}
	switch t.State {
	case "exited":
		if !t.Executed || t.ExitCode == nil {
			return errors.New("private exited receipt must prove execution")
		}
	case "rejected", "failed", "timeout", "output_limit", "cancelled", "interrupted", "unavailable":
		if t.Reason == "" {
			return errors.New("private diagnostic reason required")
		}
	default:
		return errors.New("private check is not terminal")
	}
	if t.Executed && (!autoHash256(t.RuntimeSHA) || !autoHash256(t.OutputSHA)) {
		return errors.New("private executed receipt lacks runtime/output identity")
	}
	for _, old := range v.Tests {
		if old.RequestSHA == t.RequestSHA {
			if store.J(old) == store.J(t) {
				return nil
			}
			return errors.New("private test receipt immutable")
		}
	}
	// An already reserved execution may finish after OFF, exhaustion, or rejection.
	// Recording its terminal evidence never reopens review or permits a new test.
	v.Tests = append(v.Tests, t)
	return nil
}
func autoRecordPrivateReview(v *autoPrivateIntegrationAttempt, r autoPrivateReview) error {
	if v == nil || v.Candidate == nil || r.JobID != v.ReviewerJob || r.TaskID != v.ReviewerTaskID || r.CandidateSHA != v.Candidate.TreeSHA || !autoHash256(r.ArchiveSHA) || !autoHash256(r.ReportSHA) {
		return errors.New("private review identity mismatch")
	}
	if v.Review != nil {
		if store.J(v.Review) == store.J(r) {
			return nil
		}
		return errors.New("private review immutable")
	}
	if v.Status != "reviewing" {
		return errors.New("private review phase mismatch")
	}
	if r.Approved {
		passed := false
		for _, id := range r.TestReceiptSHAs {
			found := false
			for _, t := range v.Tests {
				if t.ReceiptSHA == id {
					found = true
					if t.State == "exited" && t.Executed && t.ExitCode != nil && *t.ExitCode == 0 && !t.Truncated {
						passed = true
					}
				}
			}
			if !found {
				return errors.New("private review references unknown test")
			}
		}
		if !passed {
			return errors.New("private approval requires independently executed successful combined-tree test")
		}
	}
	v.Review = &r
	if r.Approved {
		v.Status = "publication_prepared"
	} else {
		v.Status = "rejected"
	}
	return nil
}
func autoRecordPrivatePublication(v *autoPrivateIntegrationAttempt, p autoPrivatePublication) error {
	if v == nil || v.Candidate == nil || v.Review == nil || !v.Review.Approved || p.ID != v.ID || p.RootID != v.RootID || p.ProjectID != v.Authority.Pin.ProjectID || p.PinKey != v.PinKey || p.CandidateSHA != v.Candidate.TreeSHA || p.ReviewReportSHA != v.Review.ReportSHA || p.Parent != v.Authority.Pin.BaseRevision || p.NoChanges != v.Candidate.NoChanges || p.Commit != v.Candidate.CandidateCommit || p.GitTree != v.Candidate.GitTree || !p.ConsumerReady || p.CanonicalChanged || p.Deployed || p.Ref != "refs/lectern/integrations/"+v.ID || !autoSourceHash(p.Commit) || !autoSourceHash(p.GitTree) || !autoHash256(p.ReceiptSHA) || p.State != "published_private" {
		return errors.New("private publication binding mismatch")
	}
	reviewCheck := *v
	reviewCheck.Review = nil
	reviewCheck.Status = "reviewing"
	if err := autoRecordPrivateReview(&reviewCheck, *v.Review); err != nil {
		return err
	}
	if v.Publication != nil {
		if store.J(v.Publication) == store.J(p) {
			return nil
		}
		return errors.New("private publication immutable")
	}
	if v.Status != "publication_prepared" {
		return errors.New("private publication phase mismatch")
	}
	v.Publication = &p
	v.Status = "published_private"
	return nil
}
func autoPrivateIntegrationSource(a *autoRecord, id string, project int64) (*autoPrivatePublication, error) {
	if !autoHash256(id) || a.PrivateIntegration == nil {
		return nil, errors.New("managed private source unavailable")
	}
	for _, r := range a.PrivateIntegration.Runs {
		for _, v := range r.Attempts {
			if v.ID != id {
				continue
			}
			if v.Status != "published_private" || v.Publication == nil || v.Publication.ProjectID != project {
				return nil, errors.New("managed private source is not published for this project")
			}
			p := *v.Publication
			copy := *v
			copy.Publication = nil
			copy.Status = "publication_prepared"
			if e := autoRecordPrivatePublication(&copy, p); e != nil {
				return nil, e
			}
			return &p, nil
		}
	}
	return nil, errors.New("managed private source unavailable")
}
func autoPrivateIntegrationRows(a *autoRecord) []map[string]any {
	rows := []map[string]any{}
	if a.PrivateIntegration == nil {
		return rows
	}
	keys := []string{}
	for k := range a.PrivateIntegration.Runs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := a.PrivateIntegration.Runs[key]
		for _, v := range r.Attempts {
			row := map[string]any{"root_id": r.RootID, "project_id": r.ProjectID, "source_task_id": r.SourceTaskID, "attempt": v.Number, "integration_id": v.ID, "pin_key": v.PinKey, "status": v.Status, "canonical_applied": false, "deployed": false}
			sameBaseAttempts := 0
			for _, prior := range r.Attempts {
				if prior.Authority.Pin.BaseTree == v.Authority.Pin.BaseTree {
					sameBaseAttempts++
				}
			}
			if v.Status == "rejected" && v.Number == len(r.Attempts) && sameBaseAttempts > a.Config.MaxRevisionRounds {
				row["recovery_requirement"] = map[string]any{"kind": "private_integration_progress", "state": "investigation_available", "root_id": r.RootID, "source_task_id": v.TaskID, "rejected_attempt_id": v.ID, "candidate": v.Candidate, "condition": "Planner may POST /expert-recovery with this project_id and source_task_id. Fresh independent executed evidence and both audits are required; the admitted dual lease retains this private root, original scope and acceptance. Expert approval alone cannot publish: sealed combined-tree integration tests, independent review and the private publication transaction remain mandatory."}
			}
			if v.Publication != nil {
				row["source_integration_id"] = v.Publication.ID
				row["publication"] = v.Publication
			}
			rows = append(rows, row)
		}
	}
	return rows
}

const autoPrivateExecutionBudget = 60 * time.Minute

// Per-attempt model accounting includes every operational UUID. Rebase attempts
// are distinct audited work, while test spending has a rolling root ledger below.
func autoChargePrivateIntegration(run *autoPrivateIntegrationRun, v *autoPrivateIntegrationAttempt, owner string, review bool, elapsed int64) error {
	if run == nil || v == nil || elapsed < 0 || owner == "" {
		return errors.New("invalid private execution observation")
	}
	usage := v.Usage
	if review {
		usage = v.ReviewerUsage
	}
	if usage == nil {
		usage = map[string]int64{}
		if review {
			v.ReviewerUsage = usage
		} else {
			v.Usage = usage
		}
	}
	old := usage[owner]
	if elapsed < old {
		return errors.New("private execution usage cannot decrease")
	}
	usage[owner] = elapsed
	delta := elapsed - old
	limit := (30 * time.Minute).Milliseconds()
	if delta > limit {
		delta = limit
	}
	if review {
		v.ReviewerChargedMilliseconds += delta
		if v.ReviewerChargedMilliseconds >= limit {
			return errors.New("private reviewer execution budget exhausted")
		}
	} else {
		v.ChargedMilliseconds += delta
		if v.ChargedMilliseconds >= limit {
			return errors.New("private builder execution budget exhausted")
		}
	}
	return nil
}
func autoPrivateRemaining(run *autoPrivateIntegrationRun) time.Duration {
	if run == nil || len(run.Attempts) == 0 {
		return 0
	}
	return autoPrivateRoleRemaining(run.Attempts[len(run.Attempts)-1], false)
}
func autoPrivateRoleRemaining(v *autoPrivateIntegrationAttempt, review bool) time.Duration {
	if v == nil {
		return 0
	}
	used := v.ChargedMilliseconds
	if review {
		used = v.ReviewerChargedMilliseconds
	}
	limit := (30 * time.Minute).Milliseconds()
	if used < 0 || used >= limit {
		return 0
	}
	return time.Duration(limit-used) * time.Millisecond
}

// Missing legacy flags are not a manual intervention requirement. Pinning still
// verifies the immutable archived review; an explicit later rejection wins.
func autoPrivateApprovedSource(a *autoRecord, id int64) bool {
	j := autoFindJob(a, id)
	if j != nil && ((j.PrivateIntegrationRoot != "" && j.PrivateIntegrationAttempt > 0) || j.DiagnosisRequirement != "" || (j.DocumentationRoot > 0 && (j.Documentation == nil || j.Documentation.State != "ready" || !autoHash256(j.Documentation.DerivedSHA)))) {
		return false
	}
	return j != nil && j.Role == "builder" && j.Status == "done" && !j.Rejected && (j.ReviewOutcome == "" || j.ReviewOutcome == "completed") && autoPrivateSourceReviewer(a, j) != nil
}
func autoPrivateSourceReviewer(a *autoRecord, j *autoJob) *autoJob {
	if j == nil {
		return nil
	}
	var chosen *autoJob
	if r := autoFindJob(a, j.ReviewTaskID); r != nil && r.Role == "reviewer" && r.Status == "done" {
		chosen = r
	}
	states := append([]*autonomy.State(nil), a.Runs...)
	if a.State != nil {
		states = append(states, a.State)
	}
	states = append(states, a.DeferredRuns...)
	states = append(states, a.HeldRuns...)
	for _, state := range states {
		if state == nil {
			continue
		}
		for _, b := range state.Assignments {
			if b.TaskID != j.TaskID || b.Role != "builder" || !b.Completed {
				continue
			}
			for _, r := range state.Assignments {
				if r.Role != "reviewer" || !r.Completed || r.Item != b.Item || r.Round != b.Round || r.Step != b.Step {
					continue
				}
				candidate := autoFindJob(a, r.TaskID)
				if candidate != nil && candidate.Status == "done" && candidate.Role == "reviewer" && (chosen == nil || candidate.TaskID > chosen.TaskID) {
					chosen = candidate
				}
			}
		}
	}
	return chosen
}

type autoPrivateTestLease struct {
	RequestRaw          json.RawMessage         `json:"request_raw,omitempty"`
	ObservationAttempts int                     `json:"observation_attempts,omitempty"`
	ID                  string                  `json:"id"`
	RequestSHA          string                  `json:"request_sha256"`
	OwnerJob            string                  `json:"owner_job"`
	OwnerTask           int64                   `json:"owner_task"`
	CandidateSHA        string                  `json:"candidate_tree_sha256"`
	CreatedAt           time.Time               `json:"created_at"`
	Receipt             *autoPrivateTestReceipt `json:"receipt,omitempty"`
	StopRequested       bool                    `json:"stop_requested"`
	StopConfirmed       bool                    `json:"stop_confirmed"`
	Generation          int                     `json:"generation"`
}

func autoReservePrivateTest(a *autoRecord, j *autoJob, request string, now time.Time) (*autoPrivateTestLease, error) {
	v, e := autoPrivateAttempt(a, j)
	if e != nil {
		return nil, e
	}
	if j.Role != "reviewer" || j.ID != v.ReviewerJob || j.Status != "running" || v.Status != "reviewing" || v.Candidate == nil || !autoHash256(request) {
		return nil, errors.New("private check requires current running independent reviewer")
	}
	if e = autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{j.Provider}, now); e != nil {
		return nil, e
	}
	if v.TestLeases == nil {
		v.TestLeases = map[string]*autoPrivateTestLease{}
	}
	if old := v.TestLeases[request]; old != nil {
		if old.StopRequested {
			return nil, errors.New("private check was cancelled")
		}
		return old, nil
	}
	run := a.PrivateIntegration.Runs[v.RootID]
	remaining := autoPrivateExecutionBudget.Milliseconds()
	for _, attempt := range run.Attempts {
		for _, lease := range attempt.TestLeases {
			if now.Sub(lease.CreatedAt) >= 24*time.Hour && lease.Receipt != nil {
				continue
			}
			charge := int64(600000)
			if lease.Receipt != nil {
				charge = lease.Receipt.ChargedMilliseconds
			}
			remaining -= charge
		}
	}
	if remaining < 600000 {
		return nil, errors.New("private root rolling test budget unavailable")
	}
	lease := &autoPrivateTestLease{ID: request, RequestSHA: request, OwnerJob: j.ID, OwnerTask: j.TaskID, CandidateSHA: v.Candidate.TreeSHA, CreatedAt: now, Generation: v.Generation}
	v.TestLeases[request] = lease
	return lease, nil
}
func autoRecordPrivateTest(a *autoRecord, t autoPrivateTestReceipt) error {
	if a.PrivateIntegration == nil {
		return errors.New("private check ledger unavailable")
	}
	for _, run := range a.PrivateIntegration.Runs {
		for _, v := range run.Attempts {
			if v.ID != t.IntegrationID {
				continue
			}
			lease := v.TestLeases[t.CheckID]
			if lease == nil || lease.RequestSHA != t.RequestSHA || lease.OwnerJob != t.OwnerJob || lease.OwnerTask != t.OwnerTask || lease.CandidateSHA != t.CandidateSHA || lease.Generation != t.Generation {
				return errors.New("private check lease mismatch")
			}
			if lease.Receipt != nil {
				if store.J(lease.Receipt) == store.J(t) {
					return nil
				}
				return errors.New("private check receipt immutable")
			}
			if e := autoRecordPrivateTestReceipt(v, t); e != nil {
				return e
			}
			lease.Receipt = &t
			lease.StopConfirmed = true
			return nil
		}
	}
	return errors.New("private check attempt unavailable")
}

func autoPrivateIntegrationDetail(a *autoRecord, id string) (any, error) {
	if !autoHash256(id) || a.PrivateIntegration == nil {
		return nil, errors.New("private integration unavailable")
	}
	for _, run := range a.PrivateIntegration.Runs {
		for _, v := range run.Attempts {
			if v.ID == id {
				return map[string]any{"integration_id": v.ID, "root_id": v.RootID, "attempt": v.Number, "status": v.Status, "authority": v.Authority, "candidate": v.Candidate, "tests": v.Tests, "review": v.Review, "publication": v.Publication, "canonical_applied": false, "deployed": false}, nil
			}
		}
	}
	return nil, errors.New("private integration unavailable")
}

func autoPrivatePathsCovered(want, prior []string) bool {
	for _, p := range want {
		covered := false
		for _, old := range prior {
			if old == p || (strings.HasSuffix(old, "/**") && strings.HasPrefix(strings.TrimSuffix(p, "/**"), strings.TrimSuffix(old, "**"))) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func autoValidatePrivatePrior(a *autoRecord, pin *autoPrivateIntegrationPin) error {
	if pin.PriorAttemptID == "" {
		if pin.PriorBuilderJob != "" || pin.PriorBuilderSHA != "" || pin.PriorReviewJob != "" || pin.PriorReviewSHA != "" {
			return errors.New("partial prior integration evidence")
		}
		return nil
	}
	if a.PrivateIntegration == nil {
		return errors.New("prior integration ledger missing")
	}
	run := a.PrivateIntegration.Runs[pin.RootID]
	if run == nil {
		return errors.New("prior integration root missing")
	}
	for _, v := range run.Attempts {
		if v.ID != pin.PriorAttemptID {
			continue
		}
		if !autoPrivateAttemptTerminal(v.Status) {
			return errors.New("prior integration evidence is not terminal")
		}
		if pin.PriorBuilderJob != "" && (v.Candidate == nil || v.Candidate.BuilderJob != pin.PriorBuilderJob || v.Candidate.BuilderArchiveSHA != pin.PriorBuilderSHA) {
			return errors.New("prior candidate identity changed")
		}
		if pin.PriorReviewJob != "" && (v.Review == nil || v.Review.JobID != pin.PriorReviewJob || v.Review.ArchiveSHA != pin.PriorReviewSHA) {
			return errors.New("prior review identity changed")
		}
		return nil
	}
	return errors.New("prior integration attempt missing")
}
