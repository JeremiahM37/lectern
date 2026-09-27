package api

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

// A diagnosis is one audited investigation, not another attempt at the blocked
// implementation. Its result can authorize only an existing provisioner; it
// never supplies a verified receipt or changes the original acceptance/verdict.
type autoDiagnosisFailure struct {
	PreviousTargetJob string            `json:"previous_target_job"`
	OwnerJob          string            `json:"owner_job"`
	EvidenceJob       string            `json:"evidence_job"`
	ArchiveSHA        string            `json:"archive_sha256"`
	AdmissionSHA      string            `json:"admission_sha256"`
	Request           autoPythonRequest `json:"request"`
	Receipt           autoPythonReceipt `json:"receipt"`
	EvidenceSHA       string            `json:"evidence_sha256"`
}
type autoRequirementDiagnosis struct {
	OriginApproved      bool                              `json:"origin_approved"`
	OriginRejected      bool                              `json:"origin_rejected"`
	OriginReviewOutcome string                            `json:"origin_review_outcome,omitempty"`
	OriginStatus        string                            `json:"origin_status,omitempty"`
	OriginReportError   string                            `json:"origin_report_error,omitempty"`
	EvidenceOnly        bool                              `json:"evidence_only,omitempty"`
	OriginTaskID        int64                             `json:"origin_task_id,omitempty"`
	OriginRootTaskID    int64                             `json:"origin_root_task_id,omitempty"`
	OriginReport        string                            `json:"origin_report,omitempty"`
	OriginReportFormat  string                            `json:"origin_report_format,omitempty"`
	OriginReportSHA     string                            `json:"origin_report_sha256,omitempty"`
	OriginReviewJob     string                            `json:"origin_review_job,omitempty"`
	OriginReviewSHA     string                            `json:"origin_review_archive_sha256,omitempty"`
	VerifiedEnvironment *autoVerifiedDiagnosisEnvironment `json:"verified_environment,omitempty"`
	RepairJob           string                            `json:"repair_job,omitempty"`
	RepairSHA           string                            `json:"repair_archive_sha256,omitempty"`
	Failures            []autoDiagnosisFailure            `json:"failures,omitempty"`
	AttemptedRequest    *autoPythonRequest                `json:"attempted_request,omitempty"`
	Key                 string                            `json:"key"`
	TargetTaskID        int64                             `json:"target_task_id"`
	TargetEvidenceJob   string                            `json:"target_evidence_job"`
	Reviews             []autoDiagnosisReview             `json:"reviews,omitempty"`
	TargetJob           string                            `json:"target_job"`
	TargetArchiveSHA    string                            `json:"target_archive_sha256"`
	TargetAdmissionSHA  string                            `json:"target_admission_sha256"`
	ProjectID           int64                             `json:"project_id"`
	Requirement         autonomy.Requirement              `json:"requirement"`
	Proposal            autonomy.Proposal                 `json:"proposal"`
	Cycle               int                               `json:"cycle"`
	Revision            int                               `json:"revision"`
	Item                int                               `json:"item"`
	RootTaskID          int64                             `json:"root_task_id,omitempty"`
	TaskID              int64                             `json:"task_id,omitempty"`
	ReviewTaskID        int64                             `json:"review_task_id,omitempty"`
	ReviewJob           string                            `json:"review_job,omitempty"`
	ReviewSHA           string                            `json:"review_archive_sha256,omitempty"`
	ReviewReportSHA     string                            `json:"review_report_sha256,omitempty"`
	Outcome             string                            `json:"outcome"`
	Reason              string                            `json:"reason,omitempty"`
	Remedy              *autoPythonRequest                `json:"remedy,omitempty"`
}

type autoDiagnosisReview struct {
	TaskID     int64             `json:"task_id"`
	Job        string            `json:"job"`
	ArchiveSHA string            `json:"archive_sha256"`
	ReportSHA  string            `json:"report_sha256"`
	Outcome    string            `json:"outcome"`
	Reason     string            `json:"reason"`
	Proposal   autonomy.Proposal `json:"proposal"`
}

// Failed provisioning refutes the proposed remedy, never the historical
// review. It reopens only an exact, not-yet-verified attempted request.
func autoRefuteDiagnosisRemedy(a *autoRecord, j *autoJob) {
	if !j.PythonNeedsChange || !autoDiagnosisPreflightUnavailable(j) {
		return
	}
	for _, key := range j.RequirementIDs {
		d := a.RequirementDiagnoses[autoDiagnosisID(key, j.TaskID)]
		if d == nil || d.Outcome != "completed" || d.AttemptedRequest == nil || store.J(d.AttemptedRequest) != store.J(j.PythonRequest) {
			continue
		}
		failure := autoDiagnosisFailure{PreviousTargetJob: d.TargetJob, OwnerJob: j.ID, EvidenceJob: j.PythonRequest.SourceJob, ArchiveSHA: j.PythonRequest.SourceSHA, AdmissionSHA: autoDiagnosisAdmission(j)}
		_ = json.Unmarshal([]byte(store.J(j.PythonRequest)), &failure.Request)
		_ = json.Unmarshal([]byte(store.J(j.PythonRecovery)), &failure.Receipt)
		failure.EvidenceSHA = autoSHA([]byte(store.J(failure)))
		d.Failures = append(d.Failures, failure)
		d.TargetJob = j.ID
		d.TargetEvidenceJob = failure.EvidenceJob
		d.TargetArchiveSHA = failure.ArchiveSHA
		d.TargetAdmissionSHA = failure.AdmissionSHA
		d.Outcome = "remedy_failed"
		d.Reason = "Trusted provisioner could not verify the independently reviewed remedy: " + j.PythonRecovery.Reason
	}
}
func autoDiagnosisRefutedCheckpoint(a *autoRecord, taskID int64) (*autoRequirementDiagnosis, bool) {
	j := autoFindJob(a, taskID)
	if j == nil || j.Role != "builder" || j.Status != "done" || j.DiagnosisReservation == "" {
		return nil, false
	}
	d := a.RequirementDiagnoses[j.DiagnosisReservation]
	if d == nil || d.TaskID != taskID || d.Outcome != "remedy_failed" || len(d.Failures) == 0 || autoDiagnosisBoundTarget(a, d) == nil {
		return nil, false
	}
	f := d.Failures[len(d.Failures)-1]
	original := f.EvidenceSHA
	f.EvidenceSHA = ""
	if original != autoSHA([]byte(store.J(f))) || f.OwnerJob != d.TargetJob || f.EvidenceJob != d.TargetEvidenceJob || f.ArchiveSHA != d.TargetArchiveSHA {
		return nil, false
	}
	return d, true
}
func autoRepairEvidence(a *autoRecord, taskID int64) (int64, string, bool) {
	if d, ok := autoDiagnosisRefutedCheckpoint(a, taskID); ok {
		return d.ReviewTaskID, d.Reason, true
	}
	return autoRejectedCheckpoint(a, taskID)
}

func autoDiagnosisEvidenceOwner(o autoRequirementOccurrence) string {
	if o.EvidenceJob != "" {
		return o.EvidenceJob
	}
	return o.JobID
}
func autoDiagnosisPreflightUnavailable(j *autoJob) bool {
	return j.PythonRequest != nil && j.PythonRecovery != nil && j.PythonRecovery.State == "unavailable" && j.PythonRecovery.Unsupported && autoPythonBound(j.PythonRequest, *j.PythonRecovery)
}
func autoDiagnosisOccurrence(r *autoRequirement, j *autoJob) (autoRequirementOccurrence, bool) {
	for i := len(r.Occurrences) - 1; i >= 0; i-- {
		o := r.Occurrences[i]
		if o.TaskID == j.TaskID && o.JobID == j.ID && autoHash256(o.ArchiveSHA) {
			if o.EvidenceJob != "" && o.EvidenceJob != j.ID && (!autoDiagnosisPreflightUnavailable(j) || o.EvidenceJob != j.PythonRequest.SourceJob || o.ArchiveSHA != j.PythonRequest.SourceSHA) {
				continue
			}
			return o, true
		}
	}
	// A bound unavailable preflight receipt could never pass the launch gate.
	// Only this case may use a previous completed producer's evidence.
	if autoDiagnosisPreflightUnavailable(j) {
		for i := len(r.Occurrences) - 1; i >= 0; i-- {
			o := r.Occurrences[i]
			if o.TaskID == j.TaskID && o.JobID == j.PythonRequest.SourceJob && o.ArchiveSHA == j.PythonRequest.SourceSHA && autoHash256(o.ArchiveSHA) {
				return o, true
			}
		}
	}
	return autoRequirementOccurrence{}, false
}
func autoDiagnosisAdmission(j *autoJob) string {
	if j.Admission == nil && j.PythonRequest != nil && j.PythonRequest.AdmissionSHA != "" {
		return j.PythonRequest.AdmissionSHA
	}
	return autoSHA([]byte(store.J(j.Admission)))
}
func autoDiagnosisBoundTarget(a *autoRecord, d *autoRequirementDiagnosis) *autoJob {
	j := autoFindJob(a, d.TargetTaskID)
	if d.EvidenceOnly {
		if !autoHistoricalEligible(a, j) || j.ID != d.TargetJob || autoDiagnosisAdmission(j) != d.TargetAdmissionSHA {
			return nil
		}
		return j
	}
	if j == nil || j.ID != d.TargetJob || j.Status != "deferred" || !j.RequirementHold || autoDiagnosisAdmission(j) != d.TargetAdmissionSHA {
		return nil
	}
	return j
}

func autoContainsRequirement(j *autoJob, key string) bool {
	for _, id := range j.RequirementIDs {
		if id == key {
			return true
		}
	}
	return false
}

func autoDiagnosisID(key string, taskID int64) string {
	return autoSHA([]byte(fmt.Sprintf("%s:%d", key, taskID)))
}
func autoRequirementDiagnoses(a *autoRecord, key string) []*autoRequirementDiagnosis {
	out := []*autoRequirementDiagnosis{}
	for _, d := range a.RequirementDiagnoses {
		if d.Key == key {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TargetTaskID < out[j].TargetTaskID })
	return out
}
func autoSelectedDiagnosis(a *autoRecord, p autonomy.Proposal) *autoRequirementDiagnosis {
	if p.RepairTaskID > 0 {
		if j := autoFindJob(a, p.RepairTaskID); j != nil {
			return a.RequirementDiagnoses[j.DiagnosisReservation]
		}
	}
	// An audited current binding remains exact even if another occurrence arrives.
	for _, d := range a.RequirementDiagnoses {
		if d.Key == p.DiagnoseRequirement && d.ProjectID == p.ProjectID && store.J(d.Proposal) == store.J(p) && a.State != nil && d.Cycle == a.State.Cycle && d.Revision == a.State.Revision {
			return d
		}
	}
	if r := a.Requirements[p.DiagnoseRequirement]; r != nil {
		for i := len(r.Occurrences) - 1; i >= 0; i-- {
			o := r.Occurrences[i]
			if o.ProjectID == p.ProjectID {
				if source := autoFindJob(a, o.TaskID); source != nil && autoHistoricalEligible(a, source) {
					if d := a.RequirementDiagnoses[autoHistoricalReservation(a, source)]; d != nil && d.Key == p.DiagnoseRequirement {
						return d
					}
				}
				if d := a.RequirementDiagnoses[autoDiagnosisID(p.DiagnoseRequirement, o.TaskID)]; d != nil {
					return d
				}
			}
		}
	}
	return nil
}
func autoDiagnosisTarget(a *autoRecord, p autonomy.Proposal) (*autoJob, *autoRequirement, error) {
	if !autoHash256(p.DiagnoseRequirement) || p.ContinueTaskID != 0 || p.DocumentationTaskID != 0 {
		return nil, nil, errors.New("diagnosis requires an unresolved requirement key and cannot continue, repair or complete implementation")
	}
	r := a.Requirements[p.DiagnoseRequirement]
	if r == nil {
		return nil, nil, errors.New("diagnosis requirement is missing")
	}
	if d := autoSelectedDiagnosis(a, p); p.RepairTaskID > 0 && d != nil && d.RootTaskID != 0 {
		source := autoFindJob(a, p.RepairTaskID)
		if source == nil || p.DiagnoseRequirement != d.Key || source.DiagnosisRequirement != d.Key || autoRepairRoot(a, source.TaskID) != d.RootTaskID || (d.Outcome != "rejected" && d.Outcome != "remedy_failed") || autoDiagnosisBoundTarget(a, d) == nil {
			return nil, nil, errors.New("diagnosis requires a rejected same-root repair and unchanged retained target")
		}
		if _, _, ok := autoRepairEvidence(a, p.RepairTaskID); !ok {
			return nil, nil, errors.New("diagnosis repair requires explicit rejection")
		}
		return autoDiagnosisBoundTarget(a, d), r, nil
	}
	if p.RepairTaskID != 0 {
		return nil, nil, errors.New("initial diagnosis cannot repair another task")
	}
	for i := len(r.Occurrences) - 1; i >= 0; i-- {
		o := r.Occurrences[i]
		j := autoFindJob(a, o.TaskID)
		if o.ProjectID == p.ProjectID && j != nil && j.Status == "deferred" && j.RequirementHold && autoContainsRequirement(j, p.DiagnoseRequirement) {
			if occurrence, ok := autoDiagnosisOccurrence(r, j); !ok || occurrence.ProjectID != p.ProjectID {
				continue
			}
			if d := a.RequirementDiagnoses[autoDiagnosisReservationKey(a, p, j)]; d != nil && d.RootTaskID != 0 {
				continue
			}
			return j, r, nil
		}
	}
	if source := autoHistoricalRequirementTarget(a, p, r); source != nil {
		return source, r, nil
	}
	return nil, nil, errors.New("diagnosis requires retained blocked work or an exact archived report in the selected project")
}

func autoValidateRequirementDiagnoses(a *autoRecord, items []autonomy.Proposal) error {
	seen := map[string]bool{}
	for _, p := range items {
		for _, id := range []int64{p.RepairTaskID, p.ContinueTaskID, p.DocumentationTaskID} {
			if source := autoFindJob(a, id); source != nil && source.DiagnosisRequirement != "" && (id != p.RepairTaskID || p.DiagnoseRequirement != source.DiagnosisRequirement) {
				return errors.New("diagnosis identity cannot be stripped or promoted to implementation")
			}
		}
		if p.DiagnoseRequirement == "" {
			continue
		}
		if seen[p.DiagnoseRequirement] {
			return errors.New("duplicate requirement diagnosis in one plan")
		}
		seen[p.DiagnoseRequirement] = true
		if d := autoSelectedDiagnosis(a, p); d != nil && d.RootTaskID != 0 && a.State != nil && a.State.Phase == autonomy.Build && d.Cycle == a.State.Cycle && d.Revision == a.State.Revision && d.Item == a.State.Item && store.J(d.Proposal) == store.J(p) && d.Outcome == "reserved" {
			continue // Existing admitted decision step, never a new allowance.
		}
		if _, _, err := autoDiagnosisTarget(a, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) pinAutoRequirementDiagnoses(ctx context.Context, a *autoRecord, items []autonomy.Proposal) error {
	for _, p := range items {
		if p.DiagnoseRequirement == "" {
			continue
		}
		j, r, err := autoDiagnosisTarget(a, p)
		if err != nil {
			return err
		}
		occurrence, ok := autoDiagnosisOccurrence(r, j)
		if !ok {
			return errors.New("diagnosis source occurrence missing")
		}
		var evidence *autoJob
		for _, candidate := range a.Jobs {
			if candidate.ID == autoDiagnosisEvidenceOwner(occurrence) {
				evidence = candidate
				break
			}
		}
		if evidence == nil {
			return errors.New("diagnosis evidence owner missing")
		}
		archive, err := s.autoArchiveIdentity(ctx, evidence)
		if err != nil {
			return err
		}
		matched := false
		for _, o := range r.Occurrences {
			if o.TaskID == j.TaskID && o.ProjectID == p.ProjectID && autoDiagnosisEvidenceOwner(o) == evidence.ID && o.ArchiveSHA == archive && archive == occurrence.ArchiveSHA {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("diagnosis source differs from recorded blocked evidence")
		}
		if a.RequirementDiagnoses == nil {
			a.RequirementDiagnoses = map[string]*autoRequirementDiagnosis{}
		}
		// Copy rather than alias model-supplied slices into the audit binding.
		var frozen autonomy.Requirement
		_ = json.Unmarshal([]byte(store.J(r.Request)), &frozen)
		var proposal autonomy.Proposal
		_ = json.Unmarshal([]byte(store.J(p)), &proposal)
		if existing := a.RequirementDiagnoses[autoDiagnosisReservationKey(a, p, j)]; existing != nil && existing.RootTaskID != 0 {
			prior := autoFindJob(a, p.RepairTaskID)
			if prior == nil {
				return errors.New("diagnosis repair source missing")
			}
			repairSHA, err := s.autoArchiveIdentity(ctx, prior)
			if err != nil {
				return err
			}
			existing.RepairJob = prior.ID
			existing.RepairSHA = repairSHA
			existing.Proposal = proposal
		} else {
			d := &autoRequirementDiagnosis{Cycle: a.State.Cycle, Revision: a.State.Revision, Key: p.DiagnoseRequirement, TargetTaskID: j.TaskID, TargetJob: j.ID, TargetEvidenceJob: evidence.ID, TargetArchiveSHA: archive, TargetAdmissionSHA: autoDiagnosisAdmission(j), ProjectID: p.ProjectID, Requirement: frozen, Proposal: proposal, Outcome: "pinned"}
			if autoHistoricalEligible(a, j) {
				raw, err := s.autoHistoricalReport(ctx, a, j, archive)
				if err != nil {
					return err
				}
				if autoSHA(raw) != occurrence.ReportSHA {
					return errors.New("historical archived report differs from observed evidence")
				}
				d.EvidenceOnly = true
				d.OriginApproved = j.Approved
				d.OriginRejected = j.Rejected
				d.OriginReviewOutcome = j.ReviewOutcome
				d.OriginStatus = j.Status
				d.OriginReportError = j.ReportError
				d.OriginTaskID = j.TaskID
				d.OriginRootTaskID = autoRepairRoot(a, j.TaskID)
				d.OriginReport = string(raw)
				d.OriginReportFormat = "json"
				if !json.Valid(raw) {
					d.OriginReportFormat = "malformed_text"
				}
				d.OriginReportSHA = autoSHA(raw)
				if j.ReviewTaskID > 0 {
					review := autoFindJob(a, j.ReviewTaskID)
					if review == nil || review.Status != "done" {
						return errors.New("historical review evidence unavailable")
					}
					sha, err := s.autoArchiveIdentity(ctx, review)
					if err != nil {
						return err
					}
					d.OriginReviewJob = review.ID
					d.OriginReviewSHA = sha
				}
			}
			a.RequirementDiagnoses[autoDiagnosisReservationKey(a, p, j)] = d
		}
	}
	return nil
}

func (s *Server) reserveAutoRequirementDiagnosis(a *autoRecord, j *autoJob) error {
	if j.Role != "builder" || a.State == nil || a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return nil
	}
	p := a.State.Items[a.State.Item]
	if p.DiagnoseRequirement == "" {
		return nil
	}
	d := autoSelectedDiagnosis(a, p)
	if !autoRepairAudited(a) || d == nil || store.J(d.Proposal) != store.J(p) {
		return errors.New("diagnosis requires pinned evidence and both current plan audits")
	}
	if autoDiagnosisBoundTarget(a, d) == nil {
		return errors.New("diagnosis target changed before admission")
	}
	if d.RootTaskID != 0 && (d.Outcome == "rejected" || d.Outcome == "remedy_failed") && p.RepairTaskID > 0 {
		if _, err := s.autoRepairContinuation(a, p.ProjectID, p.RepairTaskID); err != nil {
			return err
		}
		source := autoFindJob(a, p.RepairTaskID)
		if source.DiagnosisRequirement != d.Key || autoRepairRoot(a, source.TaskID) != d.RootTaskID {
			return errors.New("diagnosis repair lineage mismatch")
		}
		d.Cycle, d.Revision, d.Item = a.State.Cycle, a.State.Revision, a.State.Item
	} else if d.RootTaskID != 0 {
		if d.Cycle != a.State.Cycle || d.Revision != a.State.Revision || d.Item != a.State.Item || d.Outcome != "reserved" {
			return errors.New("diagnosis allowance already consumed")
		}
		if d.TaskID != j.TaskID {
			if a.State.Step == 0 {
				return errors.New("diagnosis already has an admitted task")
			}
			previous := autoFindJob(a, d.TaskID)
			if previous == nil || previous.Status != "done" {
				return errors.New("diagnosis decision checkpoint incomplete")
			}
		}
	} else {
		target := autoDiagnosisBoundTarget(a, d)
		if target == nil {
			return errors.New("diagnosis target changed before admission")
		}
		d.RootTaskID = j.TaskID
		d.Cycle = a.State.Cycle
		d.Revision = a.State.Revision
		d.Item = a.State.Item
	}
	j.DiagnosisRequirement = d.Key
	j.DiagnosisReservation = autoStoredDiagnosisKey(d)
	d.TaskID = j.TaskID
	d.Outcome = "reserved"
	return nil
}

func autoDiagnosisCopies(a *autoRecord, role string) ([]autoDocumentationCopy, error) {
	if role != "auditor_a" && role != "auditor_b" && !(role == "builder" && a.State.Step == 0) {
		return nil, nil
	}
	var copies []autoDocumentationCopy
	seen := map[string]bool{}
	for i, p := range a.State.Items {
		if p.DiagnoseRequirement == "" || role == "builder" && i != a.State.Item {
			continue
		}
		d := autoSelectedDiagnosis(a, p)
		if d == nil {
			return nil, errors.New("diagnosis audit evidence pin missing")
		}
		if role == "builder" && p.RepairTaskID > 0 {
			continue
		}
		if d.EvidenceOnly && d.OriginReviewJob != "" && !seen[d.OriginReviewJob] {
			copies = append(copies, autoDocumentationCopy{Command: "copy-archive-review", SourceJob: d.OriginReviewJob, SHA: d.OriginReviewSHA})
			seen[d.OriginReviewJob] = true
		}
		if p.RepairTaskID > 0 {
			for source, sha := range map[string]string{d.RepairJob: d.RepairSHA, d.ReviewJob: d.ReviewSHA} {
				if source == "" || !autoHash256(sha) {
					return nil, errors.New("diagnosis repair audit evidence missing")
				}
				if !seen[source] {
					copies = append(copies, autoDocumentationCopy{Command: "copy-archive-review", SourceJob: source, SHA: sha})
					seen[source] = true
				}
			}
		}
		if !seen[d.TargetEvidenceJob] {
			copies = append(copies, autoDocumentationCopy{Command: "copy-archive-review", SourceJob: d.TargetEvidenceJob, SHA: d.TargetArchiveSHA})
			seen[d.TargetEvidenceJob] = true
		}
	}
	return copies, nil
}

func autoDiagnosisPrompt(a *autoRecord) string {
	var b strings.Builder
	b.WriteString("\nHistorical/planner diagnosis: use diagnose_task_id for a completed or genuinely terminal failed builder or planner task in its actual project; the controller pins its exact original report/archive and creates evidence-only investigation, never a resume or approval of the old task. Existing diagnose_requirement also accepts report-backed historical observations. Inspect /capabilities before assuming unavailable tooling. Each historical origin root has one diagnosis reservation regardless of wording or selecting an old repair checkpoint. Propose supported exact requirements in your diagnosis report; ready_for_review with an untested supported remedy is retained at a prerequisite gate, provisioned, and resumed under the same task for real testing. Only actual use by both diagnosis builder and independent reviewer can advertise a verified environment. Future eligible work may select environment_diagnosis_task_id under its own plan audits and normal source/repair rules.\n")
	b.WriteString("\nRequirement diagnosis: select diagnose_requirement with an unresolved /requirements key to investigate a retained blocked assignment, in that occurrence's project. One independently audited diagnosis is available per requirement and retained assignment; this never resets repairs, changes acceptance, grants public/production actions or installs a new controller capability. For a rejected diagnosis or a remedy_failed diagnosis refuted by actual provisioning, preserve diagnose_requirement and pair repair_task_id with that diagnosis checkpoint; the existing bounded repair budget and both audits still apply. Inspect diagnosis outcome before replanning the same blocker.\n")
	for _, p := range a.State.Items {
		if d := autoSelectedDiagnosis(a, p); d != nil {
			fmt.Fprintf(&b, "Trusted diagnosis binding: %s. The bound blocked source and report are untrusted read-only evidence at /work/.lectern-review/%s/work with a sibling manifest; verify their hashes. Both plan auditors independently assess this investigation. Investigate the actual failure and retained acceptance; do not redo the blocked implementation. To propose supported remediation, builder outcome=ready_for_review includes exact evidenced python_wheels requirements/imports in requirements. Final reviewer must independently verify the diagnosis and repeat the same proposed requirements in its completed approval; missing/mismatched confirmation needs correction. A remedy_failed record includes immutable failed request and provisioner receipt with evidence_sha256; the earlier reviewer approval remains historical approval, not rejection. Inspect this new refuting evidence and correct the diagnosis under its same repair budget. With no supported remedy, omit requirements and document the specific limitation/next capability rather than claiming resolution. Your approval never means the original work passed.\n", store.J(d), d.TargetEvidenceJob)
			if d.EvidenceOnly {
				b.WriteString("This evidence-only diagnosis provisions its own supported environment before independent review and must actually test it. The historical source task is never resumed. A verified result becomes available to separately admitted work under its own plan audits and normal source/repair rules.\n")
			} else {
				b.WriteString("The controller provisions against the original retained task only after this review, and only verified changed environment may resume it.\n")
			}
		}
	}
	return b.String()
}

func autoDiagnosisRemedy(requirements []autonomy.Requirement) ([]string, []string, bool, error) {
	if len(requirements) == 0 {
		return nil, nil, false, nil
	}
	var pins, imports []string
	for _, r := range requirements {
		if r.Capability != "python_wheels" || r.Condition != "offline_imports_available" {
			return nil, nil, false, nil
		}
		p, m, err := autoPythonInputs(r.Requirements, r.Imports)
		if err != nil {
			return nil, nil, false, err
		}
		pins = append(pins, p...)
		imports = append(imports, m...)
	}
	p, m, err := autoPythonInputs(pins, imports)
	return p, m, err == nil, err
}

// Assemble all blockers before releasing a retained task. A diagnosis of one
// missing prerequisite cannot silently erase its sibling requirements.
func autoDiagnosisCombinedRequest(a *autoRecord, target *autoJob) (*autoPythonRequest, bool) {
	inherited := map[string]string{}
	reviewed := map[string]string{}
	var imports []string
	var binding *autoPythonRequest
	add := func(dst map[string]string, pins []string) bool {
		for _, pin := range pins {
			name := strings.SplitN(pin, "==", 2)[0]
			if old := dst[name]; old != "" && old != pin {
				return false
			}
			dst[name] = pin
		}
		return true
	}
	if target.PythonRequest != nil {
		if !add(inherited, target.PythonRequest.Requirements) {
			return nil, false
		}
		imports = append(imports, target.PythonRequest.Imports...)
	}
	for _, key := range target.RequirementIDs {
		if d := a.RequirementDiagnoses[autoDiagnosisID(key, target.TaskID)]; d != nil && d.TargetJob == target.ID && d.Outcome == "completed" && d.Remedy != nil {
			if !add(reviewed, d.Remedy.Requirements) {
				return nil, false
			}
			imports = append(imports, d.Remedy.Imports...)
			binding = d.Remedy
			continue
		}
		r := a.Requirements[key]
		if r == nil {
			return nil, false
		}
		pins, modules, ok, err := autoDiagnosisRemedy([]autonomy.Requirement{r.Request})
		if err != nil || !ok {
			return nil, false
		}
		if !add(inherited, pins) {
			return nil, false
		}
		imports = append(imports, modules...)
	}
	if binding == nil {
		return nil, false
	}
	// Independently reviewed exact replacements supersede only the same
	// distribution. Old request/receipt and the reviewed proposal remain durable.
	for name, pin := range reviewed {
		inherited[name] = pin
	}
	pins := []string{}
	for _, pin := range inherited {
		pins = append(pins, pin)
	}
	p, m, err := autoPythonInputs(pins, imports)
	if err != nil {
		return nil, false
	}
	out := *binding
	out.Requirements = p
	out.Imports = m
	return &out, true
}

func (s *Server) finishAutoRequirementDiagnosis(ctx context.Context, a *autoRecord, reviewer *autoJob, report []byte) error {
	if reviewer.Role != "reviewer" || a.State == nil || a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return nil
	}
	p := a.State.Items[a.State.Item]
	if p.DiagnoseRequirement == "" {
		return nil
	}
	d := autoSelectedDiagnosis(a, p)
	if d == nil || d.RootTaskID == 0 {
		return errors.New("diagnosis reservation missing at review")
	}
	builder := autoFindJob(a, d.TaskID)
	if builder == nil || builder.Status != "done" {
		return errors.New("diagnosis builder unavailable")
	}
	var verdict autonomy.Verdict
	var build autonomy.BuildReport
	if json.Unmarshal(report, &verdict) != nil || json.Unmarshal(a.State.Reports[builder.TaskID], &build) != nil {
		return errors.New("diagnosis report unavailable")
	}
	bpins, bimports, supported, err := autoDiagnosisRemedy(build.Requirements)
	if err != nil {
		return &autoReportError{fmt.Errorf("diagnosis proposed invalid supported remedy: %w", err)}
	}
	if verdict.AcceptsWork() && supported {
		rpins, rimports, rok, rerr := autoDiagnosisRemedy(verdict.Requirements)
		if rerr != nil || !rok || store.J(bpins) != store.J(rpins) || store.J(bimports) != store.J(rimports) {
			return &autoReportError{errors.New("diagnosis approval must explicitly repeat the builder's independently verified Python requirements and imports")}
		}
	}
	var verifiedEnvironment *autoVerifiedDiagnosisEnvironment
	if d.EvidenceOnly && verdict.AcceptsWork() && supported {
		verifiedEnvironment, err = autoHistoricalVerifiedEnvironment(d, builder, reviewer, build.Requirements)
		if err != nil {
			return &autoReportError{err}
		}
	}
	archive, err := s.autoArchiveIdentity(ctx, reviewer)
	if err != nil {
		return err
	}
	outcome := "rejected"
	if verdict.AcceptsWork() {
		outcome = "completed"
	}
	receipt := autoDiagnosisReview{TaskID: reviewer.TaskID, Job: reviewer.ID, ArchiveSHA: archive, ReportSHA: autoSHA(report), Outcome: outcome, Reason: verdict.Reason, Proposal: d.Proposal}
	if len(d.Reviews) == 0 || d.Reviews[len(d.Reviews)-1].Job != reviewer.ID {
		d.Reviews = append(d.Reviews, receipt)
	} else if store.J(d.Reviews[len(d.Reviews)-1]) != store.J(receipt) {
		return errors.New("diagnosis review receipt changed")
	}
	d.ReviewTaskID = reviewer.TaskID
	d.ReviewJob = reviewer.ID
	d.ReviewSHA = archive
	d.ReviewReportSHA = autoSHA(report)
	d.Reason = verdict.Reason
	entry := a.Requirements[d.Key]
	if entry == nil {
		return errors.New("diagnosis requirement disappeared")
	}
	entry.UpdatedAt = time.Now()
	if !verdict.AcceptsWork() {
		d.Outcome = "rejected"
		entry.State = "diagnosis_rejected"
		entry.Reason = verdict.Reason
		return nil
	}
	d.Outcome = "completed"
	if !supported {
		entry.State = "diagnosed_unavailable"
		entry.Reason = verdict.Reason
		return nil
	}
	if d.EvidenceOnly {
		d.VerifiedEnvironment = verifiedEnvironment
		entry.State = "verified_environment"
		entry.Reason = "New diagnosis and independent reviewer tested the same verified environment; historical outcome unchanged"
		return nil
	}
	d.Remedy = &autoPythonRequest{SchemaVersion: 1, Kind: "python_wheels", Requirements: bpins, Imports: bimports, SourceJob: d.TargetEvidenceJob, SourceSHA: d.TargetArchiveSHA, AdmissionSHA: d.TargetAdmissionSHA}
	target := autoDiagnosisBoundTarget(a, d)
	if target == nil {
		entry.State = "diagnosed"
		entry.Reason = "Diagnosis completed; its original held assignment is no longer current"
		return nil
	}
	combined, ready := autoDiagnosisCombinedRequest(a, target)
	if !ready {
		entry.State = "diagnosed"
		entry.Reason = "Supported remedy independently reviewed; other retained requirements remain unresolved"
		return nil
	}
	// A prior verified environment remains recorded for changed-condition checks.
	for _, key := range target.RequirementIDs {
		if prior := a.RequirementDiagnoses[autoDiagnosisID(key, target.TaskID)]; prior != nil && prior.TargetJob == target.ID && prior.Outcome == "completed" && prior.Remedy != nil {
			var frozen autoPythonRequest
			_ = json.Unmarshal([]byte(store.J(combined)), &frozen)
			prior.AttemptedRequest = &frozen
		}
	}
	target.PendingPythonRequest = combined
	target.RequirementHold = false
	entry.State = "pending"
	entry.Reason = "Independent diagnosis approved a supported request; actual provisioner verification remains required"
	return nil
}
