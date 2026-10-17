package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// The probe reads the same slots the code reads. These tests pin that: if
// subagentRef changes, the probe must say the same thing it does.
var _ tui.RoutingDiagnoser = (*scribeBackend)(nil)

func routerFixtures() []config.Router {
	return []config.Router{
		{},
		{Default: "openai/gpt-x"},
		{Explore: "anthropic/haiku", Code: "anthropic/sonnet"},
		{Explore: "anthropic/haiku", Default: "openai/gpt-x", Verify: "deepseek/v3", Desloppify: "groq/llama"},
		{Code: "anthropic/sonnet", Desloppify: "", Verify: "", WebSearch: "openai/search", Default: "ollama/qwen"},
	}
}

// Every subagent slot resolves to the model subagentRef would give it, with
// tier routing on. An empty result means the parent model.
func TestRouteForMatchesSubagentRefw19diag(t *testing.T) {
	specs := map[string]agent.SubagentSpec{
		"explore":    {Role: "explore", Purpose: agent.PurposeTask},
		"code":       {Role: "code", Purpose: agent.PurposeTask},
		"web_search": {Role: "web", Purpose: agent.PurposeTask},
		"desloppify": {Role: "code", Purpose: agent.PurposeCleanup},
		"verify":     {Role: "explore", Purpose: agent.PurposeReview},
	}
	for _, r := range routerFixtures() {
		for slot, spec := range specs {
			want := subagentRef(r, spec, true)
			got := routeFor(r, "parent/model", slot, true)
			if want == "" {
				want = "parent/model"
			}
			if got.ref != want {
				t.Errorf("slot %s with %+v: probe says %q, subagentRef says %q", slot, r, got.ref, want)
			}
		}
	}
}

func TestRouteForTierOffIsParentw19diag(t *testing.T) {
	r := config.Router{Explore: "anthropic/haiku", Code: "anthropic/sonnet", Verify: "x/y"}
	for _, slot := range []string{"explore", "code", "web_search", "desloppify", "verify"} {
		c := routeFor(r, "parent/model", slot, false)
		if c.ref != "parent/model" || c.via != "tier routing off" {
			t.Errorf("slot %s with tier routing off = %+v, want the parent model", slot, c)
		}
	}
}

func TestRouteForCompactTitleSemanticw19diag(t *testing.T) {
	// Compaction and titles read only their own slot, then the parent. The
	// default slot is not consulted, and the probe reports that.
	r := config.Router{Default: "openai/gpt-x"}
	if c := routeFor(r, "parent/model", "compact", true); c.ref != "parent/model" {
		t.Errorf("compact with only default set = %+v, want parent (code does not read default)", c)
	}
	if c := routeFor(r, "parent/model", "title", true); c.ref != "parent/model" {
		t.Errorf("title with only default set = %+v, want parent", c)
	}
	// Semantic summaries have no parent fallback: no slot means no summaries.
	if c := routeFor(r, "parent/model", "semantic", true); c.ref != "" {
		t.Errorf("semantic with nothing set = %+v, want no model", c)
	}
	r = config.Router{Explore: "anthropic/haiku", Compact: "anthropic/flash"}
	if c := routeFor(r, "parent/model", "semantic", true); c.ref != "anthropic/flash" || c.via != "compact" {
		t.Errorf("semantic should fall to compact, got %+v", c)
	}
	if c := routeFor(config.Router{Explore: "anthropic/haiku"}, "p/m", "semantic", true); c.ref != "anthropic/haiku" {
		t.Errorf("semantic should fall to explore, got %+v", c)
	}
}

func okProbe(dials *int32) routeProbe {
	return routeProbe{
		lookup: func(id string) (registry.ProviderInfo, bool) {
			switch id {
			case "anthropic":
				return registry.ProviderInfo{ID: id, BaseURL: "https://api.anthropic.test", NeedsKey: true, HasKey: true}, true
			case "nokey":
				return registry.ProviderInfo{ID: id, BaseURL: "https://nokey.test", NeedsKey: true}, true
			case "down":
				return registry.ProviderInfo{ID: id, BaseURL: "https://down.test", NeedsKey: true, HasKey: true}, true
			}
			return registry.ProviderInfo{}, false
		},
		dial: func(_ context.Context, base string) error {
			atomic.AddInt32(dials, 1)
			if base == "https://down.test" {
				return errors.New("connection refused")
			}
			return nil
		},
		ollama: func() bool { return false },
	}
}

func findCheck(t *testing.T, cs []tui.DiagnoseCheck, name string) tui.DiagnoseCheck {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, cs)
	return tui.DiagnoseCheck{}
}

func TestRoutingChecksReportsSlotsAndProvidersw19diag(t *testing.T) {
	var dials int32
	r := config.Router{Explore: "anthropic/haiku", Code: "anthropic/sonnet", Verify: "down/v3", Compact: "nokey/c"}
	cs := routingChecks(r, "anthropic/sonnet", true, okProbe(&dials))

	if got := findCheck(t, cs, "tier routing"); got.State != "ok" {
		t.Errorf("tier routing on should be ok, got %+v", got)
	}
	if got := findCheck(t, cs, "explore"); got.State != "ok" || got.Detail != "anthropic/haiku (explore)" {
		t.Errorf("explore row = %+v", got)
	}
	// verify points at an unreachable host, so the slot row carries that.
	if got := findCheck(t, cs, "verify"); got.State != "fail" || got.Fix == "" {
		t.Errorf("verify on an unreachable provider should fail with a fix, got %+v", got)
	}
	// A missing key is a warn and is never dialled.
	if got := findCheck(t, cs, "compact"); got.State != "warn" {
		t.Errorf("compact with no key should warn, got %+v", got)
	}
	// semantic is unset, so it falls to compact, and the row reports compact's
	// provider problem.
	if got := findCheck(t, cs, "semantic"); got.State != "warn" || got.Detail != "nokey/c (compact): no API key set" {
		t.Errorf("semantic should follow compact, got %+v", got)
	}
	// anthropic is used by three slots but is dialled once.
	if n := atomic.LoadInt32(&dials); n != 2 {
		t.Errorf("dials = %d, want 2 (anthropic once, down once; nokey is never dialled)", n)
	}
	if got := findCheck(t, cs, "anthropic"); got.State != "ok" {
		t.Errorf("anthropic provider row = %+v", got)
	}
}

func TestRoutingChecksTierOffw19diag(t *testing.T) {
	var dials int32
	cs := routingChecks(config.Router{Explore: "anthropic/haiku"}, "anthropic/sonnet", false, okProbe(&dials))
	if got := findCheck(t, cs, "tier routing"); got.State != "info" {
		t.Errorf("tier routing off should be info, got %+v", got)
	}
	if got := findCheck(t, cs, "explore"); got.Detail != "anthropic/sonnet (tier routing off)" {
		t.Errorf("explore with tier routing off = %+v", got)
	}
}

func TestRoutingChecksNoModelWarnsw19diag(t *testing.T) {
	var dials int32
	cs := routingChecks(config.Router{}, "none", true, okProbe(&dials))
	if got := findCheck(t, cs, "explore"); got.State != "warn" || got.Fix == "" {
		t.Errorf("no model anywhere should warn with a fix, got %+v", got)
	}
	if n := atomic.LoadInt32(&dials); n != 0 {
		t.Errorf("no provider is set, yet %d dials happened", n)
	}
}

func TestProviderCheckUnknownAndKeylessw19diag(t *testing.T) {
	var dials int32
	p := okProbe(&dials)
	if c := providerCheck(context.Background(), "mystery", p); c.State != "fail" {
		t.Errorf("unknown provider should fail, got %+v", c)
	}
	if c := providerCheck(context.Background(), "nokey", p); c.State != "warn" || c.Detail != "no API key set" {
		t.Errorf("keyless provider should warn without a dial, got %+v", c)
	}
	if n := atomic.LoadInt32(&dials); n != 0 {
		t.Errorf("keyless and unknown providers were dialled %d times", n)
	}
}

func TestProviderCheckOllamaUsesItsOwnProbew19diag(t *testing.T) {
	p := routeProbe{
		lookup: func(id string) (registry.ProviderInfo, bool) {
			return registry.ProviderInfo{ID: id, NeedsKey: false}, true
		},
		dial:   func(context.Context, string) error { t.Error("ollama must not use a TCP dial"); return nil },
		ollama: func() bool { return false },
	}
	c := providerCheck(context.Background(), "ollama", p)
	if c.State != "warn" {
		t.Errorf("ollama not answering should warn, got %+v", c)
	}
	p.ollama = func() bool { return true }
	if c := providerCheck(context.Background(), "ollama", p); c.State != "ok" {
		t.Errorf("ollama answering should be ok, got %+v", c)
	}
}

// Providers are checked in parallel, so four slow hosts take about one wait.
func TestProviderChecksRunInParallelw19diag(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	p := routeProbe{
		lookup: func(id string) (registry.ProviderInfo, bool) {
			return registry.ProviderInfo{ID: id, BaseURL: "https://" + id + ".test", HasKey: true, NeedsKey: true}, true
		},
		dial: func(ctx context.Context, base string) error {
			mu.Lock()
			seen[base] = true
			mu.Unlock()
			select {
			case <-time.After(300 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	start := time.Now()
	out := providerChecks([]string{"a", "b", "c", "d"}, p)
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Errorf("four 300ms connects took %v; they should run in parallel", el)
	}
	if len(out) != 4 || len(seen) != 4 {
		t.Errorf("want 4 checks and 4 dials, got %d and %d", len(out), len(seen))
	}
}

// A real TCP listener is reachable; a closed port is not. Neither sends data.
func TestDialBaseURLw19diag(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := "http://" + ln.Addr().String()
	if err := dialBaseURL(context.Background(), open); err != nil {
		t.Errorf("listening host should be reachable: %v", err)
	}
	ln.Close()
	if err := dialBaseURL(context.Background(), open); err == nil {
		t.Errorf("closed port should not be reachable")
	}
	if err := dialBaseURL(context.Background(), "not a url"); err == nil {
		t.Errorf("a base URL with no host should fail")
	}
}
