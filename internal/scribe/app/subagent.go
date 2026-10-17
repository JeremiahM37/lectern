package app

import (
	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
)

// subagentRef is the model reference a subagent runs on, or "" for the
// parent model. The purpose picks the slot first (the desloppify and verify
// slots for the post-dispatch passes, the fix agent uses the code slot),
// then the role's slot. Router.Default covers a slot left empty.
//
// tier_routing off sends every subagent to the parent model, whatever the
// slots name, as it did before the slots were honoured.
func subagentRef(r config.Router, spec agent.SubagentSpec, tierRouting bool) string {
	if !tierRouting {
		return ""
	}
	var ref string
	switch spec.Purpose {
	case agent.PurposeCleanup:
		ref = r.Desloppify
		if ref == "" {
			ref = r.Code
		}
	case agent.PurposeReview:
		ref = r.Verify
		if ref == "" {
			ref = r.Explore
		}
	case agent.PurposeFix:
		ref = r.Code
	default:
		switch spec.Role {
		case "code":
			ref = r.Code
		case "web":
			ref = r.WebSearch
		default:
			ref = r.Explore
		}
	}
	if ref == "" {
		ref = r.Default
	}
	return ref
}

// subagentWindowCap is the largest context window a subagent's token
// thresholds are measured against (spec 03 §3.5, §4.10). A subagent on a
// million-token model still nudges at 80% and stops at 90% of 200 000: its
// work is bounded by step caps, so the window it could fill is not the limit.
const subagentWindowCap = 200_000

// subagentWindow is the window a subagent's token thresholds use: the model's
// window capped at subagentWindowCap. An unknown window (zero) gets the cap.
func subagentWindow(modelWindow int) int {
	if modelWindow <= 0 || modelWindow > subagentWindowCap {
		return subagentWindowCap
	}
	return modelWindow
}

// readOnlyProjectTool is the read-only project check for explore and review
// subagents.
func readOnlyProjectTool(jobs *exec.Jobs) tools.Tool {
	return exec.NewProjectCheck(jobs)
}

// withReadCache returns reg with its read tool wrapped in the bus cache, so
// the subagents of one dispatch share file reads through bus. Reads made this
// way are not attributed to a subagent; withReadCacheFor names one.
func withReadCache(reg *tools.Registry, bus *agent.AgentBus) *tools.Registry {
	return withReadCacheFor(reg, bus, "")
}

// withReadCacheFor is withReadCache for the subagent id. The id is what the
// cache notes name, so the subagent's row can show its cache state.
func withReadCacheFor(reg *tools.Registry, bus *agent.AgentBus, id string) *tools.Registry {
	out := tools.NewRegistry()
	for _, n := range reg.Names() {
		t, _ := reg.Get(n)
		if n == "read" {
			t = agent.ReadCacheTool(t, bus, id)
		}
		out.Register(t)
	}
	return out
}
