package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestRecentSessionsUsesEndedTimeAndMarksReleasedRecords(t *testing.T) {
	h := newHarness(t)
	project := h.seededProjectID()
	projectRow, err := h.App.DB.Project(project)
	if err != nil {
		t.Fatal(err)
	}
	target := projectRow.TargetID
	old := h.session(obj{"project_id": project, "name": "old"})
	middle := h.session(obj{"project_id": project, "name": "middle"})
	released := h.post("/api/sessions/adopt", obj{
		"target_id": target, "tmux_session": "legacy-claude", "workdir": "/mock/demo-app",
	}, 201)
	// Set explicit times so allocation IDs cannot accidentally make this pass.
	for _, item := range []struct {
		id int64
		at float64
	}{
		{old.id(), 100}, {middle.id(), 300}, {released.id(), 200},
	} {
		if err := h.App.DB.Update("sessions", item.id, map[string]any{
			"status": "dead", "ended_at": item.at, "updated_at": item.at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.App.DB.Update("sessions", released.id(), map[string]any{
		"status": "idle", "ended_at": 200, "updated_at": 200,
	}); err != nil {
		t.Fatal(err)
	}
	var rows []obj
	h.decode("GET", "/api/sessions/recent?limit=2", nil, 200, &rows)
	if len(rows) != 2 || rows[0].id() != middle.id() || rows[1].id() != released.id() {
		t.Fatalf("recent ordering: %#v", rows)
	}
	if rows[0]["released"] != false || rows[1]["released"] != true {
		t.Fatalf("released markers: %#v", rows)
	}
	if rows[0]["can_resume_recent"] != false || rows[1]["can_resume_recent"] != false {
		t.Fatalf("resume capability markers: %#v", rows)
	}
	if _, ok := rows[0]["native_recovery_cid"]; ok {
		t.Fatal("recent endpoint exposed the native recovery conversation ID")
	}
	if rows[1].str("resume_recent_url") != fmt.Sprintf("/api/sessions/%d/resume-recent", released.id()) {
		t.Fatalf("resume URL: %#v", rows[1])
	}
}

func TestResumeRecentRefusesWithoutValidatedNativeHistory(t *testing.T) {
	h := newHarness(t)
	row := h.session(obj{"project_id": h.seededProjectID(), "name": "closed"})
	if err := h.App.DB.Update("sessions", row.id(), map[string]any{
		"status": "dead", "ended_at": store.Now(), "updated_at": store.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/resume-recent", row.id()), obj{}, nil)
	if code != 409 {
		t.Fatalf("resume recent: got %d %s", code, body)
	}
	if len(h.mock().CmdLog()) == 0 {
		t.Fatal("the target should have been consulted to validate native history")
	}
}

// nativeRecentSession creates an ended local-target row with a private native
// history fixture. The saved launch configuration keeps this test independent
// of the operator's current agent settings, as a restarted service must be.
func nativeRecentSession(t *testing.T, h *harness, nativeCID, resumeID string) *store.Session {
	t.Helper()
	target, err := h.App.DB.InsertTarget(&store.Target{
		Name: "native-recent-" + nativeCID[:8], Kind: "local", Status: "online",
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	workdir, home := t.TempDir(), t.TempDir()
	writeNativeLifecycleHistory(t, "claude", home, workdir, nativeCID)
	launch := sessions.LaunchConfiguration{Version: 1, Spec: sessions.Spec{
		Name: "claude", Command: "/bin/false",
		Env:          map[string]string{"CLAUDE_CONFIG_DIR": home},
		ResumeIDArgs: []string{"--resume", "{id}"},
	}}
	raw, err := json.Marshal(launch)
	if err != nil {
		t.Fatal(err)
	}
	row, err := h.App.DB.InsertSession(&store.Session{
		TargetID: target.ID, Name: "closed native", Agent: "claude", Workdir: workdir,
		TmuxSession: "gone-native", Status: sessions.StatusDead,
		ResumeID: resumeID, NativeRecoveryCID: nativeCID,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	if err := h.App.DB.Update("sessions", row.ID, map[string]any{
		"status": sessions.StatusDead, "ended_at": now, "updated_at": now,
		"launch_config_json": string(raw),
	}); err != nil {
		t.Fatal(err)
	}
	row, err = h.App.DB.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestResumeRecentUsesSavedNativeCIDBeforeOlderResumeID(t *testing.T) {
	// This uses the reviewed isolated real-target harness: native history is
	// validated by the actual read-only Python reader, while tmux is scoped to
	// this test's private server by newHarness.
	h := newHarness(t, func(c *config.Config) {
		c.Mock = false
		c.SessionPoll = time.Hour
	})
	saved := "11111111-1111-4111-8111-111111111111"
	older := "22222222-2222-4222-8222-222222222222"
	row := nativeRecentSession(t, h, saved, older)
	var launch sessions.LaunchConfiguration
	if err := json.Unmarshal([]byte(row.LaunchConfigJSON), &launch); err != nil {
		t.Fatal(err)
	}
	home := launch.Spec.Env["CLAUDE_CONFIG_DIR"]
	writeNativeLifecycleHistory(t, "claude", home, row.Workdir, older)
	newer := "55555555-5555-4555-8555-555555555555"
	writeNativeLifecycleHistory(t, "claude", home, row.Workdir, newer)
	newerPath := filepath.Join(home, "projects", claudeProjectSlug(row.Workdir), newer+".jsonl")
	if err := os.Chtimes(newerPath, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var resumed obj
	h.decode("POST", fmt.Sprintf("/api/sessions/%d/resume-recent", row.ID), obj{}, 201, &resumed)
	if resumed.str("resume_id") != saved {
		t.Fatalf("saved native CID was not selected: got %#v (older=%s)", resumed, older)
	}
}

func TestResumeRecentRefusesAuthoritativeMissingNativeCID(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Mock = false
		c.SessionPoll = time.Hour
	})
	missing := "33333333-3333-4333-8333-333333333333"
	older := "44444444-4444-4444-8444-444444444444"
	row := nativeRecentSession(t, h, missing, older)
	var launch sessions.LaunchConfiguration
	if err := json.Unmarshal([]byte(row.LaunchConfigJSON), &launch); err != nil {
		t.Fatal(err)
	}
	home := launch.Spec.Env["CLAUDE_CONFIG_DIR"]
	// Leave the older legacy binding present so this proves the authoritative
	// missing checkpoint refuses a tempting, otherwise valid fallback.
	writeNativeLifecycleHistory(t, "claude", home, row.Workdir, older)
	missingPath := filepath.Join(home, "projects", claudeProjectSlug(row.Workdir), missing+".jsonl")
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}

	code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/resume-recent", row.ID), obj{}, nil)
	if code != 409 {
		t.Fatalf("resume recent with missing authoritative CID: got %d %s", code, body)
	}
	if !contains(string(body), "saved native conversation is unavailable") {
		t.Fatalf("unexpected authoritative-CID error: %s", body)
	}
	rows, err := h.App.DB.Sessions(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != row.ID {
		t.Fatalf("missing authoritative CID created a successor (older=%s): %#v", older, rows)
	}
}

func TestRecentTenSurviveDatabaseReopen(t *testing.T) {
	h := newHarness(t)
	project := h.seededProjectID()
	projectRow, err := h.App.DB.Project(project)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		row, insertErr := h.App.DB.InsertSession(&store.Session{
			ProjectID: &project, TargetID: projectRow.TargetID,
			Name: fmt.Sprintf("closed-%02d", i), Agent: "claude",
			Workdir: "/mock/demo-app", TmuxSession: fmt.Sprintf("closed-%02d", i),
			Status: sessions.StatusDead,
		})
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		ended := float64(i + 1)
		if updateErr := h.App.DB.Update("sessions", row.ID, map[string]any{
			"status": sessions.StatusDead, "ended_at": ended, "updated_at": ended,
		}); updateErr != nil {
			t.Fatal(updateErr)
		}
	}

	path := h.App.Cfg.DBPath
	h.App.Sched.Stop()
	h.App.Sessions.Close()
	if err := h.App.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.RecentClosedSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 {
		t.Fatalf("recent row count after reopen: got %d", len(rows))
	}
	for i, row := range rows {
		want := 12 - i
		if row.Name != fmt.Sprintf("closed-%02d", want-1) {
			t.Fatalf("recent row %d after reopen: got %q", i, row.Name)
		}
	}
}
