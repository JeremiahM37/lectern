package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Explicit opt-in integration fixture. The selected directory must contain a
// root-owned disposable runner and frozen source archive prepared by the probe
// systemd proof. It never uses the production runner or workshop database.
func TestExpertProbeRealHTTPReceipt(t *testing.T) {
	fixture := os.Getenv("LECTERN_EXPERT_REAL_FIXTURE")
	if fixture == "" {
		t.Skip("requires disposable root-owned systemd fixture")
	}
	if os.Geteuid() != 0 {
		t.Fatal("real fixture requires root")
	}
	adapter := filepath.Join(fixture, "runner")
	info, err := os.Lstat(adapter)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		t.Fatal("unsafe fixture runner", err)
	}
	root := filepath.Join(fixture, "jobs")
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "fixture")
	a.ExpertRecovery.Probes = map[string]*autoExpertProbeLease{}
	a.State.Phase = autonomy.Audit
	a.State.Assignments[0].Completed = false
	owner := autoFindJob(a, a.State.Assignments[0].TaskID)
	owner.ID, owner.Status, owner.Provider = autoUUID(), "running", "codex"
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		archive, err := os.ReadFile(filepath.Join(root, entry.Name(), "artifact.tar.gz"))
		if err == nil {
			pin.SourceJob, pin.SourceSHA = entry.Name(), autoSHA(archive)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture source missing")
	}
	delete(a.ExpertRecovery.Pins, p.ExpertProgressKey)
	autoFindJob(a, pin.SourceTaskID).ID = pin.SourceJob
	pin.Key = autoExpertPinKey(pin)
	p.ExpertProgressKey = pin.Key
	a.ExpertRecovery.Pins[pin.Key] = pin
	a.State.Items = []autonomy.Proposal{p}
	expertQuota(a, time.Now())
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	ownerDir := filepath.Join(root, owner.ID)
	if err := os.Mkdir(ownerDir, 0700); err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(ownerDir, "heartbeat")
	if err := os.WriteFile(heartbeat, nil, 0600); err != nil {
		t.Fatal(err)
	}
	unit := "lectern-autonomy-" + owner.ID + ".service"
	if out, err := exec.Command("/usr/bin/systemd-run", "--quiet", "--collect", "--unit="+unit, "--property=RuntimeMaxSec=90", "/usr/bin/sleep", "90").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	t.Cleanup(func() { _ = exec.Command("/usr/bin/systemctl", "stop", unit).Run() })
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "sudo"), []byte("#!/bin/sh\nshift 2\nexec \"$LECTERN_EXPERT_TEST_RUNNER\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LECTERN_EXPERT_TEST_RUNNER", adapter)
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	input := autoExpertProbeInput{ProgressKey: p.ExpertProgressKey, Profile: "ordinary180", Script: "import value,os\nassert value.VALUE==7\nassert os.getuid()==65534\nprint('frozen source verified',value.VALUE)\n"}
	raw, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	s.autoExpertProbeBridgeAt(root, owner.ID, w, httptest.NewRequest("POST", "/expert-probes", strings.NewReader(string(raw))))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var submitted struct {
		ProbeID string `json:"probe_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil || !autoHash256(submitted.ProbeID) {
		t.Fatal("missing probe identity", w.Body.String())
	}
	t.Cleanup(func() {
		_ = exec.Command("/usr/bin/systemctl", "stop", "lectern-expert-probe-"+owner.ID+"-"+submitted.ProbeID+".service").Run()
	})
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		_ = os.Chtimes(heartbeat, time.Now(), time.Now())
		w = httptest.NewRecorder()
		s.autoExpertProbeBridgeAt(root, owner.ID, w, httptest.NewRequest("GET", "/expert-probes?id="+submitted.ProbeID, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		loaded, err := s.loadAuto()
		if err != nil {
			t.Fatal(err)
		}
		lease := loaded.ExpertRecovery.Probes[submitted.ProbeID]
		if lease.Diagnostic != nil {
			t.Fatalf("probe failed: %+v", lease.Diagnostic)
		}
		if lease.Receipt != nil {
			if lease.Receipt.ExitCode == nil || *lease.Receipt.ExitCode != 0 || lease.Receipt.SourceSHA != pin.SourceSHA {
				t.Fatal("incorrect execution receipt")
			}
			t.Logf("real HTTP probe receipt persisted: owner=%s probe=%s source=%s", owner.ID, lease.ID, pin.SourceSHA)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("probe observation deadline", w.Body.String())
}
