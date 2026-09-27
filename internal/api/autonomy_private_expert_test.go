package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateExpertFixture(t *testing.T) (*Server, *autoRecord, autonomy.Proposal, time.Time) {
	t.Helper()
	s, a, p, now := expertFixture(t)
	old := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	source := autoFindJob(a, old.SourceTaskID)
	h := autoSHA([]byte("integration root"))
	oid := strings.Repeat("a", 40)
	base := autoPrivateIntegrationPin{RootID: h, ProjectID: p.ProjectID, SourceTaskID: 900, SourceJob: "99999999-9999-4999-8999-999999999999", SourceSHA: autoSHA([]byte("approved input")), BaseKind: "canonical", BaseRevision: oid, BaseTree: oid, Paths: []string{"src/**"}, Acceptance: old.Acceptance, SourceAcceptance: []string{"approved original input acceptance"}}
	base.Key = autoPrivatePinKey(base)
	run := &autoPrivateIntegrationRun{RootID: h, ProjectID: p.ProjectID, SourceTaskID: 900}
	for i := 1; i <= 3; i++ {
		task := int64(100 + i)
		if i == 1 {
			task = old.RootTaskID
		}
		if i == 3 {
			task = source.TaskID
		}
		v := &autoPrivateIntegrationAttempt{ID: autoSHA([]byte{byte(i)}), RootID: h, Number: i, Status: "rejected", TaskID: task, ReviewerTaskID: source.ReviewTaskID, Authority: autoPrivateIntegrationAuthority{Pin: base}}
		run.Attempts = append(run.Attempts, v)
	}
	v := run.Attempts[2]
	v.JobID = source.ID
	v.Candidate = &autoPrivateCandidate{IntegrationID: v.ID, BuilderJob: source.ID, BuilderArchiveSHA: old.SourceSHA, TreeSHA: autoSHA([]byte("failed exact combined tree")), ReceiptSHA: autoSHA([]byte("candidate seal"))}
	v.Review = &autoPrivateReview{TaskID: old.ReviewTaskID, ArchiveSHA: old.ReviewSHA, Approved: false}
	source.PrivateIntegrationRoot = h
	source.PrivateIntegrationAttempt = 3
	source.RepairSourceTaskID = 0
	source.RepairAttemptTaskID = 0
	a.PrivateIntegration = &autoPrivateIntegrationLedger{Pins: map[string]*autoPrivateIntegrationPin{base.Key: &base}, Runs: map[string]*autoPrivateIntegrationRun{h: run}}
	pin := *old
	pin.PrivateIntegration = &base
	pin.PrivateSourceAttemptID = v.ID
	pin.PrivateCandidate = v.Candidate
	pin.Key = autoExpertPinKey(&pin)
	a.ExpertRecovery.Pins[pin.Key] = &pin
	p.ExpertProgressKey = pin.Key
	a.State.Items = []autonomy.Proposal{p}
	return s, a, p, now
}
func TestPrivateIntegrationExpertAdapterRequiresActualIndependentEvidence(t *testing.T) {
	s, a, p, now := privateExpertFixture(t)
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil {
		t.Fatal("expert without independent probes admitted")
	}
	expertAudits(t, s, a, p, now, "new independently executed counterexample")
	expert := expertReserve(t, a, p, now)
	v, e := autoReserveExpertPrivateIntegration(a, p, expert)
	if e != nil {
		t.Fatal(e)
	}
	if v.Number != 4 || v.RootID != a.ExpertRecovery.Pins[p.ExpertProgressKey].PrivateIntegration.RootID || v.ExpertLeaseKey != expert.LeaseKey {
		t.Fatal("dual authority lost original root or monotonic attempt")
	}
	again, e := autoReserveExpertPrivateIntegration(a, p, expert)
	if e != nil || again != v {
		t.Fatal("duplicate private attempt", e)
	}
	if e = autoRecordPrivatePublication(v, autoPrivatePublication{}); e == nil {
		t.Fatal("expert admission bypassed candidate/test/review")
	}
	run := a.PrivateIntegration.Runs[v.RootID]
	if run.Attempts[2].Status != "rejected" || run.Attempts[2].Review.Approved {
		t.Fatal("original rejection overwritten")
	}
	raw, _ := json.Marshal(a)
	var restored autoRecord
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	restoredExpert := restored.ExpertRecovery.Attempts[expert.RootTaskID][0]
	again, e = autoReserveExpertPrivateIntegration(&restored, p, restoredExpert)
	if e != nil || again.ID != v.ID || len(restored.PrivateIntegration.Runs[v.RootID].Attempts) != 4 {
		t.Fatal("restart duplicated dual reservation", e)
	}
}
func TestPrivateIntegrationExpertPinCannotSwapCandidateOrScope(t *testing.T) {
	_, a, p, _ := privateExpertFixture(t)
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	if _, e := autoExpertPin(a, p); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(pin)
	var copy autoExpertRecoveryPin
	json.Unmarshal(raw, &copy)
	copy.PrivateIntegration.Paths = []string{"**"}
	copy.PrivateIntegration.Key = autoPrivatePinKey(*copy.PrivateIntegration)
	copy.Key = autoExpertPinKey(&copy)
	a.ExpertRecovery.Pins[copy.Key] = &copy
	p.ExpertProgressKey = copy.Key
	if _, e := autoExpertPin(a, p); e == nil {
		t.Fatal("scope widened through expert selector")
	}
	p.ExpertProgressKey = pin.Key
	v := a.PrivateIntegration.Runs[pin.PrivateIntegration.RootID].Attempts[2]
	v.Candidate = &autoPrivateCandidate{TreeSHA: autoSHA([]byte("other tree"))}
	if _, e := autoExpertPin(a, p); e == nil {
		t.Fatal("rejected candidate replaced")
	}
}
func TestPrivateIntegrationLateCancellationDiagnosticRecordsWithoutReopening(t *testing.T) {
	a, v, j, now := privateFixture(t)
	key := autoSHA([]byte("cancelled"))
	r := privateTestReceipt(t, a, v, j, now, key)
	v.Status = "unavailable"
	v.Reason = "quota stopped"
	j.Status = "stopped"
	r.State = "cancelled"
	r.Reason = "generation revoked"
	r.Executed = false
	r.RuntimeSHA = ""
	r.OutputSHA = ""
	r.ChargedMilliseconds = 0
	if e := autoRecordPrivateTest(a, r); e != nil {
		t.Fatal(e)
	}
	if v.Status != "unavailable" || !v.TestLeases[key].StopConfirmed {
		t.Fatal("late receipt changed lifecycle or left stop pending")
	}
	if _, e := autoReservePrivateTest(a, j, autoSHA([]byte("new")), now); e == nil {
		t.Fatal("terminal diagnostic authorized new execution")
	}
	if store.J(v.TestLeases[key].Receipt) != store.J(r) {
		t.Fatal("diagnostic lost")
	}
}

func publishPrivateFixture(t *testing.T, a *autoRecord, v *autoPrivateIntegrationAttempt, j *autoJob, now time.Time) {
	t.Helper()
	key := autoSHA([]byte(v.ID + " executed regression"))
	check := privateTestReceipt(t, a, v, j, now, key)
	if e := autoRecordPrivateTest(a, check); e != nil {
		t.Fatal(e)
	}
	review := autoPrivateReview{JobID: j.ID, TaskID: j.TaskID, CandidateSHA: v.Candidate.TreeSHA, ArchiveSHA: key, ReportSHA: key, Approved: true, TestReceiptSHAs: []string{key}}
	if e := autoRecordPrivateReview(v, review); e != nil {
		t.Fatal(e)
	}
	pub := autoPrivatePublication{ID: v.ID, RootID: v.RootID, ProjectID: v.Authority.Pin.ProjectID, PinKey: v.PinKey, CandidateSHA: v.Candidate.TreeSHA, Commit: v.Candidate.CandidateCommit, GitTree: v.Candidate.GitTree, Parent: v.Authority.Pin.BaseRevision, Ref: "refs/lectern/integrations/" + v.ID, ReceiptSHA: key, ReviewReportSHA: key, State: "published_private", ConsumerReady: true}
	if e := autoRecordPrivatePublication(v, pub); e != nil {
		t.Fatal(e)
	}
}
func TestPrivateIntegrationExpertSuccessAllowsOnlyProvenAdvancedBase(t *testing.T) {
	a, approved, j, now := privateFixture(t)
	approved.Authority.Pin.Acceptance = []string{"same invariant"}
	publishPrivateFixture(t, a, approved, j, now)
	a.Jobs = append(a.Jobs, &autoJob{ID: approved.JobID, TaskID: approved.TaskID, Role: "builder", Status: "done", PrivateIntegrationRoot: approved.RootID, PrivateIntegrationAttempt: 1})
	// An independently published intervening change in another integration root
	// extends the exact managed publication ancestry, without editing old receipts.
	h := autoSHA([]byte("intervening integration"))
	base := approved.Authority.Pin
	base.RootID = h
	base.BasePublicationID = approved.ID
	base.BaseRevision = approved.Publication.Commit
	base.BaseTree = approved.Publication.GitTree
	base.Key = autoPrivatePinKey(base)
	middle := &autoPrivateIntegrationAttempt{ID: h, RootID: h, Number: 1, PinKey: base.Key, Status: "reserved", JobID: "66666666-6666-4666-8666-666666666666", TaskID: 10, ReviewerJob: "77777777-7777-4777-8777-777777777777", ReviewerTaskID: 11, Generation: 1, Authority: autoPrivateIntegrationAuthority{Pin: base}}
	a.PrivateIntegration.Runs[h] = &autoPrivateIntegrationRun{RootID: h, ProjectID: 1, Attempts: []*autoPrivateIntegrationAttempt{middle}}
	candidate := *approved.Candidate
	candidate.IntegrationID = h
	candidate.PinKey = base.Key
	candidate.BuilderJob = middle.JobID
	candidate.TreeSHA = h
	candidate.CandidateCommit = strings.Repeat("d", 40)
	candidate.GitTree = strings.Repeat("e", 40)
	if e := autoRecordPrivateCandidate(middle, candidate); e != nil {
		t.Fatal(e)
	}
	mj := &autoJob{ID: middle.ReviewerJob, TaskID: 11, Role: "reviewer", Status: "running", Provider: "codex", PrivateIntegrationRoot: h, PrivateIntegrationAttempt: 1}
	a.Jobs = append(a.Jobs, mj)
	publishPrivateFixture(t, a, middle, mj, now)
	failedPin := approved.Authority.Pin
	failedPin.BasePublicationID = middle.ID
	failedPin.BaseRevision = middle.Publication.Commit
	failedPin.BaseTree = middle.Publication.GitTree
	failed := &autoPrivateIntegrationAttempt{ID: autoSHA([]byte("later rejection")), RootID: approved.RootID, Number: 2, TaskID: 20, Status: "rejected", ReviewerTaskID: 21, Authority: autoPrivateIntegrationAuthority{Pin: failedPin}, Review: &autoPrivateReview{TaskID: 21, Approved: false}}
	a.PrivateIntegration.Runs[approved.RootID].Attempts = append(a.PrivateIntegration.Runs[approved.RootID].Attempts, failed)
	a.Jobs = append(a.Jobs, &autoJob{TaskID: 20, Role: "builder", Status: "done", PrivateIntegrationRoot: approved.RootID, PrivateIntegrationAttempt: 2})
	expert := &autoExpertRecoveryAttempt{Status: "approved", TaskID: approved.TaskID, ReviewTaskID: 2, Proposal: autonomy.Proposal{Acceptance: []string{"same invariant"}}}
	pin := &autoExpertRecoveryPin{ProjectID: 1, SourceTaskID: 20, PrivateIntegration: &failedPin, PrivateCandidate: &candidate, SourceAcceptanceSHA: autoSHA([]byte(store.J(expert.Proposal.Acceptance)))}
	if !autoExpertLaterRejectedMilestone(a, pin, expert) {
		t.Fatal("genuine advanced managed ancestor with same acceptance blocked")
	}
	failed.Authority.Pin.BaseTree = approved.Publication.GitTree
	if autoExpertLaterRejectedMilestone(a, pin, expert) {
		t.Fatal("title/log-only change renewed satisfied scope")
	}
	failed.Authority.Pin.BaseTree = middle.Publication.GitTree
	failed.Authority.Pin.BasePublicationID = autoSHA([]byte("unattested unrelated branch"))
	if autoExpertLaterRejectedMilestone(a, pin, expert) {
		t.Fatal("unproven ancestry renewed success")
	}
}

func TestPrivateIntegrationActualExpertPinUsesIntegrationHistoryNotOrdinaryRepairCount(t *testing.T) {
	s, a, p, _ := privateExpertFixture(t)
	project, e := s.DB.Project(p.ProjectID)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e = autoGit(context.Background(), dir, "init", "-b", "main"); e != nil {
		t.Fatal(e)
	}
	commitSource(t, dir, "current destination bytes")
	if e = s.DB.Update("projects", project.ID, map[string]any{"repo_path": dir}); e != nil {
		t.Fatal(e)
	}
	if e = s.DB.Update("targets", project.TargetID, map[string]any{"kind": "local"}); e != nil {
		t.Fatal(e)
	}
	sourceTask, e := s.DB.InsertTask(&store.Task{ProjectID: project.ID, Title: "approved original input", Status: "done"})
	if e != nil {
		t.Fatal(e)
	}
	reviewTask, e := s.DB.InsertTask(&store.Task{ProjectID: project.ID, Title: "approved input review", Status: "done"})
	if e != nil {
		t.Fatal(e)
	}
	input := &autoJob{ID: "88888888-8888-4888-8888-888888888888", TaskID: sourceTask.ID, Role: "builder", Status: "done", Approved: true, ReviewTaskID: reviewTask.ID, Admission: &autoAdmission{Proposal: autonomy.Proposal{Acceptance: []string{"original input"}}}}
	review := &autoJob{ID: "99999999-9999-4999-8999-999999999999", TaskID: reviewTask.ID, Role: "reviewer", Status: "done"}
	a.Jobs = append(a.Jobs, input, review)
	selected := autoFindJob(a, p.ExpertRecoveryTaskID)
	run := a.PrivateIntegration.Runs[selected.PrivateIntegrationRoot]
	delete(a.PrivateIntegration.Runs, run.RootID)
	newRoot := autoSHA([]byte(fmt.Sprintf("private-adaptation-v1:%d:%d", input.TaskID, p.ProjectID)))
	run.RootID = newRoot
	run.SourceTaskID = input.TaskID
	a.PrivateIntegration.Runs[newRoot] = run
	selected.PrivateIntegrationRoot = newRoot
	sha := strings.Repeat("a", 64)
	for _, v := range run.Attempts {
		v.RootID = newRoot
		v.Authority.Pin.RootID = newRoot
		v.Authority.Pin.SourceTaskID = input.TaskID
		v.Authority.Pin.SourceJob = input.ID
		v.Authority.Pin.SourceSHA = sha
		v.Authority.Pin.Key = autoPrivatePinKey(v.Authority.Pin)
	}
	prior := run.Attempts[2]
	prior.Review.JobID = autoFindJob(a, selected.ReviewTaskID).ID
	toolDir := t.TempDir()
	report := `{"approve":true,"outcome":"completed","reason":"independent original acceptance passed"}`
	body := "#!/usr/bin/python3\nimport sys,json,hashlib\nmode=sys.argv[3]\nreport=" + fmt.Sprintf("%q", report) + "\nif mode=='archive-identity': print(json.dumps({'state':'ready','sha256':'" + sha + "'}))\nelif mode=='archive-report': print(json.dumps({'state':'ready','archive_sha256':'" + sha + "','report_sha256':hashlib.sha256(report.encode()).hexdigest(),'report':report}))\nelif mode=='integration-tip': print(json.dumps({'state':'absent'}))\nelse: raise SystemExit(2)\n"
	if e = os.WriteFile(filepath.Join(toolDir, "sudo"), []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", toolDir+":"+os.Getenv("PATH"))
	if count := autoRepairAttempts(a, run.Attempts[0].TaskID); count >= a.Config.MaxRevisionRounds {
		t.Fatalf("test did not exclude ordinary repair authority: %d", count)
	}
	pin, e := s.pinAutoExpertRecovery(context.Background(), a, p.ProjectID, selected.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	if pin.PrivateIntegration == nil || pin.PrivateIntegration.RootID != newRoot || pin.PrivateSourceAttemptID != prior.ID || pin.PrivateIntegration.PriorBuilderSHA != sha {
		t.Fatal("integration recovery pin lost retained source")
	}
	if pin.PrivateIntegration.BaseRevision == prior.Authority.Pin.BaseRevision {
		t.Fatal("current destination was not repinned before audits")
	}
	if input.Rejected || !input.Approved || selected.Approved {
		t.Fatal("pinning rewrote original decisions")
	}
}
