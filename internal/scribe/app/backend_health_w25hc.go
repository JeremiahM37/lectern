package app

// REAUDIT4 #4: the Health Check popup behind /diagnose (spec 02 DiagnosePopup).
// The probes come from the existing /diagnose checks and the routing probe.
// They are grouped per backend, run in parallel, and are cut off after
// healthBudget. A probe that is still running at the deadline is shown as a
// failure, so a hung provider cannot hold the popup open. No probe sends a
// model request: providers are checked by key and a TCP connect, and the rest
// read state the managers already hold.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// healthBudget is the most time the whole Health Check may take. The spec's
// DiagnosePopup allows 90 s; the wave 25 brief caps it at 3 s.
const healthBudget = 3 * time.Second

// healthBackendOrder is the order the popup shows its backends in.
var healthBackendOrder = []string{
	"Model providers", "Model routing", "Language servers", "MCP servers",
	"Parsers", "Semantic index", "Git", "Disk", "Scribe home", "Hooks",
}

// healthSource is one group of probes. names are the backends it fills, so a
// source that does not answer in time can still show a row against each.
type healthSource struct {
	names []string
	run   func() []tui.HealthBackend
}

// HealthChecks implements tui.HealthChecker. It is the quick checks only, so
// it returns within healthBudget. The popup uses HealthChecksStream instead.
func (b *scribeBackend) HealthChecks() []tui.HealthBackend {
	return b.quickHealth()
}

// quickHealth runs the checks that answer within healthBudget.
func (b *scribeBackend) quickHealth() []tui.HealthBackend {
	a := b.current()
	times := newDialTimes()
	sources := []healthSource{
		{names: []string{"Model providers", "Model routing"}, run: func() []tui.HealthBackend {
			return routingHealth(a, times)
		}},
		{names: []string{"Parsers", "Semantic index"}, run: func() []tui.HealthBackend {
			return healthFromChecks(b.DiagnoseIndex(), []string{"Parsers", "Semantic index"})
		}},
		{names: []string{"Language servers", "MCP servers", "Git", "Disk", "Scribe home", "Hooks"}, run: func() []tui.HealthBackend {
			return healthFromChecks(b.Diagnose(), []string{"Language servers", "MCP servers", "Git", "Disk", "Scribe home", "Hooks"})
		}},
	}
	return runHealthSources(healthBudget, healthBackendOrder, sources)
}

// runHealthSources runs every source at once and waits at most budget for
// them. A source that is still running at the deadline is abandoned: its
// backends get a failing row, and its goroutine finishes on its own later.
func runHealthSources(budget time.Duration, order []string, sources []healthSource) []tui.HealthBackend {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	results := make([][]tui.HealthBackend, len(sources))
	done := make([]chan struct{}, len(sources))
	for i := range sources {
		done[i] = make(chan struct{})
		go func() {
			defer close(done[i])
			// A probe must never take the TUI down. A panic becomes a failing
			// row for each backend the source fills.
			defer func() {
				if r := recover(); r != nil {
					results[i] = panickedBackends(sources[i].names, r)
				}
			}()
			results[i] = sources[i].run()
		}()
	}
	var out []tui.HealthBackend
	for i := range sources {
		select {
		case <-done[i]:
			out = append(out, results[i]...)
		case <-ctx.Done():
			out = append(out, timedOutBackends(sources[i].names, budget)...)
		}
	}
	return sortHealth(out, order)
}

// timedOutBackends is the row shown for each backend whose source ran past the
// budget.
func timedOutBackends(names []string, budget time.Duration) []tui.HealthBackend {
	out := make([]tui.HealthBackend, 0, len(names))
	for _, n := range names {
		out = append(out, tui.HealthBackend{Name: n, Probes: []tui.HealthProbe{{
			Name:   "probe",
			State:  "fail",
			Reason: "no answer within " + budget.String(),
			Fix:    "press r to run the checks again",
			Ms:     -1,
		}}})
	}
	return out
}

// panickedBackends is the row for each backend whose source panicked.
func panickedBackends(names []string, r any) []tui.HealthBackend {
	out := make([]tui.HealthBackend, 0, len(names))
	for _, n := range names {
		out = append(out, tui.HealthBackend{Name: n, Probes: []tui.HealthProbe{{
			Name:   "probe",
			State:  "fail",
			Reason: oneLineStr(fmt.Sprint("check stopped: ", r)),
			Fix:    "this is a bug in the check; report it with the log",
			Ms:     -1,
		}}})
	}
	return out
}

// sortHealth puts the backends in display order. A name not in order goes last.
func sortHealth(bs []tui.HealthBackend, order []string) []tui.HealthBackend {
	rank := func(name string) int {
		if i := slices.Index(order, name); i >= 0 {
			return i
		}
		return len(order)
	}
	sort.SliceStable(bs, func(i, j int) bool { return rank(bs[i].Name) < rank(bs[j].Name) })
	return bs
}

// healthFromChecks groups /diagnose rows into the named backends. Each row
// becomes one probe under the backend whose area it carries.
func healthFromChecks(checks []tui.DiagnoseCheck, names []string) []tui.HealthBackend {
	out := make([]tui.HealthBackend, 0, len(names))
	for _, n := range names {
		hb := tui.HealthBackend{Name: n}
		for _, c := range checks {
			if c.Area == n {
				hb.Probes = append(hb.Probes, healthProbeFrom(c, -1))
			}
		}
		out = append(out, hb)
	}
	return out
}

// healthProbeFrom maps a /diagnose row to a probe. ok is pass, warn and fail
// as they are; info (a check that does not apply) stays info.
func healthProbeFrom(c tui.DiagnoseCheck, ms int) tui.HealthProbe {
	p := tui.HealthProbe{Name: c.Name, Reason: c.Detail, Fix: c.Fix, Ms: ms, State: "info"}
	switch c.State {
	case "ok":
		p.State = "pass"
	case "warn", "fail":
		p.State = c.State
	}
	return p
}

// routingHealth returns the Model providers and Model routing backends. It
// reuses the routing probe: the slot rows go to Model routing, and the
// provider rows to Model providers with their connect time. Providers that
// have a key but no slot are checked too, so a working key shows up even when
// it is not in use.
func routingHealth(a *App, times *dialTimes) []tui.HealthBackend {
	p := routeProbeFor(a, times.dial)
	rows := routingChecks(a.Cfg.Router, parentModelRef(a), a.Cfg.AgentFeatures.Enabled("tier_routing"), p)

	routing := tui.HealthBackend{Name: "Model routing"}
	providers := tui.HealthBackend{Name: "Model providers"}
	routed := map[string]bool{}
	for _, c := range rows {
		if isRoutingRow(c.Name) {
			routing.Probes = append(routing.Probes, healthProbeFrom(c, -1))
			continue
		}
		routed[c.Name] = true
		providers.Probes = append(providers.Probes, healthProbeFrom(c, times.forProvider(p, c.Name)))
	}

	var extra []string
	if a.Registry != nil {
		for _, pr := range a.Registry.List() {
			if pr.HasKey && !routed[pr.ID] {
				extra = append(extra, pr.ID)
			}
		}
	}
	states := providerChecks(extra, p)
	for _, pid := range extra {
		providers.Probes = append(providers.Probes, healthProbeFrom(states[pid], times.forProvider(p, pid)))
	}
	if len(providers.Probes) == 0 {
		providers.Probes = []tui.HealthProbe{{Name: "providers", State: "info", Reason: "none in use and none with a key", Ms: -1}}
	}
	return []tui.HealthBackend{providers, routing}
}

// isRoutingRow says whether a routing row is about the task-router slots or
// the tier switch, rather than a provider.
func isRoutingRow(name string) bool {
	return name == "tier routing" || slices.Contains(routeSlotNames, name)
}

// dialTimes times the connects the routing probe makes. A figure is kept per
// base URL, and only for a connect that succeeded, so a failure shows no time.
type dialTimes struct {
	mu sync.Mutex
	ms map[string]int
}

func newDialTimes() *dialTimes {
	return &dialTimes{ms: map[string]int{}}
}

// dial is dialBaseURL, timed. It has the same signature as routeProbe.dial.
func (t *dialTimes) dial(ctx context.Context, baseURL string) error {
	start := time.Now()
	if err := dialBaseURL(ctx, baseURL); err != nil {
		return err
	}
	t.mu.Lock()
	t.ms[baseURL] = int(time.Since(start).Milliseconds())
	t.mu.Unlock()
	return nil
}

// forProvider is the connect time for a provider, or -1 when it was not timed
// (Ollama is asked over its own call, and an unknown id has no address).
func (t *dialTimes) forProvider(p routeProbe, pid string) int {
	info, ok := p.lookup(pid)
	if !ok {
		return -1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ms, ok := t.ms[info.BaseURL]; ok {
		return ms
	}
	return -1
}
