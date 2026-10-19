package app

import (
	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// onDemandSubagent applies agentFeatures.on_demand_tools to one subagent. When
// the flag is on, the subagent is offered its core tools and request_tools,
// and the rest are listed in its system prompt until it loads them. It must
// run after every tool of the subagent is registered (see
// agent.DeferSubagentTools). With the flag off it returns nil and the system
// prompt unchanged, so the subagent keeps every tool it has.
func (a *App) onDemandSubagent(reg *tools.Registry, system func(string) string) (*agent.Deferred, func(string) string) {
	if !a.Cfg.AgentFeatures.Enabled("on_demand_tools") {
		return nil, system
	}
	d := agent.DeferSubagentTools(reg, coreToolNames)
	catalog := agent.DeferredCatalog(reg, d.IsCore)
	if catalog == "" {
		return d, system
	}
	return d, func(mode string) string {
		return system(mode) + "\n\n# Deferred tools\nThese tools are not loaded yet. Call request_tools with their names to load them; they can be called from the next step.\n" + catalog
	}
}
