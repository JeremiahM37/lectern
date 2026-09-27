package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"sort"
	"strconv"
	"time"
)

func autoGoFailureRequirementKey(j *autoJob) string {
	r := j.GoExpectedRuntime
	if r == nil {
		r = j.GoRuntime
	}
	return autoSHA([]byte("go-runtime:" + r.DependencyKey + ":" + r.BundleDigest + ":" + r.ToolchainDigest))
}

func (s *Server) handleAutoGoLaunchFailure(ctx context.Context, a *autoRecord, j *autoJob, raw []byte) (bool, error) {
	var st struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &st) != nil || (st.State != "failed" && st.State != "stopped") || j.GoRuntime == nil || j.GoRuntime.State != "unavailable" {
		return false, nil
	}
	r := j.GoRuntime
	if r.OwnerJob != j.ID || r.Executed == nil || *r.Executed || !autoHash256(r.DependencyKey) {
		return true, errors.New("Go failure is not bound pre-execution evidence")
	}
	if e := s.snapshotAutoJob(ctx, j); e != nil {
		return true, e
	}
	sha, e := s.autoArchiveIdentity(ctx, j)
	if e != nil {
		return true, e
	}
	task, e := s.DB.Task(j.TaskID)
	if e != nil {
		return true, e
	}
	key := autoGoFailureRequirementKey(j)
	if a.Requirements == nil {
		a.Requirements = map[string]*autoRequirement{}
	}
	entry := a.Requirements[key]
	if entry == nil {
		entry = &autoRequirement{Key: key, Request: autonomy.Requirement{Capability: "go_runtime", SchemaVersion: 1, Condition: "exact_immutable_environment_available", Evidence: []string{"Root runner prevented model execution; restore the exact selected module/toolchain bytes, or independently investigate the retained source. Never substitute a different runtime."}}}
		a.Requirements[key] = entry
	}
	copy := *r
	entry.GoReceipt = &copy
	entry.State = "unavailable"
	entry.Reason = r.Diagnostic + ": " + r.Reason
	entry.RecoveryJob = j.ID
	entry.UpdatedAt = time.Now()
	seen := false
	for _, o := range entry.Occurrences {
		if o.JobID == j.ID {
			seen = true
		}
	}
	if !seen {
		entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: j.TaskID, RootTaskID: j.TaskID, ProjectID: task.ProjectID, JobID: j.ID, ArchiveSHA: sha, Evidence: []string{entry.Reason, "dependency=" + r.DependencyKey, "bundle=" + r.BundleDigest, "toolchain=" + r.ToolchainDigest}})
	}
	found := false
	for _, id := range j.RequirementIDs {
		if id == key {
			found = true
		}
	}
	if !found {
		j.RequirementIDs = append(j.RequirementIDs, key)
	}
	j.GoNeedsResume = true
	j.GoRecoveryVerified = false
	j.GoRuntimeSourceSHA = sha
	j.RequirementHold = true
	j.RecoveryCheckAt = time.Now().Add(5 * time.Minute)
	s.closeAutoBridge(j.ID)
	autoDeferRequirements(a, j, time.Now())
	return true, s.saveAuto(a)
}

// At most one cold exact-runtime validation per tick. It uses no model tokens,
// and the root helper hashes asynchronously. Held assignments remain diagnosable.
func (s *Server) pollAutoGoRuntimeRecovery(ctx context.Context, a *autoRecord, now time.Time) bool {
	jobs := []*autoJob{}
	for _, j := range a.Jobs {
		if j.Status == "deferred" && j.GoNeedsResume && !j.GoRecoveryVerified && !now.Before(j.RecoveryCheckAt) {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].RecoveryCheckAt.Before(jobs[j].RecoveryCheckAt) })
	if len(jobs) == 0 {
		return false
	}
	j := jobs[0]
	j.RecoveryCheckAt = now.Add(5 * time.Minute)
	if e := autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{j.Provider}, now); e != nil {
		a.Reason = e.Error()
		return true
	}
	if !autoHash256(j.GoRuntimeSourceSHA) || j.GoRuntime == nil {
		a.Reason = "Go restoration source identity missing"
		return true
	}
	pin := &autoExpertRecoveryPin{SourceJob: j.ID, SourceSHA: j.GoRuntimeSourceSHA}
	key := autoGoProbeIdentity(pin, j.GoRuntime.DependencyKey)
	if a.GoProbeRuntimes == nil {
		a.GoProbeRuntimes = map[string]*autoGoProbeRuntime{}
	}
	x := a.GoProbeRuntimes[key]
	if x == nil {
		x = &autoGoProbeRuntime{SourceJob: j.ID, SourceSHA: j.GoRuntimeSourceSHA, DependencyKey: j.GoRuntime.DependencyKey, Generation: 1}
		a.GoProbeRuntimes[key] = x
	}
	if x.StopRequested && !x.Stopped {
		return true
	}
	if x.Stopped {
		x.Generation++
		x.Stopped = false
		x.StopRequested = false
		x.Receipt = nil
	}
	if e := s.saveAuto(a); e != nil {
		a.Reason = e.Error()
		return true
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, e := s.runAutoCommand(bounded, "go-runtime", "--job", x.SourceJob, "--dependency-key", x.DependencyKey, "--source-sha256", x.SourceSHA, "--generation", strconv.Itoa(x.Generation))
	if e != nil {
		a.Reason = "Go restoration validation: " + e.Error()
		return true
	}
	var receipt autoGoProbeRuntimeReceipt
	if len(raw) > 65536 || json.Unmarshal(raw, &receipt) != nil || receipt.SchemaVersion != 1 || receipt.OwnerJob != x.SourceJob || receipt.SourceSHA != x.SourceSHA || receipt.DependencyKey != x.DependencyKey || receipt.Generation != x.Generation || receipt.Provenance != "new_experiment" {
		a.Reason = "Go restoration receipt binding differs"
		return true
	}
	switch receipt.State {
	case "verified", "recovering", "checking", "waiting", "unavailable", "failed":
	default:
		a.Reason = "Go restoration state invalid"
		return true
	}
	x.Receipt = &receipt
	if receipt.State == "recovering" || receipt.State == "checking" {
		j.RecoveryCheckAt = now.Add(20 * time.Second)
	}
	if receipt.State != "verified" {
		return true
	}
	if !autoGoRuntimeValid(&receipt.autoGoRuntimeReceipt) || j.GoExpectedRuntime != nil && !autoSameGoRuntime(j.GoExpectedRuntime, &receipt.autoGoRuntimeReceipt) {
		a.Reason = "Go restoration differs from immutable expectation"
		return true
	}
	// This is verification for a future process, never a historical delivery.
	requirementKey := autoGoFailureRequirementKey(j)
	expected := receipt.autoGoRuntimeReceipt
	j.GoExpectedRuntime = &expected
	j.GoRecoveryVerified = true
	entry := a.Requirements[requirementKey]
	if entry != nil {
		entry.State = "verified"
		entry.Reason = "Exact environment content verified for same-assignment retry"
		entry.UpdatedAt = now
	}
	j.RequirementHold = false
	for _, id := range j.RequirementIDs {
		if r := a.Requirements[id]; r == nil || r.State != "verified" {
			j.RequirementHold = true
		}
	}
	return true
}
