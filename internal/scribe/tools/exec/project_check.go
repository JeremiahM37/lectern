package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// projectCheckTool is the read-only face of the project tool. Explore and
// review subagents get it so they can run the project's checks to confirm
// what they read, without the write path: it offers only check and list,
// and it never applies a fix. It is not registered with the main agent,
// which keeps the full project tool.
type projectCheckTool struct{ inner *projectTool }

// NewProjectCheck returns the read-only project check tool. It runs the same
// commands as the project tool, under the same command gate and timeouts.
func NewProjectCheck(jobs *Jobs) tools.Tool {
	return &projectCheckTool{inner: &projectTool{jobs: jobs}}
}

// projectCheckActions are the actions the read-only tool accepts.
var projectCheckActions = []string{"check", "list"}

func (t *projectCheckTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "project_check",
		Description: "Run this project's checks without changing any file: typecheck, then lint, then test, with one summary " +
			"(action check), or list the detected commands (action list). It never applies a lint fix. " +
			"Use it to confirm what you read; use shell for anything else.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("check runs typecheck, lint and test; list shows the detected commands.", "check", "list"),
			"timeout_ms": tools.Int("Timeout in milliseconds for each stage (default 600000)."),
		}),
	}
}

// ReadOnly is true: the tool never writes, so it is offered to read-only
// subagents.
func (t *projectCheckTool) ReadOnly() bool { return true }

func (t *projectCheckTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var a struct {
		Action    string `json:"action"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if len(raw) > 0 {
		if err := tools.Decode(raw, &a); err != nil {
			return tools.Errorf("%v", err)
		}
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		action = "check"
	}
	if !slices.Contains(projectCheckActions, action) {
		return tools.Errorf("project_check accepts only check or list (got %q). Use the project tool for other actions, from the main agent.", a.Action)
	}
	inner, err := json.Marshal(map[string]any{"action": action, "timeout_ms": a.TimeoutMS, "fix": false})
	if err != nil {
		return tools.Errorf("project_check: %v", err)
	}
	res := t.inner.Run(ctx, env, inner)
	if res.Summary != "" {
		res.Summary = fmt.Sprintf("read-only %s: %s", action, res.Summary)
	}
	return res
}
