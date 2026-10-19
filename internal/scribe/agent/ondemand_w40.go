package agent

import "github.com/JeremiahM37/lectern/v2/internal/scribe/tools"

// DeferSubagentTools makes a subagent's tool list on demand, which is what
// agentFeatures.on_demand_tools asks for. Only the named core tools that the
// registry has, and request_tools and release_tools, are offered at first.
// The rest are listed in the prompt (DeferredCatalog) and loaded when the
// subagent asks for them.
//
// Call it after every tool the subagent gets is registered, bus tools
// included. A tool added later is neither core nor active, so it would never
// be offered.
func DeferSubagentTools(reg *tools.Registry, core []string) *Deferred {
	have := map[string]bool{}
	for _, n := range reg.Names() {
		have[n] = true
	}
	var keep []string
	for _, n := range core {
		if have[n] {
			keep = append(keep, n)
		}
	}
	d := NewDeferred(keep)
	RegisterDeferred(reg, d)
	return d
}
