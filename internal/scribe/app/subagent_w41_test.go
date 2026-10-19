package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// w41CountTool is a read-only tool that counts its runs.
type w41CountTool struct{ runs *int }

func (w41CountTool) Spec() llm.ToolSpec { return llm.ToolSpec{Name: "grep"} }
func (w41CountTool) ReadOnly() bool     { return true }
func (t w41CountTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	*t.runs++
	return tools.Result{Output: "match"}
}

// A subagent's registry answers a repeated read-only call from the dispatch
// bus, not only the file read tool. The wrapped tool keeps its name.
func TestWithReadCacheForCachesOtherReadOnlyTools(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry()
	reg.Register(w41CountTool{runs: &runs})
	bus := agent.NewAgentBus()
	out := withReadCacheFor(reg, bus, "s1")

	tool, ok := out.Get("grep")
	if !ok || tool.Spec().Name != "grep" {
		t.Fatal("the wrapped registry lost the grep tool")
	}
	env := &tools.Env{Workdir: t.TempDir()}
	args := json.RawMessage(`{"pattern":"main"}`)
	tool.Run(context.Background(), env, args)
	tool.Run(context.Background(), env, args)
	if runs != 1 {
		t.Fatalf("grep ran %d times, want 1 (second call from the bus)", runs)
	}
}
