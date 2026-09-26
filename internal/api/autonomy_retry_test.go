package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"testing"
	"time"
)

func TestAutonomyOperationalRetryBackoffAndDailyRecovery(t *testing.T) {
	loc, _ := time.LoadLocation("America/Denver")
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, loc)
	state, _ := autonomy.NewState("2026-09-24")
	state.Pause("snapshot contains unsupported link/device pax_global_header")
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state}
	a.Config.Continuous = false
	for i, delay := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute} {
		if autoRetryReady(a, now) {
			t.Fatal("retry did not back off")
		}
		if a.RetryAt.Sub(now) != delay {
			t.Fatalf("delay %v", a.RetryAt.Sub(now))
		}
		if autoRetryReady(a, a.RetryAt.Add(-time.Second)) {
			t.Fatal("early retry")
		}
		now = a.RetryAt
		if !autoRetryReady(a, now) || a.RetryCount != i+1 {
			t.Fatal("due retry did not run")
		}
	}
	if autoRetryReady(a, now) {
		t.Fatal("unbounded retries")
	}
	if a.RetryAt.In(loc).Hour() != 8 || a.RetryAt.In(loc).Day() != 25 {
		t.Fatal("not deferred until next morning")
	}
	if !autoRetryReady(a, a.RetryAt) || a.RetryCount != 1 {
		t.Fatal("next day did not recover")
	}
}

func TestAutonomyRetryNeverReclassifiesSafetyFailures(t *testing.T) {
	for _, reason := range []string{"unsafe archive path", "Missing job receipt; manual inspection required", "State persistence failed", "Invalid runner status", "Unrecognized failure", "runner prepare: unsafe image"} {
		if autoRetryable(reason) {
			t.Fatalf("safety failure retried: %s", reason)
		}
	}
	for _, reason := range []string{"mkdir /work: permission denied", "Worker failed; artifacts retained for inspection", "Invalid worker report: no JSON", "Runner status unavailable: timeout"} {
		if !autoRetryable(reason) {
			t.Fatalf("operational failure not retried: %s", reason)
		}
	}
}

func TestStorageRecoveryOnlyMatchesCapacityInterlocks(t *testing.T) {
	for _, reason := range []string{`runner prepare: {"error":"retained job storage exceeds 50 GiB; archive before continuing"}`, `runner prepare: {"error":"retained job storage exceeds 200 GiB; archive before continuing"}`, `runner prepare: less than 20 GiB backing-volume free space`} {
		if !autoStoragePause(reason) {
			t.Fatal(reason)
		}
	}
	for _, reason := range []string{"Invalid worker report:", "runner prepare: unsafe image", "Budget pause: quota", "Stopped by you"} {
		if autoStoragePause(reason) {
			t.Fatal(reason)
		}
	}
}

func TestOperationalRetryBudgetIsBoundedPerCycle(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	state, _ := autonomy.NewState("2026-09-25")
	state.Cycle = 62
	state.Pause("Worker failed; artifacts retained for inspection")
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state, RetryDay: "2026-09-25", RetryCount: 4, RetryAt: now.Add(24 * time.Hour)}
	if autoRetryReady(a, now) || a.RetryCount != 0 || !a.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatal("legacy exhausted budget did not receive bounded recovery")
	}
	a.RetryCount = 3
	a.RetryAt = time.Time{}
	if autoRetryReady(a, now) || a.RetryAt.Sub(now) != 15*time.Minute {
		t.Fatal("same cycle bypassed cap")
	}
	a.State.Cycle++
	if autoRetryReady(a, now) || a.RetryCount != 0 || !a.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatal("older cycle exhausted new cycle recovery")
	}
	a.RetryScope = "older"
	a.State.Reason = "Missing job receipt; manual inspection required"
	if autoRetryReady(a, now) || a.RetryScope != "older" {
		t.Fatal("unsafe state migrated")
	}
}

func TestContinuousOutageRecoveryPersistsWithoutOvernightPause(t *testing.T) {
	now := time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	state, _ := autonomy.NewState("2026-09-25")
	state.Cycle = 62
	state.Pause("Worker failed; artifacts retained for inspection")
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state,
		RetryScope: "2026-09-25/62", RetryDay: "2026-09-25", RetryCount: 3, RetryAt: now.Add(12 * time.Hour)}
	if autoRetryReady(a, now) || !a.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatal("legacy overnight delay was not shortened")
	}
	for i := 0; i < 6; i++ {
		deadline := a.RetryAt
		raw, _ := json.Marshal(a)
		var restored autoRecord
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		a = &restored
		if autoRetryReady(a, deadline.Add(-time.Second)) || !a.RetryAt.Equal(deadline) {
			t.Fatal("restart changed backoff or retried early")
		}
		if !autoRetryReady(a, deadline) {
			t.Fatal("continuous retry stopped")
		}
		if autoRetryReady(a, deadline) || !a.RetryAt.Equal(deadline.Add(15*time.Minute)) {
			t.Fatal("retry storm or overnight deferral")
		}
	}
	a.State.Reason = "Missing job receipt; manual inspection required"
	if autoRetryReady(a, a.RetryAt.Add(time.Hour)) {
		t.Fatal("unsafe state retried")
	}
}
