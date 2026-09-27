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

func TestExpertProbeHTTPSubmissionObservationAndCorrection(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "prior fixture")
	a.State.Phase = autonomy.Audit
	a.State.Assignments[0].Completed = false
	owner := autoFindJob(a, a.State.Assignments[0].TaskID)
	owner.Status, owner.Provider = "running", "codex"
	peer := autoFindJob(a, a.State.Assignments[1].TaskID)
	var receipt autoExpertProbeReceipt
	for _, lease := range a.ExpertRecovery.Probes {
		if lease.OwnerJob == owner.ID {
			receipt = *lease.Receipt
		}
	}
	a.ExpertRecovery.Probes = map[string]*autoExpertProbeLease{}
	expertQuota(a, time.Now())
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, owner.ID), 0700); err != nil {
		t.Fatal(err)
	}
	input := autoExpertProbeInput{ProgressKey: p.ExpertProgressKey, Profile: "ordinary180", Script: "print('independent baseline')"}
	_, exact, err := autoExpertProbeRequestBytes(a, owner, &input, "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := autoReserveExpertProbe(a, p, owner.Role, autoSHA(exact), []string{"codex"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// This local prediction is not saved: POST must persist its own reservation.
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"probe_id": lease.ID, "state": "preparing"})
	raw, _ := json.Marshal(input)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		s.autoExpertProbeBridgeAt(root, owner.ID, w, httptest.NewRequest("POST", "/expert-probes", strings.NewReader(string(raw))))
		if w.Code != 202 {
			t.Fatalf("submission %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ExpertRecovery.Probes) != 1 {
		t.Fatal("retry allocated another probe")
	}
	written, err := os.ReadFile(filepath.Join(root, owner.ID, "expert-probe-requests", lease.ID+".json"))
	if err != nil || string(written) != string(exact) {
		t.Fatal("request bytes changed", err)
	}
	receipt.ProbeID, receipt.RequestKey = lease.ID, lease.RequestKey
	receipt.StartedAt, receipt.EndedAt = time.Now(), time.Now().Add(time.Second)
	receipt.RequestKey = strings.Repeat("f", 64)
	docResponse(t, response, receipt)
	w := httptest.NewRecorder()
	s.autoExpertProbeBridgeAt(root, owner.ID, w, httptest.NewRequest("GET", "/expert-probes?id="+lease.ID, nil))
	if w.Code != 502 {
		t.Fatalf("wrong request receipt accepted: %d %s", w.Code, w.Body.String())
	}
	loaded, _ = s.loadAuto()
	if loaded.ExpertRecovery.Probes[lease.ID].Receipt != nil {
		t.Fatal("mismatched receipt persisted")
	}
	receipt.RequestKey = lease.RequestKey
	docResponse(t, response, receipt)
	w = httptest.NewRecorder()
	s.autoExpertProbeBridgeAt(root, peer.ID, w, httptest.NewRequest("GET", "/expert-probes?id="+lease.ID, nil))
	if w.Code != 404 {
		t.Fatal("peer read probe", w.Code)
	}
	// A corrected report runs in a fresh UUID but owns the same assignment.
	old := autoFindJob(loaded, owner.TaskID)
	old.Status = "done"
	corrected := *old
	corrected.ID, corrected.Status = autoUUID(), "running"
	loaded.Jobs = append(loaded.Jobs, &corrected)
	if err := s.saveAuto(loaded); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.autoExpertProbeBridgeAt(root, corrected.ID, w, httptest.NewRequest("GET", "/expert-probes", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), lease.ID) {
		t.Fatal("correction cannot discover evidence", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.autoExpertProbeBridgeAt(root, corrected.ID, w, httptest.NewRequest("GET", "/expert-probes?id="+lease.ID, nil))
	if w.Code != 200 {
		t.Fatalf("correction cannot record receipt: %d %s", w.Code, w.Body.String())
	}
	loaded, _ = s.loadAuto()
	if loaded.ExpertRecovery.Probes[lease.ID].Receipt == nil {
		t.Fatal("terminal receipt lost")
	}
}

func TestExpertProbeRequestPublication(t *testing.T) {
	root := t.TempDir()
	job := autoUUID()
	if err := os.Mkdir(filepath.Join(root, job), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":1}`)
	lease := &autoExpertProbeLease{ID: strings.Repeat("a", 64), OwnerJob: job, RequestKey: autoSHA(raw)}
	for i := 0; i < 2; i++ {
		if err := autoWriteExpertProbeRequest(root, lease, raw); err != nil {
			t.Fatal(err)
		}
	}
	name := filepath.Join(root, job, "expert-probe-requests", lease.ID+".json")
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe published mode: %v %v", info, err)
	}
	changed := []byte(`{"schema_version":2}`)
	lease.RequestKey = autoSHA(changed)
	if err := autoWriteExpertProbeRequest(root, lease, changed); err == nil {
		t.Fatal("replaced immutable input")
	}
	got, err := os.ReadFile(name)
	if err != nil || string(got) != string(raw) {
		t.Fatal("original request lost")
	}
	lease.RequestKey = autoSHA(raw)
	for _, owner := range []string{"../escape", "/tmp/escape", "", "a/b"} {
		lease.OwnerJob = owner
		if err := autoWriteExpertProbeRequest(root, lease, raw); err == nil {
			t.Fatal("unsafe owner accepted", owner)
		}
	}
	lease.OwnerJob = job
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, name); err != nil {
		t.Fatal(err)
	}
	if err := autoWriteExpertProbeRequest(root, lease, raw); err == nil {
		t.Fatal("symlink request accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(name))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary request leaked: %v %v", entries, err)
	}
}

func TestExpertProbeDiscoveryRetainsCorrectionEvidence(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "independent causal test")
	a.State.Phase = autonomy.Audit
	a.State.Assignments[0].Completed = false
	old := autoFindJob(a, a.State.Assignments[0].TaskID)
	corrected := *old
	corrected.ID, corrected.Status = autoUUID(), "running"
	a.Jobs = append(a.Jobs, &corrected)
	rows := autoExpertProbeDiscovery(a, corrected.ID, "")["items"].([]map[string]any)
	if len(rows) != 1 || rows[0]["owner_job"] != old.ID || rows[0]["receipt_recorded"] != true {
		t.Fatalf("correction lost prior evidence or exposed peer: %v", rows)
	}
	old.Status = "running"
	if rows := autoExpertProbeDiscovery(a, corrected.ID, "")["items"].([]map[string]any); len(rows) != 0 {
		t.Fatal("live other process evidence inherited")
	}
	old.Status = "done"
	a.State.Revision++
	if rows := autoExpertProbeDiscovery(a, corrected.ID, "")["items"].([]map[string]any); len(rows) != 0 {
		t.Fatal("stale revision evidence inherited")
	}
}

func TestExpertProbeDiscoveryRejectsPeerEvidence(t *testing.T) {
	a := &autoRecord{}
	ledger := autoExpertLedger(a)
	mine, peer := strings.Repeat("a", 64), strings.Repeat("b", 64)
	ledger.Probes[mine] = &autoExpertProbeLease{ID: mine, OwnerJob: "mine"}
	ledger.Probes[peer] = &autoExpertProbeLease{ID: peer, OwnerJob: "peer"}
	got := autoExpertProbeDiscovery(a, "mine", "")
	items := got["items"].([]map[string]any)
	if len(items) != 1 || items[0]["probe_id"] != mine {
		t.Fatalf("peer data leaked: %v", got)
	}
	if next := autoExpertProbeDiscovery(a, "mine", mine); len(next["items"].([]map[string]any)) != 0 {
		t.Fatal("cursor replayed entry")
	}
}
