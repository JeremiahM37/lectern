package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"strings"
	"testing"
	"time"
)

func privateFixture(t *testing.T) (*autoRecord, *autoPrivateIntegrationAttempt, *autoJob, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	h := strings.Repeat("a", 64)
	oid := strings.Repeat("b", 40)
	job := "11111111-1111-4111-8111-111111111111"
	review := "22222222-2222-4222-8222-222222222222"
	pin := autoPrivateIntegrationPin{Key: h, RootID: h, ProjectID: 1, BaseRevision: oid, BaseTree: oid}
	v := &autoPrivateIntegrationAttempt{ID: h, RootID: h, PinKey: h, Number: 1, Status: "reserved", JobID: job, TaskID: 1, ReviewerJob: review, ReviewerTaskID: 2, Generation: 1, Authority: autoPrivateIntegrationAuthority{Pin: pin}}
	a := &autoRecord{Config: autonomy.DefaultConfig(), PrivateIntegration: &autoPrivateIntegrationLedger{Runs: map[string]*autoPrivateIntegrationRun{h: {RootID: h, ProjectID: 1, Attempts: []*autoPrivateIntegrationAttempt{v}}}}}
	expertQuota(a, now)
	j := &autoJob{ID: review, TaskID: 2, Role: "reviewer", Status: "running", Provider: "codex", PrivateIntegrationRoot: h, PrivateIntegrationAttempt: 1}
	a.Jobs = []*autoJob{j}
	c := autoPrivateCandidate{IntegrationID: h, PinKey: h, BuilderJob: job, ArchiveSHA: h, TreeSHA: h, ManifestSHA: h, BaseTree: oid, ReceiptSHA: h, CandidateCommit: oid, GitTree: oid, BaseTreeSHA: autoSHA([]byte("different base bytes")), BuilderArchiveSHA: h, PrepareRequestSHA: h}
	if e := autoRecordPrivateCandidate(v, c); e != nil {
		t.Fatal(e)
	}
	return a, v, j, now
}
func privateTestReceipt(t *testing.T, a *autoRecord, v *autoPrivateIntegrationAttempt, j *autoJob, now time.Time, key string) autoPrivateTestReceipt {
	t.Helper()
	l, e := autoReservePrivateTest(a, j, key, now)
	if e != nil {
		t.Fatal(e)
	}
	zero := 0
	return autoPrivateTestReceipt{IntegrationID: v.ID, CandidateSHA: v.Candidate.TreeSHA, OwnerJob: j.ID, OwnerTask: j.TaskID, CheckID: l.ID, RequestSHA: key, Generation: l.Generation, ChargedMilliseconds: 1000, RuntimeSHA: key, OutputSHA: key, ReceiptSHA: key, State: "exited", Executed: true, ExitCode: &zero}
}
func TestPrivateIntegrationCandidateReviewPublicationAndConsumerBindings(t *testing.T) {
	a, v, j, now := privateFixture(t)
	h := strings.Repeat("c", 64)
	r := autoPrivateReview{JobID: j.ID, TaskID: j.TaskID, CandidateSHA: v.Candidate.TreeSHA, ArchiveSHA: h, ReportSHA: h, Approved: true, TestReceiptSHAs: []string{h}}
	if autoRecordPrivateReview(v, r) == nil {
		t.Fatal("approved without actual check")
	}
	test := privateTestReceipt(t, a, v, j, now, h)
	bad := test
	bad.CandidateSHA = strings.Repeat("d", 64)
	if autoRecordPrivateTest(a, bad) == nil {
		t.Fatal("accepted another candidate")
	}
	if e := autoRecordPrivateTest(a, test); e != nil {
		t.Fatal(e)
	}
	if e := autoRecordPrivateReview(v, r); e != nil {
		t.Fatal(e)
	}
	p := autoPrivatePublication{ID: v.ID, RootID: v.RootID, ProjectID: 1, PinKey: v.PinKey, CandidateSHA: v.Candidate.TreeSHA, Commit: v.Candidate.CandidateCommit, GitTree: v.Candidate.GitTree, Parent: v.Authority.Pin.BaseRevision, Ref: "refs/lectern/integrations/" + v.ID, ReceiptSHA: h, ReviewReportSHA: h, State: "published_private", ConsumerReady: true}
	badp := p
	badp.CanonicalChanged = true
	if autoRecordPrivatePublication(v, badp) == nil {
		t.Fatal("canonical mutation laundered")
	}
	if e := autoRecordPrivatePublication(v, p); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(a)
	var restored autoRecord
	if e := json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if _, e := autoPrivateIntegrationSource(&restored, v.ID, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := autoPrivateIntegrationSource(&restored, v.ID, 2); e == nil {
		t.Fatal("cross project consumer")
	}
	v.Candidate.TreeSHA = strings.Repeat("f", 64)
	if _, e := autoPrivateIntegrationSource(a, v.ID, 1); e == nil {
		t.Fatal("mutable candidate accepted")
	}
}
func TestPrivateIntegrationPathsRejectTransportTraversalAndOverlap(t *testing.T) {
	for _, paths := range [][]string{{"../x"}, {".git/config"}, {"a/**", "a/b"}, {"a", "a"}, {"autonomy-report.json"}, {".lectern-review/x"}, {"/x"}, {"a/*"}} {
		if _, e := autoPrivatePaths(paths); e == nil {
			t.Fatalf("accepted %q", paths)
		}
	}
	if _, e := autoPrivatePaths([]string{"src/**", "README.md"}); e != nil {
		t.Fatal(e)
	}
}
func TestPrivateIntegrationCheckBudgetReloadAndCorrectedReviewer(t *testing.T) {
	a, v, j, now := privateFixture(t)
	key := strings.Repeat("c", 64)
	r := privateTestReceipt(t, a, v, j, now, key)
	oldJob := j.ID
	j.ID = "33333333-3333-4333-8333-333333333333"
	v.ReviewerJob = j.ID
	if e := autoRecordPrivateTest(a, r); e != nil {
		t.Fatalf("same task old execution lost: %v", e)
	}
	if !v.TestLeases[key].StopConfirmed {
		t.Fatal("terminal test remains cancellation pending")
	}
	if v.Tests[0].OwnerJob != oldJob {
		t.Fatal("rewrote actual executor")
	}
	for i := 0; i < 5; i++ {
		key = autoSHA([]byte{byte(i)})
		if _, e := autoReservePrivateTest(a, j, key, now); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := autoReservePrivateTest(a, j, autoSHA([]byte("overflow")), now); e == nil {
		t.Fatal("exceeded reserved root budget")
	}
	expertQuota(a, now.Add(25*time.Hour))
	if _, e := autoReservePrivateTest(a, j, autoSHA([]byte("later")), now.Add(25*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e := autoReservePrivateTest(a, j, autoSHA([]byte("still unavailable")), now.Add(25*time.Hour)); e == nil {
		t.Fatal("unresolved leases expired into free budget")
	}
}
func TestPrivateIntegrationFailedAndTruncatedTestsCannotPublish(t *testing.T) {
	for _, mode := range []string{"failed", "truncated", "notexecuted"} {
		t.Run(mode, func(t *testing.T) {
			a, v, j, now := privateFixture(t)
			key := autoSHA([]byte(mode))
			r := privateTestReceipt(t, a, v, j, now, key)
			if mode == "failed" {
				one := 1
				r.ExitCode = &one
			}
			if mode == "truncated" {
				r.Truncated = true
			}
			if mode == "notexecuted" {
				r.Executed = false
				r.State = "unavailable"
				r.Reason = "runtime unavailable"
			}
			if e := autoRecordPrivateTest(a, r); e != nil {
				t.Fatal(e)
			}
			if autoRecordPrivateReview(v, autoPrivateReview{JobID: j.ID, TaskID: j.TaskID, CandidateSHA: v.Candidate.TreeSHA, ArchiveSHA: key, ReportSHA: key, Approved: true, TestReceiptSHAs: []string{key}}) == nil {
				t.Fatal("nonpassing evidence approved")
			}
		})
	}
}

func TestPrivateIntegrationReservationKeepsSemanticRootAndHistory(t *testing.T) {
	a, v, _, now := privateFixture(t)
	yes := true
	p := autonomy.Proposal{ProjectID: 1, IntegrationTaskID: 3, IntegrationPaths: []string{"src/**"}, Title: "adapt", Why: "useful", Acceptance: []string{"combined regression passes"}}
	source := &autoJob{ID: "33333333-3333-4333-8333-333333333333", TaskID: 3, Role: "builder", Status: "done", Approved: true, ReviewTaskID: 4, ReviewOutcome: "completed"}
	review := &autoJob{ID: "44444444-4444-4444-8444-444444444444", TaskID: 4, Role: "reviewer", Status: "done"}
	a.Jobs = append(a.Jobs, source, review)
	pin := v.Authority.Pin
	pin.SourceTaskID = 3
	pin.SourceJob = source.ID
	pin.ReviewTaskID = 4
	pin.ReviewJob = review.ID
	pin.Paths = p.IntegrationPaths
	pin.Acceptance = p.Acceptance
	pin.Key = autoPrivatePinKey(pin)
	p.IntegrationPin = pin.Key
	a.PrivateIntegration.Pins = map[string]*autoPrivateIntegrationPin{pin.Key: &pin}
	a.PrivateIntegration.Runs = map[string]*autoPrivateIntegrationRun{}
	a.State = &autonomy.State{Phase: autonomy.Build, Cycle: 1, Items: []autonomy.Proposal{p}, Audits: map[string]autonomy.Verdict{"auditor_a": {Approve: &yes}, "auditor_b": {Approve: &yes}}}
	job := "55555555-5555-4555-8555-555555555555"
	first, e := autoReservePrivateIntegration(a, p, job, []string{"codex"}, now)
	if e != nil {
		t.Fatal(e)
	}
	same, e := autoReservePrivateIntegration(a, p, job, []string{"codex"}, now)
	if e != nil || same != first {
		t.Fatal("lost idempotent reservation", e)
	}
	if _, e = autoReservePrivateIntegration(a, p, "66666666-6666-4666-8666-666666666666", []string{"codex"}, now); e == nil {
		t.Fatal("duplicate live owner")
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			a.State.Cycle++
			first, e = autoReservePrivateIntegration(a, p, job, []string{"codex"}, now)
			if e != nil {
				t.Fatal(e)
			}
		}
		first.Status = "rejected"
		if i == 0 {
			first.Status = "unavailable"
			first.Reason = "bounded schema corrections exhausted, archived"
		}
	}
	a.State.Cycle++
	p.Title = "cosmetic new title"
	a.State.Items[0] = p
	if _, e = autoReservePrivateIntegration(a, p, job, []string{"codex"}, now); e == nil {
		t.Fatal("cosmetic title reset limit")
	}
	pin.BaseRevision = strings.Repeat("d", 40)
	pin.Key = autoPrivatePinKey(pin)
	a.PrivateIntegration.Pins[pin.Key] = &pin
	p.IntegrationPin = pin.Key
	a.State.Items[0] = p
	if _, e = autoReservePrivateIntegration(a, p, job, []string{"codex"}, now); e == nil {
		t.Fatal("commit-only change reset limit")
	}
	pin.BaseTree = strings.Repeat("e", 40)
	pin.Key = autoPrivatePinKey(pin)
	a.PrivateIntegration.Pins[pin.Key] = &pin
	p.IntegrationPin = pin.Key
	a.State.Items[0] = p
	next, e := autoReservePrivateIntegration(a, p, job, []string{"codex"}, now)
	if e != nil || next.Number != 4 || next.RootID != first.RootID {
		t.Fatal("changed base lost root history", e)
	}
}

func TestPrivateIntegrationLegacyReviewerResolutionNeverRewritesHistory(t *testing.T) {
	a, _, _, _ := privateFixture(t)
	source := &autoJob{ID: "33333333-3333-4333-8333-333333333333", TaskID: 3, Role: "builder", Status: "done"}
	review := &autoJob{ID: "44444444-4444-4444-8444-444444444444", TaskID: 4, Role: "reviewer", Status: "done"}
	a.Jobs = append(a.Jobs, source, review)
	a.Runs = []*autonomy.State{{Assignments: []autonomy.Assignment{{TaskID: 3, Role: "builder", Completed: true}, {TaskID: 4, Role: "reviewer", Completed: true}}}}
	if !autoPrivateApprovedSource(a, 3) || autoPrivateSourceReviewer(a, source) != review {
		t.Fatal("legacy source cannot reach immutable review inspection")
	}
	if source.Approved || source.ReviewTaskID != 0 {
		t.Fatal("historical flags rewritten")
	}
	source.Rejected = true
	if autoPrivateApprovedSource(a, 3) {
		t.Fatal("latest explicit rejection lost")
	}
	source.Rejected = false
	source.DocumentationRoot = 3
	if autoPrivateApprovedSource(a, 3) {
		t.Fatal("unverified documentary artifact eligible")
	}
}
