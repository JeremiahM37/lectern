package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"strings"
	"testing"
	"time"
)

func TestPrivateToolingSelectionAndStdlibConsumer(t *testing.T) {
	for _, key := range []string{autoSHA([]byte("exact pytest tooling")), ""} {
		a, v, j, now := privateFixture(t)
		ref := autoSHA([]byte("test"))
		r := privateTestReceipt(t, a, v, j, now, ref)
		r.PythonTestKey = key
		if e := autoRecordPrivateTest(a, r); e != nil {
			t.Fatal(e)
		}
		v.Review = &autoPrivateReview{TestReceiptSHAs: []string{ref}, Approved: true}
		v.Publication = &autoPrivatePublication{ID: v.ID}
		consumer := &autoJob{TaskID: 99, Role: "builder"}
		a.State = &autonomy.State{Items: []autonomy.Proposal{{ProjectID: 1, SourceIntegrationID: v.ID}}}
		if e := autoInheritPrivateTooling(a, consumer); e != nil {
			t.Fatal("usable tested source rejected", e)
		}
		if consumer.PythonExpectedTestKey != key {
			t.Fatal("substituted test tooling")
		}
		raw, _ := json.Marshal(consumer)
		var restored autoJob
		_ = json.Unmarshal(raw, &restored)
		if restored.PythonExpectedTestKey != key {
			t.Fatal("restart lost selection")
		}
	}
}
func TestPrivateMissingToolingAvailabilityDoesNotResetAssignment(t *testing.T) {
	a, _, j, _ := privateFixture(t)
	key := autoSHA([]byte("retained tooling"))
	j.PythonExpectedTestKey = key
	j.Recovery = &autoRecoveryReceipt{State: "not_applicable"}
	unavailable := []byte(store.J(autoPrivateToolingReceipt{Capability: "python_test_runtime", State: "unavailable", Key: key, Diagnostic: "missing", Reason: "bundle absent", Scope: "metadata only"}))
	if ready, e := autoApplyPrivateTooling(a, j, unavailable); e != nil || ready {
		t.Fatal(ready, e)
	}
	if autoDeferredReady(j) {
		t.Fatal("missing key resumed")
	}
	original := j.TaskID
	verified := []byte(store.J(autoPrivateToolingReceipt{Capability: "python_test_runtime", State: "verified", Key: key, Scope: "metadata preflight; full content verified before model execution"}))
	if ready, e := autoApplyPrivateTooling(a, j, verified); e != nil || !ready {
		t.Fatal(ready, e)
	}
	if !autoDeferredReady(j) || j.TaskID != original {
		t.Fatal("lost retained assignment")
	}
	wrong := strings.Replace(string(verified), key, autoSHA([]byte("new active tooling")), 1)
	if _, e := autoApplyPrivateTooling(a, j, []byte(wrong)); e == nil {
		t.Fatal("substituted active tooling key")
	}
}
func TestPrivateFullToolingFailurePreservesSourceAndCanUseAuditedRemedy(t *testing.T) {
	for _, diagnostic := range []string{"missing", "integrity", "incompatible"} {
		t.Run(diagnostic, func(t *testing.T) {
			s, a, p := documentationFixture(t)
			audits := a.State.Audits
			response := documentationRunner(t)
			docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
			task, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "private consumer", Status: "running"})
			if e != nil {
				t.Fatal(e)
			}
			j := &autoJob{ID: autoUUID(), TaskID: task.ID, Role: "builder", Status: "running", PythonExpectedTestKey: autoSHA([]byte("selected")), PrivateIntegrationRoot: autoSHA([]byte("source-root")), Admission: &autoAdmission{Proposal: autonomy.Proposal{ProjectID: p.ProjectID}}, Recovery: &autoRecoveryReceipt{State: "not_applicable"}}
			a.Jobs = append(a.Jobs, j)
			a.State = &autonomy.State{Date: "2026-09-26", Phase: autonomy.Build, Items: []autonomy.Proposal{{ProjectID: p.ProjectID}}, Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: "builder"}}}
			no := false
			r := autoPrivateToolingReceipt{Capability: "python_test_runtime", State: "unavailable", Key: j.PythonExpectedTestKey, Diagnostic: diagnostic, Executed: &no, Reason: "bound failure", Scope: "full content validation before model execution"}
			handled, e := s.handleAutoPrivateToolingFailure(context.Background(), a, j, []byte(store.J(map[string]any{"state": "failed", "python_test_runtime": r})))
			if e != nil || !handled {
				t.Fatal(handled, e)
			}
			if j.Status != "deferred" || !j.PythonTestNeedsResume || j.RequirementHold != (diagnostic != "missing") {
				t.Fatal("incorrect recovery disposition", j.Status, j.RequirementHold)
			}
			entry := a.Requirements[autoSHA([]byte("python-test-runtime:"+r.Key))]
			if entry == nil || entry.ToolingReceipt == nil || entry.Occurrences[len(entry.Occurrences)-1].ArchiveSHA != strings.Repeat("a", 64) {
				t.Fatal("lost exact archived failure evidence")
			}
			if diagnostic != "missing" {
				// Metadata cannot release an integrity hold. Only an independently
				// reviewed alternative request may replace the failed environment.
				j.PythonTestRecovery = &autoPrivateToolingReceipt{State: "verified", Key: j.PythonExpectedTestKey}
				if autoDeferredReady(j) {
					t.Fatal("metadata verification released integrity hold")
				}
				proposal := autonomy.Proposal{ProjectID: p.ProjectID, DiagnoseRequirement: entry.Key, Title: "diagnose retained tooling failure", Acceptance: []string{"verify supported replacement"}}
				a.State.Items = []autonomy.Proposal{proposal}
				a.State.Item = 0
				a.State.Audits = audits
				a.State.Phase = autonomy.Build
				if err := s.pinAutoRequirementDiagnoses(context.Background(), a, a.State.Items); err != nil {
					t.Fatal(err)
				}
				builder := &autoJob{ID: autoUUID(), TaskID: 900, Role: "builder", Status: "done"}
				if err := s.reserveAutoRequirementDiagnosis(a, builder); err != nil {
					t.Fatal(err)
				}
				a.Jobs = append(a.Jobs, builder)
				a.State.Reports = map[int64]json.RawMessage{900: json.RawMessage(store.J(autonomy.BuildReport{Outcome: "ready_for_review", Requirements: []autonomy.Requirement{pythonRequirement()}}))}
				yes := true
				reviewer := &autoJob{ID: autoUUID(), TaskID: 901, Role: "reviewer", Status: "exporting"}
				verdict := []byte(store.J(autonomy.Verdict{Outcome: "completed", Approve: &yes, Reason: "independently verified replacement", Requirements: []autonomy.Requirement{pythonRequirement()}}))
				if err := s.finishAutoRequirementDiagnosis(context.Background(), a, reviewer, verdict); err != nil {
					t.Fatal(err)
				}
				if j.RequirementHold || j.PendingPythonRequest == nil || !autoDeferredReady(j) {
					t.Fatal("reviewed alternative did not release retained assignment")
				}
			} else {
				j.PythonTestRecovery = &autoPrivateToolingReceipt{State: "verified", Key: j.PythonExpectedTestKey}
			}
			j.RecoveryCheckAt = time.Time{}
			if !autoResumeRecovered(a) || j.Status != "stopped" {
				t.Fatal("consumed UUID reused instead of same-task fresh resume")
			}
			clone, e := autoQueuePrivateResume(a, j, autoUUID(), strings.Repeat("a", 64))
			if e != nil {
				t.Fatal(e)
			}
			if clone.TaskID != j.TaskID || clone.PythonExpectedTestKey != j.PythonExpectedTestKey || clone.PythonTestNeedsResume {
				t.Fatal("resume lost identity or old failure cleared incorrectly")
			}
		})
	}
}
