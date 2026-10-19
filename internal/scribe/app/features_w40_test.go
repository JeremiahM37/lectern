package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// w40Tool is a registered tool with a name and nothing else to test.
type w40Tool struct{ name string }

func (t w40Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.name, Description: "Tool " + t.name + ". More detail."}
}
func (t w40Tool) ReadOnly() bool { return true }
func (t w40Tool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{}
}

func w40Registry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(w40Tool{name: "read"})
	reg.Register(w40Tool{name: "web_search"})
	return reg
}

func w40Sys(string) string { return "base" }

// With on_demand_tools off a subagent keeps every tool and its prompt.
func TestOnDemandSubagentOffLeavesToolsAlone(t *testing.T) {
	a := &App{Cfg: config.Config{}}
	reg := w40Registry()
	d, sys := a.onDemandSubagent(reg, w40Sys)
	if d != nil {
		t.Fatalf("flag off must not defer tools")
	}
	if sys("x") != "base" {
		t.Fatalf("flag off must not touch the system prompt")
	}
}

// With on_demand_tools on, a subagent gets only its core tools, and its prompt
// lists the rest so the model knows what request_tools can load.
func TestOnDemandSubagentOnDefersNonCoreTools(t *testing.T) {
	on := true
	a := &App{Cfg: config.Config{AgentFeatures: &config.AgentFeatures{OnDemandTools: &on}}}
	reg := w40Registry()
	d, sys := a.onDemandSubagent(reg, w40Sys)
	if d == nil {
		t.Fatalf("flag on must defer tools")
	}
	if d.IsCore("web_search") || !d.IsCore("read") {
		t.Fatalf("read is core and web_search is not")
	}
	prompt := sys("x")
	if !strings.Contains(prompt, "# Deferred tools") || !strings.Contains(prompt, "- web_search") {
		t.Fatalf("the prompt must list the deferred tool:\n%s", prompt)
	}
	if strings.Contains(prompt, "- read") {
		t.Fatalf("core tools are not listed as deferred:\n%s", prompt)
	}
}
