package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestProgressReservationPreparationRestartAndAdmissionSnapshot(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	expertQuota(a, time.Now())
	id, v, err := s.reserveAutoProgress(context.Background(), a, "builder", autoUUID())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	next, same, err := s.reserveAutoProgress(context.Background(), loaded, "builder", autoUUID())
	if err != nil {
		t.Fatal(err)
	}
	if id != next || v.LeaseKey != same.LeaseKey || len(loaded.ExpertRecovery.Attempts[v.RootTaskID]) != 1 {
		t.Fatal("preparation restart consumed a new reservation")
	}
	j := &autoJob{ID: id, TaskID: 998, Role: "builder"}
	if err = autoProgressBinding(loaded, j, same); err != nil {
		t.Fatal(err)
	}
	j.Admission = autoNewAdmission(loaded, j)
	if j.Admission == nil || j.Admission.ExpertRecovery == nil {
		t.Fatal("missing admission evidence")
	}
	before := store.J(j.Admission.ExpertRecovery)
	same.Status = "running"
	same.Usage[id] = 23
	if store.J(j.Admission.ExpertRecovery) != before {
		t.Fatal("admission snapshot mutated with ledger")
	}
}

func TestProgressCopiesExactSourcesAndRetainedRootCannotAcquireRepair(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	for _, role := range []string{"builder", "auditor_a", "auditor_b"} {
		copies, err := autoProgressCopies(a, role)
		if err != nil {
			t.Fatal(err)
		}
		if len(copies) != 2 || copies[0].SourceJob != pin.SourceJob || copies[0].SHA != pin.SourceSHA || copies[1].SHA != pin.ReviewSHA {
			t.Fatalf("wrong %s evidence: %+v", role, copies)
		}
		if role == "builder" && copies[0].Command != "copy-archive-work" {
			t.Fatal("builder not pinned archive")
		}
	}
	source := autoFindJob(a, p.ExpertRecoveryTaskID)
	source.ExpertRecoveryRoot = pin.RootTaskID
	if _, err := s.autoRepairContinuation(a, p.ProjectID, source.TaskID); err == nil {
		t.Fatal("expert acquired ordinary repairs")
	}
	p.DocumentationTaskID = source.TaskID
	p.ExpertRecoveryTaskID = 0
	p.ExpertProgressKey = ""
	if _, _, _, _, err := s.autoDocumentationSource(a, p); err == nil {
		t.Fatal("expert acquired documentary allowance")
	}
	a.State.Items = []autonomy.Proposal{{ContinueTaskID: source.TaskID}}
	descendant := &autoJob{TaskID: 999, Role: "builder"}
	if err := autoProgressBinding(a, descendant, nil); err != nil {
		t.Fatal(err)
	}
	if descendant.ExpertRecoveryRoot != pin.RootTaskID || descendant.ExpertRecoveryAttempt != 0 {
		t.Fatal("continuation lost root or inherited terminal attempt")
	}
}

func TestProgressUsageAcrossBuilderAndReviewerRetries(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	v := expertReserve(t, a, p, now)
	v.TaskID = 998
	v.ReviewerTaskID = 999
	j := &autoJob{ID: v.JobID, TaskID: 998, Role: "builder", ExpertRecoveryRoot: v.RootTaskID, ExpertRecoveryAttempt: v.Number}
	if err := autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":900000}`)); err != nil {
		t.Fatal(err)
	}
	j.ID = autoUUID()
	if err := autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":600000}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := autoProgressRemaining(a, j); got != 5*time.Minute {
		t.Fatalf("reset builder budget: %s", got)
	}
	if err := autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":600000}`)); err != nil {
		t.Fatal(err)
	}
	if v.ChargedMilliseconds != 1500000 {
		t.Fatal("double charged duplicate poll")
	}
	j.Role = "reviewer"
	j.TaskID = 999
	j.ID = autoUUID()
	if err := autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":1200000}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := autoProgressRemaining(a, j); got != 10*time.Minute {
		t.Fatalf("wrong independent reviewer budget: %s", got)
	}
	j.ID = autoUUID()
	if err := autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":600000}`)); err == nil {
		t.Fatal("review retry reset budget")
	}
	if v.ChargedMilliseconds != 1500000 {
		t.Fatal("review charged builder")
	}
	if err := autoProgressCharge(a, j, []byte(`{}`)); err == nil {
		t.Fatal("missing accounting admitted")
	}
}

func TestProgressReviewPreparationReservationSurvivesReload(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	v := expertReserve(t, a, p, now)
	v.TaskID = 998
	b := &autoJob{ID: v.JobID, TaskID: 998, Role: "builder", Status: "done", ExpertRecoveryRoot: v.RootTaskID, ExpertRecoveryAttempt: v.Number}
	a.Jobs = append(a.Jobs, b)
	a.State.Phase = autonomy.Review
	a.State.Assignments = append(a.State.Assignments, autonomy.Assignment{TaskID: 998, Role: "builder", Completed: true, Item: a.State.Item, Step: a.State.Step, Round: a.State.Revision})
	id, _, err := s.reserveAutoProgress(context.Background(), a, "reviewer", autoUUID())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	again, attempt, err := s.reserveAutoProgress(context.Background(), loaded, "reviewer", autoUUID())
	if err != nil {
		t.Fatal(err)
	}
	if again != id || attempt.TaskID != 998 {
		t.Fatal("review preparation changed reservation")
	}
	j := &autoJob{ID: id, TaskID: 999, Role: "reviewer"}
	if err = autoProgressBinding(loaded, j, attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.TaskID != 998 || attempt.ReviewerTaskID != 999 {
		t.Fatal("reviewer replaced builder identity")
	}
	if _, err = autoExpertAttempt(loaded, j); err != nil {
		t.Fatal(err)
	}
}

func TestProgressExhaustionWaitsForArchiveWithoutForgingReview(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	v := expertReserve(t, a, p, now)
	v.TaskID = 998
	v.ChargedMilliseconds = 1800000
	j := &autoJob{ID: v.JobID, TaskID: 998, Role: "builder", Status: "stopped", ExpertRecoveryRoot: v.RootTaskID, ExpertRecoveryAttempt: v.Number}
	a.Jobs = append(a.Jobs, j)
	a.State.Assignments = append(a.State.Assignments, autonomy.Assignment{TaskID: 998, Role: "builder"})
	original := store.J(autoFindJob(a, p.ExpertRecoveryTaskID))
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "exporting"})
	if ended, err := s.endAutoProgressBudget(context.Background(), a, j); !ended || err == nil {
		t.Fatal("failed to await archive")
	}
	if autoExpertTerminal(v.Status) || a.State.Phase == autonomy.Complete {
		t.Fatal("completed before preservation")
	}
	docResponse(t, response, map[string]string{"state": "ready"})
	if ended, err := s.endAutoProgressBudget(context.Background(), a, j); !ended || err != nil {
		t.Fatalf("exhaustion failed: %v", err)
	}
	if v.Status != "budget_exhausted" || v.ReviewTaskID != 0 || j.Approved || j.Rejected || a.State.Phase != autonomy.Complete {
		t.Fatal("budget stop fabricated review")
	}
	if store.J(autoFindJob(a, p.ExpertRecoveryTaskID)) != original {
		t.Fatal("changed original rejected checkpoint")
	}
}

func TestProgressFinishedOwnerStillNeedsProbeCancellation(t *testing.T) {
	a := &autoRecord{ExpertRecovery: &autoExpertRecoveryLedger{Probes: map[string]*autoExpertProbeLease{"p": {ID: "p", OwnerJob: "finished"}}}}
	if !autoProgressPendingProbes(a) {
		t.Fatal("finished owner's probe escaped OFF")
	}
	a.ExpertRecovery.Probes["p"].StopConfirmed = true
	if autoProgressPendingProbes(a) {
		t.Fatal("confirmed stop repeated")
	}
}

func TestProgressReportExhaustionEndsSameLeaseWithoutReview(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	v := expertReserve(t, a, p, now)
	v.TaskID = 998
	j := &autoJob{ID: v.JobID, TaskID: 998, Role: "builder", Status: "failed", ReportRepairs: 2, ReportError: "invalid report", ExpertRecoveryRoot: v.RootTaskID, ExpertRecoveryAttempt: v.Number}
	if autoReportRepairReady(a, j, now.Add(time.Second)) {
		t.Fatal("unbounded schema retry")
	}
	if v.Status != "unavailable" || v.ReviewTaskID != 0 || len(a.ExpertRecovery.Attempts[v.RootTaskID]) != 1 || a.State.Phase != autonomy.Complete {
		t.Fatal("report exhaustion lost lease or fabricated review")
	}
}

func TestProgressAsyncCopyStopRetainsOwnershipUntilConfirmed(t *testing.T) {
	j := &autoJob{Status: "prepared", DocumentationCopies: []autoDocumentationCopy{{Command: "copy-archive-work"}}}
	for _, raw := range []string{`{"state":"stopping"}`, `{}`, `broken`} {
		if autoConfirmCopyStop(j, []byte(raw)) == nil || !autoDocumentationStopPending(j) {
			t.Fatal("unconfirmed asynchronous stop lost ownership")
		}
	}
	if err := autoConfirmCopyStop(j, []byte(`{"state":"stopped"}`)); err != nil || autoDocumentationStopPending(j) {
		t.Fatal("confirmed copy stop still pending")
	}
}

func TestProgressRetryPersistsAsyncCopyBeforeEffectsAndReload(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "new experiment")
	v := expertReserve(t, a, p, now)
	v.TaskID = 998
	old := &autoJob{ID: v.JobID, TaskID: 998, Role: "builder", Status: "stopped", ReportError: "malformed transport", ExpertRecoveryRoot: v.RootTaskID, ExpertRecoveryAttempt: v.Number, Admission: &autoAdmission{JobID: v.JobID, TaskID: 998, Proposal: p}}
	a.Jobs = append(a.Jobs, old)
	a.State.Assignments = append(a.State.Assignments, autonomy.Assignment{TaskID: 998, Role: "builder", Item: a.State.Item, Round: a.State.Revision})
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	t.Setenv("LECTERN_PROGRESS_CALLS", calls)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s %s\n' "$3" "$5" >> "$LECTERN_PROGRESS_CALLS"
case "$3" in
status) echo '{"state":"stopped","elapsed_milliseconds":600000}';;
snapshot) echo '{"state":"ready"}';;
archive-identity) echo '{"state":"ready","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}';;
copy-archive-resume) echo '{"state":"copying","copy_generation":1}';;
completion-stop|expert-probe-stop) echo '{"state":"stopped"}';;
*) exit 99;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.resumeAutoJob(context.Background(), a, old); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	next := autoFindJob(loaded, 998)
	if next.ID == old.ID || next.Status != "prepared" || next.ReportRepairs != 1 || len(next.DocumentationCopies) != 1 || next.DocumentationCopies[0].Command != "copy-archive-resume" || next.DocumentationCopies[0].SHA != strings.Repeat("a", 64) {
		t.Fatalf("lost durable retry: %+v", next)
	}
	if next.Admission.JobID != next.ID || v.ChargedMilliseconds != 600000 || old.ReportRepairs != 0 || old.Status != "stopped" {
		t.Fatal("retry reset usage or mutated source")
	}
	if err = s.launchAutoJob(context.Background(), loaded, next); err != nil {
		t.Fatal(err)
	}
	same, err := autoQueueProgressResume(loaded, autoFindJobByIDForProgress(loaded, old.ID), autoUUID(), strings.Repeat("a", 64))
	if err != nil || same.ID != next.ID {
		t.Fatal("duplicate response allocated another owner", err)
	}
	loaded.Config.Enabled = false
	s.stopAutoJobs(context.Background(), loaded, "off")
	if !next.DocumentationStopped {
		t.Fatal("OFF did not own pending copy")
	}
	raw, _ := os.ReadFile(calls)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "prepare ") || strings.HasPrefix(line, "copy ") || strings.HasPrefix(line, "start ") {
			t.Fatal("synchronous/untracked effect", line)
		}
	}
}

func autoFindJobByIDForProgress(a *autoRecord, id string) *autoJob {
	for _, j := range a.Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func TestProgressCopyGenerationResumesOnlyAfterConfirmedCancellation(t *testing.T) {
	s, a, _ := documentationFixture(t)
	response := documentationRunner(t)
	j := &autoJob{ID: autoUUID(), Status: "prepared", DocumentationStopped: true, DocumentationCopies: []autoDocumentationCopy{{Command: "copy-archive-resume", SourceJob: autoUUID(), SHA: strings.Repeat("a", 64), Generation: 2, CancelRequested: true}}}
	a.Jobs = append(a.Jobs, j)
	docResponse(t, response, map[string]any{"state": "copying", "copy_generation": 3})
	if ready, err := s.pollAutoDocumentationCopies(context.Background(), a, j); ready || err != nil {
		t.Fatal("resume failed", err)
	}
	if j.DocumentationCopies[0].Generation != 3 || j.DocumentationCopies[0].CancelRequested || j.DocumentationStopped {
		t.Fatal("generation not freshly authorized")
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	var same *autoJob
	for _, candidate := range loaded.Jobs {
		if candidate.ID == j.ID {
			same = candidate
		}
	}
	if same == nil {
		t.Fatal("authorization not persisted")
	}
	if ready, err := s.pollAutoDocumentationCopies(context.Background(), loaded, same); ready || err != nil || same.DocumentationCopies[0].Generation != 3 {
		t.Fatal("poll silently changed generation", err)
	}
	docResponse(t, response, map[string]any{"state": "copied", "copy_generation": 2, "source_archive_sha256": strings.Repeat("a", 64)})
	if _, err := s.pollAutoDocumentationCopies(context.Background(), loaded, same); err == nil {
		t.Fatal("revoked copy completion accepted")
	}
	same.DocumentationCopies[0].CancelRequested = true
	same.DocumentationStopped = false
	docResponse(t, response, map[string]any{"state": "stopping"})
	if ready, err := s.pollAutoDocumentationCopies(context.Background(), loaded, same); ready || err != nil || same.DocumentationCopies[0].Generation != 3 || !same.DocumentationCopies[0].CancelRequested {
		t.Fatal("unconfirmed stop gained new launch", err)
	}
}
