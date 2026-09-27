package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func documentationFixture(t *testing.T) (*Server, *autoRecord, autonomy.Proposal) {
	s, a, project, root := repairFixture(t)
	base := autoFindJob(a, root)
	base.ID = "11111111-1111-4111-8111-111111111111"
	base.Admission = &autoAdmission{Proposal: autonomy.Proposal{Acceptance: []string{"original production and tests complete"}}}
	prior := root
	for i := 0; i < 2; i++ {
		task, e := s.DB.InsertTask(&store.Task{ProjectID: project, Title: "repair", Status: "done"})
		if e != nil {
			t.Fatal(e)
		}
		review, e := s.DB.InsertTask(&store.Task{ProjectID: project, Title: "review", Status: "done"})
		if e != nil {
			t.Fatal(e)
		}
		j := &autoJob{ID: []string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i], TaskID: task.ID, Role: "builder", Status: "done", RepairSourceTaskID: prior, RepairAttemptTaskID: task.ID, Rejected: true, ReviewTaskID: review.ID, ReviewReason: "documentation inaccurate", Admission: &autoAdmission{Proposal: autonomy.Proposal{Acceptance: []string{"repair-specific evidence remains binding"}}}}
		a.Jobs = append(a.Jobs, j, &autoJob{ID: []string{"44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"}[i], TaskID: review.ID, Role: "reviewer", Status: "done"})
		prior = task.ID
	}
	p := autonomy.Proposal{ProjectID: project, DocumentationTaskID: prior, Title: "reconcile evidence", Why: "substantive work verified", Acceptance: []string{"preserve history and correct documentation"}}
	a.State.Items = []autonomy.Proposal{p}
	a.State.Cycle = 42
	return s, a, p
}
func documentationRunner(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "response")
	t.Setenv("LECTERN_DOC_RESPONSE", out)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if e := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\ncat \"$LECTERN_DOC_RESPONSE\"\n"), 0700); e != nil {
		t.Fatal(e)
	}
	return out
}
func docResponse(t *testing.T, path string, v any) {
	t.Helper()
	if e := os.WriteFile(path, []byte(store.J(v)), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestDocumentationReservationFreezesRootAndSourceAcceptance(t *testing.T) {
	s, a, p := documentationFixture(t)
	file := documentationRunner(t)
	docResponse(t, file, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if e := s.pinAutoDocumentation(context.Background(), a, []autonomy.Proposal{p}); e != nil {
		t.Fatal(e)
	}
	pin := a.DocumentationPins[p.DocumentationTaskID]
	if pin.Acceptance[0] == pin.SourceAcceptance[0] {
		t.Fatal("root criteria replaced with repair criteria")
	}
	r, e := s.reserveAutoDocumentation(context.Background(), a, p, "66666666-6666-4666-8666-666666666666")
	if e != nil {
		t.Fatal(e)
	}
	r.TaskID = 99
	if e = s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	a, e = s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.reserveAutoDocumentation(context.Background(), a, p, "77777777-7777-4777-8777-777777777777"); e == nil {
		t.Fatal("second allowance after reload")
	}
	if _, _, _, _, e = s.autoDocumentationSource(a, p); e == nil {
		t.Fatal("reserved lineage discoverable as new allowance")
	}
	if autoCheckpointApproved(a, p.DocumentationTaskID) {
		t.Fatal("original rejection promoted")
	}
}
func TestDocumentationPinsRejectArtifactDriftAndMissingOriginalCriteria(t *testing.T) {
	s, a, p := documentationFixture(t)
	file := documentationRunner(t)
	docResponse(t, file, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if e := s.pinAutoDocumentation(context.Background(), a, []autonomy.Proposal{p}); e != nil {
		t.Fatal(e)
	}
	docResponse(t, file, map[string]string{"state": "ready", "sha256": strings.Repeat("b", 64)})
	if _, e := s.reserveAutoDocumentation(context.Background(), a, p, "x"); e == nil {
		t.Fatal("changed artifact passed audits")
	}
	root := autoRepairRoot(a, p.DocumentationTaskID)
	autoFindJob(a, root).Admission = nil
	a.Runs = nil
	if _, _, _, _, e := s.autoDocumentationSource(a, p); e == nil {
		t.Fatal("invented original criteria")
	}
}
func TestDocumentationRequiresAuditsExhaustionAndExclusiveSelector(t *testing.T) {
	s, a, p := documentationFixture(t)
	if e := s.validateAutoSources(a, []autonomy.Proposal{p, p}); e == nil {
		t.Fatal("duplicate root in one plan")
	}
	both := p
	both.RepairTaskID = p.DocumentationTaskID
	if e := s.validateAutoSources(a, []autonomy.Proposal{both}); e == nil {
		t.Fatal("mixed selectors")
	}
	a.State.Audits = nil
	if _, e := s.reserveAutoDocumentation(context.Background(), a, p, "x"); e == nil {
		t.Fatal("unaudited allowance")
	}
	a.Config.MaxRevisionRounds = 3
	if _, _, _, _, e := s.autoDocumentationSource(a, p); e == nil {
		t.Fatal("unexhausted ordinary repairs")
	}
}
func TestDocumentationReconstructionPendingRejectedAndDerivedIdentity(t *testing.T) {
	s, a, p := documentationFixture(t)
	file := documentationRunner(t)
	docResponse(t, file, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if e := s.pinAutoDocumentation(context.Background(), a, []autonomy.Proposal{p}); e != nil {
		t.Fatal(e)
	}
	r, e := s.reserveAutoDocumentation(context.Background(), a, p, "66666666-6666-4666-8666-666666666666")
	if e != nil {
		t.Fatal(e)
	}
	r.TaskID = 99
	r.Binding.BaselineSHA = strings.Repeat("b", 64)
	j := &autoJob{ID: r.JobID, TaskID: 99, Role: "builder", Status: "exporting", DocumentationRoot: r.RootTaskID}
	a.Jobs = append(a.Jobs, j)
	a.State.Assignments = append(a.State.Assignments, autonomy.Assignment{TaskID: 99, Role: "builder"})
	got := r.Binding
	got.State = "running"
	docResponse(t, file, got)
	if e = s.finishAutoDocumentation(context.Background(), a, j); !errors.Is(e, errAutoArtifactPending) {
		t.Fatal(e)
	}
	if j.Status != "exporting" {
		t.Fatal("pending worker relaunched")
	}
	got.State = "ready"
	got.DerivedSHA = strings.Repeat("c", 64)
	got.DerivedTreeSHA = strings.Repeat("d", 64)
	docResponse(t, file, got)
	if e = s.finishAutoDocumentation(context.Background(), a, j); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(j.ArtifactPath, "completion/derived/work") {
		t.Fatal("promoting raw candidate")
	}
	j.Status = "exporting"
	got.DerivedSHA = strings.Repeat("e", 64)
	docResponse(t, file, got)
	if e = s.finishAutoDocumentation(context.Background(), a, j); e == nil {
		t.Fatal("changed derived artifact")
	}
	docResponse(t, file, map[string]string{"state": "rejected", "reason": "forbidden production edit"})
	if e = s.finishAutoDocumentation(context.Background(), a, j); e != nil {
		t.Fatal(e)
	}
	if j.Status != "done" || !j.Rejected || a.State.Phase != autonomy.Complete || r.Outcome != "rejected" {
		t.Fatal("overlay rejection became operational retry")
	}
	if _, e = s.autoRepairContinuation(a, p.ProjectID, j.TaskID); e == nil {
		t.Fatal("completion acquires ordinary repair")
	}
	raw, _ := json.Marshal(a)
	var reloaded autoRecord
	json.Unmarshal(raw, &reloaded)
	if len(reloaded.DocumentationReservations) != 1 {
		t.Fatal("reservation lost")
	}
}

func TestDocumentationAsyncPrepareAndCopySurviveReload(t *testing.T) {
	s, a, p := documentationFixture(t)
	file := documentationRunner(t)
	docResponse(t, file, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if e := s.pinAutoDocumentation(context.Background(), a, []autonomy.Proposal{p}); e != nil {
		t.Fatal(e)
	}
	r, e := s.reserveAutoDocumentation(context.Background(), a, p, "66666666-6666-4666-8666-666666666666")
	if e != nil {
		t.Fatal(e)
	}
	r.TaskID = 99
	j := &autoJob{ID: r.JobID, TaskID: 99, Role: "builder", Status: "prepared", DocumentationRoot: r.RootTaskID}
	a.Jobs = append(a.Jobs, j)
	docResponse(t, file, map[string]string{"state": "preparing"})
	if ready, e := s.prepareAutoDocumentation(context.Background(), a, j); e != nil || ready {
		t.Fatal("launched before immutable baseline", ready, e)
	}
	if e = s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	a, e = s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	r = a.DocumentationReservations[r.RootTaskID]
	j = autoFindJob(a, 99)
	got := r.Binding
	got.State = "ready"
	got.BaselineSHA = strings.Repeat("b", 64)
	docResponse(t, file, got)
	if ready, e := s.prepareAutoDocumentation(context.Background(), a, j); e != nil || !ready {
		t.Fatal(ready, e)
	}
	if j.Admission == nil || j.Admission.Documentation.AcceptanceSHA != r.AcceptanceSHA {
		t.Fatal("baseline missing from durable assignment")
	}
	got.DerivedSHA = strings.Repeat("c", 64)
	j.Documentation = &got
	copy, e := s.copyAutoPromoted(context.Background(), "dest", j)
	if e != nil || copy == nil || copy.Command != "copy-derived" {
		t.Fatal("raw candidate selected", copy, e)
	}
	consumer := &autoJob{ID: "consumer", Status: "prepared", DocumentationCopies: []autoDocumentationCopy{*copy}}
	docResponse(t, file, map[string]string{"state": "copying"})
	if ready, e := s.pollAutoDocumentationCopies(context.Background(), a, consumer); e != nil || ready {
		t.Fatal(ready, e)
	}
	docResponse(t, file, map[string]string{"state": "copied", "derived_archive_sha256": strings.Repeat("d", 64)})
	if _, e := s.pollAutoDocumentationCopies(context.Background(), a, consumer); e == nil {
		t.Fatal("different derived artifact copied")
	}
	docResponse(t, file, map[string]string{"state": "copied", "derived_archive_sha256": got.DerivedSHA})
	if ready, e := s.pollAutoDocumentationCopies(context.Background(), a, consumer); e != nil || !ready {
		t.Fatal(ready, e)
	}
}
func TestDocumentationDerivedDownloadChecksBytesBeforeServing(t *testing.T) {
	name := filepath.Join(t.TempDir(), "artifact")
	os.WriteFile(name, []byte("verified"), 0600)
	f, e := os.Open(name)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = autoVerifyDerivedDownload(f, autoSHA([]byte("verified"))); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 8)
	f.Read(buf)
	if string(buf) != "verified" {
		t.Fatal("stream not rewound")
	}
	f.Seek(0, 0)
	if e = autoVerifyDerivedDownload(f, autoSHA([]byte("other"))); e == nil {
		t.Fatal("tampered download accepted")
	}
}
func TestDocumentationCancellationAttemptsEveryWorkerAfterHelperError(t *testing.T) {
	s, a, _ := documentationFixture(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	t.Setenv("LECTERN_STOP_CALLS", calls)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$3\" \"$5\" >> \"$LECTERN_STOP_CALLS\"\ncase \"$3\" in\ncompletion-stop) exit 1;;\nstop) echo '{}';;\nsnapshot) echo '{\"state\":\"ready\"}';;\n*) exit 99;;\nesac\n"
	os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700)
	a.Jobs = []*autoJob{{ID: "doc", Role: "builder", DocumentationRoot: 1, Status: "prepared"}, {ID: "other", Role: "builder", Status: "running"}}
	s.stopAutoJobs(context.Background(), a, "Budget pause")
	raw, _ := os.ReadFile(calls)
	if !strings.Contains(string(raw), "stop other") || a.Jobs[1].Status != "stopped" || a.Status != "error" {
		t.Fatalf("cancellation stopped early: %s %s", raw, a.Status)
	}
}

func TestDocumentationDisabledTickRetriesFailedHelperStopAfterReload(t *testing.T) {
	s, a, _ := documentationFixture(t)
	response := documentationRunner(t)
	a.Config.Enabled = false
	a.Jobs = []*autoJob{{ID: "pending-copy", Status: "prepared", DocumentationCopies: []autoDocumentationCopy{{Command: "copy-derived", SourceJob: "source"}}}}
	// Invalid runner exit makes the first stop fail without losing pending ownership.
	os.WriteFile(response, []byte("{}"), 0600)
	script := "#!/bin/sh\nif [ ! -f \"$LECTERN_DOC_RESPONSE.ok\" ]; then exit 1; fi\necho '{}'\n"
	os.WriteFile(filepath.Join(filepath.Dir(response), "sudo"), []byte(script), 0700)
	s.stopAutoJobs(context.Background(), a, "off")
	if a.Status != "error" || !autoDocumentationStopPending(a.Jobs[0]) {
		t.Fatal("failed stop lost ownership")
	}
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(response+".ok", nil, 0600)
	s.RunAutonomyTick(context.Background())
	loaded, e := s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	if loaded.Status != "off" || !loaded.Jobs[0].DocumentationStopped || autoDocumentationStopPending(loaded.Jobs[0]) {
		t.Fatalf("disabled tick did not finish pending stop: %+v", loaded)
	}
}

func TestDocumentationMalformedTransportDoesNotConsumeAdmission(t *testing.T) {
	s, a, p := documentationFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	if e := s.pinAutoDocumentation(context.Background(), a, []autonomy.Proposal{p}); e != nil {
		t.Fatal(e)
	}
	r, e := s.reserveAutoDocumentation(context.Background(), a, p, "66666666-6666-4666-8666-666666666666")
	if e != nil {
		t.Fatal(e)
	}
	r.TaskID = 99
	j := &autoJob{ID: r.JobID, TaskID: 99, Role: "builder", DocumentationRoot: r.RootTaskID}
	a.State.Phase = autonomy.Build
	a.State.RegisterTask("builder", 99)
	a.Config.Enabled = true
	before, _ := json.Marshal(r)
	if _, e := autoValidateWorkerReport(a, j, []byte(`{"outcome":`)); e == nil {
		t.Fatal("malformed report accepted")
	}
	if a.State.Phase != autonomy.Build || r.Outcome != "reserved" {
		t.Fatal("transport failure consumed documentary work")
	}
	next, e := autoValidateWorkerReport(a, j, []byte(`{"outcome":"ready_for_review","summary":"documentation corrected","evidence":["retained source manifest verified"]}`))
	if e != nil {
		t.Fatal(e)
	}
	if next.Phase != autonomy.Review || a.State.Phase != autonomy.Build {
		t.Fatal("validation mutated live assignment before reconstruction")
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("schema correction changed reserved allowance")
	}
}

func TestDocumentationOffDuringPreparedCopyRetainsSameUUID(t *testing.T) {
	s, a, _ := documentationFixture(t)
	response := documentationRunner(t)
	j := &autoJob{ID: "same-prepared-job", Role: "builder", Status: "prepared", DocumentationCopies: []autoDocumentationCopy{{Command: "copy-archive-review", SourceJob: "planner", SHA: strings.Repeat("a", 64)}}}
	a.Jobs = append(a.Jobs, j)
	docResponse(t, response, map[string]string{"state": "stopped"})
	s.stopAutoJobs(context.Background(), a, "off")
	if j.Status != "prepared" || !j.DocumentationStopped {
		t.Fatal("OFF reclassified unlaunched copy")
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	restored := loaded.Jobs[len(loaded.Jobs)-1]
	docResponse(t, response, map[string]string{"state": "copying"})
	if ready, err := s.pollAutoDocumentationCopies(context.Background(), loaded, restored); err != nil || ready {
		t.Fatal(ready, err)
	}
	docResponse(t, response, map[string]string{"state": "copied", "source_archive_sha256": strings.Repeat("a", 64)})
	if ready, err := s.pollAutoDocumentationCopies(context.Background(), loaded, restored); err != nil || !ready {
		t.Fatal(ready, err)
	}
	if restored.ID != j.ID || restored.Status != "prepared" || len(restored.DocumentationCopies) != 0 {
		t.Fatal("copy retry allocated or relaunched a different job")
	}
}
