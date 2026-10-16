package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The daily token budget (config daily_token_budget). Scribe counts the
// tokens each finished turn reports, keeps the running total for the local
// day under SCRIBE_HOME, and refuses to start a turn once the day's total
// reaches the budget. The total resets at local midnight: the first record
// or check on a new local date starts from zero.

// budgetWarnAt is the share of the budget at which the warning is shown.
const budgetWarnAt = 0.8

// budgetNow is the clock. Tests replace it to cross midnight.
var budgetNow = time.Now

// budgetMu serialises reads and writes of the budget file in this process.
var budgetMu sync.Mutex

// dailyUse is the budget file: the local date it counts, the tokens spent on
// it, and whether the warning has been shown that day.
type dailyUse struct {
	Date   string `json:"date"`
	Tokens int    `json:"tokens"`
	Warned bool   `json:"warned,omitempty"`
}

func budgetPath() string { return filepath.Join(config.Home(), "budget.json") }

// loadDailyUse reads the budget file for the local day now. A missing,
// unreadable or older-day file counts as nothing spent today.
func loadDailyUse(now time.Time) dailyUse {
	today := now.Format("2006-01-02")
	var u dailyUse
	if b, err := os.ReadFile(budgetPath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	if u.Date != today {
		return dailyUse{Date: today}
	}
	return u
}

// saveDailyUse writes the budget file atomically, so a crash never leaves a
// half-written total behind.
func saveDailyUse(u dailyUse) error {
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	path := budgetPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// turnTokens is what one turn counts against the budget: input, output and
// cache writes. Cache reads are left out: they re-send a prefix the provider
// already holds and would make a long turn look far larger than it was.
func turnTokens(stats agent.TurnStats) int {
	u := stats.Tokens
	return u.InputTokens + u.OutputTokens + u.CacheWriteTokens
}

// liveConfig is the configuration as it is on disk now, so a raised budget or
// a changed compaction setting takes effect at the next turn without a
// restart. If the files cannot be read, the configuration the session started
// with is used.
func (a *App) liveConfig() config.Config {
	c, err := config.Load(a.Workdir)
	if err != nil {
		return a.Cfg
	}
	return c
}

// dailyBudgetGate returns the refusal when today's spending has reached the
// budget, and nil otherwise (including when no budget is set).
func (a *App) dailyBudgetGate() error {
	limit := a.liveConfig().DailyBudget()
	if limit <= 0 {
		return nil
	}
	budgetMu.Lock()
	u := loadDailyUse(budgetNow())
	budgetMu.Unlock()
	if u.Tokens < limit {
		return nil
	}
	return fmt.Errorf("daily token budget reached: %s of %s tokens used today. Scribe starts again at local midnight. "+
		"To keep working today, raise daily_token_budget in %s (or set it to 0 for no limit)",
		groupInt(u.Tokens), groupInt(limit), config.GlobalPath())
}

// refuseTurn ends a turn that was never started. The TUI clears its busy
// state only on turn_done, so the refusal is reported the way a blocked turn
// is: a turn_done whose stats carry the error, which the chat shows once.
func (a *App) refuseTurn(err error) (agent.TurnStats, error) {
	stats := agent.TurnStats{Error: err.Error()}
	a.emit(agent.Event{Kind: agent.EvTurnDone, Stats: &stats})
	return stats, err
}

// recordDailyTokens adds a finished turn to today's total and shows the
// warning once per day when the total passes budgetWarnAt of the budget.
func (a *App) recordDailyTokens(stats agent.TurnStats) {
	limit := a.liveConfig().DailyBudget()
	n := turnTokens(stats)
	if limit <= 0 || n <= 0 {
		return
	}
	budgetMu.Lock()
	u := loadDailyUse(budgetNow())
	u.Tokens += n
	warn := !u.Warned && float64(u.Tokens) >= budgetWarnAt*float64(limit)
	if warn {
		u.Warned = true
	}
	err := saveDailyUse(u)
	budgetMu.Unlock()
	if err != nil {
		a.emit(agent.Event{Kind: agent.EvWarning, Text: "could not save the daily token count: " + err.Error()})
		return
	}
	if warn {
		a.emit(agent.Event{Kind: agent.EvNotice, Text: fmt.Sprintf(
			"Daily token budget: %s of %s tokens used today (%d%%). Scribe stops starting new turns at %s.",
			groupInt(u.Tokens), groupInt(limit), u.Tokens*100/limit, groupInt(limit))})
	}
}

// runTurnBudgeted is one user turn with the budget and compaction settings
// applied: refused before the model is called when the day's budget is spent,
// and counted after it runs. Compaction settings are pushed to the agent here
// so a changed config takes effect at the next turn.
func (a *App) runTurnBudgeted(ctx context.Context, prompt string, images []llm.Part) (agent.TurnStats, error) {
	if err := a.dailyBudgetGate(); err != nil {
		return a.refuseTurn(err)
	}
	live := a.liveConfig()
	a.Agent.SetCompaction(live.CompactStrategy, live.CompactKeepRecent)
	stats, err := agent.RunClearing(ctx, a.Agent, a.Planner, prompt, images)
	a.recordDailyTokens(stats)
	return stats, err
}

// groupInt formats n with thousands separators, e.g. 1,250,000.
func groupInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	if neg {
		return "-" + out
	}
	return out
}
