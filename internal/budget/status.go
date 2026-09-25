package budget

import (
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// PeriodStatus is one daily-or-weekly cap's current spend, for the Settings
// budgets editor, the Usage page's bars and the quota-chip area.
type PeriodStatus struct {
	CapUSD   float64 `json:"cap_usd"`
	SpentUSD float64 `json:"spent_usd"`
	Percent  float64 `json:"percent"`
	Blocked  bool    `json:"blocked"` // stop mode AND percent >= 100
}

// LimitStatus is one scope's (overall, or one agent's) full picture.
type LimitStatus struct {
	Label  string        `json:"label"`
	Mode   Mode          `json:"mode"`
	Daily  *PeriodStatus `json:"daily,omitempty"`
	Weekly *PeriodStatus `json:"weekly,omitempty"`
}

// Status is GET /api/budgets' read model and what GET /api/usage embeds
// under "budgets" — the whole feature's live state in one call.
type Status struct {
	Config  Config      `json:"config"`
	Overall LimitStatus `json:"overall"`
	// PerAgent deliberately has no `omitempty`: an empty Go map still
	// marshals to `{}` this way, never a bare missing key — the frontend
	// (UsagePanel.tsx) does an unconditional Object.entries(budgets.per_agent),
	// which throws on undefined. omitempty on a map treats "empty" the same
	// as "nil", so this bit once before: a fresh install with no per-agent
	// limits configured crashed the whole Usage page on render.
	PerAgent map[string]LimitStatus `json:"per_agent"`
	// AnyBlocked is true when at least one stop-mode limit is currently at
	// or over 100% — the Board/session-launch banner's cheapest possible
	// check, one field instead of walking the whole structure.
	AnyBlocked bool `json:"any_blocked"`
}

func periodStatus(db *store.DB, cap float64, mode Mode, agent string, start time.Time) *PeriodStatus {
	if cap <= 0 {
		return nil
	}
	spent, err := db.SpendSince(float64(start.Unix()), agent)
	if err != nil {
		spent = 0
	}
	pct := 0.0
	if cap > 0 {
		pct = spent / cap * 100
	}
	return &PeriodStatus{
		CapUSD: cap, SpentUSD: round2(spent), Percent: round2(pct),
		Blocked: mode == ModeStop && pct >= 100,
	}
}

func limitStatus(db *store.DB, label string, lim Limit, agent string, now time.Time) LimitStatus {
	return LimitStatus{
		Label:  label,
		Mode:   lim.Mode,
		Daily:  periodStatus(db, lim.DailyUSD, lim.Mode, agent, dayStart(now)),
		Weekly: periodStatus(db, lim.WeeklyUSD, lim.Mode, agent, weekStart(now)),
	}
}

// BuildStatus computes the live status for every configured limit.
func BuildStatus(db *store.DB) Status {
	cfg := Load(db)
	now := time.Now()
	st := Status{
		Config:   cfg,
		Overall:  limitStatus(db, "Overall", cfg.Overall, "", now),
		PerAgent: map[string]LimitStatus{},
	}
	blocked := func(l LimitStatus) bool {
		return (l.Daily != nil && l.Daily.Blocked) || (l.Weekly != nil && l.Weekly.Blocked)
	}
	st.AnyBlocked = blocked(st.Overall)
	for agent, lim := range cfg.PerAgent {
		ls := limitStatus(db, agent, lim, agent, now)
		st.PerAgent[agent] = ls
		if blocked(ls) {
			st.AnyBlocked = true
		}
	}
	return st
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
