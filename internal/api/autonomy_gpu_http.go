package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

type autoGPUExperiment struct {
	AssignmentKey       string                  `json:"assignment_key,omitempty"`
	RetryCount          int                     `json:"retry_count,omitempty"`
	RetryAfter          time.Time               `json:"retry_after,omitempty"`
	History             []string                `json:"history,omitempty"`
	ID                  string                  `json:"id"`
	OwnerJob            string                  `json:"owner_job"`
	OwnerTask           int64                   `json:"owner_task"`
	Input               autoGPUExperimentInput  `json:"input"`
	InputSHA            string                  `json:"input_sha256"`
	Runtime             autoGPUQualifiedRuntime `json:"runtime"`
	SnapshotID          string                  `json:"snapshot_id"`
	SnapshotRequest     string                  `json:"snapshot_request"`
	SnapshotReceipt     string                  `json:"snapshot_receipt,omitempty"`
	RunOwner            string                  `json:"run_owner,omitempty"`
	RunID               string                  `json:"run_id,omitempty"`
	RunRequest          string                  `json:"run_request,omitempty"`
	Receipt             string                  `json:"receipt,omitempty"`
	State               string                  `json:"state"`
	Reason              string                  `json:"reason,omitempty"`
	StopRequested       bool                    `json:"stop_requested,omitempty"`
	StopConfirmed       bool                    `json:"stop_confirmed,omitempty"`
	ObservationAttempts int                     `json:"observation_attempts,omitempty"`
	NextPoll            time.Time               `json:"next_poll,omitempty"`
}

func autoGPUCurrentOwner(a *autoRecord, id string) (*autoJob, error) {
	if a == nil || !a.Config.Enabled || a.State == nil {
		return nil, errors.New("GPU assignment disabled")
	}
	for _, j := range a.Jobs {
		if j.ID != id {
			continue
		}
		if j.Status != "running" || j.Admission == nil || (j.Role != "builder" && j.Role != "reviewer") {
			break
		}
		latest := autoFindJob(a, j.TaskID)
		if latest == nil || latest.ID != id {
			break
		}
		for _, as := range a.State.Assignments {
			if as.TaskID == j.TaskID && as.Role == j.Role && !as.Completed {
				return j, nil
			}
		}
	}
	return nil, errors.New("GPU request needs current admitted builder or reviewer")
}
func autoGPUQualification(root string) (autoGPUQualifiedRuntime, error) {
	var out autoGPUQualifiedRuntime
	path := filepath.Join(filepath.Dir(root), "dependencies/gpu/qualification.json")
	info, e := os.Lstat(path)
	if e != nil {
		return out, e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || info.Size() > 65536 {
		return out, errors.New("GPU qualification is not immutable root evidence")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return out, e
	}
	return autoGPUQualificationRaw(raw)
}

func autoGPUQualificationRaw(raw []byte) (autoGPUQualifiedRuntime, error) {
	var out autoGPUQualifiedRuntime
	if e := autoVerifyMaintenanceSeal(raw); e != nil {
		return out, e
	}
	var q struct {
		Schema    int               `json:"schema_version"`
		Qualified bool              `json:"qualified"`
		Target    string            `json:"target"`
		Profile   string            `json:"profile"`
		Runtime   string            `json:"runtime_key"`
		SHA       string            `json:"receipt_sha256"`
		Device    string            `json:"device_bdf"`
		Kernel    string            `json:"kernel_release"`
		Driver    string            `json:"driver_sha256"`
		Helpers   map[string]string `json:"helpers"`
	}
	if json.Unmarshal(raw, &q) != nil || q.Schema != 1 || !q.Qualified || q.Target != autonomy.GPUResearchTarget || q.Profile != "gpu-screen600" || q.Device != "0000:f4:00.0" || q.Kernel == "" || !autoHash256(q.Driver) || !autoHash256(q.Runtime) || !autoHash256(q.SHA) {
		return out, errors.New("GPU runtime is not qualified")
	}
	if len(q.Helpers) != 4 {
		return out, errors.New("GPU qualification requires exact supervisor/executor/lease/telemetry identities")
	}
	for _, key := range []string{"supervisor_sha256", "executor_sha256", "lease_sha256", "telemetry_sha256"} {
		if !autoHash256(q.Helpers[key]) {
			return out, errors.New("GPU qualification helper identity missing")
		}
	}
	return autoGPUQualifiedRuntime{RuntimeKey: q.Runtime, ReceiptSHA: q.SHA}, nil
}
func autoGPUWrite(root, job, group, id, raw string) error {
	if !autoExpertJobID.MatchString(job) || !autoHash256(id) || (group != "gpu-requests" && group != "gpu-snapshot-requests") {
		return errors.New("invalid GPU publication identity")
	}
	dir := filepath.Join(root, job, group)
	for _, part := range []string{root, filepath.Join(root, job), dir} {
		if e := os.Mkdir(part, 0700); e != nil && !os.IsExist(e) {
			return e
		}
		info, e := os.Lstat(part)
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("unsafe GPU request directory")
		}
	}
	path := filepath.Join(dir, id+".json")
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe GPU request file")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if old, e := os.ReadFile(path); e == nil {
		if string(old) != raw {
			return errors.New("immutable GPU request differs")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.CreateTemp(dir, ".gpu-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.WriteString(raw); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Link(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func autoGPUInput(raw []byte) (autoGPUExperimentInput, error) {
	var v autoGPUExperimentInput
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return v, errors.New("GPU input must be an object")
	}
	seen := map[string]bool{}
	for d.More() {
		t, e = d.Token()
		k, ok := t.(string)
		if e != nil || !ok || seen[k] || (k != "script" && k != "argv" && k != "trial" && k != "source_paths") {
			return v, errors.New("invalid or duplicate GPU input field")
		}
		seen[k] = true
		switch k {
		case "script":
			e = d.Decode(&v.Script)
		case "argv":
			e = d.Decode(&v.Argv)
		case "trial":
			e = d.Decode(&v.Trial)
		case "source_paths":
			e = d.Decode(&v.SourcePaths)
		}
		if e != nil {
			return v, e
		}
	}
	if _, e = d.Token(); e != nil {
		return v, e
	}
	if d.Decode(new(any)) != io.EOF {
		return v, errors.New("trailing GPU input")
	}
	if strings.TrimSpace(v.Script) == "" || len(v.Script) > 128<<10 || strings.ContainsRune(v.Script, 0) || len(v.Argv) > 32 || v.Trial < 0 || v.Trial >= 5 {
		return v, errors.New("GPU input limits")
	}
	for _, arg := range v.Argv {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return v, errors.New("GPU argv limits")
		}
	}
	if v.Argv == nil {
		v.Argv = []string{}
	}
	return autoGPUNormalizeInput(v)
}
func (s *Server) autoGPUBridge(job string, w http.ResponseWriter, r *http.Request) {
	s.autoGPUBridgeAt(autoRoot, job, w, r)
}
func (s *Server) autoGPUBridgeAt(root, job string, w http.ResponseWriter, r *http.Request) {
	s.autoGPUBridgeQualified(root, job, w, r, autoGPUQualification)
}
func (s *Server) autoGPUBridgeQualified(root, job string, w http.ResponseWriter, r *http.Request, qualification func(string) (autoGPUQualifiedRuntime, error)) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", 405)
		return
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		http.Error(w, "invalid query", 400)
		return
	}
	for k, v := range query {
		if len(v) != 1 || (k != "id" && k != "offset" && k != "manifest_offset") {
			http.Error(w, "invalid query selector", 400)
			return
		}
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		http.Error(w, "state unavailable", 503)
		return
	}
	if r.Method == http.MethodGet {
		id := query.Get("id")
		if !autoHash256(id) {
			http.Error(w, "exact experiment ID required", 400)
			return
		}
		x := a.GPUExperiments[id]
		var reader *autoJob
		for _, j := range a.Jobs {
			if j.ID == job {
				reader = j
			}
		}
		if x == nil || reader == nil || (reader.TaskID != x.OwnerTask && autoGPUAssignmentKey(reader) != autoGPUExperimentAssignment(a, x)) {
			http.Error(w, "experiment unavailable to this assignment", 404)
			return
		}
		if query.Has("manifest_offset") {
			offset, err := strconv.Atoi(query.Get("manifest_offset"))
			if err != nil || offset < 0 || offset > 32<<20 || query.Has("offset") || x.SnapshotReceipt == "" {
				http.Error(w, "invalid manifest offset", 400)
				return
			}
			raw, err := s.autoGPUCall(r.Context(), "gpu-source-manifest", x.OwnerJob, x.SnapshotID, "--offset", strconv.Itoa(offset))
			var out struct {
				Owner    string `json:"owner_job"`
				Snapshot string `json:"snapshot_id"`
				SHA      string `json:"manifest_sha256"`
			}
			var receipt struct {
				SHA string `json:"manifest_sha256"`
			}
			if err != nil || json.Unmarshal(raw, &out) != nil || json.Unmarshal([]byte(x.SnapshotReceipt), &receipt) != nil || out.Owner != x.OwnerJob || out.Snapshot != x.SnapshotID || !autoHash256(receipt.SHA) || out.SHA != receipt.SHA {
				http.Error(w, "manifest binding unavailable", 502)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
			return
		}
		if query.Has("offset") {
			offset, e := strconv.Atoi(query.Get("offset"))
			if e != nil || offset < 0 || offset > 16<<20 || x.RunID == "" {
				http.Error(w, "invalid output offset", 400)
				return
			}
			raw, e := s.autoGPUCall(r.Context(), "gpu-research-output", autoGPURunOwner(x), x.RunID, "--offset", strconv.Itoa(offset))
			if e != nil {
				http.Error(w, "output unavailable", 502)
				return
			}
			var out struct {
				Owner string `json:"owner_job"`
				Run   string `json:"run_id"`
				SHA   string `json:"request_sha256"`
			}
			var lease *autonomy.GPUResearchLease
			if a.GPUResearch != nil {
				lease = a.GPUResearch.Runs[x.RunID]
			}
			if json.Unmarshal(raw, &out) != nil || out.Owner != autoGPURunOwner(x) || out.Run != x.RunID || lease == nil || out.SHA != lease.RequestSHA {
				http.Error(w, "output binding differs", 502)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
			return
		}
		if e = s.autoGPUAdvance(r.Context(), a, x, root, false, false); e != nil {
			x.Reason = e.Error()
		}
		_ = s.saveAuto(a)
		writeJSON(w, 200, autoGPUView(x))
		return
	}
	if len(query) != 0 {
		http.Error(w, "POST has no selectors", 400)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if e != nil || len(raw) > 2<<20 {
		http.Error(w, "GPU input too large", 400)
		return
	}
	input, e := autoGPUInput(raw)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	owner, e := autoGPUCurrentOwner(a, job)
	if e == nil {
		e = autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{owner.Provider}, time.Now())
	}
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	qualified, e := qualification(root)
	if e != nil {
		http.Error(w, "GPU runtime qualification unavailable: "+e.Error(), 409)
		return
	}
	inputSHA := autoGPUInputSHA(input, qualified)
	id := autoSHA([]byte(fmt.Sprintf("gpu/task:%d/%s", owner.TaskID, inputSHA)))
	if pending := autoGPUPendingAssignment(a, owner, id); pending != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"state": "assignment_busy", "reason": "The retained assignment already owns a pending GPU capture or run; this input was not accepted", "existing_experiment_id": pending.ID, "existing_input_sha256": pending.InputSHA, "existing_state": pending.State})
		return
	}
	if a.GPUExperiments == nil {
		a.GPUExperiments = map[string]*autoGPUExperiment{}
	}
	if a.GPUResearch == nil {
		a.GPUResearch = &autonomy.GPUResearchLedger{}
	}
	x := a.GPUExperiments[id]
	if x == nil {
		request, _ := json.Marshal(map[string]any{"schema_version": 1, "owner_job": job, "requested_at": time.Now().UTC().Format(time.RFC3339Nano), "input_sha256": inputSHA, "source_paths": input.SourcePaths})
		x = &autoGPUExperiment{AssignmentKey: autoGPUAssignmentKey(owner), ID: id, OwnerJob: job, OwnerTask: owner.TaskID, Input: input, InputSHA: inputSHA, Runtime: qualified, SnapshotID: autoSHA(request), SnapshotRequest: string(request), State: "capturing"}
		a.GPUExperiments[id] = x
	}
	if autoGPUFinished(x) {
		if e = autoGPURenew(x, owner, time.Now()); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
	}
	if e = s.saveAuto(a); e != nil {
		http.Error(w, "GPU intent persistence failed", 503)
		return
	}
	if !time.Now().Before(x.NextPoll) {
		if e = s.autoGPUAdvance(r.Context(), a, x, root, true, false); e != nil {
			x.Reason = e.Error()
		}
	}
	if e = s.saveAuto(a); e != nil {
		http.Error(w, "GPU state persistence failed", 503)
		return
	}
	writeJSON(w, 202, autoGPUView(x))
}

func autoGPUView(x *autoGPUExperiment) any {
	return map[string]any{"id": x.ID, "state": x.State, "reason": x.Reason, "snapshot_id": x.SnapshotID, "run_id": x.RunID, "snapshot_receipt": autoGPURawView(x.SnapshotReceipt), "receipt": autoGPURawView(x.Receipt), "stop_requested": x.StopRequested, "stop_confirmed": x.StopConfirmed}
}
func (s *Server) autoGPUCall(ctx context.Context, cmd, job, id string, extra ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	args := []string{cmd, "--job", job, "--gpu-id", id}
	args = append(args, extra...)
	return s.runAutoCommand(bounded, args...)
}
func autoGPUFinished(x *autoGPUExperiment) bool {
	return x.State == "exited" || x.State == "cancelled" || x.State == "timeout" || x.State == "output_limit" || x.State == "interrupted" || x.State == "unavailable"
}
func autoGPUPending(a *autoRecord) bool {
	for _, x := range a.GPUExperiments {
		if !autoGPUFinished(x) {
			return true
		}
	}
	return false
}

func (s *Server) autoGPUAdvance(ctx context.Context, a *autoRecord, x *autoGPUExperiment, root string, start, stop bool) error {
	if autoGPUFinished(x) {
		return nil
	}
	knownTerminal := false
	if x.RunID != "" && a.GPUResearch != nil {
		if lease := a.GPUResearch.Runs[x.RunID]; lease != nil && lease.Receipt != nil {
			knownTerminal = true
		}
	}
	if (stop || x.StopRequested) && !knownTerminal {
		x.StopRequested = true
		if x.RunID != "" {
			if e := a.GPUResearch.Cancel(x.RunID); e != nil {
				return e
			}
		}
		if e := s.saveAuto(a); e != nil {
			return e
		}
		group, id, raw, command := "gpu-snapshot-requests", x.SnapshotID, x.SnapshotRequest, "gpu-source-stop"
		if x.RunID != "" {
			group, id, raw, command = "gpu-requests", x.RunID, x.RunRequest, "gpu-research-stop"
		}
		owner := x.OwnerJob
		if x.RunID != "" {
			owner = autoGPURunOwner(x)
		}
		if e := autoGPUWrite(root, owner, group, id, raw); e != nil {
			return e
		}
		out, e := s.autoGPUCall(ctx, command, owner, id)
		if e != nil {
			return e
		}
		var v struct {
			State    string `json:"state"`
			Owner    string `json:"owner_job"`
			Run      string `json:"run_id"`
			Snapshot string `json:"snapshot_id"`
			SHA      string `json:"request_sha256"`
		}
		if json.Unmarshal(out, &v) != nil || v.Owner != owner || (x.RunID == "" && v.Snapshot != x.SnapshotID) || (x.RunID != "" && (v.Run != x.RunID || v.SHA != autoSHA([]byte(x.RunRequest)))) || (v.State != "stopped" && v.State != "stopping") {
			return errors.New("GPU stop receipt binding differs")
		}
		if v.State == "stopping" {
			x.State = "stopping"
			return nil
		}
		x.StopConfirmed = true
		// Read actual terminal evidence; a stop response alone is not execution evidence.
		start = false
	}
	if x.RunID == "" {
		cmd := "gpu-source-status"
		if start && !x.StopRequested {
			if e := autoGPUWrite(root, x.OwnerJob, "gpu-snapshot-requests", x.SnapshotID, x.SnapshotRequest); e != nil {
				return e
			}
			cmd = "gpu-source-prepare"
		}
		raw, e := s.autoGPUCall(ctx, cmd, x.OwnerJob, x.SnapshotID)
		if e != nil {
			return e
		}
		var capture autoGPUCapturedSource
		if len(raw) > 2<<20 || json.Unmarshal(raw, &capture) != nil || capture.OwnerJob != x.OwnerJob || capture.SnapshotID != x.SnapshotID {
			return errors.New("GPU snapshot receipt binding differs")
		}
		switch capture.State {
		case "preparing", "stopping":
			x.State = "capturing"
			return nil
		case "cancelled", "interrupted", "unavailable":
			if e = autoVerifyMaintenanceSeal(raw); e != nil {
				return e
			}
			x.State = capture.State
			x.SnapshotReceipt = string(raw)
			return nil
		case "ready":
			if capture.InputSHA != x.InputSHA || !autoHash256(capture.ArchiveSHA) || !autoHash256(capture.TreeSHA) || capture.StartedNS <= 0 || capture.FinishedNS < capture.StartedNS {
				return errors.New("GPU captured source identity differs")
			}
			if e = autoVerifyMaintenanceSeal(raw); e != nil {
				return e
			}
			x.SnapshotReceipt = string(raw)
		default:
			return errors.New("unknown GPU snapshot state")
		}
		if x.StopRequested {
			x.State = "cancelled"
			return nil
		}
		if !start {
			x.State = "captured"
			return nil
		}
		owner, e := autoGPUCurrentOwner(a, x.OwnerJob)
		if e != nil {
			return e
		}
		if e = autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{owner.Provider}, time.Now()); e != nil {
			return e
		}
		q, e := autoGPUQualification(root)
		if e != nil {
			return e
		}
		if q != x.Runtime {
			x.State = "unavailable"
			x.Reason = "GPU qualification changed before reservation; submit a fresh request against the registered qualified runtime"
			return nil
		}
		request, e := autoGPUCapturedRequest(a, owner, x.SnapshotID, capture, q, x.Input)
		if e != nil {
			return e
		}
		if a.GPUResearch == nil {
			a.GPUResearch = &autonomy.GPUResearchLedger{}
		}
		lease, e := a.GPUResearch.Reserve(request, q.RuntimeKey, q.ReceiptSHA, time.Now(), true, true)
		if e != nil {
			return e
		}
		x.RunID = lease.ID
		x.RunOwner = lease.Request.OwnerJob
		x.RunRequest = string(lease.RequestRaw)
		x.State = "reserved"
		if e = s.saveAuto(a); e != nil {
			return e
		}
	}
	lease := a.GPUResearch.Runs[x.RunID]
	if lease == nil {
		return errors.New("GPU reservation lost")
	}
	cmd := "gpu-research-status"
	if start && !x.StopRequested && lease.Receipt == nil {
		if e := autoGPUWrite(root, autoGPURunOwner(x), "gpu-requests", x.RunID, x.RunRequest); e != nil {
			return e
		}
		if e := a.GPUResearch.Start(x.RunID, time.Now(), true, true); e != nil {
			return e
		}
		if e := s.saveAuto(a); e != nil {
			return e
		}
		cmd = "gpu-research"
	}
	raw, e := s.autoGPUCall(ctx, cmd, autoGPURunOwner(x), x.RunID)
	if e != nil {
		return e
	}
	var v struct {
		State   string `json:"state"`
		Owner   string `json:"owner_job"`
		Run     string `json:"run_id"`
		SHA     string `json:"request_sha256"`
		Reason  string `json:"reason"`
		Cleanup bool   `json:"cleanup_confirmed"`
	}
	if len(raw) > 65536 || json.Unmarshal(raw, &v) != nil || v.Owner != autoGPURunOwner(x) || v.Run != x.RunID || v.SHA != lease.RequestSHA {
		return errors.New("GPU status binding differs")
	}
	switch v.State {
	case "preparing", "waiting", "running", "stopping":
		x.State = v.State
		x.Reason = v.Reason
		return nil
	case "exited", "cancelled", "timeout", "output_limit", "interrupted", "unavailable":
		if !v.Cleanup {
			x.State = "stopping"
			x.StopRequested = true
			x.Reason = "GPU terminal execution still owns cleanup"
			return nil
		}
		if e = autoVerifyMaintenanceSeal(raw); e != nil {
			return e
		}
		receipt, e := autoGPUDecodeReceipt(raw, lease)
		if e != nil {
			return e
		}
		if e = a.GPUResearch.Record(receipt); e != nil {
			return e
		}
		x.Receipt = string(raw)
		x.State = v.State
		x.Reason = v.Reason
		return nil
	default:
		return errors.New("unknown GPU execution state")
	}
}

func (s *Server) pollAutoGPU(ctx context.Context, a *autoRecord, enabled bool) error {
	return s.pollAutoGPUAt(ctx, a, autoRoot, enabled)
}
func (s *Server) pollAutoGPUAt(ctx context.Context, a *autoRecord, root string, enabled bool) error {
	xs := []*autoGPUExperiment{}
	now := time.Now()
	for _, x := range a.GPUExperiments {
		if !autoGPUFinished(x) && (!enabled || !now.Before(x.NextPoll)) {
			xs = append(xs, x)
		}
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].ObservationAttempts != xs[j].ObservationAttempts {
			return xs[i].ObservationAttempts < xs[j].ObservationAttempts
		}
		return xs[i].ID < xs[j].ID
	})
	var joined error
	for i, x := range xs {
		if i == 4 {
			break
		}
		x.ObservationAttempts++
		x.NextPoll = now.Add(10 * time.Second)
		owner, e := autoGPUCurrentOwner(a, x.OwnerJob)
		permit := enabled && e == nil
		if permit {
			permit = autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{owner.Provider}, now) == nil
		}
		if e = s.saveAuto(a); e != nil {
			return errors.Join(joined, e)
		}
		if e = s.autoGPUAdvance(ctx, a, x, root, permit, !permit); e != nil {
			x.Reason = e.Error()
			joined = errors.Join(joined, e)
		}
		if e = s.saveAuto(a); e != nil {
			return errors.Join(joined, e)
		}
	}
	return joined
}

func autoGPURawView(raw string) any {
	if raw == "" {
		return nil
	}
	return json.RawMessage(raw)
}

// Renewal never rewrites prior execution evidence or reuses a consumed run ID.
// Fresh capture may inspect changed source; the ledger reuses any already executed semantic trial.
func autoGPURenew(x *autoGPUExperiment, owner *autoJob, now time.Time) error {
	if !autoGPUFinished(x) {
		return nil
	}
	if x.RunID != "" {
		var proof struct {
			Cleanup  bool            `json:"cleanup_confirmed"`
			Executed json.RawMessage `json:"executed"`
		}
		if json.Unmarshal([]byte(x.Receipt), &proof) != nil || !proof.Cleanup || (string(proof.Executed) != "null" && string(proof.Executed) != "true" && string(proof.Executed) != "false") {
			return errors.New("GPU prior execution cleanup unconfirmed")
		}
	} else if x.SnapshotReceipt == "" {
		return errors.New("GPU capture has no terminal evidence")
	}

	if now.Before(x.RetryAfter) {
		return errors.New("GPU retry cooling down until " + x.RetryAfter.UTC().Format(time.RFC3339))
	}
	old := *x
	old.History = nil
	raw, _ := json.Marshal(old)
	x.History = append(x.History, string(raw))
	x.RetryCount++
	delay := 30 * time.Second
	for i := 1; i < x.RetryCount && delay < 30*time.Minute; i++ {
		delay *= 2
	}
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	x.RetryAfter = now.Add(delay)
	x.OwnerJob = owner.ID
	request, _ := json.Marshal(map[string]any{"schema_version": 1, "owner_job": owner.ID, "requested_at": now.UTC().Format(time.RFC3339Nano), "input_sha256": x.InputSHA, "source_paths": x.Input.SourcePaths})
	x.SnapshotRequest = string(request)
	x.SnapshotID = autoSHA(request)
	x.SnapshotReceipt = ""
	x.RunID = ""
	x.RunOwner = ""
	x.RunRequest = ""
	x.Receipt = ""
	x.State = "capturing"
	x.Reason = ""
	x.StopRequested = false
	x.StopConfirmed = false
	x.NextPoll = x.RetryAfter
	return nil
}

func autoGPURunOwner(x *autoGPUExperiment) string {
	if x.RunOwner != "" {
		return x.RunOwner
	}
	return x.OwnerJob
}

// This pre-capture guard bounds expensive source preparation before GPU leases
// exist. Corrected process/task identities inherit their admitted assignment.
func autoGPUAssignmentKey(j *autoJob) string {
	if j == nil {
		return ""
	}
	task, project := j.TaskID, int64(0)
	if j.Admission != nil {
		if j.Admission.TaskID > 0 {
			task = j.Admission.TaskID
		}
		project = j.Admission.Proposal.ProjectID
	}
	return autoSHA([]byte(fmt.Sprintf("gpu/assignment:%d/project:%d/role:%s", task, project, j.Role)))
}
func autoGPUExperimentAssignment(a *autoRecord, x *autoGPUExperiment) string {
	if x.AssignmentKey != "" {
		return x.AssignmentKey
	}
	for _, j := range a.Jobs {
		if j.ID == x.OwnerJob {
			return autoGPUAssignmentKey(j)
		}
	}
	return ""
}
func autoGPUPendingAssignment(a *autoRecord, j *autoJob, id string) *autoGPUExperiment {
	key := autoGPUAssignmentKey(j)
	var pending *autoGPUExperiment
	for _, x := range a.GPUExperiments {
		if autoGPUFinished(x) || (x.ID == id && x.OwnerJob == j.ID) {
			continue
		}
		if x.OwnerTask != j.TaskID && autoGPUExperimentAssignment(a, x) != key {
			continue
		}
		if pending == nil || x.ID < pending.ID {
			pending = x
		}
	}
	return pending
}
