package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

type launchUsageTransport func(*http.Request) (*http.Response, error)

func (f launchUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInterruptedLaunchPaysOnlyOneCooldown(t *testing.T) {
	s, a, _, taskID := repairFixture(t)
	j := &autoJob{ID: "paid-launch", TaskID: taskID, Role: "builder", Provider: "claude", Status: "starting", RepairSourceTaskID: 201, RepairAttemptTaskID: taskID}
	j.Admission = &autoAdmission{JobID: j.ID}
	a.Jobs = append(a.Jobs, j)
	a.State.Assignments = []autonomy.Assignment{{TaskID: taskID, Role: "builder"}}
	a.State.Pause("runner start: interrupted")
	a.Config.Continuous = true
	root := t.TempDir()
	launch, status, snapshot := filepath.Join(root, "launch"), filepath.Join(root, "status"), filepath.Join(root, "snapshot")
	write := func(p, v string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(v), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(launch, `{"state":"launching"}`)
	write(status, `{"state":"failed","exit_code":1}`)
	write(snapshot, `{"state":"exporting"}`)
	t.Setenv("LECTERN_TEST_LAUNCH", launch)
	t.Setenv("LECTERN_TEST_STATUS", status)
	t.Setenv("LECTERN_TEST_SNAPSHOT", snapshot)
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	write(filepath.Join(root, "sudo"), "#!/bin/sh\ncase \"$3\" in\nlaunch-state) cat \"$LECTERN_TEST_LAUNCH\" ;;\nstatus) cat \"$LECTERN_TEST_STATUS\" ;;\nsnapshot) cat \"$LECTERN_TEST_SNAPSHOT\" ;;\nstop) echo '{}' ;;\n*) exit 99 ;;\nesac\n")
	if err := os.Chmod(filepath.Join(root, "sudo"), 0700); err != nil {
		t.Fatal(err)
	}
	s.autoBridges = map[string][]*http.Server{j.ID: {}}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	remaining := 90
	http.DefaultTransport = launchUsageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/agent-usage" {
			return nil, fmt.Errorf("unexpected request %s", r.URL)
		}
		body := fmt.Sprintf(`{"providers":[{"id":"claude","status":"ok","updated_at":%d,"buckets":[{"windows":[{"label":"weekly","used_percent":%d,"remaining_percent":%d,"resets_at":%d},{"label":"session","used_percent":%d,"remaining_percent":%d,"resets_at":%d}]}]}]}`, time.Now().Unix(), 100-remaining, remaining, time.Now().Add(time.Hour).Unix(), 100-remaining, remaining, time.Now().Add(time.Hour).Unix())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	tick := func() {
		t.Helper()
		if err := s.saveAuto(a); err != nil {
			t.Fatal(err)
		}
		s.autoChecked = time.Time{}
		s.RunAutonomyTick(context.Background())
		var err error
		a, err = s.loadAuto()
		if err != nil {
			t.Fatal(err)
		}
		j = autoFindJob(a, taskID)
	}
	// The first tick schedules a delay. Restart/reload cannot pay it early.
	tick()
	if j.LaunchRetryPaid || a.RetryAt.IsZero() {
		t.Fatal("credit granted before deadline")
	}
	tick()
	if j.LaunchRetryPaid {
		t.Fatal("restart skipped cooldown")
	}
	a.RetryAt = time.Now().Add(-time.Second)
	tick()
	if !j.LaunchRetryPaid || a.RetryCount != 1 || j.Status != "starting" {
		t.Fatal("paid retry lost", j, a.Reason)
	}
	tick()
	if !j.LaunchRetryPaid {
		t.Fatal("live launcher consumed credit")
	}
	write(launch, `{"state":"consumed","worker_state":"failed"}`)
	tick()
	if j.Status != "running" || !j.LaunchRetryPaid {
		t.Fatal("reconciliation lost credit")
	}
	tick()
	if j.Status != "exporting" || !j.LaunchRetryPaid {
		t.Fatal("pending export consumed credit")
	}
	write(snapshot, `{"state":"error"}`)
	tick()
	if !j.LaunchRetryPaid || j.Status != "exporting" {
		t.Fatal("failed export consumed credit")
	}
	// OFF and exhausted quota still prevent the credited transition.
	a.Config.Enabled = false
	tick()
	if !j.LaunchRetryPaid || j.Status != "exporting" {
		t.Fatal("OFF consumed credit")
	}
	a.Config.Enabled = true
	remaining = 10
	tick()
	if !j.LaunchRetryPaid || j.Status != "exporting" {
		t.Fatal("quota gate consumed credit")
	}
	remaining = 90
	write(snapshot, `{"state":"ready"}`)
	audits, _ := json.Marshal(a.State.Audits)
	tick()
	after, _ := json.Marshal(a.State.Audits)
	if j.LaunchRetryPaid || j.Status != "stopped" || a.State.Phase != autonomy.Build || a.RetryCount != 1 || !a.RetryAt.IsZero() || string(audits) != string(after) || j.RepairSourceTaskID != 201 || j.RepairAttemptTaskID != taskID || j.Admission.JobID != j.ID {
		t.Fatal("exported launch did not recover unchanged", j, a.Reason)
	}
	// A confirmed healthy process spends no future failure's cooldown.
	j.Status = "running"
	j.LaunchRetryPaid = true
	write(status, `{"state":"running"}`)
	tick()
	if j.LaunchRetryPaid {
		t.Fatal("healthy process retained credit")
	}
	write(status, `{"state":"failed","exit_code":1}`)
	tick()
	if j.Status != "failed" || a.State.Phase != autonomy.Paused || j.LaunchRetryPaid {
		t.Fatal("subsequent failure bypassed backoff")
	}
	tick()
	if a.RetryAt.IsZero() || a.RetryCount != 1 {
		t.Fatal("subsequent failure did not schedule normal cooldown")
	}

}

func TestLaunchCreditClearedOnlyByAuthoritativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		receipt string
		keep    bool
		valid   bool
	}{
		{`{"state":"unused"}`, false, true},
		{`{"state":"running"}`, false, true},
		{`{"state":"launching"}`, true, true},
		{`{"state":"consumed","worker_state":"done"}`, false, true},
		{`{"state":"consumed","worker_state":"failed"}`, true, true},
		{`{"state":"consumed","worker_state":"stopped"}`, true, true},
		{`{"state":"unknown"}`, true, false},
	} {
		j := &autoJob{Status: "starting", LaunchRetryPaid: true}
		_, err := autoReconcileLaunch(j, []byte(tc.receipt))
		if (err == nil) != tc.valid || j.LaunchRetryPaid != tc.keep {
			t.Fatal(tc, j, err)
		}
	}
}
