package api

import (
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
