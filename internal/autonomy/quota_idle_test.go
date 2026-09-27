package autonomy

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func TestClaudeExplicitIdleQuotaRetainsEveryOtherGate(t *testing.T) {
	now := time.Unix(1800000000, 0)
	idle := func() []ProviderUsage {
		p := quota(now)
		p[0].ID = "claude"
		w := &p[0].Buckets[0].Windows[0]
		w.Label = "5-hour"
		w.UsedPercent = number(0)
		w.RemainingPercent = number(100)
		w.ResetsAt = nil
		w.ResetState = "provider_null_zero"
		return p
	}
	for _, test := range []struct {
		name   string
		change func([]ProviderUsage)
		pass   bool
	}{
		{"explicit fresh idle", func([]ProviderUsage) {}, true},
		{"missing provenance", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].ResetState = "" }, false},
		{"unknown provenance", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].ResetState = "guessed" }, false},
		{"nonzero", func(p []ProviderUsage) {
			w := &p[0].Buckets[0].Windows[0]
			w.UsedPercent = number(.01)
			w.RemainingPercent = number(99.99)
		}, false},
		{"reset contradiction", func(p []ProviderUsage) {
			p[0].Buckets[0].Windows[0].ResetsAt = number(float64(now.Add(time.Hour).Unix()))
		}, false},
		{"reset passed", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].ResetPassed = true }, false},
		{"stale", func(p []ProviderUsage) { p[0].UpdatedAt = number(float64(now.Add(-361 * time.Second).Unix())) }, false},
		{"source failed", func(p []ProviderUsage) { p[0].Status = "stale" }, false},
		{"weekly cutoff", func(p []ProviderUsage) {
			w := &p[0].Buckets[0].Windows[1]
			w.UsedPercent = number(85)
			w.RemainingPercent = number(15)
		}, false},
		{"weekly reset unknown", func(p []ProviderUsage) { p[0].Buckets[0].Windows[1].ResetsAt = nil }, false},
		{"weekly invalid reset", func(p []ProviderUsage) { p[0].Buckets[0].Windows[1].ResetsAt = number(math.NaN()) }, false},
		{"weekly cannot be idle", func(p []ProviderUsage) {
			w := p[0].Buckets[0].Windows[0]
			w.Label = "Weekly"
			p[0].Buckets[0].Windows = append(p[0].Buckets[0].Windows, w)
		}, false},
		{"scoped cutoff", func(p []ProviderUsage) {
			w := p[0].Buckets[0].Windows[1]
			w.Label = "Weekly Fable"
			w.UsedPercent = number(85)
			w.RemainingPercent = number(15)
			p[0].Buckets[0].Windows = append(p[0].Buckets[0].Windows, w)
		}, false},
		{"codex excluded", func(p []ProviderUsage) { p[0].ID = "codex" }, false},
		{"later active", func(p []ProviderUsage) {
			w := &p[0].Buckets[0].Windows[0]
			w.ResetState = ""
			w.UsedPercent = number(1)
			w.RemainingPercent = number(99)
			w.ResetsAt = number(float64(now.Add(time.Hour).Unix()))
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := idle()
			test.change(p)
			e := QuotaGate(enabled(), p, []string{p[0].ID}, now)
			if (e == nil) != test.pass {
				t.Fatal(e)
			}
		})
	}
	// Persistence must preserve collector provenance; no fabricated reset timestamp.
	raw, _ := json.Marshal(idle())
	var restored []ProviderUsage
	json.Unmarshal(raw, &restored)
	if e := QuotaGate(enabled(), restored, []string{"claude"}, now); e != nil || restored[0].Buckets[0].Windows[0].ResetsAt != nil {
		t.Fatal(e)
	}
}

func TestClaudeIdleCapturedCollectorEvidence(t *testing.T) {
	path := os.Getenv("LECTERN_IDLE_QUOTA_FIXTURE")
	if path == "" {
		t.Skip("read-only provider capture normalized by companion collector")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p ProviderUsage
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, int64(*p.UpdatedAt*1e9)).Add(time.Second)
	if err = QuotaGate(enabled(), []ProviderUsage{p}, []string{"claude"}, now); err != nil {
		t.Fatal(err)
	}
	p.Buckets[0].Windows[0].ResetState = ""
	if QuotaGate(enabled(), []ProviderUsage{p}, []string{"claude"}, now) == nil {
		t.Fatal("old ambiguous null accepted")
	}
}
