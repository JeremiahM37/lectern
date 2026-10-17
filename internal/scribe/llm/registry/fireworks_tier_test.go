package registry

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// The large-model tier is 0.9 per million for ids above 16B. The spec gives
// one figure for it, so input and output both use it.
func TestFireworksLargeTierPricing(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	cases := []struct {
		ref       string
		wantTier  bool
		wantEstim bool
	}{
		{"fireworks/accounts/fireworks/models/llama-v3p1-405b-instruct", true, false},
		{"fireworks/accounts/fireworks/models/qwen2p5-72b-instruct", true, false},
		{"fireworks/accounts/fireworks/models/gpt-oss-120b", true, false},
		{"fireworks/accounts/fireworks/models/llama-v3p2-17.5b-instruct", true, false},
		// Exactly 16B is not above 16B, so it does not get the tier.
		{"fireworks/accounts/fireworks/models/model-16b", false, true},
		{"fireworks/accounts/fireworks/models/llama-v3p2-3b-instruct", false, true},
	}
	for _, c := range cases {
		m := r.ModelInfo(c.ref)
		isTier := m.InputPerMTok == fireworksLargeRate && m.OutputPerMTok == fireworksLargeRate
		if isTier != c.wantTier || m.Estimated != c.wantEstim {
			t.Errorf("%s = %+v, want tier %v estimated %v", c.ref, m, c.wantTier, c.wantEstim)
		}
	}
}

// Mixtral is named in the spec tier list with no figure, so it is left out:
// it must not be priced at the 0.9 tier or at the DeepSeek rate.
func TestFireworksMixtralHasNoFigure(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	for _, ref := range []string{
		"fireworks/accounts/fireworks/models/mixtral-8x22b-instruct",
		"fireworks/accounts/fireworks/models/mixtral-8x7b-instruct",
	} {
		m := r.ModelInfo(ref)
		if m.InputPerMTok == fireworksLargeRate || !m.Estimated {
			t.Errorf("%s = %+v, want an estimate outside the 0.9 tier", ref, m)
		}
	}
}

// The size rule is only for Fireworks. The same id elsewhere keeps the
// static table or the default.
func TestFireworksLargeTierStaysOnFireworks(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	if m := r.ModelInfo("groq/llama-405b-70b"); m.InputPerMTok == fireworksLargeRate {
		t.Fatalf("fireworks tier leaked to groq: %+v", m)
	}
}

func TestParamsInID(t *testing.T) {
	cases := []struct {
		id   string
		want float64
		ok   bool
	}{
		{"accounts/fireworks/models/llama-v3p1-405b-instruct", 405, true},
		{"qwen2p5-72b-instruct", 72, true},
		{"model-16.5b", 16.5, true},
		{"mixtral-8x22b-instruct", 0, false},
		{"inf-b", 0, false},
		{"model-1e3b", 0, false},
		{"model-b", 0, false},
		{"deepseek-v3p1", 0, false},
	}
	for _, c := range cases {
		got, ok := paramsInID(c.id)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("paramsInID(%q) = %v, %v; want %v, %v", c.id, got, ok, c.want, c.ok)
		}
	}
}
