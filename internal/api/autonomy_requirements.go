package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type autoPythonRequest struct {
	ExpectedInputKey  string   `json:"expected_input_key,omitempty"`
	ExpectedBundleKey string   `json:"expected_bundle_key,omitempty"`
	SchemaVersion     int      `json:"schema_version"`
	Kind              string   `json:"kind"`
	Requirements      []string `json:"requirements"`
	Imports           []string `json:"imports"`
	SourceJob         string   `json:"source_job"`
	SourceSHA         string   `json:"source_archive_sha256"`
	AdmissionSHA      string   `json:"admission_sha256"`
}
type autoPythonReceipt struct {
	BrowserKey    string `json:"browser_key,omitempty"`
	Diagnostic    string `json:"diagnostic,omitempty"`
	Unsupported   bool   `json:"unsupported,omitempty"`
	State         string `json:"state"`
	Capability    string `json:"capability"`
	InputKey      string `json:"input_key,omitempty"`
	BundleKey     string `json:"bundle_key,omitempty"`
	RuntimeDigest string `json:"runtime_digest,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SourceJob     string `json:"source_job,omitempty"`
	SourceSHA     string `json:"source_archive_sha256,omitempty"`
	AdmissionSHA  string `json:"admission_sha256,omitempty"`
}
type autoRequirementOccurrence struct {
	EvidenceJob string   `json:"evidence_job,omitempty"`
	TaskID      int64    `json:"task_id"`
	RootTaskID  int64    `json:"root_task_id"`
	ProjectID   int64    `json:"project_id"`
	JobID       string   `json:"job_id"`
	ArchiveSHA  string   `json:"archive_sha256"`
	ReportSHA   string   `json:"report_sha256"`
	Evidence    []string `json:"evidence"`
}
type autoRequirement struct {
	Environments map[string]*autoPythonReceipt `json:"environments,omitempty"`
	Key          string                        `json:"key"`
	Request      autonomy.Requirement          `json:"request"`
	State        string                        `json:"state"`
	Reason       string                        `json:"reason,omitempty"`
	RecoveryJob  string                        `json:"recovery_job,omitempty"`
	Receipt      *autoPythonReceipt            `json:"receipt,omitempty"`
	Occurrences  []autoRequirementOccurrence   `json:"occurrences"`
	UpdatedAt    time.Time                     `json:"updated_at"`
}

var autoPythonPin = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9._-]*)==([a-zA-Z0-9][a-zA-Z0-9.!+_-]*)$`)
var autoPythonImport = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
var autoPythonNameSeparators = regexp.MustCompile(`[-_.]+`)

func autoPythonInputs(requirements, imports []string) ([]string, []string, error) {
	if len(requirements) == 0 || len(requirements) > 32 || len(imports) == 0 || len(imports) > 32 {
		return nil, nil, errors.New("python_wheels requires 1–32 exact distribution pins and import names")
	}
	pins := map[string]string{}
	for _, raw := range requirements {
		m := autoPythonPin.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			return nil, nil, errors.New("supported Python inputs are exact name==version pins, without URLs, paths, flags or builds")
		}
		name := autoPythonNameSeparators.ReplaceAllString(strings.ToLower(m[1]), "-")
		if !autoPythonName.MatchString(name) || !autoPythonVersion.MatchString(m[2]) {
			return nil, nil, errors.New("Python distribution name/version exceeds registry protocol limits")
		}
		if old, ok := pins[name]; ok && old != m[2] {
			return nil, nil, fmt.Errorf("conflicting Python versions for %s", name)
		}
		pins[name] = m[2]
	}
	out := make([]string, 0, len(pins))
	for name, v := range pins {
		out = append(out, name+"=="+v)
	}
	sort.Strings(out)
	seen := map[string]bool{}
	mods := []string{}
	for _, name := range imports {
		if len(name) > 128 || !autoPythonImport.MatchString(name) {
			return nil, nil, errors.New("invalid Python import verification name")
		}
		if !seen[name] {
			mods = append(mods, name)
			seen[name] = true
		}
	}
	sort.Strings(mods)
	if len(store.J(out))+len(store.J(mods)) > 12<<10 {
		return nil, nil, errors.New("Python requirement envelope exceeds bounded provisioner input")
	}
	return out, mods, nil
}
func autoPythonSemanticKey(r *autoPythonRequest) string {
	return autoSHA([]byte(store.J(struct {
		Kind                  string
		Version               int
		Requirements, Imports []string
	}{r.Kind, r.SchemaVersion, r.Requirements, r.Imports})))
}
func autoRequirementKey(r autonomy.Requirement) string {
	r.Evidence = nil
	if pins, imports, err := autoPythonInputs(r.Requirements, r.Imports); err == nil && r.Capability == "python_wheels" {
		r.Requirements = pins
		r.Imports = imports
	}
	return autoSHA([]byte(store.J(r)))
}

// Only a validated, explicit inability to assess/complete work is recoverable.
// Ordinary negative reviews remain substantive verdicts with their usual caps.
func (s *Server) recordAutoRequirements(ctx context.Context, a *autoRecord, j *autoJob, report []byte) (bool, error) {
	var body struct {
		Requirements []autonomy.Requirement `json:"requirements"`
		Outcome      string                 `json:"outcome"`
		Approve      *bool                  `json:"approve"`
	}
	if err := json.Unmarshal(report, &body); err != nil {
		return false, err
	}
	if len(body.Requirements) == 0 {
		return false, nil
	}
	archive, err := s.autoArchiveIdentity(ctx, j)
	if err != nil {
		return false, fmt.Errorf("%w: prerequisite source archive: %v", errAutoArtifactPending, err)
	}
	task, err := s.DB.Task(j.TaskID)
	if err != nil {
		return false, err
	}
	if a.Requirements == nil {
		a.Requirements = map[string]*autoRequirement{}
	}
	historicalGate := autoHistoricalProvisioningGate(a, j, body.Outcome, body.Requirements)
	blocked := historicalGate || body.Outcome == "blocked" && (j.Role == "builder" || j.Role == "reviewer" && body.Approve != nil && !*body.Approve)
	var pins, imports []string
	diagnosis := a.RequirementDiagnoses[j.DiagnosisReservation]
	completeRecipe := diagnosis != nil && diagnosis.EvidenceOnly && j.Role == "builder"
	if j.PythonRequest != nil && !completeRecipe {
		pins = append(pins, j.PythonRequest.Requirements...)
		imports = append(imports, j.PythonRequest.Imports...)
	}
	unsupported := false
	ids := []string{}
	for _, request := range body.Requirements {
		key := autoRequirementKey(request)
		entry := a.Requirements[key]
		if entry == nil {
			if len(a.Requirements) >= 512 {
				return false, errors.New("prerequisite ledger capacity reached; retained requirements require disposition")
			}
			entry = &autoRequirement{Key: key, Request: request, State: "observed"}
			a.Requirements[key] = entry
		}
		found := false
		for _, o := range entry.Occurrences {
			if o.JobID == j.ID {
				found = true
				break
			}
		}
		if !found {
			entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: j.TaskID, RootTaskID: autoRepairRoot(a, j.TaskID), ProjectID: task.ProjectID, JobID: j.ID, ArchiveSHA: archive, ReportSHA: autoSHA(report), Evidence: append([]string(nil), request.Evidence...)})
		}
		entry.UpdatedAt = time.Now()
		ids = append(ids, key)
		if request.Capability != "python_wheels" || request.Condition != "offline_imports_available" {
			entry.State = "unsupported"
			entry.Reason = "No registered provisioner for this capability/condition. Investigate an isolated alternative or propose an independently audited capability change; this is not a human-consent requirement."
			unsupported = true
			continue
		}
		p, m, e := autoPythonInputs(request.Requirements, request.Imports)
		if e != nil {
			entry.State = "unsupported_input"
			entry.Reason = e.Error()
			unsupported = true
			continue
		}
		pins = append(pins, p...)
		imports = append(imports, m...)
	}
	if !blocked {
		return false, s.saveAuto(a)
	}
	j.RequirementIDs = ids
	if !unsupported {

		p, m, e := autoPythonInputs(pins, imports)
		if e != nil {
			unsupported = true
			for _, id := range ids {
				a.Requirements[id].State = "unsupported_input"
				a.Requirements[id].Reason = e.Error()
			}
		} else {
			admission := j.Admission
			if admission == nil {
				for i := len(a.State.Assignments) - 1; i >= 0; i-- {
					as := a.State.Assignments[i]
					if as.Role == "builder" && as.Item == a.State.Item && as.Step == a.State.Step {
						if b := autoFindJob(a, as.TaskID); b != nil {
							admission = b.Admission
						}
						break
					}
				}
			}
			// The retained assignment and audits bind reviewers even on legacy jobs
			// that predate explicit admission receipts.
			binding := store.J(admission)
			if admission == nil {
				binding = store.J(struct {
					Task  int64
					State *autonomy.State
				}{j.TaskID, a.State})
			}
			request := &autoPythonRequest{SchemaVersion: 1, Kind: "python_wheels", Requirements: p, Imports: m, SourceJob: j.ID, SourceSHA: archive, AdmissionSHA: autoSHA([]byte(binding))}
			if j.PythonRequest != nil && autoPythonSemanticKey(j.PythonRequest) == autoPythonSemanticKey(request) && j.PythonRecovery != nil && j.PythonRecovery.State == "verified" {
				unsupported = true
				for _, id := range ids {
					a.Requirements[id].State = "diagnosis_required"
					a.Requirements[id].Reason = "The same verified environment was already delivered to this assignment. A missing prerequisite has not changed; investigate the actual failure instead of repeating the worker."
				}
			} else {
				j.PendingPythonRequest = request
				for _, id := range ids {
					a.Requirements[id].State = "pending"
				}
			}
		}
	}
	s.closeAutoBridge(j.ID)
	j.Summary = "Worker blocked on recorded prerequisites; report and archive retained"
	if historicalGate {
		j.Summary = "Historical diagnosis awaits actual prerequisite verification and testing; original ready report and archive retained"
	}
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "backlog"})
	j.Status = "stopped"
	j.RequirementHold = unsupported
	if unsupported {
		autoDeferRequirements(a, j, time.Now())
	}
	return true, s.saveAuto(a)
}
func autoDeferRequirements(a *autoRecord, j *autoJob, now time.Time) {
	j.Status = "deferred"
	previous := a.State
	if j.RequirementHold || len(a.DeferredRuns) >= 16 {
		a.HeldRuns = append(a.HeldRuns, previous)
	} else {
		a.DeferredRuns = append(a.DeferredRuns, previous)
	}
	a.State = nil
	autoNewCycle(a, now)
	a.State.Backlog = append([]autonomy.Proposal(nil), previous.Backlog...)
}

func autoPythonPending(j *autoJob) bool {
	return j.PythonRequest != nil && !j.PythonStopped && (j.Status == "prepared" || j.Status == "deferred")
}
func autoPythonBound(request *autoPythonRequest, receipt autoPythonReceipt) bool {
	return request != nil && (receipt.BrowserKey == "" || autoHash256(receipt.BrowserKey)) && receipt.Capability == "python_wheels" && receipt.SourceJob == request.SourceJob && receipt.SourceSHA == request.SourceSHA && receipt.AdmissionSHA == request.AdmissionSHA
}
func autoWritePythonRequest(j *autoJob) error { return autoWritePythonRequestAt(autoRoot, j) }
func autoWritePythonRequestAt(root string, j *autoJob) error {
	path := filepath.Join(root, j.ID, "python-requirement.json")
	request := *j.PythonRequest
	request.ExpectedInputKey = j.PythonExpectedInput
	request.ExpectedBundleKey = j.PythonExpectedBundle
	raw := []byte(store.J(request))
	if existing, err := autoReadRegular(path, 64<<10); err == nil {
		if string(existing) != string(raw) {
			return errors.New("Python requirement intent changed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".python-requirement-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Server) recoverAutoPython(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.RequirementHold {
		return false, nil
	}
	if j.PythonRequest == nil || (j.Status != "prepared" && j.Status != "deferred") {
		return true, nil
	}
	j.PythonStopped = false
	if err := s.saveAuto(a); err != nil {
		return false, err
	}
	if err := autoWritePythonRequest(j); err != nil {
		return false, err
	}
	if err := s.ensureAutoBridges(j); err != nil {
		return false, err
	}
	raw, err := s.runAutoCommand(ctx, "python-dependencies", "--job", j.ID)
	if err != nil {
		return false, err
	}
	ready, err := autoApplyPythonReceipt(a, j, raw)
	if err != nil {
		return false, err
	}
	if j.RequirementHold && j.PythonRecovery != nil && j.PythonRecovery.Unsupported {
		if err = s.recordAutoProvisionerRequirement(a, j); err != nil {
			return false, err
		}
	}
	if err = s.saveAuto(a); err != nil {
		return false, err
	}
	return ready, nil
}

func autoApplyPythonReceipt(a *autoRecord, j *autoJob, raw []byte) (bool, error) {
	var receipt autoPythonReceipt
	if json.Unmarshal(raw, &receipt) != nil || !autoPythonBound(j.PythonRequest, receipt) {
		return false, errors.New("Python prerequisite receipt binding mismatch")
	}
	if len(receipt.Diagnostic) > 16<<10 {
		return false, errors.New("Python prerequisite diagnostic exceeds bound")
	}
	switch receipt.State {
	case "checking", "recovering", "waiting", "unavailable", "failed", "verified":
	default:
		return false, errors.New("invalid Python prerequisite state")
	}
	if receipt.State == "verified" && (!autoHash256(receipt.InputKey) || !autoHash256(receipt.BundleKey) || !autoHash256(receipt.RuntimeDigest)) {
		return false, errors.New("Python verification identity missing")
	}
	ready := receipt.State == "verified"
	if ready && ((j.PythonExpectedInput != "" && j.PythonExpectedInput != receipt.InputKey) || (j.PythonExpectedBundle != "" && j.PythonExpectedBundle != receipt.BundleKey)) {
		ready = false
		receipt.State = "unavailable"
		receipt.Reason = "The retained source was tested with a different dependency environment; inherited environment identity must not change silently"
		receipt.Unsupported = true
	}
	if ready && j.PythonNeedsChange && receipt.BundleKey == j.PythonPreviousBundle {
		ready = false
		receipt.State = "unavailable"
		receipt.Reason = "Verification returned the unchanged environment already used by the blocked assignment"
		receipt.Unsupported = true
	}
	j.PythonRecovery = &receipt
	if receipt.State == "unavailable" && receipt.Unsupported {
		j.RequirementHold = true
	}
	if ready {
		j.PythonNeedsChange = false
	}
	for _, key := range j.RequirementIDs {
		if r := a.Requirements[key]; r != nil {
			if receipt.InputKey != "" {
				if r.Environments == nil {
					r.Environments = map[string]*autoPythonReceipt{}
				}
				r.Environments[receipt.InputKey] = &receipt
			}
			r.Receipt = &receipt
			r.State = receipt.State
			r.Reason = receipt.Reason
			r.RecoveryJob = j.ID
			r.UpdatedAt = time.Now()
		}
	}
	if !ready {
		a.Status = "recovering_prerequisite"
		a.Reason = receipt.Reason
	}
	return ready, nil
}

const autoRequirementsPrompt = `
Prerequisite recovery: reports may optionally include requirements:[{capability:"python_wheels",schema_version:1,requirements:["django==5.2.12"],imports:["django"],condition:"offline_imports_available",evidence:["actual failed import plus evidence supporting the selected distribution/version"]}]. This is an example shape, not a recommendation to install that version. Request only exact evidenced distribution pins needed for the audited isolated task. Never infer distribution names solely from imports. Source packages under test must remain the retained checkout; do not replace them with released wheels. No URLs, VCS, local projects, builds or arbitrary commands. Existing pytest tooling remains controller-owned. The supported provisioner resolves compatible universal and native wheels and verifies them in an isolated process; unavailable or conflicting dependencies remain explicit. Discover registered browser assets through /capabilities; request the exact supported Playwright version together with project pins. Delivery requires a bound browser_key in /prerequisite python, not merely a catalog entry. Run browser-backed project tests through /opt/browser-runtime/browsers/offline-test COMMAND ARGS to retain local fixtures without credentials, bridge sockets or external networking. For a builder unable to complete, outcome=blocked with requirements preserves its assignment for verified recovery; for a reviewer unable to assess due solely to the environment, outcome=blocked,approve=false preserves the review assignment. If substantive defects are established, reject normally as incomplete; missing dependencies do not erase that rejection. Unsupported requirements may use another descriptive capability token and evidence; they are recorded for bounded diagnosis, never automatic privileges or a human-consent blocker. Inspect /requirements before repeating an unchanged blocker. Investigate supported alternatives or propose a useful isolated diagnosis through the normal independent plan audits; do not repeatedly relaunch on the same verified environment. Worker reports do not prove a prerequisite changed. Existing processes gain no mounts retroactively.
`

func autoRequirementRows(a *autoRecord) []map[string]any {
	entries := make([]*autoRequirement, 0, len(a.Requirements))
	for _, entry := range a.Requirements {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].UpdatedAt.After(entries[j].UpdatedAt) })
	rows := []map[string]any{}
	for _, r := range entries {
		if len(rows) >= 60 {
			break
		}
		diagnoses := []map[string]any{}
		for _, d := range autoRequirementDiagnoses(a, r.Key) {
			if len(diagnoses) >= 16 {
				break
			}
			diagnoses = append(diagnoses, map[string]any{"target_task_id": d.TargetTaskID, "outcome": d.Outcome, "task_id": d.TaskID, "review_task_id": d.ReviewTaskID})
		}
		rows = append(rows, map[string]any{"key": r.Key, "diagnoses": diagnoses, "capability": r.Request.Capability, "condition": r.Request.Condition, "latest_state": r.State, "reason": r.Reason, "occurrence_count": len(r.Occurrences), "environment_count": len(r.Environments), "details": "/requirements?key=" + r.Key, "scope": "Request grouping only; each retained assignment requires its own bound verification receipt"})
	}
	return rows
}

// A review or retained continuation must receive the same verified test
// environment as its source. Provisioning still independently revalidates it.
func autoInheritPythonRequest(a *autoRecord, j *autoJob) error {
	if j.PythonRequest != nil || a.State == nil || a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return nil
	}
	if j.Role == "builder" && a.State.Step == 0 && a.State.Items[a.State.Item].EnvironmentDiagnosisTaskID > 0 {
		return applyAutoDiagnosisEnvironment(a, j)
	}
	var source *autoJob
	if j.Role == "reviewer" || strings.HasPrefix(j.Role, "decision_") || j.Role == "builder" && a.State.Step > 0 {
		for i := len(a.State.Assignments) - 1; i >= 0; i-- {
			as := a.State.Assignments[i]
			if as.Role == "builder" && as.TaskID != j.TaskID && as.Item == a.State.Item && as.Completed {
				source = autoFindJob(a, as.TaskID)
				break
			}
		}
	} else if j.Role == "builder" {
		p := a.State.Items[a.State.Item]
		id := p.ContinueTaskID
		if id == 0 {
			id = p.RepairTaskID
		}
		if id == 0 {
			id = p.DocumentationTaskID
		}
		if id > 0 {
			source = autoFindJob(a, id)
		}
	}
	if source != nil && source.PythonRequest != nil && source.PythonRecovery != nil && source.PythonRecovery.State == "verified" {
		request := *source.PythonRequest
		j.PythonRequest = &request
		j.PythonRecovery = nil
		j.PythonExpectedInput = source.PythonRecovery.InputKey
		j.PythonExpectedBundle = source.PythonRecovery.BundleKey
	}
	return nil
}

func autoPreparePythonResume(old, j *autoJob) {
	j.PythonStopped = false
	if old.PythonRecovery != nil && old.PythonRecovery.State == "verified" {
		j.PythonExpectedInput = old.PythonRecovery.InputKey
		j.PythonExpectedBundle = old.PythonRecovery.BundleKey
	}
	if old.PendingPythonRequest != nil {
		request := *old.PendingPythonRequest
		j.PythonRequest = &request
		j.PendingPythonRequest = nil
		j.PythonPreviousBundle = old.PythonUsedBundle
		if j.PythonPreviousBundle == "" {
			j.PythonPreviousBundle = old.PythonPreviousBundle
		}
		if j.PythonPreviousBundle == "" && old.PythonRecovery != nil && old.PythonRecovery.State == "verified" {
			j.PythonPreviousBundle = old.PythonRecovery.BundleKey
		}
		j.PythonExpectedInput = ""
		j.PythonExpectedBundle = ""
		j.PythonNeedsChange = true
		j.PythonRecovery = nil
	}
}

// A consumer may discover an inherited environment is unavailable before it has
// any worker report. Record that trusted observation against the consumer task
// and the original archived source separately, rather than inventing a report.
func (s *Server) recordAutoProvisionerRequirement(a *autoRecord, j *autoJob) error {
	if j.PythonRequest == nil || j.PythonRecovery == nil || j.PythonRecovery.State != "unavailable" || !j.PythonRecovery.Unsupported || !autoPythonBound(j.PythonRequest, *j.PythonRecovery) {
		return errors.New("unsupported prerequisite observation lacks trusted binding")
	}
	task, err := s.DB.Task(j.TaskID)
	if err != nil {
		return err
	}
	if a.Requirements == nil {
		a.Requirements = map[string]*autoRequirement{}
	}
	if len(j.RequirementIDs) == 0 {
		request := autonomy.Requirement{Capability: "python_wheels", SchemaVersion: 1, Requirements: append([]string(nil), j.PythonRequest.Requirements...), Imports: append([]string(nil), j.PythonRequest.Imports...), Condition: "offline_imports_available", Evidence: []string{"Trusted provisioner could not reproduce the retained test environment"}}
		key := autoRequirementKey(request)
		if a.Requirements[key] == nil {
			if len(a.Requirements) >= 512 {
				return errors.New("prerequisite ledger capacity reached")
			}
			a.Requirements[key] = &autoRequirement{Key: key, Request: request}
		}
		j.RequirementIDs = []string{key}
	}
	for _, key := range j.RequirementIDs {
		entry := a.Requirements[key]
		if entry == nil {
			return errors.New("retained prerequisite record missing")
		}
		found := false
		for _, o := range entry.Occurrences {
			if o.TaskID == j.TaskID && o.JobID == j.ID {
				found = true
				break
			}
		}
		if !found {
			entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: j.TaskID, RootTaskID: autoRepairRoot(a, j.TaskID), ProjectID: task.ProjectID, JobID: j.ID, EvidenceJob: j.PythonRequest.SourceJob, ArchiveSHA: j.PythonRequest.SourceSHA, Evidence: []string{"Controller-bound provisioner observation: " + j.PythonRecovery.Reason}})
		}
		entry.State = "unavailable"
		entry.Reason = j.PythonRecovery.Reason
		entry.Receipt = j.PythonRecovery
		entry.RecoveryJob = j.ID
		entry.UpdatedAt = time.Now()
	}
	autoRefuteDiagnosisRemedy(a, j)
	return nil
}
