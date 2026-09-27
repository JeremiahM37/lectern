package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

type maintenanceIngestionFixture struct {
	root    string
	job     *autoJob
	request autoServerObservationRequest
	receipt map[string]any
	catalog map[string]any
	items   []autonomy.Proposal
}

func newMaintenanceIngestionFixture(t *testing.T) *maintenanceIngestionFixture {
	t.Helper()
	root := t.TempDir()
	job, request := serverObservationFixture(t)
	if err := os.Mkdir(filepath.Join(root, job.ID), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := autoWriteServerObservation(root, request)
	if err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{"service_id": "temperature", "action": "service_resource_limits", "min": map[string]any{"cpu_quota_percent": 50, "memory_max_bytes": 268435456, "tasks_max": 64}, "max": map[string]any{"cpu_quota_percent": 200, "memory_max_bytes": 2147483648, "tasks_max": 512}, "memory_headroom_bytes": 33554432, "memory_headroom_ratio": 2, "stateless": true, "protected": false, "validation_profile": "service_resource_limits_v1"}
	catalog := map[string]any{"schema_version": 1, "registry_sha256": request.RegistrySHA, "targets": []any{map[string]any{"id": request.TargetID, "maintenance": []any{policy}}}}
	receipt := map[string]any{"schema_version": 1, "request_id": request.RequestID, "owner_job": job.ID, "owner_task": job.TaskID, "target_id": request.TargetID, "registry_sha256": request.RegistrySHA, "request_sha256": autoSHA(raw), "mutation_performed": false, "state": "observed", "configuration_complete": true, "captured_at": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "completed_at": time.Now().UTC().Format(time.RFC3339Nano), "helper_sha256": strings.Repeat("b", 64), "facts_sha256": strings.Repeat("c", 64), "configuration_sha256": strings.Repeat("d", 64), "receipt_sha256": strings.Repeat("e", 64), "facts": map[string]any{"services": map[string]any{"temperature": map[string]any{"state": "available", "fields": map[string]any{"LoadState": "loaded"}, "identity_files": map[string]any{"program": map[string]any{"state": "available", "sha256": strings.Repeat("f", 64)}}}}}}
	items := []autonomy.Proposal{{ProjectID: 1, Acceptance: []string{"registered service remains healthy"}, Maintenance: &autonomy.MaintenanceProposal{TargetID: request.TargetID, ServiceID: "temperature", ObservationID: request.RequestID, Limits: autonomy.MaintenanceLimits{CPUPercent: 50, MemoryBytes: 268435456, Tasks: 64}}}}
	f := &maintenanceIngestionFixture{root, job, request, receipt, catalog, items}
	shim := `#!/bin/sh
case "$3" in
 server-targets) cat "$MAINTENANCE_TEST_ROOT/catalog.json" ;;
 server-observe-status)
  [ "$4" = --job ] && [ "$6" = --observation-id ] || exit 91
  [ -f "$MAINTENANCE_TEST_ROOT/$5/server-observations/$7/request.json" ] || exit 92
  printf '%s\n' "$5/$7" >> "$MAINTENANCE_TEST_ROOT/status-calls"
  cat "$MAINTENANCE_TEST_ROOT/receipt.json" ;;
 *) exit 93 ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "sudo"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":/usr/bin:/bin")
	t.Setenv("MAINTENANCE_TEST_ROOT", root)
	f.save(t)
	return f
}
func (f *maintenanceIngestionFixture) save(t *testing.T) {
	t.Helper()
	for name, value := range map[string]any{"catalog.json": f.catalog, "receipt.json": f.receipt} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(f.root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *maintenanceIngestionFixture) pin(a *autoRecord) error {
	return (&Server{}).pinAutoMaintenanceAt(context.Background(), f.root, a, f.job, f.items)
}
func TestMaintenanceBridgeIngestionTrustedReceiptPinsExactPlan(t *testing.T) {
	f := newMaintenanceIngestionFixture(t)
	a := &autoRecord{}
	if err := f.pin(a); err != nil {
		t.Fatal(err)
	}
	pin := a.MaintenancePins[f.items[0].Maintenance.Pin]
	if pin == nil || pin.BeforeSHA != f.receipt["configuration_sha256"] || pin.ObservationReceiptSHA != f.receipt["receipt_sha256"] || pin.RegistrySHA != f.request.RegistrySHA {
		t.Fatal("trusted receipt was not pinned", pin)
	}
	calls, err := os.ReadFile(filepath.Join(f.root, "status-calls"))
	if err != nil || string(calls) != f.job.ID+"/"+f.request.RequestID+"\n" {
		t.Fatal("wrong request owner queried", string(calls), err)
	}
	if err := f.pin(a); err != nil || len(a.MaintenancePins) != 1 {
		t.Fatal("exact retry changed authority", err)
	}
}
func TestMaintenanceBridgeIngestionRejectsUnboundEvidence(t *testing.T) {
	for _, which := range []string{"missing_request", "task", "planner", "registry", "receipt_owner", "service", "incomplete", "identity_missing", "identity_invalid", "supplied_pin", "stale"} {
		t.Run(which, func(t *testing.T) {
			f := newMaintenanceIngestionFixture(t)
			a := &autoRecord{}
			service := f.receipt["facts"].(map[string]any)["services"].(map[string]any)["temperature"].(map[string]any)
			switch which {
			case "missing_request":
				os.Remove(filepath.Join(f.root, f.job.ID, "server-observations", f.request.RequestID, "request.json"))
			case "task":
				f.job.TaskID++
			case "planner":
				f.job.Role = "builder"
			case "registry":
				f.catalog["registry_sha256"] = strings.Repeat("9", 64)
			case "receipt_owner":
				f.receipt["owner_job"] = "33333333-3333-4333-8333-333333333333"
			case "service":
				f.items[0].Maintenance.ServiceID = "unregistered"
			case "incomplete":
				f.receipt["configuration_complete"] = false
			case "identity_missing":
				delete(service, "identity_files")
			case "identity_invalid":
				service["identity_files"] = map[string]any{"program": map[string]any{"state": "available", "sha256": "worker says safe"}}
			case "supplied_pin":
				f.items[0].Maintenance.Pin = strings.Repeat("1", 64)
			case "stale":
				f.receipt["captured_at"] = time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339Nano)
			}
			f.save(t)
			if err := f.pin(a); err == nil {
				t.Fatal("accepted", which)
			}
			if len(a.MaintenancePins) != 0 {
				t.Fatal("failed ingestion mutated ledger")
			}
		})
	}
}
func TestMaintenanceBridgeIngestionPendingIsRetained(t *testing.T) {
	for _, state := range []string{"observing", "waiting"} {
		t.Run(state, func(t *testing.T) {
			f := newMaintenanceIngestionFixture(t)
			f.receipt["state"] = state
			f.save(t)
			a := &autoRecord{}
			err := f.pin(a)
			if !errors.Is(err, errAutoArtifactPending) {
				t.Fatal("transient observation treated as report failure", err)
			}
			if len(a.MaintenancePins) != 0 || f.items[0].Maintenance.Pin != "" {
				t.Fatal("pending observation mutated plan")
			}
		})
	}
}
func TestMaintenanceBridgeIngestionAllOrNoneAndUniqueResource(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "second_invalid", true: "duplicate"}[duplicate], func(t *testing.T) {
			f := newMaintenanceIngestionFixture(t)
			second := f.items[0]
			m := *second.Maintenance
			second.Maintenance = &m
			if !duplicate {
				m.ServiceID = "missing"
			}
			f.items = append(f.items, second)
			a := &autoRecord{}
			if err := f.pin(a); err == nil {
				t.Fatal("accepted conflicting plan")
			}
			if len(a.MaintenancePins) != 0 || f.items[0].Maintenance.Pin != "" || f.items[1].Maintenance.Pin != "" {
				t.Fatal("partial proposal or ledger mutation")
			}
		})
	}
}
func TestMaintenanceBridgeCatalogDuplicateAndProtection(t *testing.T) {
	for _, which := range []string{"duplicate", "protected", "stateful", "reversed", "profile", "missing_ratio", "wrong_ratio"} {
		t.Run(which, func(t *testing.T) {
			f := newMaintenanceIngestionFixture(t)
			target := f.catalog["targets"].([]any)[0].(map[string]any)
			policy := target["maintenance"].([]any)[0].(map[string]any)
			switch which {
			case "duplicate":
				target["maintenance"] = []any{policy, policy}
			case "protected":
				policy["protected"] = true
			case "stateful":
				policy["stateless"] = false
			case "reversed":
				policy["min"].(map[string]any)["tasks_max"] = 999
			case "missing_ratio":
				delete(policy, "memory_headroom_ratio")
			case "wrong_ratio":
				policy["memory_headroom_ratio"] = 1
			case "profile":
				policy["validation_profile"] = "arbitrary_shell"
			}
			f.save(t)
			if err := f.pin(&autoRecord{}); err == nil {
				t.Fatal("accepted invalid catalogue")
			}
		})
	}
}

func maintenanceBindingFixture(t *testing.T) (*autoRecord, *autoJob) {
	t.Helper()
	f := newMaintenanceIngestionFixture(t)
	a := &autoRecord{}
	if err := f.pin(a); err != nil {
		t.Fatal(err)
	}
	yes := true
	a.State = &autonomy.State{Phase: autonomy.Build, Revision: 2, Items: f.items, Audits: map[string]autonomy.Verdict{"auditor_a": {Approve: &yes}, "auditor_b": {Approve: &yes}}, Reports: map[int64]json.RawMessage{21: json.RawMessage(`{"approve":true,"summary":"first independent audit"}`), 22: json.RawMessage(`{"approve":true,"summary":"second independent audit"}`)}, Assignments: []autonomy.Assignment{{TaskID: 21, Role: "auditor_a", Round: 2, Completed: true}, {TaskID: 22, Role: "auditor_b", Round: 2, Completed: true}, {TaskID: 23, Role: "builder", Round: 2}}}
	a.Jobs = []*autoJob{{ID: "22222222-2222-4222-8222-222222222222", TaskID: 21, Role: "auditor_a", Status: "done"}, {ID: "33333333-3333-4333-8333-333333333333", TaskID: 22, Role: "auditor_b", Status: "done"}}
	builder := &autoJob{ID: "44444444-4444-4444-8444-444444444444", TaskID: 23, Role: "builder"}
	a.Jobs = append(a.Jobs, builder)
	return a, builder
}
func TestMaintenanceBridgeBindOnlyCurrentCompletedActualAudits(t *testing.T) {
	for _, which := range []string{"approved", "foreign_owner", "missing_owner", "missing_report", "rejected_report", "missing_approve", "incomplete", "old_round", "duplicate", "cached_only"} {
		t.Run(which, func(t *testing.T) {
			a, b := maintenanceBindingFixture(t)
			switch which {
			case "foreign_owner":
				a.Jobs[0].Role = "reviewer"
			case "missing_owner":
				a.Jobs = a.Jobs[1:]
			case "missing_report":
				delete(a.State.Reports, 21)
			case "rejected_report":
				a.State.Reports[21] = json.RawMessage(`{"approve":false}`)
			case "missing_approve":
				a.State.Reports[21] = json.RawMessage(`{"summary":"looks good"}`)
			case "incomplete":
				a.State.Assignments[0].Completed = false
			case "old_round":
				a.State.Assignments[0].Round--
			case "duplicate":
				a.State.Assignments = append(a.State.Assignments, a.State.Assignments[0])
			case "cached_only":
				a.State.Assignments = a.State.Assignments[2:]
			}
			err := autoBindMaintenanceJob(a, b)
			if which == "approved" {
				if err != nil || b.MaintenanceAdmission == nil {
					t.Fatal("valid actual audits denied", err)
				}
				if b.MaintenanceAdmission.Audits[0].ReceiptSHA != autoSHA(a.State.Reports[21]) {
					t.Fatal("audit report bytes not bound")
				}
			} else if err == nil || b.MaintenanceAdmission != nil || b.MaintenancePin != "" {
				t.Fatal("invalid audit admitted", which, err)
			}
		})
	}
}
func TestMaintenanceBridgeReviewerInheritsDetachedAdmission(t *testing.T) {
	a, b := maintenanceBindingFixture(t)
	if err := autoBindMaintenanceJob(a, b); err != nil {
		t.Fatal(err)
	}
	reviewer := &autoJob{ID: "55555555-5555-4555-8555-555555555555", TaskID: 24, Role: "reviewer"}
	if err := autoBindMaintenanceJob(a, reviewer); err != nil {
		t.Fatal(err)
	}
	if reviewer.MaintenanceAdmission == b.MaintenanceAdmission || reviewer.MaintenancePin != b.MaintenancePin {
		t.Fatal("reviewer alias or identity mismatch")
	}
	original := reviewer.MaintenanceAdmission.Pin.Acceptance[0]
	b.MaintenanceAdmission.Pin.Acceptance[0] = "changed after copy"
	b.MaintenanceAdmission.Audits[0].ReceiptSHA = "tampered"
	if reviewer.MaintenanceAdmission.Pin.Acceptance[0] != original || reviewer.MaintenanceAdmission.Audits[0].ReceiptSHA == "tampered" {
		t.Fatal("reviewer admission changed through builder alias")
	}
	b.MaintenanceAdmission = nil
	if err := autoBindMaintenanceJob(a, &autoJob{Role: "reviewer"}); err == nil {
		t.Fatal("reviewer accepted missing original admission")
	}
}
