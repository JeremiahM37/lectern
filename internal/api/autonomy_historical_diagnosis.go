package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type autoVerifiedDiagnosisEnvironment struct {
	DiagnosisTaskID int64              `json:"diagnosis_task_id"`
	OriginTaskID    int64              `json:"origin_task_id"`
	BuilderJob      string             `json:"builder_job"`
	ReviewerJob     string             `json:"reviewer_job"`
	Request         *autoPythonRequest `json:"request"`
	Receipt         *autoPythonReceipt `json:"receipt"`
	ReviewerReceipt *autoPythonReceipt `json:"reviewer_receipt"`
}

func autoHistoricalEligible(a *autoRecord, j *autoJob) bool {
	if j == nil || (j.Role != "builder" && j.Role != "planner") || j.DiagnosisRequirement != "" || j.DocumentationRoot != 0 || autoFindJob(a, j.TaskID) != j {
		return false
	}
	if j.Status == "done" {
		return true
	}
	if j.Status != "failed" {
		return false
	}
	states := append(append([]*autonomy.State{a.State}, a.DeferredRuns...), a.HeldRuns...)
	for _, state := range states {
		if state == nil || state.Phase == autonomy.Complete {
			continue
		}
		for _, id := range state.ActiveTaskIDs() {
			if id == j.TaskID {
				return false
			}
		}
	}
	return true
}

func autoHistoricalReservation(a *autoRecord, j *autoJob) string {
	return fmt.Sprintf("historical:%d", autoRepairRoot(a, j.TaskID))
}
func autoDiagnosisReservationKey(a *autoRecord, p autonomy.Proposal, j *autoJob) string {
	if autoHistoricalEligible(a, j) {
		return autoHistoricalReservation(a, j)
	}
	return autoDiagnosisID(p.DiagnoseRequirement, j.TaskID)
}
func autoStoredDiagnosisKey(d *autoRequirementDiagnosis) string {
	if d.EvidenceOnly {
		return fmt.Sprintf("historical:%d", d.OriginRootTaskID)
	}
	return autoDiagnosisID(d.Key, d.TargetTaskID)
}

func (s *Server) autoHistoricalReport(ctx context.Context, a *autoRecord, j *autoJob, archive string) ([]byte, error) {
	if a.HistoricalReportPending == nil {
		a.HistoricalReportPending = map[string]bool{}
	}
	a.HistoricalReportPending[j.ID] = true
	if err := s.saveAuto(a); err != nil {
		return nil, err
	}
	raw, err := s.runAutoCommand(ctx, "archive-report", "--job", j.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: archived report poll: %v", errAutoArtifactPending, err)
	}
	var receipt struct {
		State      string `json:"state"`
		ArchiveSHA string `json:"archive_sha256"`
		ReportSHA  string `json:"report_sha256"`
		Report     string `json:"report"`
		Reason     string `json:"reason"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return nil, errors.New("invalid archived report receipt")
	}
	if receipt.State == "exporting" || receipt.State == "waiting" {
		return nil, fmt.Errorf("%w: preserving exact historical report", errAutoArtifactPending)
	}
	if receipt.State == "unavailable" {
		delete(a.HistoricalReportPending, j.ID)
		return nil, fmt.Errorf("immutable archived report unavailable: %s; select other evidence rather than modifying historical work", receipt.Reason)
	}
	if receipt.State != "ready" || receipt.ArchiveSHA != archive || !autoHash256(receipt.ReportSHA) || autoSHA([]byte(receipt.Report)) != receipt.ReportSHA || len(receipt.Report) > 128<<10 {
		return nil, errors.New("archived report identity mismatch")
	}
	delete(a.HistoricalReportPending, j.ID)
	return []byte(receipt.Report), nil
}

// Selecting an old report creates a new observation. Its bytes and outcome are
// never rewritten, and no package or missing capability is inferred from prose.
func (s *Server) observeAutoHistoricalDiagnoses(ctx context.Context, a *autoRecord, items []autonomy.Proposal) error {
	for i := range items {
		p := &items[i]
		if p.DiagnoseTaskID == 0 {
			continue
		}
		if p.DiagnoseTaskID < 0 || p.ContinueTaskID != 0 || p.DocumentationTaskID != 0 {
			return errors.New("historical diagnosis cannot continue or complete its evidence source")
		}
		if p.RepairTaskID > 0 {
			d := autoSelectedDiagnosis(a, *p)
			if d == nil || !d.EvidenceOnly || d.OriginTaskID != p.DiagnoseTaskID {
				return errors.New("historical diagnostic correction changed origin")
			}
			continue
		}
		source := autoFindJob(a, p.DiagnoseTaskID)
		if !autoHistoricalEligible(a, source) || autoRepairRoot(a, source.TaskID) == 0 {
			return errors.New("diagnose_task_id requires a terminal archived original builder or planner report")
		}
		task, err := s.DB.Task(source.TaskID)
		if err != nil || task.ProjectID != p.ProjectID {
			return errors.New("historical diagnosis source project mismatch")
		}
		key := autoSHA([]byte(autoHistoricalReservation(a, source)))
		if p.DiagnoseRequirement != "" && p.DiagnoseRequirement != key {
			return errors.New("historical task and requirement selectors disagree")
		}
		p.DiagnoseRequirement = key
		if a.Requirements == nil {
			a.Requirements = map[string]*autoRequirement{}
		}
		entry := a.Requirements[key]
		if entry == nil {
			if len(a.Requirements) >= 512 {
				return errors.New("prerequisite ledger capacity reached")
			}
			entry = &autoRequirement{Key: key, Request: autonomy.Requirement{SchemaVersion: 1, Capability: "historical_observation", Condition: "report_backed_diagnosis", Evidence: []string{fmt.Sprintf("Selected archived task %d for a new investigation; its outcome is unchanged", source.TaskID)}}, State: "observed"}
		}
		archive, err := s.autoArchiveIdentity(ctx, source)
		if err != nil {
			return err
		}
		raw, err := s.autoHistoricalReport(ctx, a, source, archive)
		if err != nil {
			return err
		}
		found := false
		for _, o := range entry.Occurrences {
			if o.JobID == source.ID {
				if o.ArchiveSHA != archive || o.ReportSHA != autoSHA(raw) {
					return errors.New("historical source evidence changed")
				}
				found = true
			}
		}
		if !found {
			entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: source.TaskID, RootTaskID: autoRepairRoot(a, source.TaskID), ProjectID: task.ProjectID, JobID: source.ID, ArchiveSHA: archive, ReportSHA: autoSHA(raw), Evidence: append([]string(nil), entry.Request.Evidence...)})
		}
		entry.UpdatedAt = time.Now()
		a.Requirements[key] = entry
	}
	return nil
}

func autoHistoricalRequirementTarget(a *autoRecord, p autonomy.Proposal, r *autoRequirement) *autoJob {
	for i := len(r.Occurrences) - 1; i >= 0; i-- {
		o := r.Occurrences[i]
		if o.ProjectID != p.ProjectID || p.DiagnoseTaskID > 0 && o.TaskID != p.DiagnoseTaskID || !autoHash256(o.ReportSHA) || !autoHash256(o.ArchiveSHA) {
			continue
		}
		source := autoFindJob(a, o.TaskID)
		if !autoHistoricalEligible(a, source) || source.ID != o.JobID {
			continue
		}
		if d := a.RequirementDiagnoses[autoHistoricalReservation(a, source)]; d != nil && d.RootTaskID != 0 {
			continue
		}
		return source
	}
	return nil
}

func autoDiagnosisUsedRequirements(j *autoJob, requirements []autonomy.Requirement) bool {
	pins, imports, supported, err := autoDiagnosisRemedy(requirements)
	if err != nil || !supported || j == nil || j.PythonRequest == nil || j.PythonRecovery == nil || j.PythonRecovery.State != "verified" || j.PythonUsedBundle == "" || j.PythonUsedBundle != j.PythonRecovery.BundleKey || !autoPythonBound(j.PythonRequest, *j.PythonRecovery) {
		return false
	}
	available := map[string]bool{}
	for _, p := range j.PythonRequest.Requirements {
		available[p] = true
	}
	for _, p := range pins {
		if !available[p] {
			return false
		}
	}
	modules := map[string]bool{}
	for _, m := range j.PythonRequest.Imports {
		modules[m] = true
	}
	for _, m := range imports {
		if !modules[m] {
			return false
		}
	}
	return true
}
func autoHistoricalProvisioningGate(a *autoRecord, j *autoJob, outcome string, requirements []autonomy.Requirement) bool {
	d := a.RequirementDiagnoses[j.DiagnosisReservation]
	if d == nil || !d.EvidenceOnly || j.Role != "builder" || outcome != "ready_for_review" {
		return false
	}
	_, _, supported, err := autoDiagnosisRemedy(requirements)
	return err == nil && supported && !autoDiagnosisUsedRequirements(j, requirements)
}
func autoHistoricalVerifiedEnvironment(d *autoRequirementDiagnosis, builder, reviewer *autoJob, requirements []autonomy.Requirement) (*autoVerifiedDiagnosisEnvironment, error) {
	if !autoDiagnosisUsedRequirements(builder, requirements) || !autoDiagnosisUsedRequirements(reviewer, requirements) {
		return nil, errors.New("historical diagnosis requires both builder and independent reviewer to execute with the verified proposed environment; report an explicit blocked prerequisite if unavailable")
	}
	b, r := builder.PythonRecovery, reviewer.PythonRecovery
	if !autoHash256(b.InputKey) || !autoHash256(b.BundleKey) || !autoHash256(b.RuntimeDigest) || b.InputKey != r.InputKey || b.BundleKey != r.BundleKey || b.RuntimeDigest != r.RuntimeDigest || b.BrowserKey != r.BrowserKey {
		return nil, errors.New("historical diagnosis builder and reviewer used different environments")
	}
	var verified autoVerifiedDiagnosisEnvironment
	_ = json.Unmarshal([]byte(store.J(autoVerifiedDiagnosisEnvironment{DiagnosisTaskID: builder.TaskID, OriginTaskID: d.OriginTaskID, BuilderJob: builder.ID, ReviewerJob: reviewer.ID, Request: builder.PythonRequest, Receipt: b, ReviewerReceipt: r})), &verified)
	return &verified, nil
}
