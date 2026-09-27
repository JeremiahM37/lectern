package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

type autoPrivateToolingReceipt struct {
	Diagnostic string `json:"diagnostic,omitempty"`
	Executed   *bool  `json:"executed,omitempty"`
	Capability string `json:"capability"`
	State      string `json:"state"`
	Key        string `json:"key"`
	Reason     string `json:"reason,omitempty"`
	Scope      string `json:"scope"`
}

func autoInheritPrivateTooling(a *autoRecord, j *autoJob) error {
	if j.PythonExpectedTestKey != "" || a.State == nil || a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return nil
	}
	p := a.State.Items[a.State.Item]
	var source *autoJob
	if j.Role == "builder" && a.State.Step == 0 && p.SourceIntegrationID != "" {
		v := autoPrivateFind(a, p.SourceIntegrationID)
		if v == nil || v.Review == nil || v.Publication == nil {
			return errors.New("published private tooling authority unavailable")
		}
		for _, ref := range v.Review.TestReceiptSHAs {
			for _, t := range v.Tests {
				if t.ReceiptSHA == ref && t.State == "exited" && t.Executed && t.ExitCode != nil && *t.ExitCode == 0 && !t.Truncated && t.PythonBundleKey == "" && (t.PythonTestKey == "" || autoHash256(t.PythonTestKey)) {
					j.PythonExpectedTestKey = t.PythonTestKey
					return nil
				}
			}
		}
		// A project environment already includes its immutable test package closure.
		source = autoFindJob(a, v.ReviewerTaskID)
		if source == nil || source.PythonUsedBundle == "" {
			return errors.New("published private result lacks reusable tested Python tooling")
		}
		return nil
	}
	if j.Role == "reviewer" || j.Role == "builder" && a.State.Step > 0 {
		source = autoPrivateCurrentBuilder(a)
	} else if j.Role == "builder" {
		id := p.ContinueTaskID
		if id == 0 {
			id = p.RepairTaskID
		}
		if id == 0 {
			id = p.IntegrationTaskID
		}
		if id == 0 {
			id = p.ExpertRecoveryTaskID
		}
		source = autoFindJob(a, id)
	}
	if source != nil {
		j.PythonExpectedTestKey = source.PythonExpectedTestKey
	}
	return nil
}
func (s *Server) recoverAutoPrivateTooling(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.PythonExpectedTestKey == "" || j.PythonRequest != nil {
		return true, nil
	}
	if !autoHash256(j.PythonExpectedTestKey) {
		return false, errors.New("selected Python test tooling identity invalid")
	}
	raw, e := s.runAutoCommand(ctx, "python-test-runtime", "--key", j.PythonExpectedTestKey)
	if e != nil {
		return false, e
	}
	return autoApplyPrivateTooling(a, j, raw)
}
func autoApplyPrivateTooling(a *autoRecord, j *autoJob, raw []byte) (bool, error) {
	var r autoPrivateToolingReceipt
	if len(raw) > 16<<10 || json.Unmarshal(raw, &r) != nil || r.Capability != "python_test_runtime" || r.Key != j.PythonExpectedTestKey || !autoHash256(r.Key) || r.Scope == "" {
		return false, errors.New("selected Python test tooling receipt mismatch")
	}
	if r.State != "verified" && r.State != "unavailable" {
		return false, errors.New("invalid selected Python test tooling state")
	}
	if r.State == "unavailable" && r.Diagnostic != "missing" && r.Diagnostic != "incompatible" && r.Diagnostic != "integrity" {
		return false, errors.New("selected tooling diagnostic missing")
	}
	if r.State == "unavailable" && r.Reason == "" {
		return false, errors.New("unavailable selected tooling lacks diagnostic")
	}
	j.PythonTestRecovery = &r
	key := autoSHA([]byte("python-test-runtime:" + r.Key))
	if a.Requirements == nil {
		a.Requirements = map[string]*autoRequirement{}
	}
	entry := a.Requirements[key]
	if entry == nil {
		entry = &autoRequirement{Key: key, Request: autonomy.Requirement{Capability: "python_test_runtime", SchemaVersion: 1, Condition: "exact_runtime_key_" + r.Key, Evidence: []string{"Controller-pinned independent private integration test environment"}}}
		a.Requirements[key] = entry
	}
	entry.ToolingReceipt = &r
	entry.State = r.State
	entry.Reason = r.Reason
	entry.RecoveryJob = j.ID
	entry.UpdatedAt = time.Now()
	seen := false
	for _, o := range entry.Occurrences {
		if o.JobID == j.ID {
			seen = true
		}
	}
	if !seen {
		project := int64(0)
		if j.Admission != nil {
			project = j.Admission.Proposal.ProjectID
		}
		entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: j.TaskID, RootTaskID: j.TaskID, ProjectID: project, JobID: j.ID, Evidence: []string{"Exact tooling key " + r.Key + "; " + r.Scope}})
	}
	return r.State == "verified", nil
}
func autoPrivateToolingReady(j *autoJob) bool {
	return j.PendingPythonRequest != nil || j.PythonExpectedTestKey == "" || j.PythonRequest != nil || j.PythonTestRecovery != nil && j.PythonTestRecovery.Key == j.PythonExpectedTestKey && j.PythonTestRecovery.State == "verified"
}

func (s *Server) handleAutoPrivateToolingFailure(ctx context.Context, a *autoRecord, j *autoJob, status []byte) (bool, error) {
	if j.PythonExpectedTestKey == "" || j.PythonRequest != nil {
		return false, nil
	}
	var st struct {
		State   string          `json:"state"`
		Tooling json.RawMessage `json:"python_test_runtime"`
	}
	if json.Unmarshal(status, &st) != nil || len(st.Tooling) == 0 || st.State == "running" {
		return false, nil
	}
	var r autoPrivateToolingReceipt
	if json.Unmarshal(st.Tooling, &r) != nil || r.State != "unavailable" {
		return false, nil
	}
	if r.Executed == nil || *r.Executed {
		return true, errors.New("selected tooling failure does not establish model was unstarted")
	}
	switch r.Diagnostic {
	case "missing", "incompatible", "integrity":
	default:
		return true, errors.New("selected tooling failure diagnostic missing")
	}
	if _, e := autoApplyPrivateTooling(a, j, st.Tooling); e != nil {
		return true, e
	}
	return true, s.holdAutoPrivateTooling(ctx, a, j, true)
}

func (s *Server) holdAutoPrivateTooling(ctx context.Context, a *autoRecord, j *autoJob, consumed bool) error {
	r := j.PythonTestRecovery
	if r == nil {
		return errors.New("selected tooling recovery receipt missing")
	}
	if e := s.snapshotAutoJob(ctx, j); e != nil {
		return e
	}
	sha, e := s.autoArchiveIdentity(ctx, j)
	if e != nil {
		return e
	}
	key := autoSHA([]byte("python-test-runtime:" + r.Key))
	entry := a.Requirements[key]
	if entry == nil {
		return errors.New("typed selected tooling requirement missing")
	}
	task, e := s.DB.Task(j.TaskID)
	if e != nil {
		return e
	}
	entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: j.TaskID, RootTaskID: j.TaskID, ProjectID: task.ProjectID, JobID: j.ID, ArchiveSHA: sha, Evidence: []string{"Trusted exact-key runtime validation prevented model execution: " + r.Diagnostic + ": " + r.Reason}})
	j.PythonTestNeedsResume = consumed
	j.RecoveryCheckAt = time.Now().Add(5 * time.Minute)
	if r.Diagnostic != "missing" {
		j.RequirementHold = true
		j.RequirementIDs = append(j.RequirementIDs, key)
	}
	s.closeAutoBridge(j.ID)
	autoDeferRequirements(a, j, time.Now())
	return s.saveAuto(a)
}
