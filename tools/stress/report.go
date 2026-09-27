package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// markdown renders the headline table for docs/benchmarks/scale.md. Every
// cell comes from results.json; nothing is derived beyond formatting.
func markdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run started %s. Host: %v, CPU quota %v cores, %v, %v.\n\n",
		r.Started, r.Host["cpu"], r.Host["cpu_quota_cores"], r.Host["tmux"], r.Host["go"])
	row := func(label string, f func(t TierResult) string) {
		fmt.Fprintf(&b, "| %s |", label)
		for _, t := range r.Tiers {
			fmt.Fprintf(&b, " %s |", f(t))
		}
		b.WriteString("\n")
	}
	pp := func(s Stats) string {
		if s.N == 0 {
			return "–"
		}
		return fmt.Sprintf("%s / %s", ms(s.P50), ms(s.P95))
	}
	fmt.Fprintf(&b, "| Sessions |")
	for _, t := range r.Tiers {
		fmt.Fprintf(&b, " %d |", t.Sessions)
	}
	b.WriteString("\n|---|")
	for range r.Tiers {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	row("Targets", func(t TierResult) string { return fmt.Sprint(len(t.Targets)) })
	row("Launched / failed", func(t TierResult) string { return fmt.Sprintf("%d / %d", t.LaunchOK, t.LaunchFailed) })
	row("Launch POST p50 / p95", func(t TierResult) string { return pp(t.Launch) })
	row("Live at window start / end", func(t TierResult) string { return fmt.Sprintf("%d / %d", t.ActiveAtStart, t.ActiveAtEnd) })
	row("`GET /api/sessions` p50 / p95", func(t TierResult) string { return pp(t.List) })
	row("List response size", func(t TierResult) string { return fmt.Sprintf("%.0f KB", float64(t.ListBytes)/1024) })
	row("SSE fan-out p50 / p95", func(t TierResult) string { return pp(t.Fanout) })
	row("SSE events missed / delivered", func(t TierResult) string {
		return fmt.Sprintf("%d / %d", t.FanoutMissed, t.FanoutProbes*t.FanoutClients-t.FanoutMissed)
	})
	row("Approvals in window (approved)", func(t TierResult) string {
		return fmt.Sprintf("%d (%d)", t.ApprovalsRequested, t.ApprovalsApproved)
	})
	row("Approval request → visible p50 / p95", func(t TierResult) string { return pp(t.ApprovalVisible) })
	row("Decision → agent unblocked p50 / p95", func(t TierResult) string { return pp(t.ApprovalUnblock) })
	row("Approval round trip p50 / p95", func(t TierResult) string { return pp(t.ApprovalRoundTrip) })
	row("Terminal attach total p50 / p95", func(t TierResult) string { return pp(t.AttachTotal) })
	row("TUI refresh p50 / p95", func(t TierResult) string {
		var tui struct {
			Total Stats `json:"total"`
		}
		if json.Unmarshal(t.TUI, &tui) != nil {
			return "–"
		}
		return pp(tui.Total)
	})
	row("Lectern CPU avg / peak (% of a core)", func(t TierResult) string {
		return fmt.Sprintf("%.0f / %.0f", t.Usage.CPUAvgPct, t.Usage.CPUPeakPct)
	})
	row("… incl. reaped children avg", func(t TierResult) string { return fmt.Sprintf("%.0f", t.Usage.CPUWithChildren) })
	row("Lectern RSS avg / peak", func(t TierResult) string {
		return fmt.Sprintf("%.0f / %.0f MB", t.Usage.RSSAvgMB, t.Usage.RSSPeakMB)
	})
	row("HTTP errors", func(t TierResult) string {
		n := 0
		for _, v := range t.HTTPErrors {
			n += v
		}
		return fmt.Sprint(n)
	})
	row("Log WARN / ERROR", func(t TierResult) string {
		return fmt.Sprintf("%d / %d", t.LogCounts["WARN"], t.LogCounts["ERROR"])
	})
	for _, t := range r.Tiers {
		if h := t.Hang; h != nil {
			fmt.Fprintf(&b, "\nUnreachable target, %d sessions: froze %s for %.0f s. Healthy sessions' status age p50/p95 %s before, %s during; "+
				"frozen target's rows shown unreachable after %.1f s (-1: never); healthy rows wrongly flagged %d; recovered %.1f s after it answered again (-1: not within 90 s).\n",
				t.Sessions, h.Target, h.FrozenSeconds, pp(h.StalenessBefore), pp(h.StalenessDuring),
				h.SecondsToUnreachable, h.HealthyFlagged, h.SecondsToRecover)
		}
		if h := t.Hold; h != nil {
			fmt.Fprintf(&b, "\nHeld terminals, %d sessions: opened %d at once, %d still connected 3 s later, %d refused, %d retire notices.\n",
				t.Sessions, h.Opened, h.StillOpen, h.Refused, h.Notices)
		}
	}
	return b.String()
}

func ms(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}
