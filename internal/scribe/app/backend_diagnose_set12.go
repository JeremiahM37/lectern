package app

// Wave 12: the parser and semantic-index probes behind /diagnose. They read
// the code map that is already built; they never rebuild it or spend tokens.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// DiagnoseIndex implements tui.IndexDiagnoser.
//
// Parsers: one row per language the map has files for. A language with files
// but no symbols at all is a parser gap (the extractor read it and found
// nothing), so it warns.
//
// Semantic index: the map is built, has symbols, and a real lookup finds a
// function it holds by name. That round trip is the check that matters: a
// map that has counts but cannot answer a search is not usable.
func (b *scribeBackend) DiagnoseIndex() []tui.DiagnoseCheck {
	a := b.current()
	if a.ext.cmap == nil {
		return []tui.DiagnoseCheck{{Area: "Semantic index", Name: "code map", State: "info", Detail: "off (repo_map is disabled)"}}
	}
	if !isClosed(a.ext.mapOK) {
		return []tui.DiagnoseCheck{{Area: "Semantic index", Name: "code map", State: "info", Detail: "still building; run /diagnose again when it finishes"}}
	}
	st := a.ext.cmap.Stats()
	var out []tui.DiagnoseCheck

	langs := make([]string, 0, len(st.Languages))
	for l := range st.Languages {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		c := tui.DiagnoseCheck{Area: "Parsers", Name: l, State: "ok", Detail: fmt.Sprintf("%d files", st.Languages[l])}
		if st.Symbols == 0 {
			c.State, c.Detail = "warn", c.Detail+", no symbols extracted"
		}
		out = append(out, c)
	}
	if len(langs) == 0 {
		out = append(out, tui.DiagnoseCheck{Area: "Parsers", Name: "languages", State: "info", Detail: "no source files in the map"})
	}

	idx := tui.DiagnoseCheck{Area: "Semantic index", Name: "code map", State: "ok",
		Detail: fmt.Sprintf("built: %d files, %d symbols, %d edges", st.Files, st.Symbols, st.Edges)}
	if st.Symbols == 0 {
		idx.State = "warn"
		idx.Detail = "the map has files but no symbols"
		idx.Fix = "run /repo-map rebuild"
	}
	out = append(out, idx)

	out = append(out, lookupProbe(a.ext.cmap))
	return out
}

// lookupProbe takes a function the map holds and searches for it by name. It
// passes only if the search returns that symbol in the same file.
func lookupProbe(m *repomap.Map) tui.DiagnoseCheck {
	c := tui.DiagnoseCheck{Area: "Semantic index", Name: "name lookup", State: "info"}
	var pick *repomap.Symbol
	for _, s := range m.SymbolsOfKind("function", 200) {
		if len(s.Name) >= 4 {
			s := s
			pick = &s
			break
		}
	}
	if pick == nil {
		c.Detail = "no function long enough to probe with"
		return c
	}
	for _, h := range m.SearchSymbols(pick.Name, 50) {
		if h.Name == pick.Name && h.File == pick.File {
			c.State, c.Detail = "ok", fmt.Sprintf("found %s in %s", pick.Name, pick.File)
			return c
		}
	}
	c.State, c.Detail = "fail", fmt.Sprintf("search did not return %s in %s", pick.Name, pick.File)
	return c
}

// Model routing (REAUDIT3 #18). /diagnose says which model each task-router
// slot sends work to, and whether that model's provider can be reached. It
// spends nothing: the config and the key store are read, and reachability is
// a TCP connect to the provider's host. Ollama is asked with its free
// /api/tags call. No model request is ever sent.

// routeSlotNames are the slots, in the order /diagnose shows them.
var routeSlotNames = []string{"explore", "code", "web_search", "desloppify", "verify", "compact", "title", "semantic"}

// routeProbeTimeout bounds the whole routing probe. The provider connects
// run in parallel, so the probe takes about one connect, not one per provider.
const routeProbeTimeout = 2 * time.Second

// routeProbe is what routingChecks needs from the running app. Tests replace
// it with fakes.
type routeProbe struct {
	lookup func(id string) (registry.ProviderInfo, bool)
	dial   func(ctx context.Context, baseURL string) error
	ollama func() bool
}

// routeChoice is where one slot's model comes from.
type routeChoice struct {
	slot string
	ref  string // "" when the slot has no model
	via  string // the config key that set it, or the fallback it reached
}

// DiagnoseRouting implements tui.RoutingDiagnoser.
func (b *scribeBackend) DiagnoseRouting() []tui.DiagnoseCheck {
	a := b.current()
	return routingChecks(a.Cfg.Router, parentModelRef(a), a.Cfg.AgentFeatures.Enabled("tier_routing"), routeProbeFor(a, dialBaseURL))
}

// parentModelRef is the model the session is on, or "" before one is set.
func parentModelRef(a *App) string {
	if a.Agent != nil {
		return a.Agent.CurrentModel().Ref
	}
	return ""
}

// routeProbeFor is the routing probe for a running app. dial is injected so
// the Health Check can time the connect.
func routeProbeFor(a *App, dial func(context.Context, string) error) routeProbe {
	reg := a.Registry
	return routeProbe{
		lookup: func(id string) (registry.ProviderInfo, bool) {
			if reg == nil {
				return registry.ProviderInfo{}, false
			}
			return reg.Lookup(id)
		},
		dial:   dial,
		ollama: func() bool { return reg != nil && ollamaReachable(reg) },
	}
}

// routingChecks builds the rows: tier routing on or off, one row per slot,
// then one row per provider the slots use. A slot row takes its state from
// its provider, so a slot that points at an unreachable provider shows it.
func routingChecks(r config.Router, parent string, tierOn bool, p routeProbe) []tui.DiagnoseCheck {
	tier := tui.DiagnoseCheck{Area: "Model routing", Name: "tier routing", State: "ok", Detail: "on: subagents use the slots below"}
	if !tierOn {
		tier.State, tier.Detail = "info", "off: every subagent runs on the parent model"
	}
	out := []tui.DiagnoseCheck{tier}

	choices := make([]routeChoice, 0, len(routeSlotNames))
	for _, s := range routeSlotNames {
		choices = append(choices, routeFor(r, parent, s, tierOn))
	}

	// Each distinct provider is checked once, in parallel.
	var pids []string
	seen := map[string]bool{}
	for _, c := range choices {
		if pid, _ := llm.SplitModelRef(c.ref); c.ref != "" && pid != "" && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	states := providerChecks(pids, p)

	for _, c := range choices {
		out = append(out, slotCheck(c, states))
	}
	for _, pid := range pids {
		out = append(out, states[pid])
	}
	return out
}

// slotCheck is the row for one slot.
func slotCheck(c routeChoice, states map[string]tui.DiagnoseCheck) tui.DiagnoseCheck {
	row := tui.DiagnoseCheck{Area: "Model routing", Name: c.slot}
	switch {
	case c.ref == "" && c.slot == "semantic":
		row.State, row.Detail = "info", "not set: code map summaries are off"
		return row
	case c.ref == "" || c.ref == "none":
		row.State, row.Detail = "warn", "no model selected"
		row.Fix = "pick a model with /model, or set router." + c.slot + " in the config"
		return row
	}
	pid, _ := llm.SplitModelRef(c.ref)
	if pid == "" {
		row.State, row.Detail = "fail", c.ref+": no provider prefix"
		row.Fix = "write the model as provider/model"
		return row
	}
	st := states[pid]
	row.State = st.State
	row.Detail = c.ref + " (" + c.via + ")"
	if st.State == "warn" || st.State == "fail" {
		row.Detail += ": " + st.Detail
		row.Fix = st.Fix
	}
	return row
}

// providerChecks runs providerCheck for each id in parallel, bounded by
// routeProbeTimeout.
func providerChecks(pids []string, p routeProbe) map[string]tui.DiagnoseCheck {
	ctx, cancel := context.WithTimeout(context.Background(), routeProbeTimeout)
	defer cancel()
	results := make([]tui.DiagnoseCheck, len(pids))
	var wg sync.WaitGroup
	for i, pid := range pids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = providerCheck(ctx, pid, p)
		}()
	}
	wg.Wait()
	out := make(map[string]tui.DiagnoseCheck, len(pids))
	for i, pid := range pids {
		out[pid] = results[i]
	}
	return out
}

// providerCheck says whether a provider has what it needs to answer: a key,
// and a host that accepts connections. A missing key is reported without a
// connect, since the call would fail before it left the machine anyway.
func providerCheck(ctx context.Context, pid string, p routeProbe) tui.DiagnoseCheck {
	c := tui.DiagnoseCheck{Area: "Model routing", Name: pid, State: "ok"}
	info, ok := p.lookup(pid)
	switch {
	case !ok:
		c.State, c.Detail = "fail", "unknown provider"
		c.Fix = "fix the provider id in the router config (lectern code --list-providers shows the ids)"
		return c
	case info.NeedsKey && !info.HasKey:
		c.State, c.Detail = "warn", "no API key set"
		c.Fix = "store a key with /setup or /settings, then run /diagnose again"
		return c
	case pid == "ollama":
		if p.ollama() {
			c.Detail = "answering on its address"
			return c
		}
		c.State, c.Detail = "warn", "not answering on its address"
		c.Fix = "start Ollama, or set OLLAMA_HOST to a server that is running"
		return c
	}
	if err := p.dial(ctx, info.BaseURL); err != nil {
		c.State, c.Detail = "fail", "not reachable: "+oneLineStr(err.Error())
		c.Fix = "check the network and the provider's base URL"
		return c
	}
	c.Detail = "host accepts connections"
	return c
}

// routeFor says which model a slot uses. It follows the same fallbacks the
// code uses: subagentRef for the subagent slots, the compact model for
// compaction, the title model for commit messages, and summarizerModel for
// code map summaries. With tier routing off, every subagent slot is the
// parent model, as subagentRef does.
func routeFor(r config.Router, parent, slot string, tierOn bool) routeChoice {
	var chain []routeChoice
	terminal := routeChoice{slot: slot, ref: parent, via: "parent model"}
	switch slot {
	case "explore", "code", "web_search", "desloppify", "verify":
		if !tierOn {
			return routeChoice{slot: slot, ref: parent, via: "tier routing off"}
		}
		chain = subagentChain(r, slot)
	case "compact":
		chain = []routeChoice{{"compact", r.Compact, "compact"}}
	case "title":
		chain = []routeChoice{{"title", r.Title, "title"}}
	case "semantic":
		// summarizerModel never falls back to the parent: no slot, no summaries.
		chain = []routeChoice{{"semantic", r.Semantic, "semantic"}, {"compact", r.Compact, "compact"}, {"explore", r.Explore, "explore"}}
		terminal = routeChoice{slot: slot, ref: ""}
	}
	for _, c := range chain {
		if c.ref != "" {
			return routeChoice{slot: slot, ref: c.ref, via: c.via}
		}
	}
	return terminal
}

// subagentChain lists the config keys a subagent slot reads, in order. The
// last link is the default slot, which subagentRef consults before the
// parent model.
func subagentChain(r config.Router, slot string) []routeChoice {
	def := routeChoice{"default", r.Default, "default"}
	switch slot {
	case "explore":
		return []routeChoice{{"explore", r.Explore, "explore"}, def}
	case "code":
		return []routeChoice{{"code", r.Code, "code"}, def}
	case "web_search":
		return []routeChoice{{"web_search", r.WebSearch, "web_search"}, def}
	case "desloppify":
		return []routeChoice{{"desloppify", r.Desloppify, "desloppify"}, {"code", r.Code, "code"}, def}
	case "verify":
		return []routeChoice{{"verify", r.Verify, "verify"}, {"explore", r.Explore, "explore"}, def}
	}
	return nil
}

// dialBaseURL opens a TCP connection to the host of baseURL and closes it.
// Nothing is sent, so no model is called and no key leaves the machine.
func dialBaseURL(ctx context.Context, baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("base URL %q has no host", baseURL)
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	d := net.Dialer{Timeout: routeProbeTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return err
	}
	return conn.Close()
}
