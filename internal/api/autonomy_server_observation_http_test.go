package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observationHTTPFixture(t *testing.T) (*Server, *autoRecord, *autoJob, string, string) {
	t.Helper()
	s := autoTestServer(t)
	j := &autoJob{ID: "11111111-1111-4111-8111-111111111111", TaskID: 12, Role: "planner", Status: "running"}
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: &autonomy.State{Phase: autonomy.Plan, Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: j.Role}}}, Jobs: []*autoJob{j}}
	a.Config.Enabled = true
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, j.ID), 0700); e != nil {
		t.Fatal(e)
	}
	shim := t.TempDir()
	log := filepath.Join(shim, "calls")
	t.Setenv("LECTERN_OBS_ROOT", root)
	t.Setenv("LECTERN_OBS_LOG", log)
	t.Setenv("LECTERN_OBS_MODE", "")
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	script := `#!/usr/bin/python3
import os,sys,json,pathlib,hashlib
cmd=sys.argv[3]; mode=os.environ.get('LECTERN_OBS_MODE','')
with open(os.environ['LECTERN_OBS_LOG'],'a') as f:f.write(cmd+'\n')
h=lambda x:hashlib.sha256(x).hexdigest()
if cmd=='server-targets':
 if mode=='catalog_failure':sys.exit(90)
 print(json.dumps({'schema_version':1,'registry_sha256':h(b'registry'),'targets':[{'id':'local'}]}));sys.exit()
if cmd not in ('server-observe','server-observe-status'):sys.exit(91)
job=sys.argv[sys.argv.index('--job')+1];id=sys.argv[sys.argv.index('--observation-id')+1]
p=pathlib.Path(os.environ['LECTERN_OBS_ROOT'])/job/'server-observations'/id/'request.json'
# A launch is impossible without the exact controller request already durable.
raw=p.read_bytes();r=json.loads(raw)
if mode=='lost':sys.exit(92)
r.update(state='observed',request_sha256=h(raw),mutation_performed=False,helper_sha256=h(b'helper'),facts_sha256=h(b'facts'),configuration_sha256=h(b'configuration'),receipt_sha256=h(b'receipt'),captured_at='2026-01-01T00:00:00Z',completed_at='2026-01-01T00:00:01Z',facts={},configuration_complete=False)
if mode=='foreign':r['owner_task']=999
if mode=='mutation':r['mutation_performed']=True
if mode=='missing':del r['mutation_performed']
if mode=='no_timestamp':del r['captured_at']
if mode=='backwards_time':r['completed_at']='2025-01-01T00:00:00Z'
if mode=='no_facts':del r['facts']
if mode=='unknown_completeness':del r['configuration_complete']
print(json.dumps(r))
`
	if e := os.WriteFile(filepath.Join(shim, "sudo"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return s, a, j, root, log
}
func observationHTTP(s *Server, root, job, method, url, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.autoServerObservationBridgeAt(root, job, w, httptest.NewRequest(method, url, strings.NewReader(body)))
	return w
}
func TestServerObservationHTTPDurableRetryAndStatusOnly(t *testing.T) {
	s, _, j, root, log := observationHTTPFixture(t)
	t.Setenv("LECTERN_OBS_MODE", "lost")
	w := observationHTTP(s, root, j.ID, "POST", "/server-observations", `{"target_id":"local"}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var first map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &first); e != nil {
		t.Fatal(e)
	}
	id := first["request_id"].(string)
	raw, e := os.ReadFile(filepath.Join(root, j.ID, "server-observations", id, "request.json"))
	if e != nil {
		t.Fatal(e)
	}
	var req autoServerObservationRequest
	if e = json.Unmarshal(raw, &req); e != nil {
		t.Fatal(e)
	}
	// Replaying exact controller bytes must be idempotent independently of minute boundaries.
	same, e := autoWriteServerObservation(root, req)
	if e != nil || string(same) != string(raw) {
		t.Fatal("durable request changed", e)
	}
	t.Setenv("LECTERN_OBS_MODE", "")
	w = observationHTTP(s, root, j.ID, "GET", "/server-observations?id="+id, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "server-observe\n") != 1 || strings.Count(string(calls), "server-observe-status\n") != 1 {
		t.Fatal("status unexpectedly launched", string(calls))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cached host observation")
	}
}
func TestServerObservationHTTPForeignAndMissingBindingRejected(t *testing.T) {
	s, _, j, root, _ := observationHTTPFixture(t)
	for _, mode := range []string{"foreign", "mutation", "missing", "no_timestamp", "backwards_time", "no_facts", "unknown_completeness"} {
		t.Setenv("LECTERN_OBS_MODE", mode)
		w := observationHTTP(s, root, j.ID, "POST", "/server-observations", `{"target_id":"local"}`)
		if w.Code != 502 {
			t.Fatalf("%s: %d %s", mode, w.Code, w.Body.String())
		}
	}
}
func TestServerObservationHTTPOffAndStaleOwnerCannotLaunch(t *testing.T) {
	s, a, j, root, log := observationHTTPFixture(t)
	for _, mode := range []string{"off", "stale", "stopped", "completed"} {
		a.Config.Enabled = true
		j.Status = "running"
		a.State.Assignments = []autonomy.Assignment{{TaskID: j.TaskID, Role: j.Role}}
		switch mode {
		case "off":
			a.Config.Enabled = false
		case "stale":
			a.State.Assignments = nil
		case "stopped":
			j.Status = "stopped"
		case "completed":
			a.State.Assignments[0].Completed = true
		}
		if e := s.saveAuto(a); e != nil {
			t.Fatal(e)
		}
		w := observationHTTP(s, root, j.ID, "POST", "/server-observations", `{"target_id":"local"}`)
		if w.Code != 409 {
			t.Fatal(mode, w.Code, w.Body.String())
		}
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "server-observe\n") {
		t.Fatal("unauthorized launch", string(calls))
	}
}
func TestServerObservationHTTPSelectorsAndCatalogFailures(t *testing.T) {
	s, _, j, root, log := observationHTTPFixture(t)
	cases := [][3]string{{"POST", "/server-observations", `{"target_id":"../local"}`}, {"POST", "/server-observations", `{"target_id":"local","command":"id"}`}, {"POST", "/server-observations?target_id=local", `{"target_id":"local"}`}, {"GET", "/server-observations?id=../foo", ""}, {"GET", "/server-observations?id=" + strings.Repeat("a", 64) + "&id=" + strings.Repeat("b", 64), ""}, {"GET", "/server-targets?path=/etc/passwd", ""}, {"POST", "/server-observations", `{"target_id":"local","target_id":"local"}`}, {"POST", "/server-observations", `{"target_id":"local"}` + strings.Repeat(" ", 5000) + `{}`}}
	for _, c := range cases {
		w := observationHTTP(s, root, j.ID, c[0], c[1], c[2])
		if w.Code != 400 {
			t.Errorf("accepted malformed input %s body length %d: %d", c[1], len(c[2]), w.Code)
		}
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "server-observe\n") {
		t.Error("malformed input reached launch")
	}
	t.Setenv("LECTERN_OBS_MODE", "catalog_failure")
	for _, m := range []string{"GET", "POST"} {
		url := "/server-targets"
		body := ""
		if m == "POST" {
			url = "/server-observations"
			body = `{"target_id":"local"}`
		}
		w := observationHTTP(s, root, j.ID, m, url, body)
		if w.Code != 503 {
			t.Fatal("catalog outage hidden", w.Code, w.Body.String())
		}
	}
}
func TestServerObservationHTTPRetainedReadWhileOffAndForeignID(t *testing.T) {
	s, a, j, root, _ := observationHTTPFixture(t)
	req, e := autoNewServerObservation(j, "local", autoSHA([]byte("registry")), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = autoWriteServerObservation(root, req); e != nil {
		t.Fatal(e)
	}
	a.Config.Enabled = false
	j.Status = "done"
	if e = s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	w := observationHTTP(s, root, j.ID, "GET", "/server-observations?id="+req.RequestID, "")
	if w.Code != 200 {
		t.Fatal("lost retained observation on OFF", w.Code, w.Body.String())
	}
	w = observationHTTP(s, root, j.ID, "GET", "/server-observations?id="+strings.Repeat("f", 64), "")
	if w.Code != 404 {
		t.Fatal("unowned observation accessible", w.Code)
	}
}

func TestServerObservationHTTPRejectsSymlinkRequestDirectory(t *testing.T) {
	s, _, j, root, log := observationHTTPFixture(t)
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, j.ID, "server-observations")); e != nil {
		t.Fatal(e)
	}
	w := observationHTTP(s, root, j.ID, "POST", "/server-observations", `{"target_id":"local"}`)
	if w.Code != 409 {
		t.Fatal("accepted symlink directory", w.Code, w.Body.String())
	}
	entries, e := os.ReadDir(outside)
	if e != nil || len(entries) != 0 {
		t.Fatal("wrote through escape", e)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "server-observe\n") {
		t.Fatal("launched through escape")
	}
}
