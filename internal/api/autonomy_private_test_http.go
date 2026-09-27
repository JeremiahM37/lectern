package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

func (s *Server) autoPrivateTestBridge(jobID string, w http.ResponseWriter, r *http.Request) {
	s.autoPrivateTestBridgeAt(autoRoot, jobID, w, r)
}

func (s *Server) autoPrivateTestBridgeAt(root, jobID string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.autoPrivateTestRead(jobID, w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "test input belongs in JSON body", 400)
		return
	}
	input, err := autoDecodePrivateTestInput(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.autoMu.Lock()
	a, err := s.loadAuto()
	var j *autoJob
	var v *autoPrivateIntegrationAttempt
	if err == nil {
		j, v, err = autoPrivateTestOwner(a, jobID)
	}
	if err != nil {
		s.autoMu.Unlock()
		http.Error(w, "test owner is not the current independent reviewer", 409)
		return
	}
	testKey := ""
	if value, ok := autoPythonTestRuntime(filepath.Join(filepath.Dir(root), "dependencies", "python"))["key"].(string); ok && autoHash256(value) {
		testKey = value
	}
	if j.PythonExpectedTestKey != "" {
		if !autoHash256(j.PythonExpectedTestKey) {
			s.autoMu.Unlock()
			http.Error(w, "reviewer expected test tooling identity invalid", 409)
			return
		}
		testKey = j.PythonExpectedTestKey
	}
	raw, err := autoPrivateTestRequestBytes(a, j, input, testKey)
	var lease *autoPrivateTestLease
	if err == nil {
		lease, err = autoReservePrivateTest(a, j, autoSHA(raw), time.Now())
	}
	if err == nil {
		if len(lease.RequestRaw) > 0 && string(lease.RequestRaw) != string(raw) {
			err = errors.New("private test request changed after reservation")
		} else {
			lease.RequestRaw = append(json.RawMessage(nil), raw...)
		}
	}
	if err == nil {
		err = s.saveAuto(a)
	}
	if err == nil {
		err = autoWritePrivateRequestAt(root, jobID, v.ID, "check-"+lease.ID, raw)
	}
	if err != nil {
		s.autoMu.Unlock()
		http.Error(w, err.Error(), 409)
		return
	}
	id, check, generation := v.ID, lease.ID, lease.Generation
	s.autoMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.runAutoCommand(ctx, "integration-test", "--job", jobID, "--integration-id", id, "--check-id", check, "--generation", strconv.Itoa(generation))
	if err != nil {
		writeJSON(w, 202, map[string]any{"integration_id": id, "check_id": check, "state": "pending", "reason": "Observation unavailable; preserve this check ID and poll before considering a retry"})
		return
	}
	var body map[string]any
	if len(result) > 256<<10 || json.Unmarshal(result, &body) != nil || body["integration_id"] != id || body["check_id"] != check || body["generation"] != float64(generation) {
		http.Error(w, "test observation identity invalid; retain check ID "+check, 502)
		return
	}
	writeJSON(w, 202, body)
}

// Read scopes survive a terminal old process and same-task report correction,
// but never grant another reviewer access to this assignment's private checks.
func autoPrivateWorkerTest(a *autoRecord, jobID, check string) (*autoPrivateIntegrationAttempt, *autoPrivateTestLease, error) {
	if !autoHash256(check) {
		return nil, nil, errors.New("invalid test identity")
	}
	for _, j := range a.Jobs {
		if j.ID != jobID || j.Role != "reviewer" {
			continue
		}
		v, err := autoPrivateAttempt(a, j)
		if err != nil {
			return nil, nil, err
		}
		lease := v.TestLeases[check]
		if lease == nil || lease.OwnerTask != j.TaskID || v.Candidate == nil || lease.CandidateSHA != v.Candidate.TreeSHA {
			return nil, nil, errors.New("test does not belong to this candidate review")
		}
		if lease.OwnerJob == jobID {
			return v, lease, nil
		}
		if v.ReviewerJob == jobID && v.ReviewerTaskID == j.TaskID {
			for _, old := range a.Jobs {
				if old.ID == lease.OwnerJob && old.TaskID == j.TaskID && old.Role == "reviewer" && (old.Status == "done" || old.Status == "failed" || old.Status == "stopped") {
					return v, lease, nil
				}
			}
		}
	}
	return nil, nil, errors.New("test belongs to another review")
}

func autoPrivateTestDiscovery(a *autoRecord, jobID, after string) []map[string]any {
	ids := []string{}
	if a.PrivateIntegration != nil {
		for _, run := range a.PrivateIntegration.Runs {
			for _, v := range run.Attempts {
				for id := range v.TestLeases {
					if id > after {
						if _, _, err := autoPrivateWorkerTest(a, jobID, id); err == nil {
							ids = append(ids, id)
						}
					}
				}
			}
		}
	}
	sort.Strings(ids)
	if len(ids) > 26 {
		ids = ids[:26]
	}
	rows := []map[string]any{}
	for _, id := range ids {
		v, l, _ := autoPrivateWorkerTest(a, jobID, id)
		rows = append(rows, map[string]any{"check_id": id, "integration_id": v.ID, "candidate_tree_sha256": l.CandidateSHA, "owner_job": l.OwnerJob, "receipt_recorded": l.Receipt != nil})
	}
	return rows
}

func (s *Server) autoPrivateTestRead(jobID string, w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "invalid test query", 400)
		return
	}
	for key, values := range query {
		if len(values) != 1 || (key != "id" && key != "after" && key != "stream" && key != "offset") {
			http.Error(w, "invalid test selector", 400)
			return
		}
	}
	check, stream, after := query.Get("id"), query.Get("stream"), query.Get("after")
	if check == "" {
		if len(query) > 1 || (len(query) == 1 && len(query["after"]) != 1) || (after != "" && !autoHash256(after)) {
			http.Error(w, "invalid test discovery selector", 400)
			return
		}
		s.autoMu.Lock()
		a, e := s.loadAuto()
		if e != nil {
			s.autoMu.Unlock()
			http.Error(w, "test discovery unavailable", 503)
			return
		}
		rows := autoPrivateTestDiscovery(a, jobID, after)
		s.autoMu.Unlock()
		next := ""
		if len(rows) > 25 {
			rows = rows[:25]
			next = rows[24]["check_id"].(string)
		}
		writeJSON(w, 200, map[string]any{"items": rows, "next_after": next})
		return
	}
	if !autoHash256(check) || len(query["after"]) != 0 || (stream != "" && stream != "stdout" && stream != "stderr") {
		http.Error(w, "invalid test identity or stream", 400)
		return
	}
	offset := 0
	if values, ok := query["offset"]; ok {
		offset, err = strconv.Atoi(values[0])
		if err != nil || offset < 0 || offset > 16<<20 || stream == "" {
			http.Error(w, "invalid test output offset", 400)
			return
		}
	}
	s.autoMu.Lock()
	a, err := s.loadAuto()
	var v *autoPrivateIntegrationAttempt
	var lease *autoPrivateTestLease
	if err == nil {
		v, lease, err = autoPrivateWorkerTest(a, jobID, check)
	}
	if err != nil {
		s.autoMu.Unlock()
		http.Error(w, "test unavailable for this review", 404)
		return
	}
	id, owner, request, generation := v.ID, lease.OwnerJob, lease.RequestSHA, v.Generation
	leaseSnapshot := *lease
	s.autoMu.Unlock()
	command := "integration-test-status"
	args := []string{"--job", owner, "--integration-id", id, "--check-id", check, "--generation", strconv.Itoa(generation)}
	if stream != "" {
		command = "integration-test-output"
		args = append(args, "--stream", stream, "--offset", strconv.Itoa(offset))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	raw, err := s.runAutoCommand(ctx, append([]string{command}, args...)...)
	if err != nil {
		writeJSON(w, 503, map[string]any{"check_id": check, "retryable": true, "reason": "Observation unavailable; preserve this check ID"})
		return
	}
	var body map[string]any
	if len(raw) > 256<<10 || json.Unmarshal(raw, &body) != nil || body["integration_id"] != id || body["check_id"] != check {
		http.Error(w, "test observation identity invalid", 502)
		return
	}
	if body["generation"] != float64(generation) {
		http.Error(w, "test observation generation mismatch", 502)
		return
	}
	if stream != "" {
		if body["stream"] != stream {
			http.Error(w, "test output stream mismatch", 502)
			return
		}
		writeJSON(w, 200, body)
		return
	}
	if body["request_sha256"] != request {
		http.Error(w, "test request binding mismatch", 502)
		return
	}
	state, _ := body["state"].(string)
	terminal := state == "exited" || state == "timeout" || state == "output_limit" || state == "cancelled" || state == "interrupted" || state == "unavailable" || state == "failed" || state == "rejected"
	if terminal {
		receipt, decodeErr := autoDecodeBoundPrivateTestReceipt(raw, id, &leaseSnapshot, generation)
		if decodeErr != nil {
			http.Error(w, "invalid execution receipt", 502)
			return
		}
		s.autoMu.Lock()
		current, e := s.loadAuto()
		if e == nil {
			_, _, e = autoPrivateWorkerTest(current, jobID, check)
		}
		if e == nil {
			e = autoRecordPrivateTest(current, receipt)
		}
		if e == nil {
			e = s.saveAuto(current)
		}
		s.autoMu.Unlock()
		if e != nil {
			http.Error(w, "test receipt could not be recorded: "+e.Error(), 409)
			return
		}
	}
	writeJSON(w, 200, body)
}

// Missing execution accounting must never release a reserved test budget.
// In particular, absent JSON fields are not evidence of a zero-cost failure.
func autoDecodePrivateTestReceipt(raw []byte) (autoPrivateTestReceipt, error) {
	var receipt autoPrivateTestReceipt
	var accounting struct {
		Executed  *bool  `json:"executed"`
		Charged   *int64 `json:"charged_ms"`
		Truncated *bool  `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &accounting); err != nil {
		return receipt, err
	}
	if accounting.Executed == nil || accounting.Charged == nil || *accounting.Charged < 0 || *accounting.Charged > 610000 {
		return receipt, errors.New("missing or invalid execution accounting")
	}
	if *accounting.Executed && accounting.Truncated == nil {
		return receipt, errors.New("missing execution output completeness")
	}
	err := json.Unmarshal(raw, &receipt)
	return receipt, err
}
