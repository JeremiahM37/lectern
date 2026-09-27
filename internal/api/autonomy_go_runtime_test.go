package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func goRuntimeJob(j *autoJob) {
	j.Recovery = &autoRecoveryReceipt{State: "verified", Capability: "go_modules", Key: autoSHA([]byte("module input"))}
	j.GoRuntime = &autoGoRuntimeReceipt{SchemaVersion: 1, State: "verified", OwnerJob: j.ID, DependencyKey: j.Recovery.Key, BundleDigest: autoSHA([]byte("actual module snapshot")), ToolchainDigest: autoSHA([]byte("actual toolchain snapshot"))}
}
func TestGoRuntimeDeliverySelectionAndImmutableInheritance(t *testing.T) {
	j := &autoJob{ID: autoUUID()}
	goRuntimeJob(j)
	raw := []byte(store.J(map[string]any{"go_runtime": j.GoRuntime}))
	j.GoRuntime = nil
	if e := autoRecordGoRuntime(j, raw); e != nil {
		t.Fatal(e)
	}
	var selected autoExpertProbeRuntime
	if e := autoSelectGoTestRuntime(j, &selected); e != nil || selected.GoToolchain != j.GoRuntime.ToolchainDigest {
		t.Fatal(e)
	}
	root := t.TempDir()
	clone := *j
	clone.ID = autoUUID()
	os.Mkdir(filepath.Join(root, clone.ID), 0700)
	if e := autoPrepareGoRuntimeAt(root, &clone); e != nil {
		t.Fatal(e)
	}
	if clone.GoRuntime != nil || !autoSameGoRuntime(clone.GoExpectedRuntime, j.GoRuntime) {
		t.Fatal("historical delivery not separated from next process")
	}
	before, _ := os.ReadFile(filepath.Join(root, clone.ID, "go-runtime-requirement.json"))
	if e := autoPrepareGoRuntimeAt(root, &clone); e != nil {
		t.Fatal(e)
	}
	changed := *j.GoRuntime
	changed.OwnerJob = clone.ID
	changed.ToolchainDigest = autoSHA([]byte("host upgraded"))
	if autoRecordGoRuntime(&clone, []byte(store.J(map[string]any{"go_runtime": changed}))) == nil {
		t.Fatal("host change silently replaced selected toolchain")
	}
	changed.ToolchainDigest = j.GoRuntime.ToolchainDigest
	if e := autoRecordGoRuntime(&clone, []byte(store.J(map[string]any{"go_runtime": changed}))); e != nil {
		t.Fatal(e)
	}
	clone.GoExpectedRuntime.ToolchainDigest = autoSHA([]byte("new"))
	if autoPrepareGoRuntimeAt(root, &clone) == nil {
		t.Fatal("replaced immutable expectation")
	}
	after, _ := os.ReadFile(filepath.Join(root, clone.ID, "go-runtime-requirement.json"))
	if string(before) != string(after) {
		t.Fatal("request changed")
	}
}
func TestGoPrivateTestRequiresExactAllThreeRuntimeIdentities(t *testing.T) {
	a, v, j, now := privateFixture(t)
	goRuntimeJob(j)
	requestRaw, e := autoPrivateTestRequestBytes(a, j, &autoPrivateTestInput{CandidateSHA: v.Candidate.TreeSHA, Profile: "integration600", Script: "go test"}, strings.Repeat("d", 64))
	if e != nil {
		t.Fatal(e)
	}
	var request autoPrivateTestRequest
	json.Unmarshal(requestRaw, &request)
	key := autoSHA(requestRaw)
	r := privateTestReceipt(t, a, v, j, now, key)
	lease := v.TestLeases[key]
	lease.RequestRaw = requestRaw
	var body map[string]any
	json.Unmarshal([]byte(store.J(r)), &body)
	body["execution_generation"] = r.Generation
	for _, field := range []string{"dependency", "bundle", "toolchain"} {
		bad := request.Runtime
		switch field {
		case "dependency":
			bad.GoDependency = ""
		case "bundle":
			bad.GoBundle = autoSHA([]byte("other"))
		case "toolchain":
			bad.GoToolchain = ""
		}
		body["runtime"] = bad
		if _, e := autoDecodeBoundPrivateTestReceipt([]byte(store.J(body)), v.ID, lease, r.Generation); e == nil {
			t.Fatal("unbound Go receipt", field)
		}
	}
	body["runtime"] = request.Runtime
	got, e := autoDecodeBoundPrivateTestReceipt([]byte(store.J(body)), v.ID, lease, r.Generation)
	if e != nil || !autoPrivateCheckMatchesRuntime(j, got) {
		t.Fatal(e)
	}
	j.GoRuntime = nil
	if autoPrivateCheckMatchesRuntime(j, got) {
		t.Fatal("missing Go delivery promoted")
	}
}
func TestGoHistoricalPreflightKeepsSourceAndNewExperimentSeparate(t *testing.T) {
	s, a, p, _ := expertFixture(t)
	expertQuota(a, time.Now())
	pin, _ := autoExpertPin(a, p)
	source := autoGoProbeSource(a, pin)
	source.Recovery = &autoRecoveryReceipt{State: "verified", Capability: "go_modules", Key: autoSHA([]byte("exactkey"))}
	owner := &autoJob{Provider: "codex"}
	response := documentationRunner(t)
	receipt := autoGoProbeRuntimeReceipt{autoGoRuntimeReceipt: autoGoRuntimeReceipt{SchemaVersion: 1, State: "verified", OwnerJob: source.ID, DependencyKey: source.Recovery.Key, BundleDigest: autoSHA([]byte("snapshot")), ToolchainDigest: autoSHA([]byte("currenttoolchain"))}, SourceSHA: pin.SourceSHA, Generation: 1, Provenance: "new_experiment"}
	docResponse(t, response, receipt)
	ready, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey)
	if e != nil || !ready {
		t.Fatal(e)
	}
	if source.GoRuntime != nil {
		t.Fatal("fabricated historical use")
	}
	var selected autoExpertProbeRuntime
	if e = autoUseExpertGoRuntime(a, pin, &selected); e != nil || selected.GoBundle != receipt.BundleDigest {
		t.Fatal(e)
	}
	saved, _ := json.Marshal(a)
	var restored autoRecord
	json.Unmarshal(saved, &restored)
	if e = autoUseExpertGoRuntime(&restored, pin, &selected); e != nil {
		t.Fatal("restart lost exact selection", e)
	}
	pin.SourceSHA = autoSHA([]byte("different archive"))
	if autoUseExpertGoRuntime(a, pin, &selected) == nil {
		t.Fatal("foreign archive reused")
	}
}
func TestGoExecutedExpertReceiptCannotOmitSelectedEnvironment(t *testing.T) {
	r := autoExpertProbeRuntime{GoDependency: autoSHA([]byte("k")), GoBundle: autoSHA([]byte("b")), GoToolchain: autoSHA([]byte("t"))}
	lease := &autoExpertProbeLease{Runtime: &r}
	proof := autoExpertProbeReceipt{State: "exited"}
	if autoGoReceiptRuntime(lease, proof) == nil {
		t.Fatal("missing actual runtime")
	}
	proof.Runtime = &r
	if e := autoGoReceiptRuntime(lease, proof); e != nil {
		t.Fatal(e)
	}
	if autoGoReceiptRuntime(&autoExpertProbeLease{}, proof) == nil {
		t.Fatal("legacy receipt gained Go")
	}
}

func TestGoSelectedRuntimeSurvivesGeneratedSourceInputs(t *testing.T) {
	source := &autoJob{ID: autoUUID()}
	goRuntimeJob(source)
	reviewer := &autoJob{ID: autoUUID(), Recovery: &autoRecoveryReceipt{State: "verified", Key: autoSHA([]byte("archived generated go.sum"))}}
	autoInheritGoRuntime(reviewer, source)
	actual := *source.GoRuntime
	actual.OwnerJob = reviewer.ID
	if e := autoRecordGoRuntime(reviewer, []byte(store.J(map[string]any{"go_runtime": actual}))); e != nil {
		t.Fatal(e)
	}
	var runtime autoExpertProbeRuntime
	if e := autoSelectGoTestRuntime(reviewer, &runtime); e != nil || runtime.GoDependency != source.Recovery.Key {
		t.Fatal(e)
	}
	actual.DependencyKey = reviewer.Recovery.Key
	if autoRecordGoRuntime(reviewer, []byte(store.J(map[string]any{"go_runtime": actual}))) == nil {
		t.Fatal("current-source cache substituted for selected snapshot")
	}
}

func TestGoPreflightCancellationRetainsGenerationAndRejectsForeignReceipt(t *testing.T) {
	s, a, p, _ := expertFixture(t)
	expertQuota(a, time.Now())
	pin, _ := autoExpertPin(a, p)
	source := autoGoProbeSource(a, pin)
	source.Recovery = &autoRecoveryReceipt{State: "verified", Key: autoSHA([]byte("key"))}
	owner := &autoJob{Provider: "codex"}
	response := documentationRunner(t)
	receipt := autoGoProbeRuntimeReceipt{autoGoRuntimeReceipt: autoGoRuntimeReceipt{SchemaVersion: 1, State: "recovering", OwnerJob: source.ID, DependencyKey: source.Recovery.Key}, SourceSHA: pin.SourceSHA, Generation: 1, Provenance: "new_experiment"}
	docResponse(t, response, receipt)
	if ready, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey); e != nil || ready {
		t.Fatal(ready, e)
	}
	x := a.GoProbeRuntimes[autoGoProbeIdentity(pin, source.Recovery.Key)]
	docResponse(t, response, map[string]any{"state": "stopping", "owner_job": source.ID, "generation": 1, "go_dependency_key": source.Recovery.Key, "source_archive_sha256": pin.SourceSHA})
	if s.stopAutoGoProbeRuntimes(context.Background(), a) == nil || !x.StopRequested || x.Stopped {
		t.Fatal("unconfirmed stop released ownership")
	}
	if _, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey); e == nil {
		t.Fatal("restarted before stop confirmation")
	}
	docResponse(t, response, map[string]any{"state": "stopped", "owner_job": source.ID, "generation": 1, "go_dependency_key": source.Recovery.Key, "source_archive_sha256": pin.SourceSHA})
	if e := s.stopAutoGoProbeRuntimes(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	saved, _ := json.Marshal(a)
	var restored autoRecord
	json.Unmarshal(saved, &restored)
	a = &restored
	receipt.Generation = 1
	docResponse(t, response, receipt)
	if _, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey); e == nil {
		t.Fatal("old generation accepted")
	}
	x = a.GoProbeRuntimes[autoGoProbeIdentity(pin, source.Recovery.Key)]
	if x.Generation != 2 || x.StopRequested {
		t.Fatal("same reservation did not renew")
	}
	receipt.Generation = 2
	receipt.SourceSHA = autoSHA([]byte("foreign"))
	docResponse(t, response, receipt)
	if _, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey); e == nil {
		t.Fatal("foreign source accepted")
	}
	receipt.SourceSHA = pin.SourceSHA
	docResponse(t, response, receipt)
	if _, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey); e != nil {
		t.Fatal(e)
	}
	a.Quota.Providers = nil
	if _, e := s.prepareAutoExpertGoRuntime(context.Background(), a, owner, p.ExpertProgressKey); e == nil {
		t.Fatal("renewed without fresh quota")
	}
}

func TestGoLaunchFailureHeldThenExactRestorationUsesFreshUUID(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": autoSHA([]byte("archive"))})
	task, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "Go consumer", Status: "running"})
	if e != nil {
		t.Fatal(e)
	}
	j := &autoJob{ID: autoUUID(), TaskID: task.ID, Role: "builder", Provider: "codex", Status: "running"}
	goRuntimeJob(j)
	selected := *j.GoRuntime
	j.GoExpectedRuntime = &selected
	a.Jobs = append(a.Jobs, j)
	original := &autonomy.State{Date: "2026-09-27", Phase: autonomy.Build, Items: []autonomy.Proposal{{ProjectID: p.ProjectID}}, Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: "builder"}}}
	a.State = original
	no := false
	failure := selected
	failure.State = "unavailable"
	failure.Executed = &no
	failure.Diagnostic = "integrity"
	failure.Reason = "selected snapshot content changed"
	raw := []byte(store.J(map[string]any{"state": "failed", "go_runtime": failure}))
	if e := autoRecordGoRuntime(j, raw); e != nil {
		t.Fatal(e)
	}
	if handled, e := s.handleAutoGoLaunchFailure(context.Background(), a, j, raw); !handled || e != nil {
		t.Fatal(handled, e)
	}
	if j.Status != "deferred" || !j.RequirementHold || !j.GoNeedsResume || len(a.HeldRuns) != 1 || a.HeldRuns[0] != original {
		t.Fatal("failed runtime lost retained assignment")
	}
	if len(j.RequirementIDs) != 1 || a.Requirements[j.RequirementIDs[0]].GoReceipt.Diagnostic != "integrity" {
		t.Fatal("missing actionable requirement")
	}
	j.RequirementHold = false
	if autoDeferredReady(j) {
		t.Fatal("model diagnosis alone bypassed actual restored content check")
	}
	j.RequirementHold = true
	expertQuota(a, time.Now())
	now := time.Now().Add(time.Second)
	j.RecoveryCheckAt = time.Time{}
	verified := autoGoProbeRuntimeReceipt{autoGoRuntimeReceipt: selected, SourceSHA: j.GoRuntimeSourceSHA, Generation: 1, Provenance: "new_experiment"}
	verified.ToolchainDigest = autoSHA([]byte("differenthost"))
	docResponse(t, response, verified)
	if !s.pollAutoGoRuntimeRecovery(context.Background(), a, now) || j.GoRecoveryVerified {
		t.Fatal("different restored environment accepted")
	}
	verified.ToolchainDigest = selected.ToolchainDigest
	docResponse(t, response, verified)
	j.RecoveryCheckAt = time.Time{}
	if !s.pollAutoGoRuntimeRecovery(context.Background(), a, now) || !j.GoRecoveryVerified || j.RequirementHold {
		t.Fatal("matching restoration did not release hold", a.Reason)
	}
	if j.GoRuntime.State != "unavailable" {
		t.Fatal("restoration fabricated historical model use")
	}
	if !autoResumeRecovered(a) || j.Status != "stopped" || a.State != original {
		t.Fatal("consumed UUID returned as prepared")
	}
	clone := *j
	clone.ID = autoUUID()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, clone.ID), 0700)
	if e := autoPrepareGoRuntimeAt(root, &clone); e != nil || clone.GoNeedsResume || clone.GoRuntime != nil || !autoSameGoRuntime(clone.GoExpectedRuntime, &selected) {
		t.Fatal("fresh UUID lost exact environment", e)
	}
}
