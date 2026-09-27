// Package outcomes closes the "cost per outcome" competitive gap: tying
// every dollar spent to what it produced (a passing check, an accepted
// change, kept lines, an eval pass), per agent and model. See
// docs/outcomes.md for the full design and precedence rules.
package outcomes

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// PricesSettingsKey is the settings row the optional per-model price table
// lives in — one JSON blob, the same "own key, own validation" convention
// internal/budget.SettingsKey uses rather than the generic settings endpoint.
const PricesSettingsKey = "model_prices"

// ModelPrice is one model's per-million-token rate, USD. Used only to
// ESTIMATE cost for an agent that reports tokens but never a dollar figure
// (docs/outcomes.md: "codex tasks without $ cost show tokens-based estimates
// only if a price table is configured") — never to override a real cost
// figure Claude Code, OTel or an eval already measured.
type ModelPrice struct {
	InputPer1M  float64 `json:"input_per_1m"`
	OutputPer1M float64 `json:"output_per_1m"`
	// CachedInputPer1M, when set, prices input tokens the agent reports as
	// served from its prompt cache (Codex's cached_input_tokens, a subset of
	// its input tokens). Unset bills them at InputPer1M.
	CachedInputPer1M *float64 `json:"cached_input_per_1m,omitempty"`
}

// PriceConfig is GET/PUT /api/model-prices' whole body: a model name to its
// rate. Empty is the default — nothing is estimated until an operator
// configures a model here, so a fresh install never shows an invented number.
type PriceConfig struct {
	Prices map[string]ModelPrice `json:"prices"`
}

func (c *PriceConfig) normalize() {
	if c.Prices == nil {
		c.Prices = map[string]ModelPrice{}
	}
}

// Validate rejects a config the API must not persist.
func (c PriceConfig) Validate() error {
	for model, p := range c.Prices {
		if strings.TrimSpace(model) == "" {
			return errors.New("a model price needs a model name")
		}
		if p.InputPer1M < 0 || p.OutputPer1M < 0 || (p.CachedInputPer1M != nil && *p.CachedInputPer1M < 0) {
			return fmt.Errorf("%s: rates must not be negative", model)
		}
	}
	return nil
}

// LoadPrices reads the current price table, defaulting to empty for a fresh
// install or a corrupt/unparseable stored blob.
func LoadPrices(db *store.DB) PriceConfig {
	cfg := PriceConfig{}
	if raw := db.Setting(PricesSettingsKey); raw != "" {
		var stored PriceConfig
		if json.Unmarshal([]byte(raw), &stored) == nil {
			cfg = stored
		}
	}
	cfg.normalize()
	return cfg
}

// SavePrices validates and persists a price table.
func SavePrices(db *store.DB, cfg PriceConfig) (PriceConfig, error) {
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := db.SetSetting(PricesSettingsKey, string(raw)); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Estimate prices inputTokens/outputTokens against model's configured rate.
// ok is false when the model has no price entry (or the config is empty),
// so a caller never mistakes "no data" for "free".
func (c PriceConfig) Estimate(model string, inputTokens, outputTokens int64) (usd float64, ok bool) {
	return c.estimate(model, Tokens{Input: inputTokens, Output: outputTokens})
}

// Tokens is one reading to price. CachedInput is the part of Input served
// from the prompt cache.
type Tokens struct {
	Input, CachedInput, Output int64
}

func (c PriceConfig) estimate(model string, t Tokens) (float64, bool) {
	p, found := c.Prices[model]
	if !found {
		return 0, false
	}
	cached := t.CachedInput
	if cached < 0 || cached > t.Input {
		cached = 0
	}
	cachedRate := p.InputPer1M
	if p.CachedInputPer1M != nil {
		cachedRate = *p.CachedInputPer1M
	}
	return float64(t.Input-cached)/1e6*p.InputPer1M + float64(cached)/1e6*cachedRate +
		float64(t.Output)/1e6*p.OutputPer1M, true
}

// EstimateTokens is EstimateFor with the cached-input split.
func (c PriceConfig) EstimateTokens(agent, model string, t Tokens) (usd float64, ok bool) {
	if model != "" {
		if usd, ok := c.estimate(model, t); ok {
			return usd, true
		}
	}
	if agent == "" {
		return 0, false
	}
	return c.estimate(agent, t)
}

// EstimateFor is Estimate with one fallback: when model has no entry (or is
// empty, as it is for a Codex run on its default model), the agent's own
// name is tried as a key, so an operator can price "codex" once without
// naming every model it might pick.
func (c PriceConfig) EstimateFor(agent, model string, inputTokens, outputTokens int64) (usd float64, ok bool) {
	return c.EstimateTokens(agent, model, Tokens{Input: inputTokens, Output: outputTokens})
}

// EstimateResult fills in an estimated cost on one normalised result event
// payload that reported tokens but no dollar figure (Codex's turn.completed).
// It sets cost_usd and cost_source:"estimated" in place and reports whether
// it did. A payload that already carries a real cost, has no tokens, or whose
// agent/model has no configured price is left untouched.
func EstimateResult(prices PriceConfig, agent, model string, payload map[string]any) bool {
	if cost, ok := payload["cost_usd"].(float64); ok && cost > 0 {
		return false
	}
	_, in, out := resultUsage(payload)
	if in == 0 && out == 0 {
		return false
	}
	cached, _ := payload["cached_input_tokens"].(float64)
	usd, ok := prices.EstimateTokens(agent, model, Tokens{Input: in, CachedInput: int64(cached), Output: out})
	if !ok {
		return false
	}
	payload["cost_usd"] = usd
	payload["cost_source"] = "estimated"
	return true
}

// SeenModel is one agent/model that reported token usage but no dollar figure
// of its own in the window: the entries a price table is for. Priced says
// whether the current table covers it (by model, or by agent name).
type SeenModel struct {
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Tokens int64  `json:"tokens"`
	Priced bool   `json:"priced"`
}

// SeenTokenOnlyModels lists agent/model pairs from sessions (usage_daily) and
// finished task attempts since cutoff (unix seconds) whose usage carried
// tokens but no reported cost — an estimate, or nothing. Most tokens first.
func SeenTokenOnlyModels(db *store.DB, cutoff float64) ([]SeenModel, error) {
	type key struct{ agent, model string }
	totals := map[key]int64{}
	date := time.Unix(int64(cutoff), 0).UTC().Format("2006-01-02")
	rows, err := db.Query(`SELECT agent, model, SUM(input_tokens + output_tokens) FROM usage_daily
		WHERE session_id IS NOT NULL AND date >= ? AND cost_usd - estimated_usd <= 0
		GROUP BY agent, model HAVING SUM(input_tokens + output_tokens) > 0`, date)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var n int64
		if rows.Scan(&k.agent, &k.model, &n) == nil {
			totals[k] += n
		}
	}
	rows.Close()
	rows, err = db.Query(`SELECT t.agent, COALESCE(NULLIF(a.model,''), t.model), a.result_json
		FROM attempts a JOIN tasks t ON t.id = a.task_id
		WHERE a.finished_at IS NOT NULL AND a.finished_at >= ? AND a.result_json != '{}'`, cutoff)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var raw string
		if rows.Scan(&k.agent, &k.model, &raw) != nil {
			continue
		}
		result := store.UnjObj(raw)
		cost, in, out := resultUsage(result)
		if (cost > 0 && result["cost_source"] != "estimated") || in+out == 0 {
			continue
		}
		totals[k] += in + out
	}
	rows.Close()
	prices := LoadPrices(db)
	out := make([]SeenModel, 0, len(totals))
	for k, n := range totals {
		_, priced := prices.EstimateTokens(k.agent, k.model, Tokens{Input: 1})
		out = append(out, SeenModel{Agent: k.agent, Model: k.model, Tokens: n, Priced: priced})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Agent+out[i].Model < out[j].Agent+out[j].Model
	})
	return out, nil
}
