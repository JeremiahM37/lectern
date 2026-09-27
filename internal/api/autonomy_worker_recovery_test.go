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

func TestWorkerRecoveryNeverClassifiesSilenceOrRejectedWorkAsInterruption(t *testing.T) {
	for _, raw := range []string{`{"state":"running","output_idle_milliseconds":7200000}`, `{"state":"done","exit_code":0}`, `{"state":"failed","exit_code":0}`, `{"state":"failed"}`, `bad`} {
		if _, ok := autoWorkerTerminalFailure([]byte(raw)); ok {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{`{"state":"failed","exit_code":124,"reason":"worker runtime limit reached"}`, `{"state":"stopped"}`, `{"state":"failed","exit_code":137}`} {
		if _, ok := autoWorkerTerminalFailure([]byte(raw)); !ok {
			t.Fatal(raw)
		}
	}
}
func TestWorkerRecoveryQuarantineKeepsExactAssignmentAndCounters(t *testing.T) {
	s, a, _, id := repairFixture(t)
	a.Config.Continuous = true
	j := &autoJob{ID: "failed-worker", TaskID: id, Role: "builder", Status: "failed", WorkerFailures: 2, RepairSourceTaskID: 201, RepairAttemptTaskID: id, Admission: &autoAdmission{TaskID: id, JobID: "original", Proposal: autonomy.Proposal{Acceptance: []string{"original unchanged"}}}}
	a.Jobs = append(a.Jobs, j)
	a.State.Assignments = []autonomy.Assignment{{TaskID: id, Role: "builder"}}
	admission := store.J(j.Admission)
	state := a.State
	now := time.Now()
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	handled, err := s.deferAutoInterruptedWorker(context.Background(), a, j, []byte(`{"state":"failed","exit_code":124}`), now)
	if err != nil || !handled || j.Status != "deferred" || j.WorkerFailures != 3 || !j.WorkerRecoveryAt.Equal(now.Add(30*time.Minute)) {
		t.Fatal(j, handled, err)
	}
	if len(a.DeferredRuns) != 1 || a.DeferredRuns[0] != state || a.State == state || store.J(j.Admission) != admission || j.RepairAttemptTaskID != id || j.Approved || j.Rejected {
		t.Fatal("lost admitted assignment or fabricated verdict")
	}
	if autoDeferredReady(j) {
		t.Fatal("cooldown skipped")
	}
	raw := store.J(a)
	var restored autoRecord
	if json.Unmarshal([]byte(raw), &restored) != nil {
		t.Fatal("reload")
	}
	saved := autoFindJob(&restored, id)
	if saved.WorkerFailures != 3 || !saved.WorkerRecoveryAt.Equal(j.WorkerRecoveryAt) {
		t.Fatal("restart reset lifetime counter/deadline")
	}
	saved.WorkerRecoveryAt = time.Now().Add(-time.Second)
	if !autoResumeRecovered(&restored) || saved.Status != "stopped" || restored.State.Assignments[0].TaskID != id || saved.WorkerFailures != 3 {
		t.Fatal("due slot lost same task")
	}
	for n := 4; n < 30; n++ {
		if autoWorkerBackoff(n) < time.Hour || autoWorkerBackoff(n) > 6*time.Hour {
			t.Fatal("unbounded or reset retry", n)
		}
	}
}
func TestWorkerRecoveryObservationIdempotentAndExpertBudgetUntouched(t *testing.T) {
	s, a, _, id := repairFixture(t)
	j := &autoJob{ID: "old", TaskID: id, Status: "failed", Role: "builder"}
	a.Jobs = append(a.Jobs, j)
	a.State.Assignments = []autonomy.Assignment{{TaskID: id, Role: "builder"}}
	for i := 0; i < 4; i++ {
		_, err := s.deferAutoInterruptedWorker(context.Background(), a, j, []byte(`{"state":"failed","exit_code":124}`), time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	if j.WorkerFailures != 1 {
		t.Fatal("one failed process charged repeatedly")
	}
	for _, kind := range []string{"expert", "private", "documentary"} {
		copy := *j
		copy.WorkerFailureRecorded = false
		switch kind {
		case "expert":
			copy.ExpertRecoveryAttempt = 1
		case "private":
			copy.PrivateIntegrationRoot = "root"
		case "documentary":
			copy.DocumentationRoot = 1
		}
		handled, err := s.deferAutoInterruptedWorker(context.Background(), a, &copy, []byte(`{"state":"failed","exit_code":124}`), time.Now())
		if err != nil || handled || copy.WorkerFailures != 1 {
			t.Fatal("generic gate reset specialized lifetime budget", kind)
		}
	}
}
func TestWorkerRecoveryPersistsArchiveCopyBeforePrepareAndNoLiveSource(t *testing.T) {
	s, a, _, id := repairFixture(t)
	old := &autoJob{ID: "11111111-1111-4111-8111-111111111111", TaskID: id, Role: "builder", Status: "stopped", WorkerFailures: 4, WorkerFailureRecorded: true, WorkerRecoveryAt: time.Now().Add(-time.Second), WorkerRecoveryArchive: strings.Repeat("a", 64), ReportRepairs: 1, RepairSourceTaskID: 201, RepairAttemptTaskID: id, Admission: &autoAdmission{TaskID: id, JobID: "origin"}}
	pending, err := autoNodeInputs(nodeRequirement())
	if err != nil {
		t.Fatal(err)
	}
	old.NodeRequest = &autoNodeRequest{Requirements: []string{"old@1.0.0"}}
	old.PendingNodeRequest = pending
	old.NodeGeneration = 4
	old.NodeStopped = true
	old.NodeStopRequested = true
	old.NodeNeedsResume = true
	a.Jobs = append(a.Jobs, old)
	a.State.Assignments = []autonomy.Assignment{{TaskID: id, Role: "builder"}}
	root := t.TempDir()
	status := filepath.Join(root, "status")
	log := filepath.Join(root, "calls")
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	t.Setenv("WORKER_TEST_STATUS", status)
	t.Setenv("WORKER_TEST_CALLS", log)
	script := `#!/bin/sh
printf '%s\n' "$3" >> "$WORKER_TEST_CALLS"
case "$3" in
status) cat "$WORKER_TEST_STATUS" ;;
snapshot) echo '{"state":"ready"}' ;;
archive-identity) echo '{"state":"ready","sha256":"` + strings.Repeat("a", 64) + `"}' ;;
*) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(status, []byte(`{"state":"running"}`), 0600)
	beforeJobs := len(a.Jobs)
	if err := s.resumeAutoJob(context.Background(), a, old); err == nil || len(a.Jobs) != beforeJobs {
		t.Fatal("active process copied", err, len(a.Jobs))
	}
	os.WriteFile(status, []byte(`{"state":"failed","exit_code":124}`), 0600)
	old.WorkerRecoveryArchive = strings.Repeat("b", 64)
	if err := s.resumeAutoJob(context.Background(), a, old); err == nil || len(a.Jobs) != beforeJobs {
		t.Fatal("changed quarantine archive selected", err)
	}
	old.WorkerRecoveryArchive = strings.Repeat("a", 64)
	original := store.J(old)
	if err := s.resumeAutoJob(context.Background(), a, old); err != nil {
		t.Fatal(err)
	}
	next := autoFindJob(a, id)
	if next.ID == old.ID || next.TaskID != old.TaskID || next.WorkerFailures != 4 || next.WorkerFailureRecorded || !next.WorkerRecoveryAt.IsZero() || next.ReportRepairs != 1 || next.RepairAttemptTaskID != old.RepairAttemptTaskID || store.J(old) != original {
		t.Fatal("retry reset evidence/counters", next)
	}
	if next.NodeNeedsResume || next.NodeGeneration != 1 || next.NodeStopped || next.NodeStopRequested || next.PendingNodeRequest != nil || !next.NodeNeedsChange || store.J(next.NodeRequest) != store.J(pending) {
		t.Fatal("Node remedy or consumed marker lost in actual resume", next)
	}
	if len(next.DocumentationCopies) != 1 || next.DocumentationCopies[0].Command != "copy-archive-resume" || next.DocumentationCopies[0].SHA != old.WorkerRecoveryArchive {
		t.Fatal("missing exact asynchronous transport")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "prepare") || strings.Contains(string(calls), "copy\n") {
		t.Fatal("unpersisted synchronous effect", string(calls))
	}
	loaded, err := s.loadAuto()
	if err != nil || autoFindJob(loaded, id).ID != next.ID {
		t.Fatal("new owner not durable", err)
	}
	if err := s.resumeAutoJob(context.Background(), a, old); err != nil || autoFindJob(a, id).ID != next.ID {
		t.Fatal("lost response allocated duplicate UUID", err)
	}
}

func TestWorkerRecoveryPairedAuditorsRequireConfirmedStopAndResumeTogether(t *testing.T) {
	s, a, _, id := repairFixture(t)
	a.Config.Continuous = true
	first := &autoJob{ID: "first", TaskID: id, Role: "auditor_a", Status: "failed", WorkerFailures: 2}
	peer := &autoJob{ID: "peer", TaskID: id + 1, Role: "auditor_b", Status: "running"}
	a.Jobs = append(a.Jobs, first, peer)
	a.State.Assignments = []autonomy.Assignment{{TaskID: id, Role: "auditor_a"}, {TaskID: id + 1, Role: "auditor_b"}}
	oldState := a.State
	root := t.TempDir()
	status := filepath.Join(root, "stop")
	t.Setenv("WORKER_PEER_STOP", status)
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	script := `#!/bin/sh
case "$3" in
stop) cat "$WORKER_PEER_STOP" ;;
snapshot) echo '{"state":"ready"}' ;;
archive-identity) echo '{"state":"ready","sha256":"` + strings.Repeat("a", 64) + `"}' ;;
*) exit 99 ;;
esac
`
	os.WriteFile(filepath.Join(root, "sudo"), []byte(script), 0700)
	os.WriteFile(status, []byte(`{"state":"running"}`), 0600)
	handled, err := s.deferAutoInterruptedWorker(context.Background(), a, first, []byte(`{"state":"failed","exit_code":124}`), time.Now())
	if handled || err == nil || a.State != oldState || peer.Status != "running" || len(a.DeferredRuns) != 0 {
		t.Fatal("unconfirmed peer escaped into new cycle", handled, err)
	}
	os.WriteFile(status, []byte(`{"state":"stopped"}`), 0600)
	handled, err = s.deferAutoInterruptedWorker(context.Background(), a, first, []byte(`{"state":"failed","exit_code":124}`), time.Now())
	if !handled || err != nil || a.State == oldState || peer.Status != "deferred" || first.WorkerFailures != 3 || peer.WorkerFailures != 0 {
		t.Fatal("paired preservation changed attempts", handled, err)
	}
	if autoResumePairedWorkerRecovery(a, time.Now()) {
		t.Fatal("paired backoff skipped")
	}
	due := first.WorkerRecoveryAt
	if !autoResumePairedWorkerRecovery(a, due) || a.State != oldState || first.Status != "stopped" || peer.Status != "stopped" {
		t.Fatal("paired audit split across cycles")
	}
}

func TestWorkerRecoveryPreparedPeerKeepsUnstartedCopyUUID(t *testing.T) {
	s, a, _, id := repairFixture(t)
	a.Config.Continuous = true
	first := &autoJob{ID: "first", TaskID: id, Role: "auditor_a", Status: "failed", WorkerFailures: 2}
	peer := &autoJob{ID: "not-created-yet", TaskID: id + 1, Role: "auditor_b", Status: "prepared", DocumentationCopies: []autoDocumentationCopy{{Command: "copy-archive-resume", SourceJob: "older", SHA: strings.Repeat("a", 64), Generation: 1}}}
	peer.NodeRequest = &autoNodeRequest{Kind: "node_packages"}
	peer.NodeGeneration = 2
	a.Jobs = append(a.Jobs, first, peer)
	a.State.Assignments = []autonomy.Assignment{{TaskID: id, Role: "auditor_a"}, {TaskID: id + 1, Role: "auditor_b"}}
	root := t.TempDir()
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	stopFile := filepath.Join(root, "node-stop")
	t.Setenv("NODE_PEER_STOP", stopFile)
	os.WriteFile(stopFile, []byte(`{"state":"stopped","job":"not-created-yet","generation":1}`), 0600)
	script := `#!/bin/sh
case "$3" in
node-dependencies-stop) cat "$NODE_PEER_STOP" ;;
completion-stop) echo '{"state":"stopped"}' ;;
archive-identity) echo '{"state":"ready","sha256":"` + strings.Repeat("a", 64) + `"}' ;;
*) exit 99 ;;
esac
`
	os.WriteFile(filepath.Join(root, "sudo"), []byte(script), 0700)
	held, firstErr := s.deferAutoInterruptedWorker(context.Background(), a, first, []byte(`{"state":"failed","exit_code":124}`), time.Now())
	if held || firstErr == nil || peer.NodeStopped || len(a.DeferredRuns) > 0 {
		t.Fatal("unconfirmed Node helper released paired work", firstErr)
	}
	os.WriteFile(stopFile, []byte(`{"state":"stopped","job":"not-created-yet","generation":2}`), 0600)
	handled, err := s.deferAutoInterruptedWorker(context.Background(), a, first, []byte(`{"state":"failed","exit_code":124}`), time.Now())
	if !handled || err != nil || !peer.WorkerRecoveryPrepared || !peer.DocumentationStopped || !peer.NodeStopped {
		t.Fatal("unstarted UUID treated as executed worker", err)
	}
	if !autoResumePairedWorkerRecovery(a, first.WorkerRecoveryAt) || peer.Status != "prepared" || peer.ID != "not-created-yet" || len(peer.DocumentationCopies) != 1 || !peer.WorkerRecoveryAt.IsZero() {
		t.Fatal("retained copy could not resume under same UUID")
	}
}
