package budget

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// anomalyMinSeconds is the shortest run this package will compute an hourly
// rate for — a five-second, ten-cent attempt is not meaningfully "$72/hour".
const anomalyMinSeconds = 300

// alertRetention is how long a budget_alerts_sent row survives once its own
// period is over. Only needed so the table does not grow forever; nothing
// ever needs to know a period's alerts were sent once that period has long
// passed.
const alertRetention = 90 * 24 * time.Hour

// Checker evaluates every configured limit, the account quota and cost
// anomalies once per call, deduplicating through store.DB's
// budget_alerts_sent table so a threshold notifies exactly once per period
// even across a restart. Wired as internal/scheduler.Scheduler.Budgets, so
// it runs on the scheduler's own tick — no separate goroutine or ticker.
type Checker struct {
	DB       *store.DB
	Notifier *sinks.Notifier
	// Clock overrides time.Now for tests. nil means time.Now.
	Clock func() time.Time

	lastPrune time.Time
}

func (c *Checker) now() time.Time {
	if c.Clock != nil {
		return c.Clock()
	}
	return time.Now()
}

// Tick runs every check once. Safe to call on any cadence; each individual
// alert is still rate-limited to "once per period" by the DB regardless of
// how often Tick itself runs.
func (c *Checker) Tick(ctx context.Context) {
	cfg := Load(c.DB)
	now := c.now()

	c.checkLimit(cfg, "overall", "Overall budget", cfg.Overall, "", now)
	// Deterministic iteration order keeps alert ordering stable for tests.
	agents := make([]string, 0, len(cfg.PerAgent))
	for a := range cfg.PerAgent {
		agents = append(agents, a)
	}
	sort.Strings(agents)
	for _, a := range agents {
		c.checkLimit(cfg, "agent:"+a, a+" budget", cfg.PerAgent[a], a, now)
	}
	c.checkQuota(cfg, now)
	if cfg.AnomalyEnabled {
		c.checkAnomalies(cfg, now)
	}

	if c.lastPrune.IsZero() || now.Sub(c.lastPrune) > time.Hour {
		c.lastPrune = now
		_ = c.DB.PruneBudgetAlerts(float64(now.Add(-alertRetention).Unix()))
	}
}

func (c *Checker) checkLimit(cfg Config, scopeID, label string, lim Limit, agent string, now time.Time) {
	if !lim.hasCap() {
		return
	}
	if lim.DailyUSD > 0 {
		if spent, err := c.DB.SpendSince(float64(dayStart(now).Unix()), agent); err == nil {
			c.fireThresholds(cfg.Thresholds, scopeID+":daily", periodKeyDaily(now), label+" (daily)", spent, lim.DailyUSD)
		}
	}
	if lim.WeeklyUSD > 0 {
		if spent, err := c.DB.SpendSince(float64(weekStart(now).Unix()), agent); err == nil {
			c.fireThresholds(cfg.Thresholds, scopeID+":weekly", periodKeyWeekly(now), label+" (weekly)", spent, lim.WeeklyUSD)
		}
	}
}

// fireThresholds notifies once for every configured threshold spend has
// reached that has not already been sent this period — not just the
// highest, so a spend that jumps straight past 75% and 90% in one tick still
// raises both.
func (c *Checker) fireThresholds(thresholds []int, scopeKey, periodKey, label string, spent, cap float64) {
	if cap <= 0 {
		return
	}
	pct := spent / cap * 100
	sorted := append([]int(nil), thresholds...)
	sort.Ints(sorted)
	for _, t := range sorted {
		if pct+1e-9 < float64(t) {
			continue
		}
		sent, err := c.DB.MarkBudgetAlertSent(scopeKey, periodKey, t)
		if err != nil || !sent {
			continue
		}
		title := fmt.Sprintf("Budget %d%%: %s", t, label)
		if t >= 100 {
			title = fmt.Sprintf("Budget exhausted: %s", label)
		}
		body := fmt.Sprintf("%s has spent $%.2f of $%.2f (%.0f%%)", label, spent, cap, pct)
		c.Notifier.Notify(title, body, "/settings", &sinks.Extra{Kind: "budget"})
	}
}

// checkQuota alerts on Claude's account-wide 5h/7d quota windows, read from
// the same "rate_limits" settings row internal/api/usage.go's usageQuota
// chip already reads (written by internal/agentevents.IngestStatusline).
// The window's own resets_at is the period key: once it moves, a fresh
// period has begun and thresholds may fire again.
func (c *Checker) checkQuota(cfg Config, now time.Time) {
	raw := c.DB.Setting("rate_limits")
	if raw == "" {
		return
	}
	obj := store.UnjObj(raw)
	at, _ := obj["at"].(float64)
	if at <= 0 || now.Unix()-int64(at) > 1800 {
		return // stale, same 30-minute rule the quota chip uses
	}
	check := func(key, label string) {
		w, _ := obj[key].(map[string]any)
		if w == nil {
			return
		}
		pct, _ := w["used_percentage"].(float64)
		resets, _ := w["resets_at"].(float64)
		periodKey := fmt.Sprintf("%.0f", resets)
		c.fireThresholdsPercent(cfg.QuotaThresholds, "quota:"+key, periodKey,
			fmt.Sprintf("Claude %s quota", label), pct)
	}
	check("five_hour", "5-hour")
	check("seven_day", "7-day")
}

func (c *Checker) fireThresholdsPercent(thresholds []int, scopeKey, periodKey, label string, pct float64) {
	sorted := append([]int(nil), thresholds...)
	sort.Ints(sorted)
	for _, t := range sorted {
		if pct+1e-9 < float64(t) {
			continue
		}
		sent, err := c.DB.MarkBudgetAlertSent(scopeKey, periodKey, t)
		if err != nil || !sent {
			continue
		}
		c.Notifier.Notify(
			fmt.Sprintf("%s at %d%%", label, t),
			fmt.Sprintf("%s is at %.0f%% for its current window", label, pct),
			"/settings", &sinks.Extra{Kind: "quota"})
	}
}

// checkAnomalies compares every active session/task's own live $/hour rate
// against the account's trailing 7-day median $/hour across finished work
// (docs/budgets.md "Cost anomalies" explains why this baseline is
// account-wide rather than per-entity: a one-shot task or a brand new
// session has no history of its own to compare against). Firing more than
// AnomalyMultiplier x that baseline sends one alert per entity per day —
// the period key is today's date, so a session that stays anomalously
// expensive all day is not re-pinged every tick, but a NEW day (or a
// different session/task) gets its own alert.
func (c *Checker) checkAnomalies(cfg Config, now time.Time) {
	baseline, err := c.DB.FinishedHourlyRates(
		float64(now.Add(-7*24*time.Hour).Unix()), float64(now.Unix()), anomalyMinSeconds)
	if err != nil || len(baseline) < 3 {
		return // not enough history to call anything an outlier yet
	}
	rates := make([]float64, len(baseline))
	for i, s := range baseline {
		rates[i] = s.Rate
	}
	median := store.Median(rates)
	if median <= 0 {
		return
	}
	active, err := c.DB.ActiveHourlyRates(float64(now.Unix()), anomalyMinSeconds)
	if err != nil {
		return
	}
	mult := cfg.AnomalyMultiplier
	if mult < 1 {
		mult = 3
	}
	periodKey := periodKeyDaily(now)
	for _, a := range active {
		if a.Rate < median*mult {
			continue
		}
		scopeKey := fmt.Sprintf("anomaly:%s:%d", a.Kind, a.ID)
		sent, err := c.DB.MarkBudgetAlertSent(scopeKey, periodKey, 0)
		if err != nil || !sent {
			continue
		}
		kind := "Session"
		if a.Kind == "task" {
			kind = "Task"
		}
		c.Notifier.Notify(
			fmt.Sprintf("Cost anomaly: %s", a.Name),
			fmt.Sprintf("%s %q is spending $%.2f/hour — %.1fx the account's trailing 7-day median ($%.2f/hour)",
				kind, a.Name, a.Rate, a.Rate/median, median),
			"/settings", &sinks.Extra{Kind: "budget"})
	}
}
