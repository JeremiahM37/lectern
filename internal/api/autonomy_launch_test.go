package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchReconciliationRetainsAssignmentWithoutDuplicateStart(t *testing.T) {
	for _, tc := range []struct{ name, receipt, want string }{
		{"live launcher", `{"state":"launching"}`, "starting"},
		{"live unit", `{"state":"running"}`, "running"},
		{"failed consumed launch", `{"state":"consumed","worker_state":"failed"}`, "running"},
		{"completed consumed launch", `{"state":"consumed","worker_state":"done"}`, "running"},
		{"unused launch", `{"state":"unused"}`, "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, _, _ := repairFixture(t)
			j := &autoJob{ID: "launch-reconciliation", TaskID: 456, Role: "builder", Status: "starting", Provider: "claude", Model: "sonnet", RepairSourceTaskID: 201, RepairAttemptTaskID: 456}
			j.Admission = &autoAdmission{JobID: j.ID}
			a.Jobs = append(a.Jobs, j)
			s.autoBridges = map[string][]*http.Server{j.ID: {}}
			before, _ := json.Marshal(a.State)
			jobs := len(a.Jobs)
			root := t.TempDir()
			state, calls := filepath.Join(root, "state"), filepath.Join(root, "calls")
			if err := os.WriteFile(state, []byte(tc.receipt), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LECTERN_TEST_LAUNCH_STATE", state)
			t.Setenv("LECTERN_TEST_LAUNCH_CALLS", calls)
			t.Setenv("PATH", root+":"+os.Getenv("PATH"))
			stub := "#!/bin/sh\ncase \"$3\" in\nlaunch-state) cat \"$LECTERN_TEST_LAUNCH_STATE\" ;;\nstart) echo start >> \"$LECTERN_TEST_LAUNCH_CALLS\"; echo '{\"state\":\"running\"}' ;;\n*) exit 99 ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(root, "sudo"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			if err := s.launchAutoJob(context.Background(), a, j); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(a.State)
			if j.Status != tc.want || string(before) != string(after) || len(a.Jobs) != jobs || j.TaskID != 456 || j.RepairAttemptTaskID != 456 || j.RepairSourceTaskID != 201 || j.Admission.JobID != j.ID {
				t.Fatal("launch reconciliation changed assignment, audits or repair lineage", j)
			}
			called, _ := os.ReadFile(calls)
			if (tc.name == "unused launch" && string(called) != "start\n") || (tc.name != "unused launch" && len(called) != 0) {
				t.Fatalf("unexpected duplicate start: %q", called)
			}
			if err := s.saveAuto(a); err != nil {
				t.Fatal(err)
			}
			loaded, err := s.loadAuto()
			if err != nil {
				t.Fatal(err)
			}
			got := autoFindJob(loaded, j.TaskID)
			if got == nil || got.ID != j.ID || got.RepairAttemptTaskID != j.RepairAttemptTaskID || got.Status != tc.want {
				t.Fatal("reconciliation lost after reload", got)
			}
		})
	}
}

func TestLaunchReconciliationUnknownEvidenceFailsClosed(t *testing.T) {
	for _, raw := range []string{`{`, `{}`, `{"state":"ready"}`, `{"state":"consumed","worker_state":"running"}`, `{"state":"consumed"}`} {
		j := &autoJob{Status: "starting"}
		if _, err := autoReconcileLaunch(j, []byte(raw)); err == nil || j.Status != "starting" || autoRetryable(err.Error()) {
			t.Fatal("invalid launch evidence accepted", raw, err)
		}
	}
}
