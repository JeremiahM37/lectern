// Package outcomes closes the "cost per outcome" competitive gap: tying
// every dollar spent to what it produced (a passing check, an accepted
// change, kept lines, an eval pass), per agent and model. See
// docs/outcomes.md for the full design and precedence rules.
package outcomes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
		if p.InputPer1M < 0 || p.OutputPer1M < 0 {
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
	p, found := c.Prices[model]
	if !found {
		return 0, false
	}
	return float64(inputTokens)/1e6*p.InputPer1M + float64(outputTokens)/1e6*p.OutputPer1M, true
}
