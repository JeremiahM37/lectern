package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// Each subagent purpose and role resolves to its own slot, with the default
// slot covering an empty one.
func TestSubagentRefPicksSlotByPurpose(t *testing.T) {
	r := config.Router{
		Explore: "p/explore", Code: "p/code", WebSearch: "p/web",
		Desloppify: "p/clean", Verify: "p/verify", Default: "p/default",
	}
	task := func(role string) agent.SubagentSpec {
		return agent.SubagentSpec{Role: role, Purpose: agent.PurposeTask}
	}
	cases := []struct {
		name string
		spec agent.SubagentSpec
		want string
	}{
		{"explore task", task("explore"), "p/explore"},
		{"code task", task("code"), "p/code"},
		{"web task", task("web"), "p/web"},
		{"cleanup pass", agent.SubagentSpec{Role: "code", Purpose: agent.PurposeCleanup}, "p/clean"},
		{"review pass", agent.SubagentSpec{Role: "explore", Purpose: agent.PurposeReview}, "p/verify"},
		{"fix agent", agent.SubagentSpec{Role: "code", Purpose: agent.PurposeFix}, "p/code"},
	}
	for _, c := range cases {
		if got := subagentRef(r, c.spec, true); got != c.want {
			t.Errorf("%s: slot = %q, want %q", c.name, got, c.want)
		}
	}
}

// With no purpose slot set, the passes use the role slots they used before,
// and an empty role slot falls back to the default.
func TestSubagentRefFallbacks(t *testing.T) {
	r := config.Router{Code: "p/code", Explore: "p/explore"}
	if got := subagentRef(r, agent.SubagentSpec{Role: "code", Purpose: agent.PurposeCleanup}, true); got != "p/code" {
		t.Errorf("cleanup without a cleanup slot = %q, want the code slot", got)
	}
	if got := subagentRef(r, agent.SubagentSpec{Role: "explore", Purpose: agent.PurposeReview}, true); got != "p/explore" {
		t.Errorf("review without a verify slot = %q, want the explore slot", got)
	}
	if got := subagentRef(r, agent.SubagentSpec{Role: "web", Purpose: agent.PurposeTask}, true); got != "" {
		t.Errorf("web with no slot or default = %q, want the parent model (empty)", got)
	}
	r.Default = "p/default"
	if got := subagentRef(r, agent.SubagentSpec{Role: "web", Purpose: agent.PurposeTask}, true); got != "p/default" {
		t.Errorf("web with default = %q", got)
	}
}

// tier_routing off sends every subagent to the parent model.
func TestSubagentRefTierRoutingOff(t *testing.T) {
	r := config.Router{Explore: "p/explore", Verify: "p/verify", Default: "p/default"}
	for _, spec := range []agent.SubagentSpec{
		{Role: "explore", Purpose: agent.PurposeTask},
		{Role: "explore", Purpose: agent.PurposeReview},
	} {
		if got := subagentRef(r, spec, false); got != "" {
			t.Errorf("tier routing off, %+v: slot = %q, want parent model", spec, got)
		}
	}
}
