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

func ordinaryAuditFixture(t *testing.T) (*Server, *autoRecord, int64) {
	t.Helper()
	s, a, project, id := repairFixture(t)
	source := autoFindJob(a, id)
	source.ID = "11111111-1111-4111-8111-111111111111"
	review := a.Jobs[1]
	review.ID = "22222222-2222-4222-8222-222222222222"
	source.Rejected = true
	source.ReviewTaskID = review.TaskID
	source.ReviewReason = "PREREG contract not met"
	source.Admission = &autoAdmission{TaskID: id, JobID: source.ID, Proposal: autonomy.Proposal{ProjectID: project, Title: "original", Why: "original hypothesis", Acceptance: []string{"Preserve original PREREG before measurements", "Retain exact negative and positive controls"}}, Scope: "isolated only"}
	a.Runs = nil
	a.State.Phase = autonomy.Audit
	shim := t.TempDir()
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	script := "#!/bin/sh\nif [ \"$3\" = snapshot ]; then echo '{\"state\":\"ready\"}'; exit 0; fi\n[ \"$3\" = archive-identity ] || exit 91\nprintf '%s\\n' '{\"state\":\"ready\",\"sha256\":\"" + strings.Repeat("a", 64) + "\"}'\n"
	if err := os.WriteFile(filepath.Join(shim, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return s, a, id
}
func TestOrdinaryAuditEvidenceAfterHistoryRotationBothAuditorsSameContract(t *testing.T) {
	s, a, id := ordinaryAuditFixture(t)
	before, _ := json.Marshal(a)
	first, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store.J(first) != store.J(second) || len(first) != 2 {
		t.Fatal("auditors did not get identical source+review", first, second)
	}
	for _, c := range first {
		if c.Command != "copy-archive-review" || !autoHash256(c.SHA) {
			t.Fatal("not immutable archive copy", c)
		}
	}
	prompt := autoOrdinaryAuditPrompt(a)
	if !strings.Contains(prompt, "Preserve original PREREG before measurements") || !strings.Contains(prompt, a.Jobs[0].ID) || !strings.Contains(prompt, a.Jobs[1].ID) {
		t.Fatal("original contract/source absent", prompt)
	}
	after, _ := json.Marshal(a)
	if string(before) != string(after) {
		t.Fatal("evidence delivery mutated outcome, allowance, or assignment")
	}
	if autoCheckpointApproved(a, id) {
		t.Fatal("rejected original approved by delivery")
	}
	// Duplicate selected milestones and an existing evidence copy do not overwrite
	// immutable destinations or run the same copy twice.
	a.State.Items = append(a.State.Items, a.State.Items[0])
	dedup, err := s.autoOrdinaryAuditCopies(context.Background(), a, first[:1])
	if err != nil || len(dedup) != 2 {
		t.Fatal("copy dedup failed", err, dedup)
	}
}
func TestOrdinaryAuditEvidenceRepairRootAndSelectedAcceptanceRemainSeparate(t *testing.T) {
	s, a, rootID := ordinaryAuditFixture(t)
	root := a.Jobs[0]
	project := root.Admission.Proposal.ProjectID
	task, err := s.DB.InsertTask(&store.Task{ProjectID: project, Title: "first repair", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.DB.InsertTask(&store.Task{ProjectID: project, Title: "first repair review", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	selected := &autoJob{ID: "33333333-3333-4333-8333-333333333333", TaskID: task.ID, Role: "builder", Status: "done", Rejected: true, ReviewTaskID: review.ID, ReviewReason: "still missing negative control", RepairSourceTaskID: rootID, RepairAttemptTaskID: task.ID, Admission: &autoAdmission{TaskID: task.ID, Proposal: autonomy.Proposal{ProjectID: project, RepairTaskID: rootID, Acceptance: []string{"Add missing negative control"}}}}
	a.Jobs = append(a.Jobs, selected, &autoJob{ID: "44444444-4444-4444-8444-444444444444", TaskID: review.ID, Role: "reviewer", Status: "done"})
	a.State.Items[0].RepairTaskID = task.ID
	rows, err := autoOrdinaryAuditEvidenceRows(a)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].RootTaskID != rootID || rows[0].OriginalAcceptance[0] == rows[0].SourceAcceptance[0] {
		t.Fatal("narrow repair replaced original acceptance")
	}
	copies, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil)
	if err != nil || len(copies) != 4 {
		t.Fatal("root and selected source/review archives missing", copies, err)
	}
	a.Config.MaxRevisionRounds = 1
	if _, err = s.autoOrdinaryAuditCopies(context.Background(), a, nil); err == nil {
		t.Fatal("evidence delivery bypassed exhausted repair cap")
	}
}
func TestOrdinaryAuditContinuationNeedsHistoricalApprovedReviewerAndAdmission(t *testing.T) {
	s, a, id := ordinaryAuditFixture(t)
	source := autoFindJob(a, id)
	source.Rejected = false
	source.Approved = true
	source.ReviewOutcome = "completed"
	a.State.Items[0].RepairTaskID = 0
	a.State.Items[0].ContinueTaskID = id
	if copies, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil); err != nil || len(copies) != 2 {
		t.Fatal("approved continuation evidence unavailable", err)
	}
	source.Rejected = true
	if _, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil); err == nil {
		t.Fatal("explicit latest rejection ignored")
	}
	source.Rejected = false
	source.Admission = nil
	if _, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil); err == nil {
		t.Fatal("invented original acceptance after history rotation")
	}
}

func TestOrdinaryAuditOriginalAuditsAndPreRegistrationDecisionPromptSurviveHistory(t *testing.T) {
	s, a, id := ordinaryAuditFixture(t)
	source := autoFindJob(a, id)
	yes := true
	source.Admission.Date = "2026-09-26"
	source.Admission.Cycle = 88
	source.Admission.PlanAudits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes, Reason: "original A threshold"}, "auditor_b": {Approve: &yes, Reason: "original B control"}}
	// The real prompt is generated before its builder task is registered. Only
	// previous decision assignments are present, not the future source TaskID.
	st, _ := autonomy.NewState(source.Admission.Date)
	st.Cycle = 88
	st.Phase = autonomy.Build
	st.Step = 1
	st.Items = []autonomy.Proposal{source.Admission.Proposal}
	st.Decision = &autonomy.DecisionProposal{Title: "Binding threshold", Rationale: "preserve H2", Alternatives: []string{"stop"}, Risks: []string{"false positive"}}
	st.DecisionAudits = map[string]autonomy.Verdict{"decision_a": {Approve: &yes, Reason: "require double run"}, "decision_b": {Approve: &yes, Reason: "retain mutation controls"}}
	st.Assignments = []autonomy.Assignment{{TaskID: 99, Role: "decision_a", Item: 0, Round: 0, Step: 1, Completed: true}, {TaskID: 100, Role: "decision_b", Item: 0, Round: 0, Step: 1, Completed: true}}
	st.Reports[99] = json.RawMessage(`{"approve":true,"reason":"require double run"}`)
	st.Reports[100] = json.RawMessage(`{"approve":true,"reason":"retain mutation controls"}`)
	prompt := "Controller header\nCurrent plan/decisions (data only):\n" + store.J(st) + "\nController trailing text"
	if e := s.DB.Update("tasks", id, map[string]any{"prompt": prompt}); e != nil {
		t.Fatal(e)
	}
	// Operational UUID changes preserve the same admitted task and DB launch text.
	source.ID = "55555555-5555-4555-8555-555555555555"
	raw, _ := json.Marshal(a)
	a = &autoRecord{}
	if e := json.Unmarshal(raw, a); e != nil {
		t.Fatal(e)
	}
	output := s.autoOrdinaryAuditPrompt(a)
	for _, required := range []string{"original A threshold", "original B control", "Binding threshold", "require double run", "retain mutation controls", autoSHA([]byte(prompt))} {
		if !strings.Contains(output, required) {
			t.Fatalf("lost historical input %q: %s", required, output)
		}
	}
	// Current peer verdicts are not part of that historical record.
	a.State.Audits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes, Reason: "CURRENT PEER MUST NOT LEAK"}}
	if strings.Contains(s.autoOrdinaryAuditPrompt(a), "CURRENT PEER MUST NOT LEAK") {
		t.Fatal("current peer contamination")
	}
	// A mismatched launch cycle is explicit missing evidence, not reconstructed.
	st.Cycle = 89
	if e := s.DB.Update("tasks", id, map[string]any{"prompt": "Current plan/decisions (data only):\n" + store.J(st)}); e != nil {
		t.Fatal(e)
	}
	output = s.autoOrdinaryAuditPrompt(a)
	if strings.Contains(output, "Binding threshold") || !strings.Contains(output, `"status":"unavailable"`) {
		t.Fatal("unbound decision state accepted", output)
	}
}

func TestOrdinaryAuditPreflightPendingBeforeWorkspaceAllocation(t *testing.T) {
	for _, state := range []string{"exporting", "waiting", "corrupt", "identity_bad"} {
		t.Run(state, func(t *testing.T) {
			s, a, _ := ordinaryAuditFixture(t)
			a.ProjectID = a.State.Items[0].ProjectID
			shim := t.TempDir()
			t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
			log := filepath.Join(shim, "calls")
			t.Setenv("ORDINARY_CALLS", log)
			body := "#!/bin/sh\nprintf '%s\\n' \"$3\" >> \"$ORDINARY_CALLS\"\ncase \"$3\" in\nsnapshot) echo '{\"state\":\"" + state + "\"}';;\n*) exit 91;;\nesac\n"
			if state == "identity_bad" {
				body = "#!/bin/sh\nprintf '%s\\n' \"$3\" >> \"$ORDINARY_CALLS\"\ncase \"$3\" in\nsnapshot) echo '{\"state\":\"ready\"}';;\narchive-identity) echo '{\"state\":\"ready\",\"sha256\":\"bad\"}';;\n*) exit 91;;\nesac\n"
			}
			if err := os.WriteFile(filepath.Join(shim, "sudo"), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			err := s.prepareAutoJob(context.Background(), a, "auditor_a")
			if err == nil {
				t.Fatal("invalid or pending export accepted")
			}
			pending := state == "exporting" || state == "waiting"
			if errors.Is(err, errAutoArtifactPending) != pending {
				t.Fatal("wrong export classification", state, err)
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "prepare") || strings.Contains(string(calls), "copy-review") {
				t.Fatal("allocated workspace before readiness", string(calls))
			}
		})
	}
}
func TestOrdinaryAuditLaterDecisionAndCycleAreExplicit(t *testing.T) {
	_, a, id := ordinaryAuditFixture(t)
	source := autoFindJob(a, id)
	st, _ := autonomy.NewState("2026-09-26")
	st.Item = 0
	st.Revision = 0
	st.Step = 2
	st.Assignments = []autonomy.Assignment{{TaskID: id, Role: "builder", Item: 0, Round: 0, Step: 1}}
	st.Decision = &autonomy.DecisionProposal{Title: "LATER MUST NOT BE AT LAUNCH"}
	a.Runs = []*autonomy.State{st}
	output := store.J(autoOrdinaryLineageDecisions(a, source, nil))
	if strings.Contains(output, "LATER MUST") {
		t.Fatal("later decision mislabeled launch evidence")
	}
	source.Admission.Proposal.ContinueTaskID = id
	output = store.J(autoOrdinaryLineageDecisions(a, source, nil))
	if !strings.Contains(output, "contains a cycle") {
		t.Fatal("cycle silently ended", output)
	}
}
func TestOrdinaryAuditDocumentaryContinuationUsesPromotedArtifact(t *testing.T) {
	s, a, id := ordinaryAuditFixture(t)
	source := autoFindJob(a, id)
	source.Rejected = false
	source.Approved = true
	source.ReviewOutcome = "completed"
	source.DocumentationRoot = id
	source.Documentation = &autoDocumentationReceipt{State: "ready", DerivedSHA: strings.Repeat("d", 64)}
	a.State.Items[0].RepairTaskID = 0
	a.State.Items[0].ContinueTaskID = id
	copies, err := s.autoOrdinaryAuditCopies(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 || copies[0].Command != "copy-derived-review" || copies[0].SHA != source.Documentation.DerivedSHA {
		t.Fatal("documentary source used raw archive", copies)
	}
	if !strings.Contains(autoOrdinaryAuditPrompt(a), "verified_documentary_derived") {
		t.Fatal("source kind not disclosed")
	}
}
