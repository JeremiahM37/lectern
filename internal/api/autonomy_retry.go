package api

import (
	"fmt"
	"strings"
	"time"
)

// Only operational failures are retried. Corrupt receipts, unsafe snapshots and
// unknown pause reasons remain fail-closed. No retry changes config.Enabled or
// skips the quota gate; the caller checks both before reaching here.
func autoRetryable(reason string) bool {
	if strings.HasPrefix(reason, "Invalid worker report:") && !autoReportRepairable(strings.TrimPrefix(reason, "Invalid worker report:")) {
		return false
	}
	for _, denied := range []string{"unsafe", "escaping", "Missing job receipt", "State persistence", "Invalid runner status"} {
		if strings.Contains(reason, denied) {
			return false
		}
	}
	for _, prefix := range []string{"mkdir ", "open ", "snapshot git:", "snapshot contains unsupported link/device pax_global_header", "runner ", "Worker failed;", "Invalid worker report:", "Bridge unavailable", "Runner status unavailable:"} {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}

func autoRetryReady(a *autoRecord, now time.Time) bool {
	a.Status = "paused"
	a.Reason = a.State.Reason
	if !autoRetryable(a.State.Reason) {
		return false
	}
	loc, _ := time.LoadLocation(a.Config.Timezone)
	day := now.In(loc).Format("2006-01-02")
	// Failures in a completed cycle must not exhaust recovery for every later
	// cycle that day. A legacy record gets one bounded recovery window too.
	scope := fmt.Sprintf("%s/%d", a.State.Date, a.State.Cycle)
	if a.RetryScope != scope {
		a.RetryScope = scope
		a.RetryCount = 0
		a.RetryAt = time.Time{}
	}
	if a.RetryDay != day {
		a.RetryDay = day
		a.RetryCount = 0
	}
	// Continuous mode must recover from a provider outage without waiting for
	// tomorrow or a person. Shorten legacy overnight deferrals once; subsequent
	// retries retain their deadline across controller restarts.
	if a.Config.Continuous && a.RetryAt.After(now.Add(15*time.Minute)) {
		a.RetryAt = now.Add(time.Minute)
	}
	if a.RetryAt.IsZero() {
		delays := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
		if a.RetryCount >= len(delays) {
			if a.Config.Continuous {
				a.RetryAt = now.Add(15 * time.Minute)
			} else {
				local := now.In(loc)
				a.RetryAt = time.Date(local.Year(), local.Month(), local.Day()+1, a.Config.MorningHour, 0, 0, 0, loc)
			}
		} else {
			a.RetryAt = now.Add(delays[a.RetryCount])
		}
	}
	a.Reason = fmt.Sprintf("%s · Automatic retry at %s (%d retries used for this cycle today)", a.State.Reason, a.RetryAt.In(loc).Format("Jan 2 15:04 MST"), a.RetryCount)
	if now.Before(a.RetryAt) {
		return false
	}
	a.RetryCount++
	a.RetryAt = time.Time{}
	return true
}

// Match only runner capacity interlocks, not unrelated operational or safety errors.
func autoStoragePause(reason string) bool {
	if !strings.HasPrefix(reason, "runner prepare:") {
		return false
	}
	return strings.Contains(reason, "retained job storage exceeds") || strings.Contains(reason, "less than 20 GiB backing-volume free space")
}
