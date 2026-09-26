package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDependencyProtocolRejectsArbitraryDestinations(t *testing.T) {
	for _, p := range []string{"/github.com/google/uuid/@v/v1.6.0.mod", "/github.com/!owner/module/@v/v1.0.0.zip", "/sumdb/sum.golang.org/supported", "/sumdb/sum.golang.org/lookup/github.com/google/uuid@v1.6.0", "/sumdb/sum.golang.org/tile/8/0/123"} {
		u, e := autoDependencyURL(p)
		if e != nil || (u.Host != "proxy.golang.org" && u.Host != "sum.golang.org") || u.Scheme != "https" {
			t.Fatalf("%s: %v", p, e)
		}
	}
	for _, p := range []string{"//evil.test/@v/a", "/../../etc/passwd", "/github.com/a/@v/x?token=secret", "/foo/%2e%2e/@v/x", "/sumdb/evil.test/supported", "/credentials", "/github.com/a/@v/x\\y"} {
		if _, e := autoDependencyURL(p); e == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, method := range []string{"POST", "PUT", "CONNECT", "DELETE"} {
		w := httptest.NewRecorder()
		autoDependencyFetch(w, httptest.NewRequest(method, "/github.com/a/@v/v1.0.0.mod", nil))
		if w.Code != 403 {
			t.Fatal(method, w.Code)
		}
	}
	for _, raw := range []string{"http://storage.googleapis.com/x", "https://evil.test/x", "https://user:pass@storage.googleapis.com/x", "https://storage.googleapis.com:443/x"} {
		u, _ := url.Parse(raw)
		if autoDependencyRedirect(&http.Request{URL: u}, nil) == nil {
			t.Fatal(raw)
		}
	}
	u, _ := url.Parse("https://storage.googleapis.com/proxy-golang-org-prod/x?signature=abc")
	if e := autoDependencyRedirect(&http.Request{URL: u}, nil); e != nil {
		t.Fatal(e)
	}
	if e := autoDependencyRedirect(&http.Request{URL: u}, make([]*http.Request, 3)); e == nil {
		t.Fatal("redirect loop accepted")
	}
}
func TestPrerequisiteReadinessRequiresVerifiedIdentity(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, state := range []string{"recovering", "waiting", "failed", "unavailable"} {
		ready, err := autoRecoveryReady(autoRecoveryReceipt{State: state, Capability: "go_modules", Key: key})
		if ready || err != nil {
			t.Fatal(state, ready, err)
		}
	}
	for _, state := range []string{"verified", "not_applicable"} {
		ready, err := autoRecoveryReady(autoRecoveryReceipt{State: state, Capability: "go_modules", Key: key})
		if !ready || err != nil {
			t.Fatal(state, ready, err)
		}
	}
	for _, r := range []autoRecoveryReceipt{{State: "verified", Key: key}, {State: "verified", Capability: "go_modules", Key: "bad"}, {State: "done", Capability: "go_modules", Key: key}} {
		if _, err := autoRecoveryReady(r); err == nil {
			t.Fatal(r)
		}
	}
}

// Opt-in integration: uses a disposable prepared job and real root runner,
// registry bridge and systemd/bwrap isolation. Never touches tmux or model APIs.
func TestDependencyRecoveryReal(t *testing.T) {
	runner := os.Getenv("LECTERN_RECOVERY_TEST_RUNNER")
	if runner == "" {
		t.Skip("explicit disposable runner required")
	}
	job := autoUUID()
	command := func(args ...string) []byte {
		t.Helper()
		out, e := exec.Command("sudo", append([]string{"-n", runner}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %v %s", args, e, out)
		}
		return out
	}
	command("prepare", "--job", job)
	s := &Server{}
	j := &autoJob{ID: job}
	if e := s.ensureAutoBridges(j); e != nil {
		t.Fatal(e)
	}
	defer s.closeAutoBridge(job)
	dir := filepath.Join(autoRoot, job, "work")
	mod := []byte("module example.org/recovery-" + job + "\n\ngo 1.23.0\n\nrequire github.com/google/uuid v1.6.0\n")
	if e := os.WriteFile(filepath.Join(dir, "go.mod"), mod, 0600); e != nil {
		t.Fatal(e)
	}
	// Missing go.sum is intentional: new projects must recover too.
	t.Log("disposable recovery job", job)
	var receipt autoRecoveryReceipt
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		raw := command("dependencies", "--job", job)
		if e := json.Unmarshal(raw, &receipt); e != nil {
			t.Fatal(e, string(raw))
		}
		if receipt.State == "verified" {
			break
		}
		if receipt.State == "failed" || receipt.State == "unavailable" || receipt.State == "waiting" {
			t.Fatalf("recovery failed: %s", raw)
		}
		time.Sleep(time.Second)
	}
	if receipt.State != "verified" {
		t.Fatal("did not verify", receipt)
	}
	got, e := os.ReadFile(filepath.Join(dir, "go.mod"))
	if e != nil || !bytes.Equal(got, mod) {
		t.Fatal("source inputs changed", e)
	}
	if _, e = os.Stat(filepath.Join(dir, "go.sum")); !os.IsNotExist(e) {
		t.Fatal("source sum file created", e)
	}
	again := command("dependencies", "--job", job)
	if !bytes.Contains(again, []byte(`"state": "verified"`)) {
		t.Fatal(string(again))
	}
	// Consume the installed cache inside a fresh network namespace: neither
	// host caches nor network access can hide an incomplete bundle.
	code := []byte("package recovery\nimport (\"testing\"; \"github.com/google/uuid\")\nfunc TestUUID(t *testing.T){ if uuid.New()==uuid.Nil {t.Fatal(\"nil UUID\")} }\n")
	if e = os.WriteFile(filepath.Join(dir, "recovery_test.go"), code, 0644); e != nil {
		t.Fatal(e)
	}
	bundle := filepath.Join(filepath.Dir(autoRoot), "dependencies", "go", receipt.Key, "mod")
	args := []string{"--unshare-all", "--die-with-parent", "--clearenv", "--tmpfs", "/", "--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin", "--ro-bind", "/lib", "/lib", "--ro-bind", "/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--bind", dir, "/work", "--ro-bind", bundle, "/modules", "--chdir", "/work", "--setenv", "HOME", "/tmp", "--setenv", "PATH", "/usr/local/go/bin:/usr/bin:/bin", "--setenv", "GOMODCACHE", "/modules", "--setenv", "GOCACHE", "/tmp/build", "--setenv", "GOPATH", "/tmp/go", "--setenv", "GOTOOLCHAIN", "local", "--setenv", "GOPROXY", "off", "--setenv", "GOSUMDB", "off", "--setenv", "GOENV", "off", "--setenv", "GOWORK", "off", "--", "/bin/sh", "-c", "go mod download all && go test ./..."}
	out, e := exec.Command("/usr/bin/bwrap", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("offline bundle consumption: %v %s", e, out)
	}
	t.Log(string(out))
	t.Log("PASS exact-input dependency repaired without model calls or host cache; source preserved; repeated preflight verified", receipt.Key)
}

func TestRecoveryDoesNotRepeatPreflightAfterWorkerStartIntent(t *testing.T) {
	// No DB/runner exists: an accidental preflight here fails loudly. A crash
	// after successful worker start must go straight to idempotent runner start.
	s := &Server{}
	for _, state := range []string{"starting", "running"} {
		ready, err := s.recoverAutoPrerequisites(context.Background(), &autoRecord{}, &autoJob{Role: "builder", Status: state})
		if err != nil || !ready {
			t.Fatal(state, ready, err)
		}
	}
}

func TestRecoveryEvidenceIsBoundToWorkerSocket(t *testing.T) {
	s, a, _, _ := repairFixture(t)
	a.Jobs = append(a.Jobs, &autoJob{ID: "recovery-worker", TaskID: 101, Recovery: &autoRecoveryReceipt{State: "unavailable", Capability: "go_modules", Key: strings.Repeat("a", 64), Reason: "missing toolchain"}})
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	s.autoJobReadBridge("recovery-worker")(w, httptest.NewRequest("GET", "/prerequisite?job_id=other", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "missing toolchain") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.autoJobReadBridge("other")(w, httptest.NewRequest("GET", "/prerequisite?job_id=recovery-worker", nil))
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestDependencyRecoveryCancellationReal(t *testing.T) {
	runner := os.Getenv("LECTERN_RECOVERY_TEST_RUNNER")
	if runner == "" {
		t.Skip("explicit disposable runner required")
	}
	job := autoUUID()
	command := func(args ...string) []byte {
		t.Helper()
		out, e := exec.Command("sudo", append([]string{"-n", runner}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %v %s", args, e, out)
		}
		return out
	}
	command("prepare", "--job", job)
	dir := filepath.Join(autoRoot, job)
	if e := os.WriteFile(filepath.Join(dir, "work/go.mod"), []byte("module example.org/cancel-"+job+"\n\ngo 1.23.0\nrequire github.com/google/uuid v1.6.0\n"), 0600); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("unix", filepath.Join(dir, "dependency.sock"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Join(dir, "dependency.sock"), 0666); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{}, 10)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() })}
	go srv.Serve(listener)
	defer srv.Close()
	var receipt autoRecoveryReceipt
	raw := command("dependencies", "--job", job)
	if e = json.Unmarshal(raw, &receipt); e != nil {
		t.Fatal(e)
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("downloader never reached controlled bridge")
	}
	command("dependencies-stop", "--job", job)
	out, _ := exec.Command("systemctl", "is-active", "lectern-dependencies-"+receipt.Key+".service").CombinedOutput()
	if strings.TrimSpace(string(out)) == "active" {
		t.Fatal("recovery outlived cancellation")
	}
	// A cancelled attempt is retained as failed, never promoted to verified.
	raw = command("dependencies", "--job", job)
	if bytes.Contains(raw, []byte(`"state": "verified"`)) {
		t.Fatal("cancelled download promoted", string(raw))
	}
	t.Log("PASS cancellation stopped only the dependency cgroup; no verified bundle", job, receipt.Key)
}

func TestDeferredReviewerResumesSameAuditedLineageAfterRecovery(t *testing.T) {
	_, a, _, _ := repairFixture(t)
	original := a.State
	original.Phase = autonomy.Review
	original.Cycle = 40
	original.Assignments = []autonomy.Assignment{{TaskID: 100, Role: "builder", Completed: true}, {TaskID: 101, Role: "reviewer", ReportVersion: 2}}
	job := &autoJob{TaskID: 101, Status: "prepared", Role: "reviewer", Recovery: &autoRecoveryReceipt{State: "unavailable", Capability: "go_modules", Key: strings.Repeat("a", 64), Reason: "network outage"}}
	a.Jobs = append(a.Jobs, job)
	if !autoDeferPrerequisite(a, job, time.Now()) {
		t.Fatal("not deferred")
	}
	if original.Phase != autonomy.Review || original.Assignments[1].Completed || a.State.Cycle <= 40 || job.Status != "deferred" {
		t.Fatal("lost pending review lineage")
	}
	if autoResumeRecovered(a) {
		t.Fatal("unverified prerequisite resumed")
	}
	a.State.Phase = autonomy.Complete
	a.State.Cycle = 42
	job.Recovery.State = "verified"
	if !autoResumeRecovered(a) || a.State != original || job.Status != "prepared" {
		t.Fatal("same admitted reviewer not resumed")
	}
	if original.Assignments[1].TaskID != 101 || len(a.DeferredRuns) != 0 {
		t.Fatal("new review reservation invented")
	}
	original.Phase = autonomy.Complete
	autoNewCycle(a, time.Now())
	if a.State.Cycle <= 42 {
		t.Fatal("resuming old cycle reused a newer cycle number", a.State.Cycle)
	}
}

// Runs the actual controller persistence/deferral/poll/resumption path against
// the installed runner. Only this disposable capability's timer timestamps are
// advanced; real failed downloads, verification and unchanged source are checked.
func TestDependencyRecoveryDeferredControllerReal(t *testing.T) {
	if os.Getenv("LECTERN_RECOVERY_TEST_RUNNER") != autoRunner {
		t.Skip("requires explicitly selected installed runner")
	}
	s, a, _, _ := repairFixture(t)
	jobID := autoUUID()
	command := func(args ...string) []byte {
		t.Helper()
		out, e := exec.Command("sudo", append([]string{"-n", autoRunner}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %v %s", args, e, out)
		}
		return out
	}
	command("prepare", "--job", jobID)
	dir := filepath.Join(autoRoot, jobID)
	mod := []byte("module example.org/controller-recovery-" + jobID + "\n\ngo 1.23.0\nrequire github.com/google/uuid v1.6.0\n")
	if e := os.WriteFile(filepath.Join(dir, "work/go.mod"), mod, 0600); e != nil {
		t.Fatal(e)
	}
	j := &autoJob{ID: jobID, TaskID: 1001, Role: "reviewer", Status: "prepared"}
	a.Jobs = append(a.Jobs, j)
	a.State.Phase = autonomy.Review
	a.State.Cycle = 101
	a.State.Assignments = []autonomy.Assignment{{TaskID: 1000, Role: "builder", Completed: true}, {TaskID: j.TaskID, Role: "reviewer", ReportVersion: 2}}
	originalAudits := store.J(a.State.Audits)
	listener, e := net.Listen("unix", filepath.Join(dir, "dependency.sock"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Join(dir, "dependency.sock"), 0666); e != nil {
		t.Fatal(e)
	}
	outage := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "controlled registry outage", 503) })}
	s.autoBridges = map[string][]*http.Server{jobID: {outage}}
	go outage.Serve(listener)
	defer s.closeAutoBridge(jobID)
	advance := func(cooldown bool) {
		t.Helper()
		// Trusted test fixture paths only; never touch production receipts or state.
		path := filepath.Join(filepath.Dir(autoRoot), "dependencies", "recovery-"+j.Recovery.Key, "receipt.json")
		code := "import json,pathlib,sys;p=pathlib.Path(sys.argv[1]);d=json.loads(p.read_text());d['retry_at']=0;d['started_at']-=21601 if sys.argv[2]=='cooldown' else 0;p.write_text(json.dumps(d))"
		mode := "retry"
		if cooldown {
			mode = "cooldown"
		}
		if out, e := exec.Command("sudo", "-n", "python3", "-c", code, path, mode).CombinedOutput(); e != nil {
			t.Fatal(e, string(out))
		}
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) && j.Status != "deferred" {
		ready, err := s.recoverAutoPrerequisites(context.Background(), a, j)
		if err != nil || ready {
			t.Fatal("failed prerequisite incorrectly admitted", ready, err)
		}
		if j.Recovery.State == "waiting" || j.Recovery.State == "failed" {
			advance(false)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if j.Status != "deferred" || j.Recovery.Attempts != 3 || len(a.DeferredRuns) != 1 {
		t.Fatalf("not durably deferred after actual failures: %+v", j)
	}
	a, e = s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	j = autoFindJob(a, 1001)
	if len(a.DeferredRuns) != 1 || a.DeferredRuns[0].Phase != autonomy.Review || a.DeferredRuns[0].Assignments[1].Completed {
		t.Fatal("restart lost unfinished review")
	}
	if autoResumeRecovered(a) {
		t.Fatal("failed prerequisite resumed after restart")
	}
	advance(true)
	// Deferral closed the outage socket. Normal polling recreates the real
	// read-only broker and actually downloads/verifies the missing module.
	deadline = time.Now().Add(time.Minute)
	for time.Now().Before(deadline) && j.Recovery.State != "verified" {
		j.RecoveryCheckAt = time.Time{}
		s.pollDeferredPrerequisites(context.Background(), a, time.Now())
		if e = s.saveAuto(a); e != nil {
			t.Fatal(e)
		}
		if j.Recovery.State == "failed" || j.Recovery.State == "unavailable" {
			t.Fatal("recovered registry still failed", j.Recovery)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if j.Recovery.State != "verified" {
		t.Fatal("controller did not verify recovered prerequisite", j.Recovery)
	}
	a, e = s.loadAuto()
	if e != nil {
		t.Fatal(e)
	}
	a.State.Phase = autonomy.Complete
	if !autoResumeRecovered(a) {
		t.Fatal("verified deferred assignment not resumed")
	}
	if a.State.Cycle != 101 || a.State.Phase != autonomy.Review || store.J(a.State.Audits) != originalAudits || a.State.Assignments[1].TaskID != 1001 || a.State.Assignments[1].Completed {
		t.Fatal("resumption replaced original review or audits")
	}
	j = autoFindJob(a, 1001)
	if j.Status != "prepared" || j.Approved || j.Rejected {
		t.Fatal("preflight fabricated a review verdict or failed to prepare the retained job")
	}
	got, e := os.ReadFile(filepath.Join(dir, "work/go.mod"))
	if e != nil || !bytes.Equal(got, mod) {
		t.Fatal("recovery modified source inputs")
	}
	t.Log("PASS three real failures -> persisted deferred reviewer -> reload -> timed probe -> real verified bundle -> same audited review resumes; no model calls", jobID)
}
