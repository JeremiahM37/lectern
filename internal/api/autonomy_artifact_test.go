package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestArtifactStatusReadDoesNotWaitForController(t *testing.T) {
	s := autoTestServer(t)
	a := &autoRecord{Config: autonomy.DefaultConfig(), Status: "preserving_artifacts", Jobs: []*autoJob{{ID: "export-fixture", Status: "exporting"}}}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	s.autoMu.Lock()
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		s.getAutonomy(w, httptest.NewRequest("GET", "/api/autonomy", nil))
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("status %d", code)
		}
	case <-time.After(time.Second):
		t.Error("status blocked on exporter/controller")
	}
	s.autoMu.Unlock()
}

func TestArtifactOffPreservesPendingOriginalAssignment(t *testing.T) {
	s := autoTestServer(t)
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: &autonomy.State{Phase: autonomy.Build}, Jobs: []*autoJob{{ID: "must-not-invoke-runner", Status: "exporting", TaskID: 190}}}
	a.Config.Enabled = false
	s.stopAutoJobs(context.Background(), a, "Turned off by you")
	if a.Status != "off" || a.Jobs[0].Status != "exporting" || a.State.Phase != autonomy.Paused {
		t.Fatalf("lost pending export: %+v", a)
	}
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	restored, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Config.Enabled || restored.Jobs[0].Status != "exporting" {
		t.Fatal("restart lost OFF or original pending work")
	}
}

// Explicit read-only live migration probe: validates the real paused checkpoint
// and report in a disposable controller. It never writes the production DB.
func TestArtifactLegacyTimeoutLive(t *testing.T) {
	if os.Getenv("LECTERN_ARTIFACT_LIVE_PROBE") != "1" {
		t.Skip("opt-in read-only live probe")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:9110/api/autonomy")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var a autoRecord
	if err = json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	ids := a.State.ActiveTaskIDs()
	if len(ids) != 1 {
		t.Fatalf("expected one retained assignment, got %v", ids)
	}
	original := autoFindJob(&a, ids[0])
	if original == nil {
		t.Fatal("missing job")
	}
	uuid, repairs := original.ID, original.ReportRepairs
	s := autoTestServer(t)
	if !s.recoverLegacyArtifactTimeout(context.Background(), &a, original.Provider, time.Now()) {
		t.Fatalf("checkpoint not recoverable: %s / %s", a.State.Reason, original.Status)
	}
	if original.ID != uuid || original.Status != "exporting" || original.ReportRepairs != repairs || a.State.Phase == autonomy.Paused {
		t.Fatal("recovery changed identity, repair count, or lost phase")
	}
	if len(a.State.ActiveTaskIDs()) != 1 || a.State.ActiveTaskIDs()[0] != ids[0] {
		t.Fatal("assignment was applied or replaced before preservation")
	}
	t.Logf("validated original task %d job %s for preservation-only recovery", ids[0], uuid)
}
