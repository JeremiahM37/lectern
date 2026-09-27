package budget

import (
	"fmt"
	"sort"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Window is one provider usage window as its CLI last reported it: Claude's
// statusline and Codex's rollout both report a 5-hour and a 7-day window,
// per login. Any agent whose sessions write rate_5h/7d columns shows up.
type Window struct {
	Agent     string  `json:"agent"`
	TargetID  int64   `json:"target_id"`
	AccountID *int64  `json:"account_id,omitempty"`
	Name      string  `json:"name"` // "5h" or "7d"
	UsedPct   float64 `json:"used_percentage"`
	ResetsAt  float64 `json:"resets_at"`
	At        float64 `json:"at"`
}

// ProviderWindows is the latest live reading of every (machine, agent,
// account) window. A window whose reset time has passed is over and left
// out; its next reading starts a new one.
func ProviderWindows(db *store.DB, now time.Time) []Window {
	rows, err := db.Query(`SELECT s.target_id, s.agent, s.account_id, s.rate_5h_pct, s.rate_5h_reset,
		s.rate_7d_pct, s.rate_7d_reset, s.usage_at FROM sessions s
		WHERE s.usage_at IS NOT NULL AND s.usage_at = (SELECT MAX(s2.usage_at) FROM sessions s2
			WHERE s2.target_id = s.target_id AND s2.agent = s.agent
			AND IFNULL(s2.account_id, 0) = IFNULL(s.account_id, 0))`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Window
	seen := map[string]bool{}
	for rows.Next() {
		var targetID int64
		var agent string
		var account *int64
		var p5, p7 *float64
		var r5, r7 *float64
		var at float64
		if rows.Scan(&targetID, &agent, &account, &p5, &r5, &p7, &r7, &at) != nil {
			continue
		}
		key := fmt.Sprint(targetID, agent, account)
		if seen[key] {
			continue
		}
		seen[key] = true
		add := func(name string, pct, reset *float64) {
			if pct == nil {
				return
			}
			rs := 0.0
			if reset != nil {
				rs = *reset
			}
			if rs > 0 && rs < float64(now.Unix()) {
				return
			}
			out = append(out, Window{Agent: agent, TargetID: targetID, AccountID: account, Name: name,
				UsedPct: *pct, ResetsAt: rs, At: at})
		}
		add("5h", p5, r5)
		add("7d", p7, r7)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// claudeQuotaCovered is whether Claude's own account quota alerts
// (checkQuota) already watch the default Claude login.
func claudeQuotaCovered(db *store.DB, cfg Config, now time.Time) bool {
	if len(cfg.QuotaThresholds) == 0 {
		return false
	}
	obj := store.UnjObj(db.Setting("rate_limits"))
	at, _ := obj["at"].(float64)
	return at > 0 && now.Unix()-int64(at) <= 1800
}

// checkProviderUsage pushes once per window when any provider's usage
// window reaches UsageWarnPercent (docs/budgets.md "Provider usage").
func (c *Checker) checkProviderUsage(cfg Config, now time.Time) {
	covered := claudeQuotaCovered(c.DB, cfg, now)
	for _, w := range ProviderWindows(c.DB, now) {
		if w.UsedPct < float64(cfg.UsageWarnPercent) {
			continue
		}
		if w.Agent == "claude" && w.AccountID == nil && covered {
			continue
		}
		account := "default"
		if w.AccountID != nil {
			account = fmt.Sprint(*w.AccountID)
			if a, err := c.DB.Account(*w.AccountID); err == nil && a != nil {
				account = a.Label
			}
		}
		scope := fmt.Sprintf("usage:%s:%d:%s:%s", w.Agent, w.TargetID, account, w.Name)
		sent, err := c.DB.MarkBudgetAlertSent(scope, fmt.Sprintf("%.0f", w.ResetsAt), cfg.UsageWarnPercent)
		if err != nil || !sent {
			continue
		}
		label := ProviderLabel(w.Agent)
		window := map[string]string{"5h": "5-hour", "7d": "7-day"}[w.Name]
		who := ""
		if account != "default" {
			who = " (" + account + ")"
		}
		c.Notifier.Notify(
			fmt.Sprintf("%s%s %s usage at %.0f%%", label, who, window, w.UsedPct),
			fmt.Sprintf("%s%s has used %.0f%% of its %s window. Warning set at %d%%.", label, who, w.UsedPct, window, cfg.UsageWarnPercent),
			"/settings", &sinks.Extra{Kind: "quota"})
	}
}

// ProviderLabel is the name people know a CLI by.
func ProviderLabel(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini CLI"
	}
	return agent
}
