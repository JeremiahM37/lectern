// Package budget closes the "hard stop / spend alerts" competitive gap:
// daily/weekly USD caps (overall and optionally per agent), a per-task
// budget_usd field, Claude 5h/7d quota-threshold alerts, and a cost-anomaly
// check — all built on top of the account's own usage_daily/attempts
// history (internal/store) and delivered through the existing sinks.Notifier
// (docs/agent-events.md section 3), the same way internal/alerts and
// internal/autonomy already extend that infrastructure rather than
// duplicating it. See docs/budgets.md for the full design.
package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// SettingsKey is the settings row the whole feature's configuration lives
// in — one JSON blob, append-only alongside sinks.Keys and "templates",
// rather than a flat key per field, since GET/PUT /api/budgets owns its own
// validation instead of sharing the generic settings endpoint.
const SettingsKey = "budget_config"

// Mode is what happens once a limit reaches 100%.
type Mode string

const (
	ModeWarn Mode = "warn"
	ModeStop Mode = "stop"
)

// Limit is one daily+weekly USD cap and what happens at 100%. A zero
// DailyUSD/WeeklyUSD means "no cap on that period" — both may be zero
// (limit configured but not yet capped on either axis), in which case the
// whole Limit is inert.
type Limit struct {
	DailyUSD  float64 `json:"daily_usd"`
	WeeklyUSD float64 `json:"weekly_usd"`
	Mode      Mode    `json:"mode"`
}

func (l Limit) hasCap() bool { return l.DailyUSD > 0 || l.WeeklyUSD > 0 }

// Config is the whole feature's settings — GET/PUT /api/budgets.
type Config struct {
	Overall  Limit            `json:"overall"`
	PerAgent map[string]Limit `json:"per_agent"`
	// Thresholds are the spend-limit alert points, default 75/90/100.
	Thresholds []int `json:"thresholds"`
	// QuotaThresholds are the Claude 5h/7d account-quota alert points,
	// default 75/90 (matching the competitive gap's own "GitHub budget
	// alerts at 75/90/100%" framing minus the terminal one — a quota window
	// always eventually reaches 100% on its own and resets, so alerting on
	// it there adds noise without a decision to make).
	QuotaThresholds   []int   `json:"quota_thresholds"`
	AnomalyEnabled    bool    `json:"anomaly_enabled"`
	AnomalyMultiplier float64 `json:"anomaly_multiplier"`
}

// DefaultConfig is what a fresh install has: nothing capped, warn-only,
// anomaly detection on at the documented 3x default. Every limit starts at
// 0 (no cap) rather than some arbitrary dollar figure — this feature must
// never surprise an existing installation by silently start blocking it.
func DefaultConfig() Config {
	return Config{
		Overall:           Limit{Mode: ModeWarn},
		PerAgent:          map[string]Limit{},
		Thresholds:        []int{75, 90, 100},
		QuotaThresholds:   []int{75, 90},
		AnomalyEnabled:    true,
		AnomalyMultiplier: 3,
	}
}

// normalize fills in anything a partial PUT or an older stored blob left
// zero, so callers never have to nil-check Thresholds/QuotaThresholds/Mode.
func (c *Config) normalize() {
	if len(c.Thresholds) == 0 {
		c.Thresholds = []int{75, 90, 100}
	}
	if len(c.QuotaThresholds) == 0 {
		c.QuotaThresholds = []int{75, 90}
	}
	if c.AnomalyMultiplier <= 0 {
		c.AnomalyMultiplier = 3
	}
	if c.Overall.Mode == "" {
		c.Overall.Mode = ModeWarn
	}
	if c.PerAgent == nil {
		c.PerAgent = map[string]Limit{}
	}
	for k, v := range c.PerAgent {
		if v.Mode == "" {
			v.Mode = ModeWarn
			c.PerAgent[k] = v
		}
	}
}

func (l Limit) validate(label string) error {
	if l.DailyUSD < 0 || l.WeeklyUSD < 0 {
		return fmt.Errorf("%s: limits must not be negative", label)
	}
	if l.Mode != "" && l.Mode != ModeWarn && l.Mode != ModeStop {
		return fmt.Errorf("%s: mode must be %q or %q", label, ModeWarn, ModeStop)
	}
	return nil
}

func validPercents(vals []int, label string) error {
	for _, v := range vals {
		if v <= 0 || v > 100 {
			return fmt.Errorf("%s must each be 1-100", label)
		}
	}
	return nil
}

// Validate rejects a config the API must not persist: negative limits, a
// bogus mode, or a threshold outside 1-100.
func (c Config) Validate() error {
	if err := c.Overall.validate("overall"); err != nil {
		return err
	}
	for name, l := range c.PerAgent {
		if strings.TrimSpace(name) == "" {
			return errors.New("a per-agent limit needs an agent name")
		}
		if err := l.validate("agent " + name); err != nil {
			return err
		}
	}
	if err := validPercents(c.Thresholds, "thresholds"); err != nil {
		return err
	}
	if err := validPercents(c.QuotaThresholds, "quota_thresholds"); err != nil {
		return err
	}
	if c.AnomalyMultiplier != 0 && c.AnomalyMultiplier < 1 {
		return errors.New("anomaly_multiplier must be at least 1 (or 0 to use the default)")
	}
	return nil
}

// Load reads the current config, falling back to DefaultConfig for a fresh
// install or a corrupt/unparseable stored blob.
func Load(db *store.DB) Config {
	cfg := DefaultConfig()
	if raw := db.Setting(SettingsKey); raw != "" {
		var stored Config
		if json.Unmarshal([]byte(raw), &stored) == nil {
			cfg = stored
		}
	}
	cfg.normalize()
	return cfg
}

// Save validates and persists a config.
func Save(db *store.DB, cfg Config) (Config, error) {
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := db.SetSetting(SettingsKey, string(raw)); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// dayStart is midnight UTC on now's calendar date.
func dayStart(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// weekStart is midnight UTC on the Monday of now's ISO week — a deterministic,
// non-rolling boundary, so "once per period" has an unambiguous period to key
// off (unlike GET /api/usage's own trailing-7-day convention, which resets
// continuously and so has no boundary an alert could dedupe against).
func weekStart(now time.Time) time.Time {
	now = now.UTC()
	start := dayStart(now)
	wd := int(start.Weekday())
	if wd == 0 { // Sunday
		wd = 7
	}
	return start.AddDate(0, 0, -(wd - 1))
}

func periodKeyDaily(now time.Time) string { return now.UTC().Format("2006-01-02") }
func periodKeyWeekly(now time.Time) string {
	y, w := now.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// Gate returns a non-nil error, its message safe to use directly as an HTTP
// refusal, when a "stop"-mode budget (overall, or the given agent's) is at
// or over 100% right now. agent == "" checks the overall limit only. Called
// synchronously from task dispatch and interactive session launch — cheap
// enough (at most two SpendSince queries) to run on every request rather
// than trust a cached "blocked" flag that could go stale.
func Gate(db *store.DB, agent string) error {
	cfg := Load(db)
	now := time.Now()
	if err := gateLimit(db, "overall budget", cfg.Overall, "", now); err != nil {
		return err
	}
	if agent != "" {
		if lim, ok := cfg.PerAgent[agent]; ok {
			if err := gateLimit(db, agent+" budget", lim, agent, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateLimit(db *store.DB, label string, lim Limit, agent string, now time.Time) error {
	if !lim.hasCap() || lim.Mode != ModeStop {
		return nil
	}
	if lim.DailyUSD > 0 {
		spent, err := db.SpendSince(float64(dayStart(now).Unix()), agent)
		if err == nil && spent >= lim.DailyUSD {
			return fmt.Errorf("%s exhausted: $%.2f of $%.2f spent today (daily cap, stop mode)", label, spent, lim.DailyUSD)
		}
	}
	if lim.WeeklyUSD > 0 {
		spent, err := db.SpendSince(float64(weekStart(now).Unix()), agent)
		if err == nil && spent >= lim.WeeklyUSD {
			return fmt.Errorf("%s exhausted: $%.2f of $%.2f spent this week (weekly cap, stop mode)", label, spent, lim.WeeklyUSD)
		}
	}
	return nil
}

// ExhaustedLabel reports the first "stop"-mode budget (overall, then the
// given agent's) that is currently at or over 100%, for the interactive
// session hook note (docs/budgets.md "Interactive sessions"). Unlike Gate,
// this never returns an error — a session hook response is not an HTTP
// refusal, just advisory text the caller decides what to do with.
func ExhaustedLabel(db *store.DB, agent string) (label string, exhausted bool) {
	if err := Gate(db, agent); err != nil {
		return err.Error(), true
	}
	return "", false
}
