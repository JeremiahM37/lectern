package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestGPUInputRejectsAuthorityAndAmbiguity(t *testing.T) {
	for _, raw := range []string{`{"script":"x","script":"y"}`, `{"script":"x","target":"other"}`, `{"script":"x","source_paths":["../secret"]}`, `{"script":"x","source_paths":["src","src/a"]}`, `{"script":"x","trial":5}`, `{"script":"x"} {}`} {
		if _, err := autoGPUInput([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	got, err := autoGPUInput([]byte(`{"script":"print(1)","source_paths":["tests","src"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.SourcePaths, ",") != "src,tests" || got.Argv == nil {
		t.Fatal("input not normalized")
	}
}
func TestGPURequestPublicationImmutableAndSymlinkSafe(t *testing.T) {
	root := t.TempDir()
	job := "11111111-1111-1111-1111-111111111111"
	id := strings.Repeat("a", 64)
	raw := "{\"script\":\"a < b\"}\n"
	if err := autoGPUWrite(root, job, "gpu-requests", id, raw); err != nil {
		t.Fatal(err)
	}
	if err := autoGPUWrite(root, job, "gpu-requests", id, raw); err != nil {
		t.Fatal(err)
	}
	if err := autoGPUWrite(root, job, "gpu-requests", id, "{}"); err == nil {
		t.Fatal("immutable request replaced")
	}
	path := filepath.Join(root, job, "gpu-requests", id+".json")
	got, _ := os.ReadFile(path)
	if string(got) != raw {
		t.Fatal("exact bytes lost")
	}
	other := strings.Repeat("b", 64)
	if err := os.Symlink(path, filepath.Join(filepath.Dir(path), other+".json")); err != nil {
		t.Fatal(err)
	}
	if err := autoGPUWrite(root, job, "gpu-requests", other, raw); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestGPUHTTPStatusNeverStartsAndRejectsForeignReceipt(t *testing.T) {
	s, a, _ := documentationFixture(t)
	j := a.Jobs[0]
	id := strings.Repeat("a", 64)
	snapshot := strings.Repeat("b", 64)
	x := &autoGPUExperiment{ID: id, OwnerJob: j.ID, OwnerTask: j.TaskID, SnapshotID: snapshot, State: "capturing"}
	a.GPUExperiments = map[string]*autoGPUExperiment{id: x}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("GPU_CALLS", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	response := `{"state":"preparing","owner_job":"` + j.ID + `","snapshot_id":"` + snapshot + `"}`
	t.Setenv("GPU_RESPONSE", response)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GPU_CALLS\"\nprintf '%s' \"$GPU_RESPONSE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.autoGPUBridgeAt(t.TempDir(), j.ID, w, httptest.NewRequest("GET", "/research-runs?id="+id, nil))
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "gpu-source-status") || strings.Contains(string(calls), "prepare") {
		t.Fatalf("status launched: %s", calls)
	}
	t.Setenv("GPU_RESPONSE", `{"state":"preparing","owner_job":"foreign","snapshot_id":"`+snapshot+`"}`)
	loaded, _ := s.loadAuto()
	if err := s.autoGPUAdvance(context.Background(), loaded, loaded.GPUExperiments[id], t.TempDir(), false, false); err == nil {
		t.Fatal("foreign receipt accepted")
	}
	for _, query := range []string{"?id=" + id + "&id=" + id, "?id=" + id + "&path=/etc/passwd", "?id=../x"} {
		w = httptest.NewRecorder()
		s.autoGPUBridgeAt(t.TempDir(), j.ID, w, httptest.NewRequest("GET", "/research-runs"+query, nil))
		if w.Code != 400 {
			t.Fatalf("selector accepted %s: %d", query, w.Code)
		}
	}
}
func TestGPURenewRetainsExactEvidenceAndRequiresCleanup(t *testing.T) {
	_, j, _, q := gpuAPIContext()
	now := time.Now().UTC()
	x := &autoGPUExperiment{OwnerTask: j.TaskID, OwnerJob: j.ID, Input: autoGPUExperimentInput{Script: "x", SourcePaths: []string{"src"}}, Runtime: q, InputSHA: strings.Repeat("a", 64), State: "cancelled", SnapshotID: strings.Repeat("b", 64), SnapshotRequest: "old exact\n", SnapshotReceipt: `{"state":"cancelled"}`}
	if err := autoGPURenew(x, j, now); err != nil {
		t.Fatal(err)
	}
	if len(x.History) != 1 || !strings.Contains(x.History[0], `old exact\n`) || x.SnapshotID == strings.Repeat("b", 64) || x.StopRequested {
		t.Fatal("renewal lost old intent")
	}
	x.State = "cancelled"
	x.SnapshotReceipt = `{"state":"cancelled"}`
	if err := autoGPURenew(x, j, now.Add(time.Second)); err == nil {
		t.Fatal("cooldown bypass")
	}
	x.RunID = strings.Repeat("c", 64)
	x.Receipt = `{"executed":true,"cleanup_confirmed":false}`
	if err := autoGPURenew(x, j, now.Add(time.Hour)); err == nil {
		t.Fatal("unclean execution renewed")
	}
	x.Receipt = `{"executed":true,"cleanup_confirmed":true}`
	if err := autoGPURenew(x, j, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if x.RunID != "" || x.NextPoll.Before(x.RetryAfter) {
		t.Fatal("fresh capture bypassed cooldown or retained old execution")
	}
}

func TestGPUHTTPPostPersistsBoundIntentBeforeHandshakeAndOFFRefuses(t *testing.T) {
	s, a, _ := documentationFixture(t)
	j := a.Jobs[0]
	j.Status = "running"
	j.Provider = "codex"
	a.State.Assignments = []autonomy.Assignment{{TaskID: j.TaskID, Role: j.Role}}
	expertQuota(a, time.Now())
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("GPU_ROOT", root)
	t.Setenv("GPU_CALLS", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	script := `#!/usr/bin/python3
import json,os,pathlib,sys
args=sys.argv
job=args[args.index('--job')+1];sid=args[args.index('--gpu-id')+1]
p=pathlib.Path(os.environ['GPU_ROOT'])/job/'gpu-snapshot-requests'/(sid+'.json')
r=json.loads(p.read_text())
assert r['owner_job']==job and r['source_paths']==['src']
with open(os.environ['GPU_CALLS'],'a') as f:f.write(sid+'\n')
print(json.dumps({'state':'preparing','owner_job':job,'snapshot_id':sid}))
`
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, _, _, q := gpuAPIContext()
	qualification := func(string) (autoGPUQualifiedRuntime, error) { return q, nil }
	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.autoGPUBridgeQualified(root, j.ID, w, httptest.NewRequest("POST", "/research-runs", strings.NewReader(`{"script":"print(1)","source_paths":["src"]}`)), qualification)
		return w
	}
	w := post()
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	loaded, _ := s.loadAuto()
	if len(loaded.GPUExperiments) != 1 || len(loaded.GPUResearch.Runs) != 0 {
		t.Fatal("GPU reserved before capture")
	}
	var first *autoGPUExperiment
	for _, x := range loaded.GPUExperiments {
		first = x
	}
	if first.SnapshotID != autoSHA([]byte(first.SnapshotRequest)) {
		t.Fatal("source intent not exact")
	}
	w = post()
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	loaded, _ = s.loadAuto()
	if len(loaded.GPUExperiments) != 1 {
		t.Fatal("duplicate intent")
	}
	calls, _ := os.ReadFile(log)
	if strings.TrimSpace(string(calls)) != first.SnapshotID+"\n"+first.SnapshotID {
		t.Fatalf("not same capture retry: %s", calls)
	}
	conflict := httptest.NewRecorder()
	s.autoGPUBridgeQualified(root, j.ID, conflict, httptest.NewRequest("POST", "/research-runs", strings.NewReader(`{"script":"print(2)","source_paths":["src"]}`)), qualification)
	var busy struct {
		State string `json:"state"`
		ID    string `json:"existing_experiment_id"`
		Input string `json:"existing_input_sha256"`
	}
	if conflict.Code != 409 || json.Unmarshal(conflict.Body.Bytes(), &busy) != nil || busy.State != "assignment_busy" || busy.ID != first.ID || busy.Input != first.InputSHA {
		t.Fatalf("different input not explicitly refused: %d %s", conflict.Code, conflict.Body.String())
	}
	afterConflict, _ := os.ReadFile(log)
	if string(afterConflict) != string(calls) {
		t.Fatal("second input launched source preparation")
	}
	loaded, _ = s.loadAuto()
	if len(loaded.GPUExperiments) != 1 {
		t.Fatal("conflicting input persisted another experiment")
	}
	loaded.Config.Enabled = false
	if err := s.saveAuto(loaded); err != nil {
		t.Fatal(err)
	}
	w = post()
	if w.Code != 409 {
		t.Fatal("OFF admitted new work")
	}
	after, _ := os.ReadFile(log)
	if string(after) != string(calls) {
		t.Fatal("OFF handshake occurred")
	}
}

func TestGPUSchedulerOFFPublishesBeforeStopAndRetainsSealedEvidence(t *testing.T) {
	s, a, _ := documentationFixture(t)
	j := a.Jobs[0]
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	raw := `{"schema_version":1,"owner_job":"` + j.ID + `","requested_at":"now","input_sha256":"` + id + `","source_paths":["src"]}`
	x := &autoGPUExperiment{ID: id, OwnerJob: j.ID, OwnerTask: j.TaskID, SnapshotID: autoSHA([]byte(raw)), SnapshotRequest: raw, State: "capturing"}
	a.GPUExperiments = map[string]*autoGPUExperiment{id: x}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("GPU_ROOT", root)
	t.Setenv("GPU_CALLS", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	script := `#!/usr/bin/python3
import json,os,pathlib,sys,hashlib
args=sys.argv; job=args[args.index('--job')+1];sid=args[args.index('--gpu-id')+1]
p=pathlib.Path(os.environ['GPU_ROOT'])/job/'gpu-snapshot-requests'/(sid+'.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()==sid
with open(os.environ['GPU_CALLS'],'a') as f:f.write(' '.join(args)+'\n')
v={'owner_job':job,'snapshot_id':sid}
if 'gpu-source-stop' in args:v['state']='stopped'
elif 'gpu-source-status' in args:
 v.update(state='cancelled',executed=False)
 v['receipt_sha256']=hashlib.sha256(json.dumps(v,sort_keys=True,separators=(',',':')).encode()).hexdigest()
else:raise Exception('OFF started work')
print(json.dumps(v))
`
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.pollAutoGPUAt(context.Background(), a, root, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	done := loaded.GPUExperiments[id]
	if !done.StopRequested || !done.StopConfirmed || done.State != "cancelled" || done.SnapshotReceipt == "" || autoGPUPending(loaded) {
		t.Fatal("OFF failed durable cleanup")
	}
	before, _ := os.ReadFile(log)
	if err := s.pollAutoGPUAt(context.Background(), loaded, root, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(log)
	if string(before) != string(after) {
		t.Fatal("terminal cleanup repeatedly stopped")
	}
	if done.SnapshotRequest != raw {
		t.Fatal("request changed after reload")
	}
}

func TestGPUReusedExecutionUsesOriginalOwnerAndStatusOnly(t *testing.T) {
	s, a, _ := documentationFixture(t)
	ctx, owner, source, q := gpuAPIContext()
	req, err := autoGPUAdmittedRequest(ctx, owner, source, strings.Repeat("a", 64), q, autoGPUExperimentInput{Script: "print(1)"})
	if err != nil {
		t.Fatal(err)
	}
	a.GPUResearch = &autonomy.GPUResearchLedger{}
	lease, err := a.GPUResearch.Reserve(req, q.RuntimeKey, q.ReceiptSHA, time.Now(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"owner_job": owner.ID, "run_id": lease.ID, "request_sha256": lease.RequestSHA, "state": "exited", "executed": true, "elapsed_ms": 17, "charged_ms": 17, "exit_code": 0, "output_sha256": strings.Repeat("c", 64), "cleanup_confirmed": true}
	seal, _ := json.Marshal(data)
	data["receipt_sha256"] = autoSHA(seal)
	raw, _ := json.Marshal(data)
	receipt, err := autoGPUDecodeReceipt(raw, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.GPUResearch.Record(receipt); err != nil {
		t.Fatal(err)
	}
	x := &autoGPUExperiment{ID: strings.Repeat("d", 64), OwnerJob: source.ID, OwnerTask: owner.TaskID, RunOwner: owner.ID, RunID: lease.ID, RunRequest: string(lease.RequestRaw), State: "reserved"}
	a.GPUExperiments = map[string]*autoGPUExperiment{x.ID: x}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("GPU_CALLS", log)
	t.Setenv("GPU_RESPONSE", string(raw))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if err = os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GPU_CALLS\"\nprintf '%s' \"$GPU_RESPONSE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.autoGPUAdvance(context.Background(), a, x, t.TempDir(), true, false); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "gpu-research-status") || !strings.Contains(string(calls), owner.ID) || strings.Contains(string(calls), source.ID) || x.State != "exited" || x.Receipt != string(raw) {
		t.Fatalf("wrong replay: %s %s", calls, x.State)
	}
}

func TestGPUSchedulerFailedCleanupIsFairAcrossReload(t *testing.T) {
	s, a, _ := documentationFixture(t)
	j := a.Jobs[0]
	a.GPUExperiments = map[string]*autoGPUExperiment{}
	for i := 0; i < 5; i++ {
		raw := string(rune('a' + i))
		id := autoSHA([]byte(raw))
		a.GPUExperiments[id] = &autoGPUExperiment{ID: id, OwnerJob: j.ID, OwnerTask: j.TaskID, SnapshotID: id, SnapshotRequest: raw, State: "capturing"}
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("GPU_CALLS", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GPU_CALLS\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := s.pollAutoGPUAt(context.Background(), a, root, false); err == nil {
		t.Fatal("transport failure concealed")
	}
	loaded, _ := s.loadAuto()
	untried := ""
	for _, x := range loaded.GPUExperiments {
		if x.ObservationAttempts == 0 {
			untried = x.ID
		}
	}
	if untried == "" {
		t.Fatal("per-tick bound exceeded")
	}
	_ = s.pollAutoGPUAt(context.Background(), loaded, root, false)
	if loaded.GPUExperiments[untried].ObservationAttempts != 1 {
		t.Fatal("earlier failures starved fifth cleanup")
	}
	if !autoGPUPending(loaded) {
		t.Fatal("failed cleanup dropped ownership")
	}
}

func TestGPUPendingAssignmentSurvivesProcessCorrectionAndHistoryPruning(t *testing.T) {
	a, owner, _, _ := gpuAPIContext()
	x := &autoGPUExperiment{AssignmentKey: autoGPUAssignmentKey(owner), ID: strings.Repeat("a", 64), OwnerJob: owner.ID, OwnerTask: owner.TaskID, State: "capturing"}
	a.GPUExperiments = map[string]*autoGPUExperiment{x.ID: x}
	retry := *owner
	retry.ID = "33333333-3333-3333-3333-333333333333"
	retry.TaskID = 99
	// Original process history can be pruned without reopening source fanout.
	a.Jobs = []*autoJob{&retry}
	for _, state := range []string{"capturing", "captured", "reserved", "preparing", "running", "stopping"} {
		x.State = state
		if got := autoGPUPendingAssignment(a, &retry, strings.Repeat("b", 64)); got != x {
			t.Fatalf("correction bypassed pending %s", state)
		}
	}
	if autoGPUPendingAssignment(a, owner, x.ID) != nil {
		t.Fatal("same-owner identical retry blocked")
	}
	if autoGPUPendingAssignment(a, &retry, x.ID) != x {
		t.Fatal("new process launched original process's pending capture")
	}
	independent := retry
	copyAdmission := *retry.Admission
	copyAdmission.TaskID = 100
	independent.Admission = &copyAdmission
	independent.TaskID = 100
	if autoGPUPendingAssignment(a, &independent, strings.Repeat("b", 64)) != nil {
		t.Fatal("unrelated assignment blocked")
	}
	x.State = "cancelled"
	if autoGPUPendingAssignment(a, &retry, strings.Repeat("b", 64)) != nil {
		t.Fatal("settled ownership never releases capture slot")
	}
}

func gpuHTTPSeal(t *testing.T, v map[string]any) []byte {
	t.Helper()
	delete(v, "receipt_sha256")
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	v["receipt_sha256"] = autoSHA(raw)
	raw, err = json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestGPUQualificationRequiresExactFourMeasuredHelpers(t *testing.T) {
	hash := strings.Repeat("a", 64)
	helpers := map[string]string{"supervisor_sha256": hash, "executor_sha256": hash, "lease_sha256": hash, "telemetry_sha256": hash}
	q := map[string]any{"schema_version": 1, "qualified": true, "target": autonomy.GPUResearchTarget, "profile": "gpu-screen600", "runtime_key": hash, "device_bdf": "0000:f4:00.0", "kernel_release": "test-kernel", "driver_sha256": hash, "helpers": helpers}
	if _, err := autoGPUQualificationRaw(gpuHTTPSeal(t, q)); err != nil {
		t.Fatal(err)
	}
	delete(helpers, "supervisor_sha256")
	if _, err := autoGPUQualificationRaw(gpuHTTPSeal(t, q)); err == nil {
		t.Fatal("unmeasured supervisor accepted")
	}
	helpers["supervisor_sha256"] = "bad"
	if _, err := autoGPUQualificationRaw(gpuHTTPSeal(t, q)); err == nil {
		t.Fatal("invalid supervisor digest accepted")
	}
	helpers["supervisor_sha256"] = hash
	helpers["unrecognized_sha256"] = hash
	if _, err := autoGPUQualificationRaw(gpuHTTPSeal(t, q)); err == nil {
		t.Fatal("unexpected qualification authority accepted")
	}
}
func TestGPUHTTPAcceptsLargeEscapedValidInputBeforeQualification(t *testing.T) {
	s, a, _ := documentationFixture(t)
	j := a.Jobs[0]
	j.Status = "running"
	j.Provider = "codex"
	a.State.Assignments = []autonomy.Assignment{{TaskID: j.TaskID, Role: j.Role}}
	expertQuota(a, time.Now())
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	args := make([]string, 32)
	for i := range args {
		args[i] = strings.Repeat("<", 4096)
	}
	raw, err := json.Marshal(map[string]any{"script": strings.Repeat("<", 128<<10), "argv": args, "source_paths": []string{"src"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 256<<10 || len(raw) > 2<<20 {
		t.Fatalf("fixture outside intended boundary: %d", len(raw))
	}
	called := false
	q := func(string) (autoGPUQualifiedRuntime, error) {
		called = true
		return autoGPUQualifiedRuntime{}, os.ErrNotExist
	}
	w := httptest.NewRecorder()
	s.autoGPUBridgeQualified(t.TempDir(), j.ID, w, httptest.NewRequest("POST", "/research-runs", strings.NewReader(string(raw))), q)
	if !called || w.Code != 409 {
		t.Fatalf("valid large protocol rejected before qualification: %d %s", w.Code, w.Body.String())
	}
	called = false
	w = httptest.NewRecorder()
	s.autoGPUBridgeQualified(t.TempDir(), j.ID, w, httptest.NewRequest("POST", "/research-runs", strings.NewReader(strings.Repeat(" ", (2<<20)+1))), q)
	if called || w.Code != 400 {
		t.Fatal("oversized wire reached qualification")
	}
}
func TestGPUCapturedSelectionReceiptAbove64KiB(t *testing.T) {
	s, a, _ := documentationFixture(t)
	j := a.Jobs[0]
	response := documentationRunner(t)
	x := &autoGPUExperiment{OwnerJob: j.ID, SnapshotID: strings.Repeat("a", 64), InputSHA: strings.Repeat("b", 64), State: "capturing"}
	selected := make([]string, 64)
	excluded := make([]string, 128)
	for i := range selected {
		selected[i] = strings.Repeat("s", 1024)
	}
	for i := range excluded {
		excluded[i] = strings.Repeat("e", 1024)
	}
	receipt := map[string]any{"state": "ready", "owner_job": j.ID, "snapshot_id": x.SnapshotID, "input_sha256": x.InputSHA, "source_archive_sha256": strings.Repeat("c", 64), "source_tree_sha256": strings.Repeat("d", 64), "capture_started_unix_ns": 1, "capture_finished_unix_ns": 2, "selected_paths": selected, "excluded_paths": excluded}
	raw := gpuHTTPSeal(t, receipt)
	if len(raw) <= 65536 {
		t.Fatal("not a large receipt")
	}
	if err := os.WriteFile(response, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.autoGPUAdvance(context.Background(), a, x, t.TempDir(), false, false); err != nil {
		t.Fatal(err)
	}
	if x.State != "captured" || x.SnapshotReceipt != string(raw) {
		t.Fatal("large exact receipt lost")
	}
}
func TestGPUUnknownExecutionCaptureRenewalStillRequiresConfirmedCleanup(t *testing.T) {
	_, j, _, q := gpuAPIContext()
	now := time.Now()
	for _, receipt := range []string{`{"executed":null,"cleanup_confirmed":false}`, `{"cleanup_confirmed":true}`} {
		x := &autoGPUExperiment{State: "interrupted", RunID: strings.Repeat("a", 64), Receipt: receipt}
		if err := autoGPURenew(x, j, now); err == nil {
			t.Fatal("incomplete execution/cleanup released")
		}
	}
	x := &autoGPUExperiment{OwnerTask: j.TaskID, OwnerJob: j.ID, Input: autoGPUExperimentInput{Script: "x", SourcePaths: []string{"src"}}, InputSHA: strings.Repeat("b", 64), Runtime: q, State: "interrupted", RunID: strings.Repeat("a", 64), Receipt: `{"executed":null,"cleanup_confirmed":true}`}
	if err := autoGPURenew(x, j, now); err != nil {
		t.Fatal(err)
	}
	if x.RunID != "" || x.State != "capturing" || len(x.History) != 1 {
		t.Fatal("renewal dispatched GPU or lost unknown evidence")
	}
}
