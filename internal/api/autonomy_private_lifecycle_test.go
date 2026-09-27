package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestPrivateRequestPublicationImmutableBoundedAndSafe(t *testing.T) {
	root := t.TempDir()
	job := autoUUID()
	id := autoSHA([]byte("operation"))
	raw := []byte(store.J(map[string]any{"schema_version": 1, "owner_job": job, "integration_id": id}))
	if e := autoWritePrivateRequestAt(root, job, id, "prepare", raw); e != nil {
		t.Fatal(e)
	}
	if e := autoWritePrivateRequestAt(root, job, id, "prepare", raw); e != nil {
		t.Fatal("lost idempotency", e)
	}
	changed := append(raw[:len(raw)-1:len(raw)-1], []byte(",\"changed\":true}")...)
	if autoWritePrivateRequestAt(root, job, id, "prepare", changed) == nil {
		t.Fatal("overwrote sealed request")
	}
	if autoWritePrivateRequestAt(root, autoUUID(), id, "prepare", raw) == nil {
		t.Fatal("accepted different worker")
	}
	if autoWritePrivateRequestAt(root, job, id, "../../escape", raw) == nil {
		t.Fatal("accepted traversal")
	}
	body := map[string]any{"schema_version": 1, "owner_job": job, "integration_id": id, "fixture": strings.Repeat("x", 300<<10)}
	large := []byte(store.J(body))
	if autoWritePrivateRequestAt(root, job, id, "seal", large) == nil {
		t.Fatal("unbounded non-check request")
	}
	if e := autoWritePrivateRequestAt(root, job, id, "check-"+autoSHA(large), large); e != nil {
		t.Fatal("bounded check fixture rejected", e)
	}
	other := autoUUID()
	if e := os.Symlink(filepath.Join(root, job), filepath.Join(root, other)); e != nil {
		t.Fatal(e)
	}
	body["owner_job"] = other
	if autoWritePrivateRequestAt(root, other, id, "audit", []byte(store.J(body))) == nil {
		t.Fatal("followed job symlink")
	}
	st, e := os.Stat(filepath.Join(root, job, "integration-requests", id+".prepare.json"))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("request permissions", e)
	}
}

func TestPrivateReviewerCorrectionRetainsCandidateBudgetAndIndependentReceipts(t *testing.T) {
	a, v, j, now := privateFixture(t)
	key := autoSHA([]byte("independent-test"))
	r := privateTestReceipt(t, a, v, j, now, key)
	if e := autoRecordPrivateTest(a, r); e != nil {
		t.Fatal(e)
	}
	if e := autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":600000}`)); e != nil {
		t.Fatal(e)
	}
	j.ReportError = "receipt citation omitted"
	oldID := j.ID
	before := store.J(v.Candidate)
	receiptBefore := store.J(v.Tests)
	clone, e := autoQueuePrivateResume(a, j, autoUUID(), autoSHA([]byte("exact archived failed transport")))
	if e != nil {
		t.Fatal(e)
	}
	if clone.TaskID != j.TaskID || clone.ReportRepairs != 1 || v.ReviewerJob != clone.ID || len(a.PrivateIntegration.Runs[v.RootID].Attempts) != 1 {
		t.Fatal("correction changed reservation")
	}
	if clone.DocumentationCopies[0].Command != "copy-archive-resume" || clone.DocumentationCopies[0].SourceJob != oldID {
		t.Fatal("lost malformed archived report")
	}
	raw, _ := json.Marshal(a)
	var restored autoRecord
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	same := autoFindJob(&restored, j.TaskID)
	if same.ID != clone.ID {
		t.Fatal("restart lost latest owner")
	}
	if left, e := autoProgressRemaining(&restored, same); e != nil || left != 20*time.Minute {
		t.Fatal("retry reset runtime", left, e)
	}
	rv, _ := autoPrivateAttempt(&restored, same)
	if store.J(rv.Candidate) != before || store.J(rv.Tests) != receiptBefore {
		t.Fatal("rewrote prior independent evidence")
	}
	approved := []byte(`{"outcome":"completed","approve":true,"reason":"All acceptance checked; retained independent receipt ` + key + `"}`)
	if e = autoValidatePrivateReport(&restored, same, approved); e != nil {
		t.Fatal("lost same-task retained check", e)
	}
	if autoValidatePrivateReport(&restored, same, []byte(`{"outcome":"completed","approve":true,"reason":"looks fine"}`)) == nil {
		t.Fatal("accepted unsupported approval")
	}
	repeated, e := autoQueuePrivateResume(a, j, autoUUID(), autoSHA([]byte("same archive")))
	if e != nil || repeated.ID != clone.ID {
		t.Fatal("duplicate retry reservation", e)
	}
}

func TestPrivateAdmissionAndPreparedRequestFreezeAuditedScope(t *testing.T) {
	a, v, j, _ := privateFixture(t)
	j.Role = "builder"
	j.TaskID = v.TaskID
	j.ID = v.JobID
	yes := true
	a.State = &autonomy.State{Phase: autonomy.Build, Items: []autonomy.Proposal{{ProjectID: 1, IntegrationTaskID: 3, IntegrationPin: v.PinKey, IntegrationPaths: []string{"src/**"}}}, Audits: map[string]autonomy.Verdict{"auditor_a": {Approve: &yes}, "auditor_b": {Approve: &yes}}}
	v.Authority.Pin.Paths = []string{"src/**"}
	j.Admission = autoNewAdmission(a, j)
	if j.Admission == nil || j.Admission.PrivateIntegration == nil {
		t.Fatal("missing frozen private authority")
	}
	v.Authority.Pin.Paths[0] = "other/**"
	if j.Admission.PrivateIntegration.Pin.Paths[0] != "src/**" {
		t.Fatal("admission mutated with ledger")
	}
	m := autoPrivateEnvelope(&v.Authority.Pin, v.ID, j)
	if e := autoQueuePrivate(j, v.ID, "prepare", 1, m); e != nil {
		t.Fatal(e)
	}
	before := string(j.PrivateOperations[0].Request)
	m["owner_task"] = int64(99)
	if string(j.PrivateOperations[0].Request) != before {
		t.Fatal("request aliases input map")
	}
	if autoQueuePrivate(j, v.ID, "prepare", 1, m) == nil {
		t.Fatal("replaced immutable request")
	}
}

func TestPrivateBuilderCannotRerunSealedCandidateAndSchemaExhaustionReleasesLease(t *testing.T) {
	a, v, j, now := privateFixture(t)
	j.Role = "builder"
	j.TaskID = v.TaskID
	j.ID = v.JobID
	if _, e := autoQueuePrivateResume(a, j, autoUUID(), autoSHA([]byte("archive"))); e == nil {
		t.Fatal("reran sealed builder")
	}
	a.State = &autonomy.State{Phase: autonomy.Paused}
	j.ReportRepairs = 2
	if autoReportRepairReady(a, j, now) {
		t.Fatal("reset correction allowance")
	}
	if v.Status != "unavailable" || a.State.Phase != autonomy.Complete || v.Review != nil {
		t.Fatal("schema exhaustion fabricated verdict or retained active lease")
	}
}

func TestPrivateSourceConsumerRefusesAnotherPublishedTree(t *testing.T) {
	id := autoSHA([]byte("publication"))
	tree := autoSHA([]byte("sealed-tree"))
	commit := strings.Repeat("a", 64)
	receipt := autoSHA([]byte("published-receipt"))
	request := map[string]any{"publication_receipt_sha256": receipt, "commit": commit, "tree_oid": tree, "tree_sha256": tree}
	op := &autoPrivateOperation{IntegrationID: id, Phase: "consume", Request: []byte(store.J(request))}
	exact := []byte(store.J(request))
	if e := autoValidatePrivateTerminal(op, exact); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"publication_receipt_sha256", "commit", "tree_oid", "tree_sha256"} {
		bad := map[string]any{}
		for k, v := range request {
			bad[k] = v
		}
		bad[key] = autoSHA([]byte("other"))
		if autoValidatePrivateTerminal(op, []byte(store.J(bad))) == nil {
			t.Fatal("accepted substituted", key)
		}
	}
}

func TestPrivateExpertRetryKeepsBothLeasesAndChargesBothBudgets(t *testing.T) {
	s, a, p, now := privateExpertFixture(t)
	expertAudits(t, s, a, p, now, "new independently verified repair strategy")
	expert := expertReserve(t, a, p, now)
	v, e := autoReserveExpertPrivateIntegration(a, p, expert)
	if e != nil {
		t.Fatal(e)
	}
	j := &autoJob{ID: expert.JobID, TaskID: 998, Role: "builder", Status: "stopped", PrivateIntegrationRoot: v.RootID, PrivateIntegrationAttempt: v.Number}
	v.TaskID = j.TaskID
	if e = autoProgressBinding(a, j, expert); e != nil {
		t.Fatal(e)
	}
	j.Admission = autoNewAdmission(a, j)
	a.Jobs = append(a.Jobs, j)
	if j.Admission == nil || j.Admission.PrivateIntegration == nil || j.Admission.ExpertRecovery == nil {
		t.Fatal("combined immutable authority missing")
	}
	if e = autoProgressCharge(a, j, []byte(`{"elapsed_milliseconds":300000}`)); e != nil {
		t.Fatal(e)
	}
	clone, e := autoQueuePrivateResume(a, j, autoUUID(), autoSHA([]byte("archived interrupted implementation")))
	if e != nil {
		t.Fatal(e)
	}
	if expert.JobID != clone.ID || v.JobID != clone.ID || clone.ExpertRecoveryAttempt != expert.Number || clone.PrivateIntegrationAttempt != v.Number {
		t.Fatal("split execution ownership")
	}
	if e = autoProgressCharge(a, clone, []byte(`{"elapsed_milliseconds":120000}`)); e != nil {
		t.Fatal(e)
	}
	if expert.ChargedMilliseconds != 420000 || v.ChargedMilliseconds != 420000 {
		t.Fatal("one allowance reset", expert.ChargedMilliseconds, v.ChargedMilliseconds)
	}
	if remaining, e := autoProgressRemaining(a, clone); e != nil || remaining != 23*time.Minute {
		t.Fatal("combined remaining budget", remaining, e)
	}
	if prior := a.PrivateIntegration.Runs[v.RootID].Attempts[2]; prior.Status != "rejected" || prior.Review.Approved {
		t.Fatal("rewrote original private rejection")
	}
}

func TestPrivateExpertLowerBudgetClosesBothLeasesWithoutZeroSecondLaunch(t *testing.T) {
	s, a, p, now := privateExpertFixture(t)
	expertAudits(t, s, a, p, now, "new progress for bounded private recovery")
	expert := expertReserve(t, a, p, now)
	v, e := autoReserveExpertPrivateIntegration(a, p, expert)
	if e != nil {
		t.Fatal(e)
	}
	j := &autoJob{ID: expert.JobID, TaskID: 998, Role: "builder", Status: "prepared", PrivateIntegrationRoot: v.RootID, PrivateIntegrationAttempt: v.Number}
	v.TaskID = j.TaskID
	if e = autoProgressBinding(a, j, expert); e != nil {
		t.Fatal(e)
	}
	a.Jobs = append(a.Jobs, j)
	expert.ChargedMilliseconds = (30 * time.Minute).Milliseconds()
	v.ChargedMilliseconds = 1000
	ended, e := s.endAutoProgressBudget(context.Background(), a, j)
	if e != nil || !ended {
		t.Fatal("lower lease did not close", ended, e)
	}
	if v.Status != "budget_exhausted" || expert.Status != "budget_exhausted" || j.Status != "failed" || a.State.Phase != autonomy.Complete {
		t.Fatal("split budget terminal state")
	}
}

func TestPrivateApprovalCannotSubstituteNewUntestedRuntime(t *testing.T) {
	a, v, j, now := privateFixture(t)
	key := autoSHA([]byte("test-old-env"))
	r := privateTestReceipt(t, a, v, j, now, key)
	r.PythonBundleKey = autoSHA([]byte("old"))
	if e := autoRecordPrivateTest(a, r); e != nil {
		t.Fatal(e)
	}
	j.PythonUsedBundle = autoSHA([]byte("new"))
	j.PythonRecovery = &autoPythonReceipt{State: "verified", BundleKey: j.PythonUsedBundle}
	raw := []byte(`{"outcome":"completed","approve":true,"reason":"verified ` + key + `"}`)
	if autoValidatePrivateReport(a, j, raw) == nil {
		t.Fatal("published new environment using old test")
	}
}

func TestPrivateCorrectionDeliversPinnedPriorEvidenceWithoutReplacingBase(t *testing.T) {
	prior := autoUUID()
	review := autoUUID()
	sha := autoSHA([]byte("failed combined candidate"))
	rsha := autoSHA([]byte("independent rejection"))
	pin := &autoPrivateIntegrationPin{PriorBuilderJob: prior, PriorBuilderSHA: sha, PriorReviewJob: review, PriorReviewSHA: rsha}
	j := &autoJob{DocumentationCopies: []autoDocumentationCopy{{Command: "copy-archive-review", SourceJob: prior, SHA: sha}}}
	if e := autoPrivatePriorCopies(j, pin); e != nil {
		t.Fatal(e)
	}
	if len(j.DocumentationCopies) != 2 {
		t.Fatal("duplicated causal evidence")
	}
	for _, c := range j.DocumentationCopies {
		if c.Command != "copy-archive-review" {
			t.Fatal("historical candidate replaces fresh base")
		}
	}
	pin.PriorBuilderSHA = autoSHA([]byte("other bytes"))
	if autoPrivatePriorCopies(j, pin) == nil {
		t.Fatal("accepted unpinned prior version")
	}
}
