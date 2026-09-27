package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func expertFixture(t *testing.T) (*Server, *autoRecord, autonomy.Proposal, time.Time) {
	t.Helper()
	s, a, p := documentationFixture(t)
	p.ExpertRecoveryTaskID = p.DocumentationTaskID
	p.DocumentationTaskID = 0
	p.Title = "bounded causal recovery"
	f := documentationRunner(t)
	docResponse(t, f, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
	pin, e := s.pinAutoExpertRecovery(context.Background(), a, p.ProjectID, p.ExpertRecoveryTaskID)
	if e != nil {
		t.Fatal(e)
	}
	p.ExpertProgressKey = pin.Key
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	expertQuota(a, now)
	a.State.Items = []autonomy.Proposal{p}
	return s, a, p, now
}
func expertQuota(a *autoRecord, now time.Time) {
	a.Config.Enabled = true
	stamp := float64(now.Unix())
	reset := stamp + 3600
	remaining := 70.0
	used := 30.0
	a.Quota.Providers = []autonomy.ProviderUsage{{ID: "codex", Status: "ok", UpdatedAt: &stamp, Buckets: []autonomy.UsageBucket{{ID: "codex", Windows: []autonomy.UsageWindow{{Label: "Weekly", RemainingPercent: &remaining, UsedPercent: &used, ResetsAt: &reset}}}}}}
}
func expertAudits(t *testing.T, s *Server, a *autoRecord, p autonomy.Proposal, now time.Time, experiment string) {
	t.Helper()
	a.State.Phase = autonomy.Audit
	a.State.Assignments = nil
	a.State.Audits = map[string]autonomy.Verdict{}
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	for index, role := range []string{"auditor_a", "auditor_b"} {
		task, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: role, Status: "running"})
		if e != nil {
			t.Fatal(e)
		}
		j := &autoJob{ID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", task.ID), TaskID: task.ID, Role: role, Status: "running"}
		a.Jobs = append(a.Jobs, j)
		a.State.Assignments = append(a.State.Assignments, autonomy.Assignment{TaskID: task.ID, Role: role, Round: a.State.Revision})
		lease, e := autoReserveExpertProbe(a, p, role, autoSHA([]byte(experiment)), []string{"codex"}, now)
		if e != nil {
			t.Fatal(e)
		}
		exit := 1
		executed := true
		r := autoExpertProbeReceipt{Executed: &executed, RequestKey: lease.RequestKey, Profile: "ordinary180", ProbeID: lease.ID, ProgressKey: pin.Key, SourceSHA: pin.SourceSHA, RootAcceptanceSHA: pin.AcceptanceSHA, SourceAcceptanceSHA: pin.SourceAcceptanceSHA, OwnerJob: j.ID, OwnerTask: j.TaskID, Role: role, StartedAt: now, EndedAt: now.Add(time.Second), ExitCode: &exit, State: "exited", ReceiptSHA: autoSHA([]byte(fmt.Sprint(task.ID))), SourceTreeSHA: autoSHA([]byte("actual mounted source")), ScriptSHA: autoSHA([]byte(experiment)), FixturesSHA: autoSHA([]byte("case bytes")), ArgvSHA: autoSHA([]byte("fixed args")), RuntimeSHA: autoSHA([]byte("verified runtime")), PolicySHA: autoSHA([]byte("fixed launch policy")), OutputSHA: autoSHA([]byte(fmt.Sprint(index, " actual output")))}
		if e = autoRecordExpertProbe(a, r); e != nil {
			t.Fatal(e)
		}
		j.Status = "done"
		a.State.Assignments[index].Completed = true
		yes := true
		a.State.Audits[role] = autonomy.Verdict{Approve: &yes, Reason: "independently reproduced", ExpertRecovery: []autonomy.ExpertRecoveryAudit{{SourceTaskID: p.ExpertRecoveryTaskID, ProgressKey: pin.Key, ProbeIDs: []string{lease.ID}, FailureFamily: pin.AcceptanceSHA, PriorAttempt: len(a.ExpertRecovery.Attempts[pin.RootTaskID]), MaterialChange: true, CausalExplanation: "counterexample isolates EOF finalization", DifferentStrategy: "apply retention after final decoder flush", StopCriterion: "original invariants all pass or retain rejection"}}}
	}
	a.State.Phase = autonomy.Build
}
func expertReserve(t *testing.T, a *autoRecord, p autonomy.Proposal, now time.Time) *autoExpertRecoveryAttempt {
	t.Helper()
	v, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestExpertRecoveryPinsAcceptanceReservesOnceAndPreservesOldOutcome(t *testing.T) {
	s, a, p, now := expertFixture(t)
	before := autoFindJob(a, p.ExpertRecoveryTaskID)
	raw := store.J(before)
	root := autoExpertRoot(a, p.ExpertRecoveryTaskID)
	count := autoRepairAttempts(a, root)
	expertAudits(t, s, a, p, now, "causal experiment A")
	v := expertReserve(t, a, p, now)
	again := expertReserve(t, a, p, now)
	if v != again || len(a.ExpertRecovery.Attempts[root]) != 1 {
		t.Fatal("duplicate reservation")
	}
	if _, e := autoReserveExpertRecovery(a, p, "88888888-8888-4888-8888-888888888888", []string{"codex"}, now); e == nil {
		t.Fatal("different owner accepted")
	}
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	loaded, e := s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	if expertReserve(t, loaded, p, now).LeaseKey != v.LeaseKey {
		t.Fatal("reload lost lease")
	}
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	if pin.AcceptanceSHA == pin.SourceAcceptanceSHA {
		t.Fatal("root acceptance narrowed")
	}
	if store.J(before) != raw || autoRepairAttempts(a, root) != count || autoCheckpointApproved(a, before.TaskID) {
		t.Fatal("old approval/cap changed")
	}
}
func TestExpertRecoveryUnchangedCannotRenewByTimeTitleArchiveOrOutput(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "same causal script")
	first := expertReserve(t, a, p, now)
	if e := autoFinishExpertRecovery(first, "rejected", 123, "counterexample still fails", now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	now = now.Add(25 * time.Hour)
	expertQuota(a, now)
	a.State.Cycle++
	p.Title = "brand new title"
	a.State.Items = []autonomy.Proposal{p}
	expertAudits(t, s, a, p, now, "same causal script")
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil || !strings.Contains(e.Error(), "unchanged") {
		t.Fatalf("unchanged conditions accepted: %v", e)
	}
	for _, v := range a.ExpertRecovery.Probes {
		if v.Cycle == a.State.Cycle {
			v.Receipt.OutputSHA = autoSHA([]byte("new logging"))
			v.Receipt.SourceTreeSHA = autoSHA([]byte("cosmetic unrelated source"))
		}
	}
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil {
		t.Fatal("log/source changes renewed eligibility")
	}
}
func TestExpertRecoveryRealNewEvidenceCanReopenWithoutCounterReset(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "first experiment")
	v := expertReserve(t, a, p, now)
	if e := autoFinishExpertRecovery(v, "rejected", 123, "wrong causal hypothesis", now); e != nil {
		t.Fatal(e)
	}
	now = now.Add(2 * time.Hour)
	expertQuota(a, now)
	a.State.Cycle++
	expertAudits(t, s, a, p, now, "independent discriminating experiment")
	second := expertReserve(t, a, p, now)
	if second.Number != 2 || v.Status != "rejected" {
		t.Fatal("history reset")
	}
	if e := autoFinishExpertRecovery(second, "rejected", 124, "new regression", now); e != nil {
		t.Fatal(e)
	}
	now = now.Add(2 * time.Hour)
	expertQuota(a, now)
	a.State.Cycle++
	expertAudits(t, s, a, p, now, "third evidence")
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil || !strings.Contains(e.Error(), "rate limit") {
		t.Fatalf("rate gate bypass: %v", e)
	}
	now = now.Add(25 * time.Hour)
	expertQuota(a, now)
	a.State.Cycle++
	expertAudits(t, s, a, p, now, "later genuinely different evidence")
	third := expertReserve(t, a, p, now)
	if third.Number != 3 || len(a.ExpertRecovery.Attempts[v.RootTaskID]) != 3 {
		t.Fatal("permanent lifetime cap or reset")
	}
}
func TestExpertRecoveryAuditIdentityAndSemanticGate(t *testing.T) {
	for _, change := range []string{"missing", "shared", "stale", "not_material", "disagree", "acceptance", "quota"} {
		t.Run(change, func(t *testing.T) {
			s, a, p, now := expertFixture(t)
			expertAudits(t, s, a, p, now, "probe")
			v := a.State.Audits["auditor_b"]
			switch change {
			case "missing":
				v.ExpertRecovery = nil
			case "shared":
				v.ExpertRecovery[0].ProbeIDs = a.State.Audits["auditor_a"].ExpertRecovery[0].ProbeIDs
			case "stale":
				v.ExpertRecovery[0].PriorAttempt = 1
			case "not_material":
				v.ExpertRecovery[0].MaterialChange = false
			case "disagree":
				v.ExpertRecovery[0].FailureFamily = "different defect"
			case "acceptance":
				autoFindJob(a, p.ExpertRecoveryTaskID).Admission.Proposal.Acceptance = []string{"relaxed"}
			case "quota":
				a.Quota.Providers = nil
			}
			a.State.Audits["auditor_b"] = v
			if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil {
				t.Fatal("invalid gate accepted")
			}
		})
	}
}
func TestExpertRecoveryProbeBudgetSurvivesNewProposalOwners(t *testing.T) {
	s, a, p, now := expertFixture(t)
	for i := 0; i < 6; i++ {
		a.State.Cycle++
		expertAudits(t, s, a, p, now, fmt.Sprint("experiment", i))
	}
	a.State.Phase = autonomy.Audit
	j, _ := autoExpertAuditOwner(a, "auditor_a")
	j.Status = "running"
	if _, e := autoReserveExpertProbe(a, p, "auditor_a", autoSHA([]byte("another")), []string{"codex"}, now); e == nil {
		t.Fatal("root probe budget laundered by new owners")
	}
	var restored autoRecord
	raw, _ := json.Marshal(a)
	if e := json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if _, e := autoReserveExpertProbe(&restored, p, "auditor_a", autoSHA([]byte("another")), []string{"codex"}, now); e == nil {
		t.Fatal("restart reset probe spending")
	}
}
func TestExpertRecoveryUsageAndOutcomeImmutable(t *testing.T) {
	v := &autoExpertRecoveryAttempt{CreatedAt: time.Now(), Status: "active"}
	one := "11111111-1111-4111-8111-111111111111"
	two := "22222222-2222-4222-8222-222222222222"
	if e := autoChargeExpertRecovery(v, one, 1000000); e != nil {
		t.Fatal(e)
	}
	if e := autoChargeExpertRecovery(v, one, 1000000); e != nil || v.ChargedMilliseconds != 1000000 {
		t.Fatal("usage replay doubled")
	}
	if e := autoChargeExpertRecovery(v, one, 1); e == nil {
		t.Fatal("usage reduced")
	}
	if e := autoChargeExpertRecovery(v, two, 800000); e == nil || v.ChargedMilliseconds != 1800000 {
		t.Fatal("clone replenished budget")
	}
	now := v.CreatedAt.Add(time.Hour)
	if e := autoFinishExpertRecovery(v, "budget_exhausted", 0, "bounded runtime spent", now); e != nil {
		t.Fatal(e)
	}
	if e := autoFinishExpertRecovery(v, "approved", 2, "rewrite", now); e == nil {
		t.Fatal("terminal result rewritten")
	}
}

func TestExpertRecoveryPinnedArchivesAndReceiptBindingsFailClosed(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "probe")
	if e := s.validateAutoExpertSources(context.Background(), a, []autonomy.Proposal{p}); e != nil {
		t.Fatal(e)
	}
	f := documentationRunner(t)
	docResponse(t, f, map[string]string{"state": "ready", "sha256": strings.Repeat("b", 64)})
	if e := s.validateAutoExpertSources(context.Background(), a, []autonomy.Proposal{p}); e == nil {
		t.Fatal("changed archive allowed")
	}
	for _, lease := range a.ExpertRecovery.Probes {
		r := *lease.Receipt
		r.RequestKey = autoSHA([]byte("other sealed request"))
		if e := autoRecordExpertProbe(a, r); e == nil {
			t.Fatal("wrong request binding")
		}
		r = *lease.Receipt
		r.Profile = "extended600"
		if e := autoRecordExpertProbe(a, r); e == nil {
			t.Fatal("unaudited extended probe profile")
		}
		r = *lease.Receipt
		r.Truncated = true
		if e := autoRecordExpertProbe(a, r); e == nil {
			t.Fatal("truncated execution treated as progress")
		}
		r = *lease.Receipt
		r.OutputSHA = autoSHA([]byte("edited output"))
		if e := autoRecordExpertProbe(a, r); e == nil {
			t.Fatal("immutable receipt overwritten")
		}
	}
}

func TestExpertRecoveryTerminalProbeDiagnosticsPersistWithoutProgress(t *testing.T) {
	for _, state := range []string{"failed", "timeout", "output_limit", "cancelled", "interrupted", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			s, a, p, now := expertFixture(t)
			expertAudits(t, s, a, p, now, "case")
			var lease *autoExpertProbeLease
			for _, v := range a.ExpertRecovery.Probes {
				lease = v
				break
			}
			r := *lease.Receipt
			lease.Receipt = nil
			r.State = state
			r.ExitCode = nil
			r.Reason = "actual bounded runner failure"
			r.StartedAt = time.Time{}
			r.EndedAt = time.Time{}
			r.Executed = nil
			if e := autoRecordExpertProbe(a, r); e != nil {
				t.Fatal(e)
			}
			if lease.Receipt != nil || lease.Diagnostic == nil || lease.ChargedMilliseconds != autoExpertProbeBudget.Milliseconds() {
				t.Fatal("diagnostic became proof or unknown cost vanished")
			}
			raw, _ := json.Marshal(a)
			var restored autoRecord
			if e := json.Unmarshal(raw, &restored); e != nil {
				t.Fatal(e)
			}
			if e := autoRecordExpertProbe(&restored, r); e != nil {
				t.Fatal(e)
			}
			exit := 0
			r.ExitCode = &exit
			r.State = "exited"
			r.StartedAt = now
			r.EndedAt = now.Add(time.Second)
			if e := autoRecordExpertProbe(&restored, r); e == nil {
				t.Fatal("same failed execution retried as success")
			}
			if _, e := autoReserveExpertRecovery(&restored, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil {
				t.Fatal("failed diagnostic admitted implementation")
			}
		})
	}
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "case")
	for _, lease := range a.ExpertRecovery.Probes {
		r := *lease.Receipt
		lease.Receipt = nil
		r.State = "failed"
		r.Reason = "source preparation never executed"
		r.SourceTreeSHA = ""
		r.OutputSHA = ""
		r.ExitCode = nil
		if e := autoRecordExpertProbe(a, r); e == nil {
			t.Fatal("missing evidence accepted without nonexecution attestation")
		}
		no := false
		r.Executed = &no
		if e := autoRecordExpertProbe(a, r); e != nil {
			t.Fatal(e)
		}
		if lease.ChargedMilliseconds != 0 {
			t.Fatal("explicit nonexecution charged runtime")
		}
		break
	}
}
func TestExpertRecoveryAuditorAttestationValidatedBeforeAdvancement(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "case")
	a.State.Phase = autonomy.Audit
	v := a.State.Audits["auditor_a"]
	raw, _ := json.Marshal(v)
	if e := validateAutoExpertAuditReport(a, "auditor_a", raw); e != nil {
		t.Fatal(e)
	}
	v.ExpertRecovery = nil
	raw, _ = json.Marshal(v)
	if e := validateAutoExpertAuditReport(a, "auditor_a", raw); e == nil {
		t.Fatal("approving auditor missing progress accepted")
	}
	no := false
	v.Approve = &no
	raw, _ = json.Marshal(v)
	if e := validateAutoExpertAuditReport(a, "auditor_a", raw); e != nil {
		t.Fatal("honest rejection requires positive evidence", e)
	}
	if a.State.Phase != autonomy.Audit {
		t.Fatal("validation advanced state")
	}
}
func TestExpertRecoveryReviewerRetryBudgetSeparateAndBound(t *testing.T) {
	v := &autoExpertRecoveryAttempt{Status: "reviewing"}
	one := "11111111-1111-4111-8111-111111111111"
	two := "22222222-2222-4222-8222-222222222222"
	if e := autoChargeExpertReview(v, one, 1000000); e != nil {
		t.Fatal(e)
	}
	if e := autoChargeExpertReview(v, one, 1000000); e != nil || v.ReviewChargedMilliseconds != 1000000 {
		t.Fatal("review replay doubled")
	}
	if autoExpertRemaining(v) != autoExpertBuildBudget {
		t.Fatal("review charges changed build budget")
	}
	if e := autoChargeExpertReview(v, two, 800000); e == nil || autoExpertReviewRemaining(v) != 0 {
		t.Fatal("new review UUID reset budget")
	}
}
func TestExpertRecoveryCooldownOffAndMissingQuotaDoNotReserve(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "case")
	a.Config.Enabled = false
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil {
		t.Fatal("OFF accepted")
	}
	a.Config.Enabled = true
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", nil, now); e == nil {
		t.Fatal("missing quota providers accepted")
	}
	v := expertReserve(t, a, p, now)
	autoFinishExpertRecovery(v, "rejected", 123, "failed", now)
	a.State.Cycle++
	expertAudits(t, s, a, p, now, "new case")
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil || !strings.Contains(e.Error(), "cooldown") {
		t.Fatal("cooldown bypass", e)
	}
}

func TestExpertRecoveryCancelledProbeCannotReserveAgain(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "case")
	a.State.Phase = autonomy.Audit
	j, e := autoExpertAuditOwner(a, "auditor_a")
	if e != nil {
		t.Fatal(e)
	}
	j.Status = "running"
	var lease *autoExpertProbeLease
	for _, v := range a.ExpertRecovery.Probes {
		if v.Role == "auditor_a" {
			lease = v
			break
		}
	}
	lease.StopRequestedAt = now
	if _, e := autoReserveExpertProbe(a, p, "auditor_a", lease.RequestKey, []string{"codex"}, now); e == nil {
		t.Fatal("cancelled execution relaunched")
	}
}

func TestExpertRecoveryStoppedRetainedOwnerStillBlocksCompetingLease(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "case")
	j := &autoJob{ID: "88888888-8888-4888-8888-888888888888", TaskID: 888, Role: "builder", Status: "stopped", RepairSourceTaskID: p.ExpertRecoveryTaskID}
	a.Jobs = append(a.Jobs, j)
	held, _ := autonomy.NewState("2026-09-26")
	held.Phase = autonomy.Build
	held.Assignments = []autonomy.Assignment{{TaskID: j.TaskID, Role: "builder"}}
	a.HeldRuns = append(a.HeldRuns, held)
	if _, e := autoReserveExpertRecovery(a, p, "99999999-9999-4999-8999-999999999999", []string{"codex"}, now); e == nil || !strings.Contains(e.Error(), "retained assignment") {
		t.Fatal("stopped owner lost reservation", e)
	}
}

func TestExpertRecoveryAuditReportCorrectionKeepsSameTaskProbe(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "case")
	old, e := autoExpertAuditOwner(a, "auditor_a")
	if e != nil {
		t.Fatal(e)
	}
	old.Status = "stopped"
	clone := *old
	clone.ID = "77777777-7777-4777-8777-777777777777"
	clone.Status = "done"
	a.Jobs = append(a.Jobs, &clone)
	if _, e := autoExpertAuditOwner(a, "auditor_a"); e != nil {
		t.Fatal(e)
	}
	v := expertReserve(t, a, p, now)
	found := false
	for _, r := range v.ProbeReceipts {
		if r.Role == "auditor_a" {
			found = true
			if r.OwnerJob != old.ID {
				t.Fatal("receipt rewritten to latest UUID")
			}
		}
	}
	if !found {
		t.Fatal("lost earlier probe")
	}
	// Same task number is insufficient when the earlier owner was a peer or is
	// still running: the correction path must never borrow another execution.
	lease := a.ExpertRecovery.Probes[a.State.Audits["auditor_a"].ExpertRecovery[0].ProbeIDs[0]]
	old.Role = "auditor_b"
	if autoExpertProbeOwnerValid(a, lease, &clone) {
		t.Fatal("peer probe crossover")
	}
	old.Role = "auditor_a"
	old.Status = "running"
	if autoExpertProbeOwnerValid(a, lease, &clone) {
		t.Fatal("live earlier owner allowed")
	}
}

func TestExpertRecoveryLegacyAuditorNeedsNoNewOwnerRecord(t *testing.T) {
	a := &autoRecord{State: &autonomy.State{Phase: autonomy.Audit, Items: []autonomy.Proposal{{ProjectID: 1, Title: "ordinary"}}}}
	if e := validateAutoExpertAuditReport(a, "auditor_a", []byte(`{"approve":true,"reason":"legacy verdict"}`)); e != nil {
		t.Fatal("expert path constrained unrelated legacy audit", e)
	}
	if e := validateAutoExpertAuditReport(a, "auditor_a", []byte(`{"approve":true,"reason":"bad extra","expert_recovery":[{}]}`)); e == nil {
		t.Fatal("unsolicited expert attestation ignored")
	}
}

func TestExpertRecoveryApprovedScopeDoesNotPermanentlyBlockNewRejectedDescendant(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "first case")
	first := expertReserve(t, a, p, now)
	built, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "approved expert", Status: "done"})
	if e != nil {
		t.Fatal(e)
	}
	review, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "approved review", Status: "done"})
	if e != nil {
		t.Fatal(e)
	}
	first.TaskID = built.ID
	a.Jobs = append(a.Jobs, &autoJob{ID: first.JobID, TaskID: built.ID, Role: "builder", Status: "done", Approved: true, ReviewTaskID: review.ID, ExpertRecoveryRoot: first.RootTaskID, ExpertRecoveryAttempt: first.Number, Admission: &autoAdmission{Proposal: p}}, &autoJob{ID: "66666666-6666-4666-8666-666666666666", TaskID: review.ID, Role: "reviewer", Status: "done"})
	if e = autoFinishExpertRecovery(first, "approved", review.ID, "original scope independently complete", now); e != nil {
		t.Fatal(e)
	}
	now = now.Add(2 * time.Hour)
	expertQuota(a, now)
	a.State.Cycle++
	if _, e = autoExpertRecoveryEligible(a, p, now); e == nil {
		t.Fatal("satisfied old source reopened")
	}
	child, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "new milestone", Status: "done"})
	if e != nil {
		t.Fatal(e)
	}
	reject, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "new independent rejection", Status: "done"})
	if e != nil {
		t.Fatal(e)
	}
	next := &autoJob{ID: "55555555-7777-4777-8777-777777777777", TaskID: child.ID, Role: "builder", Status: "done", Rejected: true, ReviewTaskID: reject.ID, ReviewReason: "new milestone boundary fails", ExpertRecoveryRoot: first.RootTaskID, Admission: &autoAdmission{Proposal: autonomy.Proposal{ProjectID: p.ProjectID, ContinueTaskID: built.ID, Acceptance: []string{"new independent milestone acceptance"}}}}
	a.Jobs = append(a.Jobs, next, &autoJob{ID: "44444444-7777-4777-8777-777777777777", TaskID: reject.ID, Role: "reviewer", Status: "done"})
	pin, e := s.pinAutoExpertRecovery(context.Background(), a, p.ProjectID, child.ID)
	if e != nil {
		t.Fatal(e)
	}
	secondProposal := p
	secondProposal.ExpertRecoveryTaskID = child.ID
	secondProposal.ExpertProgressKey = pin.Key
	secondProposal.Title = "recover later rejected milestone"
	a.State.Items = []autonomy.Proposal{secondProposal}
	expertAudits(t, s, a, secondProposal, now, "independent new milestone experiment")
	second := expertReserve(t, a, secondProposal, now)
	if second.Number != 2 || second.RootTaskID != first.RootTaskID || first.Status != "approved" {
		t.Fatal("success history reset or descendant stranded")
	}
	saved := next.Admission.Proposal
	next.Admission.Proposal.ContinueTaskID = 0
	if autoExpertLaterRejectedMilestone(a, pin, first) {
		t.Fatal("unrelated sibling accepted")
	}
	next.Admission.Proposal = saved
	altered := *pin
	altered.SourceAcceptanceSHA = autoSHA([]byte(store.J(first.Proposal.Acceptance)))
	if autoExpertLaterRejectedMilestone(a, &altered, first) {
		t.Fatal("identical acceptance accepted as new milestone")
	}
}
