package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maintenanceHTTPFixture(t *testing.T) (*Server, *autoRecord, *autoJob, string, string) {
	t.Helper()
	s := autoTestServer(t)
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: &autonomy.State{Phase: autonomy.Review}}
	expertQuota(a, time.Now())
	a.Config.Enabled = true
	pin := autoMaintenancePlanPin{ProjectID: 1, TargetID: "local", ServiceID: "temp-api", RegistrySHA: autoSHA([]byte("registry")), BeforeSHA: autoSHA([]byte("before")), Limits: autoMaintenanceLimits{CPUPercent: 50, MemoryBytes: 256 << 20, Tasks: 64}, Acceptance: []string{"healthy"}}
	pin.CandidateSHA = autoMaintenanceCandidate(pin.Limits)
	pin.Key = autoMaintenancePinHash(pin)
	j := &autoJob{ID: "11111111-1111-4111-8111-111111111111", TaskID: 20, Role: "reviewer", Status: "running", Provider: "codex", MaintenancePin: pin.Key, MaintenanceAdmission: &autoMaintenanceAdmitted{Pin: pin, TaskID: 10, JobID: "22222222-2222-4222-8222-222222222222"}}
	a.Jobs = []*autoJob{j}
	a.State.Assignments = []autonomy.Assignment{{TaskID: 20, Role: "reviewer"}}
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, j.ID), 0700); e != nil {
		t.Fatal(e)
	}
	shim := t.TempDir()
	log := filepath.Join(shim, "calls")
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	t.Setenv("LECTERN_MAINT_ROOT", root)
	t.Setenv("LECTERN_MAINT_LOG", log)
	t.Setenv("LECTERN_MAINT_MODE", "")
	script := `#!/usr/bin/python3
import json,sys,os,pathlib,hashlib
h=lambda b:hashlib.sha256(b).hexdigest()
args=sys.argv;cmd=args[3]
with open(os.environ['LECTERN_MAINT_LOG'],'a') as f:f.write(cmd+'\n')
op=args[args.index('--operation-id')+1];job=args[args.index('--job')+1];gen=int(args[args.index('--generation')+1])
if cmd=='server-maintenance-stop':print(json.dumps(dict(operation_id=op,generation=gen,state='stopped',no_effects=True)));sys.exit()
if cmd not in ('server-maintenance-validate','server-maintenance-status'):sys.exit(90)
raw=(pathlib.Path(os.environ['LECTERN_MAINT_ROOT'])/job/'server-maintenance'/(op+'.validate.json')).read_bytes();r=json.loads(raw)
mode=os.environ.get('LECTERN_MAINT_MODE','')
if mode=='lost':sys.exit(91)
inner=dict(state='validated',operation_id=op,pin_sha256=r['pin_sha256'],owner_job=job,owner_task=r['owner_task'],registry_sha256=r['registry_sha256'],before_sha256=r['expected_state_sha256'],candidate_sha256=h(json.dumps(r['limits'],sort_keys=True,separators=(',',':')).encode()),profile='service_resource_limits_v1',profile_sha256=h(b'profile'),output_sha256=h(b'output'),receipt_sha256=h(b'inner'),executed=True,exit_code=0,mutation_performed=False)
outer=dict(schema_version=1,phase='validate',operation_id=op,owner_job=job,owner_task=r['owner_task'],pin_sha256=r['pin_sha256'],generation=gen,request_sha256=h(raw),state='validated',result=inner,receipt_sha256=h(b'outer'))
if mode=='foreign':outer['owner_task']=99
if mode=='unexecuted':inner['executed']=False
if mode=='candidate':inner['candidate_sha256']=h(b'wrong')
if mode=='mutation':inner['mutation_performed']=True
if mode=='phase':outer['phase']='apply'
if mode=='cancelled':outer['state']='cancelled';outer.pop('result')
if mode=='conflict':outer['state']='conflict';outer.pop('result')
inner.pop('receipt_sha256',None);inner['receipt_sha256']=h(json.dumps(inner,sort_keys=True,separators=(',',':')).encode())
outer.pop('receipt_sha256',None);outer['receipt_sha256']=h(json.dumps(outer,sort_keys=True,separators=(',',':')).encode())
if mode=='tampered':outer['reason']='changed after seal'
print(json.dumps(outer))
`
	if e := os.WriteFile(filepath.Join(shim, "sudo"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return s, a, j, root, log
}
func maintenanceHTTP(s *Server, root, job, method, url, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.autoMaintenanceValidationBridgeAt(root, job, w, httptest.NewRequest(method, url, strings.NewReader(body)))
	return w
}
func TestMaintenanceHTTPDurableLostResponseAndSameTaskCorrection(t *testing.T) {
	s, _, j, root, log := maintenanceHTTPFixture(t)
	t.Setenv("LECTERN_MAINT_MODE", "lost")
	input := `{"pin_sha256":"` + j.MaintenancePin + `"}`
	w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	id := body["id"].(string)
	a, e := s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	l := a.MaintenanceValidations[id]
	if l == nil || len(l.RequestRaw) == 0 {
		t.Fatal("reservation missing")
	}
	raw, e := os.ReadFile(filepath.Join(root, l.OwnerJob, "server-maintenance", l.OperationID+".validate.json"))
	if e != nil || autoSHA(raw) != l.RequestSHA {
		t.Fatal("request not durable", e)
	}
	// A corrected report is a new process on the same admitted reviewer task.
	old := a.Jobs[0]
	old.Status = "stopped"
	clone := *old
	clone.ID = "33333333-3333-4333-8333-333333333333"
	clone.Status = "running"
	a.Jobs = append(a.Jobs, &clone)
	if e = s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	t.Setenv("LECTERN_MAINT_MODE", "")
	w = maintenanceHTTP(s, root, clone.ID, "GET", "/maintenance-validation?id="+id, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, e = s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	l = a.MaintenanceValidations[id]
	if l.Validation == nil || l.Validation.OwnerJob != old.ID || len(l.ReceiptRaw) == 0 {
		t.Fatal("execution provenance lost")
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "server-maintenance-validate\n") != 1 || strings.Count(string(calls), "server-maintenance-status\n") != 1 {
		t.Fatal("GET relaunched validation", string(calls))
	}
	w = maintenanceHTTP(s, root, clone.ID, "POST", "/maintenance-validation", input)
	if w.Code != 200 || len(a.MaintenanceValidations) != 1 {
		t.Fatal("duplicate reservation")
	}
}
func TestMaintenanceHTTPRejectsForeignOrUnexecutedReceipt(t *testing.T) {
	for _, mode := range []string{"foreign", "unexecuted", "candidate", "mutation", "phase", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			s, _, j, root, _ := maintenanceHTTPFixture(t)
			t.Setenv("LECTERN_MAINT_MODE", mode)
			w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", `{"pin_sha256":"`+j.MaintenancePin+`"}`)
			if w.Code != 502 {
				t.Fatal(w.Code, w.Body.String())
			}
			a, _ := s.loadAuto()
			for _, l := range a.MaintenanceValidations {
				if l.Validation != nil || len(l.ReceiptRaw) > 0 {
					t.Fatal("bad receipt persisted as terminal")
				}
			}
		})
	}
}
func TestMaintenanceHTTPAuthorityAndInputBoundaries(t *testing.T) {
	s, a, j, root, log := maintenanceHTTPFixture(t)
	input := `{"pin_sha256":"` + j.MaintenancePin + `"}`
	for _, bad := range []string{input[:len(input)-1] + `,"script":"evil"}`, `{"pin_sha256":"../escape"}`, input[:len(input)-1] + `,"pin_sha256":"` + j.MaintenancePin + `"}`, input + strings.Repeat(" ", 5000) + `{}`} {
		w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", bad)
		if w.Code != 400 {
			t.Fatal("bad input", w.Code)
		}
	}
	for _, kind := range []string{"off", "stale", "completed", "quota", "builder"} {
		a.Config.Enabled = true
		j.Role = "reviewer"
		a.State.Assignments = []autonomy.Assignment{{TaskID: j.TaskID, Role: "reviewer"}}
		expertQuota(a, time.Now())
		switch kind {
		case "off":
			a.Config.Enabled = false
		case "stale":
			a.State.Assignments = nil
		case "completed":
			a.State.Assignments[0].Completed = true
		case "quota":
			a.Quota.Providers = nil
		case "builder":
			j.Role = "builder"
		}
		if e := s.saveAuto(a); e != nil {
			t.Fatal(e)
		}
		w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
		if w.Code != 409 {
			t.Fatal(kind, w.Code, w.Body.String())
		}
	}
	calls, _ := os.ReadFile(log)
	if len(calls) > 0 {
		t.Fatal("unauthorized action reached runner", string(calls))
	}
}

func TestMaintenanceHTTPDurableOffCancellationAndReadOnlyPolling(t *testing.T) {
	s, _, j, root, log := maintenanceHTTPFixture(t)
	t.Setenv("LECTERN_MAINT_MODE", "lost")
	input := `{"pin_sha256":"` + j.MaintenancePin + `"}`
	w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	id := body["id"].(string)
	a, e := s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	a.Config.Enabled = false
	if e = s.stopAutoMaintenanceValidations(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	a, e = s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	l := a.MaintenanceValidations[id]
	if !l.StopRequested || !l.StopConfirmed || autoMaintenanceValidationStopPending(a) {
		t.Fatal("cancel intent/confirmation not durable")
	}
	w = maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 409 {
		t.Fatal("OFF restarted", w.Code)
	}
	w = maintenanceHTTP(s, root, j.ID, "GET", "/maintenance-validation?id="+id, "")
	if w.Code != 202 {
		t.Fatal("retained polling blocked", w.Code, w.Body.String())
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "server-maintenance-validate\n") != 1 || strings.Count(string(calls), "server-maintenance-stop\n") != 1 {
		t.Fatal("unexpected extra effect", string(calls))
	}
}

func TestMaintenanceValidationCancellationRenewalPreservesReservationAndHistory(t *testing.T) {
	s, _, j, root, _ := maintenanceHTTPFixture(t)
	input := `{"pin_sha256":"` + j.MaintenancePin + `"}`
	t.Setenv("LECTERN_MAINT_MODE", "lost")
	w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	id := body["id"].(string)
	a, _ := s.loadAuto()
	op := a.MaintenanceValidations[id].OperationID
	original := append([]byte(nil), a.MaintenanceValidations[id].RequestRaw...)
	a.Config.Enabled = false
	if e := s.stopAutoMaintenanceValidations(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	// Confirmed stop alone cannot erase an unobserved completed result.
	a, _ = s.loadAuto()
	a.Config.Enabled = true
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	w = maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 409 {
		t.Fatal("renewed without terminal evidence", w.Code)
	}
	t.Setenv("LECTERN_MAINT_MODE", "cancelled")
	w = maintenanceHTTP(s, root, j.ID, "GET", "/maintenance-validation?id="+id, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ = s.loadAuto()
	a.Config.Enabled = false
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	w = maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 409 {
		t.Fatal("OFF reauthorized")
	}
	a, _ = s.loadAuto()
	a.Config.Enabled = true
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	t.Setenv("LECTERN_MAINT_MODE", "")
	w = maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ = s.loadAuto()
	l := a.MaintenanceValidations[id]
	if len(a.MaintenanceValidations) != 1 || l.OperationID != op || l.Generation != 2 || len(l.History) != 1 || !strings.EqualFold(string(l.History[0].RequestRaw), string(original)) || l.History[0].State != "cancelled" || l.Validation == nil {
		t.Fatal("lost reservation/history", l)
	}
	var request autoMaintenanceValidationRequest
	file, e := os.ReadFile(filepath.Join(root, l.OwnerJob, "server-maintenance", op+".validate.json"))
	if e != nil || json.Unmarshal(file, &request) != nil || request.Generation != 2 {
		t.Fatal("generation publication failed", e)
	}
}
func TestMaintenanceValidationRegistryConflictIsTerminalDiagnostic(t *testing.T) {
	s, _, j, root, log := maintenanceHTTPFixture(t)
	t.Setenv("LECTERN_MAINT_MODE", "conflict")
	input := `{"pin_sha256":"` + j.MaintenancePin + `"}`
	for i := 0; i < 2; i++ {
		w := maintenanceHTTP(s, root, j.ID, "POST", "/maintenance-validation", input)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	a, _ := s.loadAuto()
	for _, l := range a.MaintenanceValidations {
		if l.State != "conflict" || len(l.ReceiptRaw) == 0 || l.Validation != nil {
			t.Fatal("conflict treated as verification")
		}
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "server-maintenance-validate\n") != 1 {
		t.Fatal("terminal stale pin relaunched")
	}
}
func TestMaintenanceValidationPythonCanonicalReceipt(t *testing.T) {
	// Python json.dumps(sort_keys=True,separators=(',',':'),ensure_ascii=True).
	canonical := `{"float":1.0,"text":"\u00e9\ud83d\ude80<>&\u007f","zero":0}`
	raw := canonical[:len(canonical)-1] + `,"receipt_sha256":"` + autoSHA([]byte(canonical)) + `"}`
	if e := autoVerifyMaintenanceSeal([]byte(raw)); e != nil {
		t.Fatal(e)
	}
	if autoVerifyMaintenanceSeal([]byte(strings.Replace(raw, `"zero":0`, `"zero":1`, 1))) == nil {
		t.Fatal("tamper accepted")
	}
	if autoVerifyMaintenanceSeal([]byte(strings.Replace(raw, `"zero":0`, `"zero":0,"zero":0`, 1))) == nil {
		t.Fatal("duplicate fields accepted")
	}
}
func TestMaintenanceValidationActualWrapperCompatibility(t *testing.T) {
	prefix := os.Getenv("LECTERN_MAINTENANCE_WRAPPER_FIXTURE")
	if prefix == "" {
		t.Skip("set fixture prefix for retained real systemd wrapper")
	}
	request, e := os.ReadFile(prefix + "-request.json")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(prefix + "-receipt.json")
	if e != nil {
		t.Fatal(e)
	}
	var r autoMaintenanceValidationRequest
	if e = json.Unmarshal(request, &r); e != nil {
		t.Fatal(e)
	}
	l := &autoMaintenanceValidationLease{OperationID: r.OperationID, OwnerJob: r.OwnerJob, OwnerTask: r.OwnerTask, PinSHA: r.PinSHA, Generation: r.Generation, RequestRaw: request, RequestSHA: autoSHA(request)}
	state, validation, e := autoDecodeMaintenanceValidation(raw, l)
	if e != nil || state != "validated" || validation == nil {
		t.Fatal("actual wrapper rejected", state, e)
	}
}
