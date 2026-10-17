package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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

// withReadCache returns reg with its read tool wrapped in readCache, so the
// subagents of one dispatch share file reads through bus.
func withReadCache(reg *tools.Registry, bus *agent.AgentBus) *tools.Registry {
	out := tools.NewRegistry()
	for _, n := range reg.Names() {
		t, _ := reg.Get(n)
		if n == "read" {
			t = readCache{Tool: t, bus: bus}
		}
		out.Register(t)
	}
	return out
}

// readCache serves a repeated read of an unchanged file from the dispatch's
// bus. The stamp is the file's size and modification time, so an edit makes
// the next read a miss. Reads with paths, lines or symbols, and reads that
// ask to force, go straight to the tool. A cache hit still marks the file read
// for the edit guard, because the agent did see it.
type readCache struct {
	tools.Tool
	bus *agent.AgentBus
}

func (t readCache) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var args map[string]json.RawMessage
	if env == nil || json.Unmarshal(raw, &args) != nil {
		return t.Tool.Run(ctx, env, raw)
	}
	for _, k := range []string{"paths", "lines", "symbol", "force"} {
		if _, set := args[k]; set {
			return t.Tool.Run(ctx, env, raw)
		}
	}
	var path string
	if json.Unmarshal(args["path"], &path) != nil || path == "" {
		return t.Tool.Run(ctx, env, raw)
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(env.Workdir, path)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return t.Tool.Run(ctx, env, raw)
	}
	stamp := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	key := abs + "\x00" + string(raw)
	if out, ok := t.bus.CachedRead(key, stamp); ok {
		if env.Files != nil {
			env.Files.MarkRead(abs)
		}
		return tools.Result{Output: out, Summary: "read (cached in this dispatch)"}
	}
	res := t.Tool.Run(ctx, env, raw)
	if !res.IsError {
		t.bus.StoreRead(key, stamp, res.Output)
	}
	return res
}
