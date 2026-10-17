package app

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// A source that hangs must not hold the Health Check past its budget. The
// backend it fills shows a failing row, and the quick source still reports.
func TestRunHealthSourcesCutsOffHungSource(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	sources := []healthSource{
		{names: []string{"Git"}, run: func() []tui.HealthBackend {
			return []tui.HealthBackend{{Name: "Git", Probes: []tui.HealthProbe{{Name: "repository", State: "pass", Ms: -1}}}}
		}},
		{names: []string{"MCP servers"}, run: func() []tui.HealthBackend {
			<-block
			return nil
		}},
	}
	start := time.Now()
	out := runHealthSources(100*time.Millisecond, healthBackendOrder, sources)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Health Check took %v with a 100ms budget", el)
	}
	byName := map[string]tui.HealthBackend{}
	for _, b := range out {
		byName[b.Name] = b
	}
	if g := byName["Git"]; len(g.Probes) != 1 || g.Probes[0].State != "pass" {
		t.Fatalf("quick source lost its row: %+v", g)
	}
	m := byName["MCP servers"]
	if len(m.Probes) != 1 || m.Probes[0].State != "fail" || m.Probes[0].Reason != "no answer within 100ms" {
		t.Fatalf("hung source row = %+v", m)
	}
}

// Backends come out in the popup's order, whatever order the sources ran in.
func TestRunHealthSourcesOrdersBackends(t *testing.T) {
	sources := []healthSource{
		{names: []string{"Hooks"}, run: func() []tui.HealthBackend { return []tui.HealthBackend{{Name: "Hooks"}} }},
		{names: []string{"Model providers"}, run: func() []tui.HealthBackend {
			return []tui.HealthBackend{{Name: "Model providers"}}
		}},
	}
	out := runHealthSources(time.Second, healthBackendOrder, sources)
	if len(out) != 2 || out[0].Name != "Model providers" || out[1].Name != "Hooks" {
		t.Fatalf("order = %v", names(out))
	}
}

// Rows from /diagnose land under the backend named by their area, in the
// order the names are given. A backend with no rows is still listed.
func TestHealthFromChecksGroupsByArea(t *testing.T) {
	checks := []tui.DiagnoseCheck{
		{Area: "MCP servers", Name: "files", State: "ok", Detail: "3 tools"},
		{Area: "Language servers", Name: "go", State: "fail", Detail: "gopls missing", Fix: "install gopls"},
		{Area: "Providers", Name: "anthropic", State: "ok"},
	}
	out := healthFromChecks(checks, []string{"Language servers", "MCP servers", "Hooks"})
	if len(out) != 3 || out[0].Name != "Language servers" || out[2].Name != "Hooks" {
		t.Fatalf("groups = %v", names(out))
	}
	if p := out[0].Probes[0]; p.State != "fail" || p.Reason != "gopls missing" || p.Fix != "install gopls" {
		t.Fatalf("language server row = %+v", p)
	}
	if p := out[1].Probes[0]; p.State != "pass" || p.Name != "files" {
		t.Fatalf("MCP row = %+v", p)
	}
	if len(out[2].Probes) != 0 {
		t.Fatalf("a backend with no rows has probes: %+v", out[2].Probes)
	}
}

// Each /diagnose state maps to the popup's state: ok is pass, and a check that
// does not apply stays info.
func TestHealthProbeFromMapsStates(t *testing.T) {
	cases := map[string]string{"ok": "pass", "warn": "warn", "fail": "fail", "info": "info", "": "info"}
	for in, want := range cases {
		if got := healthProbeFrom(tui.DiagnoseCheck{State: in}, -1).State; got != want {
			t.Errorf("state %q maps to %q, want %q", in, got, want)
		}
	}
}

// A keyless provider is reported without a connect at all, so no request goes
// out for it.
func TestProviderCheckKeylessSkipsDial(t *testing.T) {
	dials := 0
	p := routeProbe{
		lookup: func(id string) (registry.ProviderInfo, bool) {
			return registry.ProviderInfo{ID: id, BaseURL: "https://api.example.test/v1", NeedsKey: true}, true
		},
		dial:   func(context.Context, string) error { dials++; return nil },
		ollama: func() bool { return false },
	}
	c := providerCheck(context.Background(), "example", p)
	if c.State != "warn" || dials != 0 {
		t.Fatalf("keyless provider: state %q, %d dials", c.State, dials)
	}
}

// A keyed provider is checked by one connect to its host. A failed connect is
// reported as a failure with its reason.
func TestProviderCheckKeyedDialsOnce(t *testing.T) {
	dials := 0
	p := routeProbe{
		lookup: func(id string) (registry.ProviderInfo, bool) {
			return registry.ProviderInfo{ID: id, BaseURL: "https://api.example.test/v1", NeedsKey: true, HasKey: true}, true
		},
		dial:   func(context.Context, string) error { dials++; return errors.New("connection refused") },
		ollama: func() bool { return false },
	}
	c := providerCheck(context.Background(), "example", p)
	if dials != 1 || c.State != "fail" || !strings.Contains(c.Detail, "connection refused") {
		t.Fatalf("keyed provider: %+v after %d dials", c, dials)
	}
}

// The timed connect records how long a successful connect took, and only for
// a connect that worked. A failed connect has no time.
func TestDialTimesRecordsOnlySuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	defer ln.Close()

	times := newDialTimes()
	good := "http://" + ln.Addr().String()
	if err := times.dial(t.Context(), good); err != nil {
		t.Fatalf("connect to local listener: %v", err)
	}
	p := routeProbe{lookup: func(id string) (registry.ProviderInfo, bool) {
		return registry.ProviderInfo{ID: id, BaseURL: good}, true
	}}
	if ms := times.forProvider(p, "local"); ms < 0 {
		t.Fatalf("successful connect has no time: %d", ms)
	}

	// A closed port: the listener is gone, so the connect is refused.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadURL := "http://" + dead.Addr().String()
	dead.Close()
	if err := times.dial(t.Context(), deadURL); err == nil {
		t.Fatal("connect to a closed port succeeded")
	}
	p.lookup = func(id string) (registry.ProviderInfo, bool) {
		return registry.ProviderInfo{ID: id, BaseURL: deadURL}, true
	}
	if ms := times.forProvider(p, "dead"); ms != -1 {
		t.Fatalf("failed connect shows %d ms, want -1", ms)
	}
}

// The health probe connects and sends nothing: a provider that accepts the
// connection reads no bytes, so no key or request leaves the machine.
func TestDialTimesSendsNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		b, _ := io.ReadAll(c)
		got <- b
	}()
	if err := newDialTimes().dial(t.Context(), "http://"+ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if b := <-got; len(b) != 0 {
		t.Fatalf("the probe sent %d bytes: %q", len(b), b)
	}
}

func names(bs []tui.HealthBackend) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}

// On a real backend with no model, Registry or agent, the whole Health Check
// returns within its budget, names every backend the popup expects, and sends
// no request: the probes it runs are the local checks and connects only.
func TestHealthChecksOnBareBackendIsBounded(t *testing.T) {
	b, _ := newTestBackend(t, t.TempDir())
	start := time.Now()
	out := b.HealthChecks()
	if el := time.Since(start); el > healthBudget+time.Second {
		t.Fatalf("Health Check took %v, budget %v", el, healthBudget)
	}
	got := map[string]bool{}
	for _, bk := range out {
		got[bk.Name] = true
	}
	for _, want := range []string{"Model routing", "Git", "Disk", "Hooks", "Parsers", "Semantic index"} {
		if !got[want] {
			t.Errorf("backend %q missing from %v", want, names(out))
		}
	}
}

// A source that panics fails its own backends and leaves the others alone.
func TestRunHealthSourcesRecoversPanic(t *testing.T) {
	sources := []healthSource{
		{names: []string{"Disk"}, run: func() []tui.HealthBackend { panic("boom") }},
		{names: []string{"Git"}, run: func() []tui.HealthBackend {
			return []tui.HealthBackend{{Name: "Git", Probes: []tui.HealthProbe{{Name: "repository", State: "pass", Ms: -1}}}}
		}},
	}
	out := runHealthSources(time.Second, healthBackendOrder, sources)
	if len(out) != 2 {
		t.Fatalf("got %v, want both backends", names(out))
	}
	byName := map[string]tui.HealthBackend{}
	for _, b := range out {
		byName[b.Name] = b
	}
	if d := byName["Disk"]; len(d.Probes) != 1 || d.Probes[0].State != "fail" || !strings.Contains(d.Probes[0].Reason, "boom") {
		t.Fatalf("panicked source row = %+v", d)
	}
	if g := byName["Git"]; len(g.Probes) != 1 || g.Probes[0].State != "pass" {
		t.Fatalf("healthy source row = %+v", g)
	}
}
