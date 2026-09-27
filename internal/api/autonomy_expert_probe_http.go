package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

var autoExpertJobID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type autoExpertProbeRuntime struct {
	GoDependency string `json:"go_dependency_key,omitempty"`
	GoBundle     string `json:"go_bundle_digest,omitempty"`
	GoToolchain  string `json:"go_toolchain_digest,omitempty"`
	NodeBundle   string `json:"node_bundle_key,omitempty"`
	NodeInput    string `json:"node_input_key,omitempty"`
	NodeLock     string `json:"node_lock_sha256,omitempty"`
	NodeRuntime  string `json:"node_runtime_digest,omitempty"`
	PythonBundle string `json:"python_bundle_key"`
	PythonInput  string `json:"python_input_key"`
	Browser      string `json:"browser_key"`
	PythonTest   string `json:"python_test_key"`
}

type autoExpertProbeRequest struct {
	SchemaVersion       int                      `json:"schema_version"`
	OwnerJob            string                   `json:"owner_job"`
	OwnerTask           int64                    `json:"owner_task"`
	Role                string                   `json:"role"`
	Revision            int                      `json:"audit_revision"`
	ProgressKey         string                   `json:"progress_key"`
	RootTaskID          int64                    `json:"root_task_id"`
	SourceJob           string                   `json:"source_job"`
	SourceTaskID        int64                    `json:"source_task_id"`
	SourceSHA           string                   `json:"source_archive_sha256"`
	RootAcceptanceSHA   string                   `json:"root_acceptance_sha256"`
	SourceAcceptanceSHA string                   `json:"source_acceptance_sha256"`
	Profile             string                   `json:"profile"`
	Script              string                   `json:"script"`
	Fixtures            []autoExpertProbeFixture `json:"fixtures"`
	Argv                []string                 `json:"argv"`
	Runtime             autoExpertProbeRuntime   `json:"runtime"`
}

func autoExpertProbeOwner(a *autoRecord, jobID string) (*autoJob, error) {
	if !a.Config.Enabled || a.State == nil || a.State.Phase != autonomy.Audit {
		return nil, errors.New("probe requires enabled audit phase")
	}
	for _, role := range []string{"auditor_a", "auditor_b"} {
		j, e := autoExpertAuditOwner(a, role)
		if e == nil && j.ID == jobID && j.Status == "running" {
			return j, nil
		}
	}
	return nil, errors.New("probe requires current independent auditor")
}

func autoExpertProbeRequestBytes(a *autoRecord, j *autoJob, input *autoExpertProbeInput, testKey string) (autonomy.Proposal, []byte, error) {
	var selected autonomy.Proposal
	count := 0
	for _, p := range a.State.Items {
		if p.ExpertProgressKey == input.ProgressKey && p.ExpertRecoveryTaskID > 0 {
			selected = p
			count++
		}
	}
	if count != 1 {
		return selected, nil, errors.New("probe must select one current admitted plan item")
	}
	pin, e := autoExpertPin(a, selected)
	if e != nil {
		return selected, nil, e
	}
	runtime := autoExpertProbeRuntime{}
	if j.PythonUsedBundle != "" {
		if j.PythonRecovery == nil || j.PythonRecovery.State != "verified" || j.PythonRecovery.BundleKey != j.PythonUsedBundle {
			return selected, nil, errors.New("auditor runtime identity unavailable")
		}
		runtime.PythonBundle = j.PythonUsedBundle
		runtime.PythonInput = j.PythonRecovery.InputKey
		runtime.Browser = j.PythonRecovery.BrowserKey
	} else {
		runtime.PythonTest = testKey
	}
	if e := autoSelectGoTestRuntime(j, &runtime); e != nil {
		return selected, nil, e
	}
	if err := autoUseExpertGoRuntime(a, pin, &runtime); err != nil {
		return selected, nil, err
	}
	if e := autoSelectNodeTestRuntime(j, &runtime); e != nil {
		return selected, nil, e
	}
	if runtime.NodeBundle == "" {
		if e := autoUseExpertNodeSource(a, pin, &runtime); e != nil {
			return selected, nil, e
		}
	}
	fixtures := input.Fixtures
	if fixtures == nil {
		fixtures = []autoExpertProbeFixture{}
	}
	argv := input.Argv
	if argv == nil {
		argv = []string{}
	}
	request := autoExpertProbeRequest{SchemaVersion: 1, OwnerJob: j.ID, OwnerTask: j.TaskID, Role: j.Role, Revision: a.State.Revision, ProgressKey: pin.Key, RootTaskID: pin.RootTaskID, SourceJob: pin.SourceJob, SourceTaskID: pin.SourceTaskID, SourceSHA: pin.SourceSHA, RootAcceptanceSHA: pin.AcceptanceSHA, SourceAcceptanceSHA: pin.SourceAcceptanceSHA, Profile: input.Profile, Script: input.Script, Fixtures: fixtures, Argv: argv, Runtime: runtime}
	raw, e := json.Marshal(request)
	if e == nil && len(raw) > autoExpertInputLimit {
		e = errors.New("sealed probe request exceeds size limit")
	}
	return selected, raw, e
}

// Write-once controller input; the runner rechecks ownership and seals these
// exact bytes before asynchronous preparation. Worker-writable work is excluded.
func autoWriteExpertProbeRequest(root string, lease *autoExpertProbeLease, raw []byte) error {
	if lease == nil || !autoExpertJobID.MatchString(lease.OwnerJob) || !autoHash256(lease.ID) || autoSHA(raw) != lease.RequestKey {
		return errors.New("probe request identity mismatch")
	}
	dir := filepath.Join(root, lease.OwnerJob, "expert-probe-requests")
	if e := os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
		return e
	}
	info, e := os.Lstat(dir)
	if e != nil {
		return e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe probe request directory")
	}
	name := filepath.Join(dir, lease.ID+".json")
	f, e := os.CreateTemp(dir, ".probe-request-")
	if e != nil {
		return e
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	// Link publishes a complete inode without replacing an existing request.
	if e = os.Link(temporary, name); os.IsExist(e) {
		old, readErr := autoReadRegular(name, autoExpertInputLimit)
		if readErr != nil {
			return readErr
		}
		if string(old) != string(raw) {
			return errors.New("immutable probe request changed")
		}
		return nil
	} else if e != nil {
		return e
	}
	directory, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Server) autoExpertProbeBridge(jobID string, w http.ResponseWriter, r *http.Request) {
	s.autoExpertProbeBridgeAt(autoRoot, jobID, w, r)
}

// Root is controller configuration, never a worker-supplied filesystem path.
func (s *Server) autoExpertProbeBridgeAt(root, jobID string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.autoExpertProbeRead(jobID, w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "probe input belongs in JSON body", 400)
		return
	}
	input, e := autoDecodeExpertProbeInput(r.Body)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	s.autoMu.Lock()
	a, e := s.loadAuto()
	var owner *autoJob
	if e == nil {
		owner, e = autoExpertProbeOwner(a, jobID)
	}
	if e != nil {
		s.autoMu.Unlock()
		http.Error(w, "probe owner is not a current admitted auditor", 409)
		return
	}
	ready, preflightErr := s.prepareAutoExpertGoRuntime(r.Context(), a, owner, input.ProgressKey)
	if preflightErr != nil || !ready {
		detail := autoExpertGoPreflightView(a, input.ProgressKey)
		s.autoMu.Unlock()
		if preflightErr != nil {
			http.Error(w, preflightErr.Error(), 409)
		} else {
			writeJSON(w, 202, map[string]any{"state": "preparing_go_runtime", "runtime_preflight": detail, "reason": "Exact historical source receives a new isolated experiment environment; retry this same POST after preflight. No test execution has been reserved."})
		}
		return
	}
	testKey := ""
	testInfo := autoPythonTestRuntime(filepath.Join(filepath.Dir(root), "dependencies", "python"))
	if value, ok := testInfo["key"].(string); ok && autoHash256(value) {
		testKey = value
	}
	p, raw, e := autoExpertProbeRequestBytes(a, owner, input, testKey)
	var lease *autoExpertProbeLease
	if e == nil {
		lease, e = autoReserveExpertProbe(a, p, owner.Role, autoSHA(raw), []string{owner.Provider}, time.Now())
	}
	if e == nil {
		e = autoBindExpertNodeRuntime(lease, raw)
	}
	if e == nil {
		e = s.saveAuto(a)
	} // Reservation precedes every filesystem/process side effect.
	if e == nil {
		e = autoWriteExpertProbeRequest(root, lease, raw)
	}
	s.autoMu.Unlock()
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	// OFF cancellation persists first and the runner shares its start/stop guard.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, e := s.runAutoCommand(ctx, "expert-probe", "--job", jobID, "--probe-id", lease.ID)
	if e != nil {
		writeJSON(w, 202, map[string]any{"probe_id": lease.ID, "state": "pending", "reason": "runner observation unavailable; retain this ID and poll, do not submit a replacement"})
		return
	}
	var body map[string]any
	if len(result) > 256<<10 || json.Unmarshal(result, &body) != nil || body["probe_id"] != lease.ID {
		http.Error(w, fmt.Sprintf("invalid probe observation; retain ID %s", lease.ID), 503)
		return
	}
	writeJSON(w, 202, body)
}

func autoExpertWorkerLease(a *autoRecord, jobID, id string) (*autoExpertProbeLease, error) {
	if !autoHash256(id) || a.ExpertRecovery == nil {
		return nil, errors.New("probe not found")
	}
	lease := a.ExpertRecovery.Probes[id]
	if lease == nil {
		return nil, errors.New("probe not found")
	}
	if lease.OwnerJob == jobID {
		return lease, nil
	}
	owner, e := autoExpertProbeOwner(a, jobID)
	if e != nil || lease.Cycle != a.State.Cycle || lease.Revision != a.State.Revision || !autoExpertProbeOwnerValid(a, lease, owner) {
		return nil, errors.New("probe belongs to another audit assignment")
	}
	return lease, nil
}

func (s *Server) autoExpertProbeRead(jobID string, w http.ResponseWriter, r *http.Request) {
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		http.Error(w, "invalid probe query", 400)
		return
	}
	if len(query) == 0 || (len(query) == 1 && len(query["after"]) == 1) {
		after := query.Get("after")
		if after != "" && !autoHash256(after) {
			http.Error(w, "invalid probe cursor", 400)
			return
		}
		s.autoMu.Lock()
		a, err := s.loadAuto()
		if err == nil {
			_, err = autoExpertProbeOwner(a, jobID)
		}
		if err != nil {
			s.autoMu.Unlock()
			http.Error(w, "probe discovery requires current auditor", 409)
			return
		}
		body := autoExpertProbeDiscovery(a, jobID, after)
		s.autoMu.Unlock()
		writeJSON(w, 200, body)
		return
	}
	for k, v := range query {
		if len(v) != 1 || (k != "id" && k != "stream" && k != "offset") {
			http.Error(w, "invalid probe selector", 400)
			return
		}
	}
	id := query.Get("id")
	stream := query.Get("stream")
	offset := 0
	if !autoHash256(id) || (stream != "" && stream != "stdout" && stream != "stderr") {
		http.Error(w, "expected probe ID and optional output stream", 400)
		return
	}
	if v, ok := query["offset"]; ok {
		offset, e = strconv.Atoi(v[0])
		if e != nil || offset < 0 || offset > 16<<20 || stream == "" {
			http.Error(w, "invalid output offset", 400)
			return
		}
	}
	s.autoMu.Lock()
	a, e := s.loadAuto()
	var lease *autoExpertProbeLease
	if e == nil {
		lease, e = autoExpertWorkerLease(a, jobID, id)
	}
	if e != nil {
		s.autoMu.Unlock()
		http.Error(w, "probe unavailable for this assignment", 404)
		return
	}
	owner, requestKey := lease.OwnerJob, lease.RequestKey
	s.autoMu.Unlock()
	args := []string{"expert-probe-status", "--job", owner, "--probe-id", id}
	if stream != "" {
		args = []string{"expert-probe-output", "--job", owner, "--probe-id", id, "--stream", stream, "--offset", strconv.Itoa(offset)}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	raw, e := s.runAutoCommand(ctx, args...)
	if e != nil {
		writeJSON(w, 503, map[string]any{"probe_id": id, "reason": "observation unavailable; preserve this probe ID", "retryable": true})
		return
	}
	if len(raw) > 256<<10 {
		http.Error(w, "probe observation exceeds bounds", 502)
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["probe_id"] != id {
		http.Error(w, "probe observation identity invalid", 502)
		return
	}
	if stream != "" {
		if body["stream"] != stream {
			http.Error(w, "output stream identity invalid", 502)
			return
		}
		writeJSON(w, 200, body)
		return
	}
	if body["request_key"] != requestKey {
		http.Error(w, "probe request identity invalid", 502)
		return
	}
	state, _ := body["state"].(string)
	terminal := state == "exited" || state == "timeout" || state == "output_limit" || state == "cancelled" || state == "interrupted" || state == "unavailable" || state == "failed"
	if terminal {
		var receipt autoExpertProbeReceipt
		if e = json.Unmarshal(raw, &receipt); e != nil {
			http.Error(w, "invalid execution receipt", 502)
			return
		}
		s.autoMu.Lock()
		current, loadErr := s.loadAuto()
		if loadErr == nil {
			_, loadErr = autoExpertWorkerLease(current, jobID, id)
		}
		if loadErr == nil {
			loadErr = autoRecordExpertProbe(current, receipt)
		}
		if loadErr == nil {
			loadErr = s.saveAuto(current)
		}
		s.autoMu.Unlock()
		if loadErr != nil {
			http.Error(w, "execution receipt could not be recorded: "+loadErr.Error(), 409)
			return
		}
	} else if state == "waiting" {
		body["resume"] = "If preparation never started, re-submit the same input; do not change scripts or allocate replacement probes solely because observation was interrupted"
	}
	writeJSON(w, 200, body)
}

// Report correction can replace the process UUID while retaining its audit
// assignment. Discovery preserves those probes without exposing peer evidence.
func autoExpertProbeDiscovery(a *autoRecord, jobID, after string) map[string]any {
	ids := []string{}
	if a.ExpertRecovery != nil {
		for id := range a.ExpertRecovery.Probes {
			if id > after {
				if _, err := autoExpertWorkerLease(a, jobID, id); err == nil {
					ids = append(ids, id)
				}
			}
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > 25 {
		ids = ids[:25]
		next = ids[24]
	}
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		lease := a.ExpertRecovery.Probes[id]
		items = append(items, map[string]any{"probe_id": id, "progress_key": lease.ProgressKey, "request_key": lease.RequestKey, "owner_job": lease.OwnerJob, "created_at": lease.CreatedAt, "receipt_recorded": lease.Receipt != nil, "diagnostic_recorded": lease.Diagnostic != nil})
	}
	return map[string]any{"items": items, "next_after": next}
}
