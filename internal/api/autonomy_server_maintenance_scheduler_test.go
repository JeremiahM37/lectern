package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestMaintenanceSchedulerIndependentProviderAndCompletedCycle(t *testing.T) {
	for _, scenario := range []string{"model", "complete", "quota", "off"} {
		t.Run(scenario, func(t *testing.T) {
			completed := scenario != "model"
			a, tx, _ := maintenanceControllerFixture(t)
			s := autoTestServer(t)
			tx.Started = true
			if scenario == "off" {
				a.Config.Enabled = false
			}
			a.RememberedDay = a.State.Date
			a.NextCycleScheduled = true
			a.NextCycleAt = time.Now().Add(time.Hour)
			a.State.Phase = autonomy.Complete
			a.State.Assignments = nil
			if !completed {
				a.State.Phase = autonomy.Build
				a.State.Assignments = []autonomy.Assignment{{TaskID: 999, Role: "builder"}}
				a.Jobs = append(a.Jobs, &autoJob{ID: "33333333-3333-4333-8333-333333333333", TaskID: 999, Role: "builder", Provider: "claude", Status: "running"})
			}
			root := t.TempDir()
			log := filepath.Join(root, "calls")
			policy := map[string]any{"service_id": tx.Candidate.Admission.Pin.ServiceID, "action": "service_resource_limits", "min": map[string]any{"cpu_quota_percent": 50, "memory_max_bytes": 268435456, "tasks_max": 64}, "max": map[string]any{"cpu_quota_percent": 200, "memory_max_bytes": 2147483648, "tasks_max": 512}, "memory_headroom_bytes": 33554432, "memory_headroom_ratio": 2, "stateless": true, "protected": false, "validation_profile": "service_resource_limits_v1"}
			catalog, _ := json.Marshal(map[string]any{"schema_version": 1, "registry_sha256": tx.Candidate.Admission.Pin.RegistrySHA, "targets": []any{map[string]any{"id": tx.Candidate.Admission.Pin.TargetID, "maintenance": []any{policy}}}})
			os.WriteFile(filepath.Join(root, "catalog"), catalog, 0600)
			os.WriteFile(filepath.Join(root, "status"), controllerReceipt(t, tx, "running"), 0600)
			stop, _ := json.Marshal(map[string]any{"state": "stopped", "no_effects": true, "operation_id": tx.ID, "generation": tx.Generation})
			os.WriteFile(filepath.Join(root, "stop"), stop, 0600)
			script := `#!/bin/sh
printf '%s\n' "$3" >> "$SCHED_ROOT/calls"
case "$3" in
server-targets) cat "$SCHED_ROOT/catalog";;
server-maintenance-status) cat "$SCHED_ROOT/status";;
server-maintenance-stop) cat "$SCHED_ROOT/stop";;
stop) echo '{}' ;;
snapshot) echo '{"state":"exporting"}' ;;
*) exit 91;;
esac
`
			os.WriteFile(filepath.Join(root, "sudo"), []byte(script), 0700)
			t.Setenv("PATH", root+":"+os.Getenv("PATH"))
			t.Setenv("SCHED_ROOT", root)
			calls := 0
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			http.DefaultTransport = launchUsageTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/api/agent-usage" {
					t.Fatal(r.URL)
				}
				// No Claude allowance: the running model is ineligible, while this already
				// admitted codex maintenance operation has a fresh, ample weekly budget.
				expertQuota(a, time.Now())
				raw, _ := json.Marshal(a.Quota)
				if scenario == "quota" {
					raw = []byte(`{"providers":[]}`)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})
			if err := s.saveAuto(a); err != nil {
				t.Fatal(err)
			}
			s.RunAutonomyTick(context.Background())
			got, err := s.loadAuto()
			if err != nil {
				t.Fatal(err)
			}
			owned := got.MaintenanceTransactions[tx.ID]
			if scenario == "quota" || scenario == "off" {
				wantCalls := 1
				if scenario == "off" {
					wantCalls = 0
				}
				if calls != wantCalls || owned.Polls != 1 || !owned.StopConfirmed || owned.State != "cancelled" {
					t.Fatalf("owned cancellation lost: fetch=%d tx=%+v", calls, owned)
				}
				if got.MaintenanceStatus == nil || got.MaintenanceStatus.Pending != 1 {
					t.Fatal("cancelled candidate disappeared instead of remaining resumable")
				}
				return
			}
			if calls != 1 || owned.Polls != 1 || owned.StopRequested {
				t.Fatalf("maintenance improperly gated or polled twice: fetch=%d tx=%+v", calls, owned)
			}
			data, _ := os.ReadFile(log)
			if strings.Contains(string(data), "server-maintenance-stop") || !strings.Contains(string(data), "server-maintenance-status") {
				t.Fatalf("wrong maintenance effects: %s", data)
			}
			if got.MaintenanceStatus == nil || got.MaintenanceStatus.Pending != 1 {
				t.Fatal("lost persisted maintenance projection", got.MaintenanceStatus)
			}
			if completed {
				if got.Status != "maintenance_pending" {
					t.Fatal(got.Status, got.Reason)
				}
			} else {
				if autoFindJob(got, 999).Status != "stopped" {
					t.Fatal("ineligible model not stopped")
				}
				if strings.Index(string(data), "server-maintenance-status") > strings.LastIndex(string(data), "\nstop\n") {
					t.Fatal("model route ran before maintenance", string(data))
				}
			}
		})
	}
}

func TestMaintenanceSchedulerEndedValidationAndCorrectionOwner(t *testing.T) {
	for _, mode := range []string{"terminal", "lost", "running_correction"} {
		t.Run(mode, func(t *testing.T) {
			s, _, j, root, log := maintenanceHTTPFixture(t)
			t.Setenv("LECTERN_MAINT_MODE", "lost")
			w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", `{"pin_sha256":"`+j.MaintenancePin+`"}`)
			if w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			a, err := s.loadAuto()
			if err != nil {
				t.Fatal(err)
			}
			j = autoFindJob(a, j.TaskID)
			j.Status = "done"
			if mode == "running_correction" {
				next := *j
				next.ID = "33333333-3333-4333-8333-333333333333"
				next.Status = "running"
				a.Jobs = append(a.Jobs, &next)
			}
			os.WriteFile(log, nil, 0600)
			if mode == "terminal" {
				t.Setenv("LECTERN_MAINT_MODE", "")
			}
			if err = s.reconcileEndedMaintenanceValidations(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(log)
			for _, l := range a.MaintenanceValidations {
				switch mode {
				case "running_correction":
					if len(data) != 0 || l.StopConfirmed {
						t.Fatal("disturbed running correction", string(data))
					}
				case "terminal":
					if l.Validation == nil || len(l.ReceiptRaw) == 0 || !l.StopConfirmed || strings.Contains(string(data), "-stop") {
						t.Fatal("lost successful ended validation", l, string(data))
					}
				case "lost":
					if !l.StopRequested || !l.StopConfirmed || !strings.Contains(string(data), "server-maintenance-stop") {
						t.Fatal("ended lost helper leaked", l, string(data))
					}
				}
			}
		})
	}
}

func TestMaintenanceSchedulerQuotaFailureClearsPriorSample(t *testing.T) {
	a := &autoRecord{}
	expertQuota(a, time.Now())
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = launchUsageTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})
	if refreshAutoQuota(context.Background(), a) == nil || len(a.Quota.Providers) != 0 {
		t.Fatal("old quota remained admissible")
	}
}

func TestMaintenanceSchedulerEndedValidationFairnessAndStrictStop(t *testing.T) {
	s := autoTestServer(t)
	a := &autoRecord{Config: autonomy.DefaultConfig(), MaintenanceValidations: map[string]*autoMaintenanceValidationLease{}}
	root := t.TempDir()
	t.Setenv("SCHED_FAIR_ROOT", root)
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	script := `#!/usr/bin/python3
import sys,json,os
args=sys.argv;cmd=args[3];op=args[args.index('--operation-id')+1]
with open(os.environ['SCHED_FAIR_ROOT']+'/calls','a') as f:f.write(cmd+' '+op[0]+'\n')
if cmd=='server-maintenance-status':sys.exit(1)
if cmd!='server-maintenance-stop':sys.exit(92)
if op[0]=='e':print(json.dumps(dict(operation_id=op,generation=1,state='stopped',no_effects=True)))
elif op[0]=='a':print(json.dumps(dict(operation_id=op,generation=1,state='stopped')))
else:sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(root, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		a.MaintenanceValidations[id] = &autoMaintenanceValidationLease{ID: id, OperationID: strings.Repeat(id, 64), OwnerJob: "fixture", OwnerTask: 1, Generation: 1}
	}
	if err := s.reconcileEndedMaintenanceValidations(context.Background(), a); err == nil {
		t.Fatal("failed cleanup hidden")
	}
	if a.MaintenanceValidations["a"].StopConfirmed {
		t.Fatal("unproven no-effect stop accepted")
	}
	data, _ := os.ReadFile(filepath.Join(root, "calls"))
	if strings.Contains(string(data), " e\n") {
		t.Fatal("unbounded firsttick")
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	var err error
	a, err = s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.reconcileEndedMaintenanceValidations(context.Background(), a)
	if !a.MaintenanceValidations["e"].StopConfirmed {
		t.Fatal("four broken leases starved fifth across restart")
	}
}
