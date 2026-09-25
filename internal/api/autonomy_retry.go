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
	if a.RetryDay != day {
		a.RetryDay = day
		a.RetryCount = 0
	}
	if a.RetryAt.IsZero() {
		delays := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
		if a.RetryCount >= len(delays) {
			local := now.In(loc)
			a.RetryAt = time.Date(local.Year(), local.Month(), local.Day()+1, a.Config.MorningHour, 0, 0, 0, loc)
		} else {
			a.RetryAt = now.Add(delays[a.RetryCount])
		}
	}
	a.Reason = fmt.Sprintf("%s · Automatic retry at %s (%d/3 retries used today)", a.State.Reason, a.RetryAt.In(loc).Format("Jan 2 15:04 MST"), a.RetryCount)
	if now.Before(a.RetryAt) {
		return false
	}
	a.RetryCount++
	a.RetryAt = time.Time{}
	return true
}
