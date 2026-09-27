package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"strings"
	"testing"
)

func TestNodeLaunchFailurePreservesAndHoldsConsumedAssignment(t *testing.T) {
	for _, diagnostic := range []string{"missing", "integrity"} {
		t.Run(diagnostic, func(t *testing.T) {
			s, a, p := documentationFixture(t)
			response := documentationRunner(t)
			docResponse(t, response, map[string]string{"state": "ready", "sha256": strings.Repeat("a", 64)})
			task, e := s.DB.InsertTask(&store.Task{ProjectID: p.ProjectID, Title: "Node consumer", Status: "running"})
			if e != nil {
				t.Fatal(e)
			}
			request, e := autoNodeInputs(nodeRequirement())
			if e != nil {
				t.Fatal(e)
			}
			request.SourceJob = autoUUID()
			request.SourceSHA = autoSHA([]byte("source"))
			request.AdmissionSHA = autoSHA([]byte("admission"))
			j := &autoJob{ID: autoUUID(), TaskID: task.ID, Role: "builder", Status: "running", Recovery: &autoRecoveryReceipt{State: "not_applicable"}, NodeRequest: request, NodeGeneration: 2}
			verified := nodeVerified(j)
			j.NodeRecovery = &verified
			a.Jobs = append(a.Jobs, j)
			original := &autonomy.State{Date: "2026-09-26", Phase: autonomy.Build, Items: []autonomy.Proposal{{ProjectID: p.ProjectID}}, Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: "builder"}}}
			a.State = original
			var failure map[string]any
			_ = json.Unmarshal([]byte(store.J(verified)), &failure)
			failure["state"] = "unavailable"
			failure["unsupported"] = true
			failure["executed"] = false
			failure["scope"] = "full content validation before model execution"
			failure["diagnostic"] = diagnostic
			failure["reason"] = "selected content invalid"
			raw := []byte(store.J(map[string]any{"state": "failed", "node_runtime": failure}))
			handled, e := s.handleAutoNodeLaunchFailure(context.Background(), a, j, raw)
			if e != nil || !handled {
				t.Fatal(handled, e)
			}
			if j.Status != "deferred" || !j.RequirementHold || !j.NodeNeedsResume || len(a.HeldRuns) != 1 || a.HeldRuns[0] != original {
				t.Fatal("lost held assignment", j.Status, j.RequirementHold, a.HeldRuns)
			}
			if len(j.RequirementIDs) != 1 || a.Requirements[j.RequirementIDs[0]].NodeReceipt.Diagnostic != diagnostic {
				t.Fatal("missing bound diagnosis")
			}
			j.NodeRecovery = &verified
			if autoDeferredReady(j) {
				t.Fatal("metadata cleared integrity hold")
			}
			// Simulate the separately tested audited remedy release; selection is still
			// the same task, but the already consumed process UUID cannot be relaunched.
			j.RequirementHold = false
			if !autoResumeRecovered(a) || j.Status != "stopped" || a.State != original {
				t.Fatal("did not retain stopped assignment")
			}
			clone := *j
			autoPrepareNodeResume(j, &clone)
			if clone.NodeNeedsResume {
				t.Fatal("new process inherits consumed marker")
			}
		})
	}
}
func TestNodeLaunchFailureRejectsUnboundOrExecutedEvidence(t *testing.T) {
	request, _ := autoNodeInputs(nodeRequirement())
	request.SourceJob = autoUUID()
	request.SourceSHA = autoSHA([]byte("source"))
	request.AdmissionSHA = autoSHA([]byte("admission"))
	for _, field := range []string{"generation", "bundle_key", "lock_sha256", "source_job", "executed", "scope"} {
		j := &autoJob{NodeRequest: request, NodeGeneration: 2}
		verified := nodeVerified(j)
		j.NodeRecovery = &verified
		var failure map[string]any
		_ = json.Unmarshal([]byte(store.J(verified)), &failure)
		failure["state"] = "unavailable"
		failure["unsupported"] = true
		failure["executed"] = false
		failure["scope"] = "full content validation before model execution"
		failure["diagnostic"] = "integrity"
		switch field {
		case "generation":
			failure[field] = 1
		case "executed":
			failure[field] = true
		default:
			failure[field] = "wrong"
		}
		s := &Server{}
		handled, e := s.handleAutoNodeLaunchFailure(context.Background(), &autoRecord{}, j, []byte(store.J(map[string]any{"state": "failed", "node_runtime": failure})))
		if !handled || e == nil || j.RequirementHold || j.NodeRecovery != &verified {
			t.Fatal("accepted invalid evidence", field, handled, e)
		}
	}
}
