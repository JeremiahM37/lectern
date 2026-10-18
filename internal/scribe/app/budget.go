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
	// Chats is the same count per chat, keyed by chatKey, for the per-chat
	// budget (chat_daily_token_budget). Chats counts every chat's turns, so
	// a budget set later starts from the real figure, as the global one does.
	Chats map[string]chatUse `json:"chats,omitempty"`
}

// chatUse is one chat's share of the day's budget file.
type chatUse struct {
	Tokens int  `json:"tokens"`
	Warned bool `json:"warned,omitempty"`
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

// turnOnly returns stats with Tokens cut to what this turn added. The agent's
// TurnStats.Tokens is the session's running total, so counting it as is
// added every earlier turn again: live, a chat showed 619k of a 500k budget
// after turns that used 271k, and was refused. before is the agent's total
// when the turn started. A total that went down (a reset session) counts
// from zero.
func turnOnly(stats agent.TurnStats, before llm.Usage) agent.TurnStats {
	t := stats.Tokens
	if t.InputTokens < before.InputTokens || t.OutputTokens < before.OutputTokens || t.CacheWriteTokens < before.CacheWriteTokens {
		return stats
	}
	t.InputTokens -= before.InputTokens
	t.OutputTokens -= before.OutputTokens
	t.CacheWriteTokens -= before.CacheWriteTokens
	t.CacheReadTokens = max(0, t.CacheReadTokens-before.CacheReadTokens)
	stats.Tokens = t
	return stats
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

// chatKey names this chat in the budget file: the session id, so a resumed
// session keeps its count. A chat that is not saved uses the App's own id,
// which lasts as long as the chat does.
func (a *App) chatKey() string {
	if a.Meta.ID != "" {
		return a.Meta.ID
	}
	return a.claimOwner
}

// dailyBudgetGate returns the refusal when today's spending has reached the
// global budget or this chat's budget, and nil otherwise (including when no
// budget is set).
func (a *App) dailyBudgetGate() error {
	cfg := a.liveConfig()
	limit, chatLimit := cfg.DailyBudget(), cfg.ChatDailyBudget()
	if limit <= 0 && chatLimit <= 0 {
		return nil
	}
	budgetMu.Lock()
	u := loadDailyUse(budgetNow())
	budgetMu.Unlock()
	if limit > 0 && u.Tokens >= limit {
		return fmt.Errorf("daily token budget reached: %s of %s tokens used today. Scribe starts again at local midnight. "+
			"To keep working today, raise daily_token_budget in %s (or set it to 0 for no limit)",
			groupInt(u.Tokens), groupInt(limit), config.GlobalPath())
	}
	if spent := u.Chats[a.chatKey()].Tokens; chatLimit > 0 && spent >= chatLimit {
		return fmt.Errorf("daily token budget reached for this chat: %s of %s tokens used in this chat today. "+
			"This chat starts again at local midnight. To keep working in it today, raise chat_daily_token_budget in %s "+
			"(or set it to 0 for no limit)",
			groupInt(spent), groupInt(chatLimit), config.GlobalPath())
	}
	return nil
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
	cfg := a.liveConfig()
	limit, chatLimit := cfg.DailyBudget(), cfg.ChatDailyBudget()
	n := turnTokens(stats)
	// Spend is counted even without a budget, so /context can show today's
	// total and a budget set later starts from the real figure.
	if n <= 0 {
		return
	}
	key := a.chatKey()
	budgetMu.Lock()
	u := loadDailyUse(budgetNow())
	u.Tokens += n
	warn := limit > 0 && !u.Warned && float64(u.Tokens) >= budgetWarnAt*float64(limit)
	if warn {
		u.Warned = true
	}
	if u.Chats == nil {
		u.Chats = map[string]chatUse{}
	}
	c := u.Chats[key]
	c.Tokens += n
	chatWarn := chatLimit > 0 && !c.Warned && float64(c.Tokens) >= budgetWarnAt*float64(chatLimit)
	if chatWarn {
		c.Warned = true
	}
	u.Chats[key] = c
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
	if chatWarn {
		a.emit(agent.Event{Kind: agent.EvNotice, Text: fmt.Sprintf(
			"This chat's daily token budget: %s of %s tokens used today (%d%%). Scribe stops starting new turns in this chat at %s.",
			groupInt(c.Tokens), groupInt(chatLimit), c.Tokens*100/chatLimit, groupInt(chatLimit))})
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
	a.Agent.SetMaxToolResults(live.ToolResultsKept())
	before, _ := a.Agent.Usage()
	stats, err := agent.RunClearing(ctx, a.Agent, a.Planner, prompt, images)
	a.recordDailyTokens(turnOnly(stats, before))
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
