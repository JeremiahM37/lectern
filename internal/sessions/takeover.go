package sessions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// ExactResumeID never falls back to the last conversation in a directory.
func (m *Manager) ExactResumeID(agent, id string) string {
	spec, ok := Find(m.specs(), agent)
	if !ok || len(spec.ResumeIDArgs) == 0 {
		return ""
	}
	return id
}

// PrepareTakeover preserves the task's runtime configuration while removing
// broker hooks tied to the background attempt. Nothing is launched here.
func (m *Manager) PrepareTakeover(ctx context.Context, ex executor.Executor, task *store.Task, project *store.Project, att *store.Attempt, env map[string]string) (LaunchOpts, error) {
	spec, ok := Find(m.specs(), task.Agent)
	if !ok {
		return LaunchOpts{}, fmt.Errorf("unknown interactive agent %q", task.Agent)
	}
	// Without an exact conversation identity, keep a written handoff alongside
	// the unchanged files. Never resume an unrelated --last conversation.
	resumeID := att.SessionID
	if len(spec.ResumeIDArgs) == 0 {
		resumeID = ""
	}
	prime := ""
	if resumeID == "" {
		prompt := firstNonEmpty(att.Prompt, task.Prompt)
		handoff := "# Interactive takeover\n\nThe operator took over this existing run. Preserve its work and wait for their next instruction.\n\n## Original request\n" + prompt + "\n\n## Last result\n" + att.ResultJSON + "\n\nThe complete event log is in .lectern/events.jsonl.\n"
		path := agents.RuntimeDir(att.WorktreePath) + "/takeover.md"
		if err := ex.WriteFile(ctx, path, []byte(handoff)); err != nil {
			return LaunchOpts{}, err
		}
		prime = "Read " + path + " for the previous run's context. This is an interactive takeover in the same worktree; preserve its changes and wait for my next instruction."
	}
	// Carry authentication, project environment and Claude's explicit tool/MCP
	// configuration into the interactive process. Approval broker hooks belong
	// to the cancelled attempt, so they must not be copied into the new one.
	var extraArgs []string
	if task.Agent == "claude" || task.Agent == "" {
		extraArgs = append(extraArgs, "--permission-mode", firstNonEmpty(task.PermissionMode, "default"))
		raw, err := ex.ReadFile(ctx, agents.RuntimeDir(att.WorktreePath)+"/settings.json", 0)
		if err != nil {
			return LaunchOpts{}, err
		}
		settings := map[string]any{}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &settings); err != nil {
				return LaunchOpts{}, fmt.Errorf("read task settings: %w", err)
			}
			delete(settings, "hooks")
			path := agents.RuntimeDir(att.WorktreePath) + "/interactive-settings.json"
			if err = ex.WriteFile(ctx, path, []byte(store.J(settings))); err != nil {
				return LaunchOpts{}, err
			}
			extraArgs = append(extraArgs, "--settings", path)
		}
		strictMCP := att.StrictMCP != 0
		mcpJSON := att.MCPJSON
		if att.MCPSnapshot == 0 {
			// Attempts created before the snapshot column existed may still have
			// the legacy worktree copy. Prefer it before falling back to the
			// current project, so takeover remains a continuation of that run.
			legacy, readErr := ex.ReadFile(ctx, agents.RuntimeDir(att.WorktreePath)+"/mcp.json", 0)
			if readErr != nil {
				return LaunchOpts{}, readErr
			}
			if len(legacy) > 0 {
				mcpJSON = string(legacy)
				strictMCP = project.StrictMCP != 0
			} else {
				mcpJSON = project.MCPJSON
				strictMCP = project.StrictMCP != 0
			}
		}
		mcp := store.UnjObj(mcpJSON)
		if len(mcp) > 0 || strictMCP {
			raw, mcpErr := agents.MCPPayload(mcp)
			if mcpErr != nil {
				return LaunchOpts{}, mcpErr
			}
			nonce, nonceErr := interactiveMCPNonce()
			if nonceErr != nil {
				return LaunchOpts{}, nonceErr
			}
			stateEnv, stateEnvErr := agents.MCPStateEnvPrefix(env)
			if stateEnvErr != nil {
				return LaunchOpts{}, stateEnvErr
			}
			result, installErr := ex.Run(ctx, stateEnv+agents.MCPInstallCommand(agents.TaskMCPRel(att.ID, nonce), raw), executor.RunOpts{Timeout: 20})
			if installErr != nil || !result.OK() {
				return LaunchOpts{}, fmt.Errorf("could not secure takeover MCP runtime")
			}
			configPath, pathErr := agents.PrivateMCPPath(result.Stdout)
			if pathErr != nil {
				return LaunchOpts{}, pathErr
			}
			extraArgs = append(extraArgs, "--mcp-config", configPath)
		}
		if strictMCP {
			extraArgs = append(extraArgs, "--strict-mcp-config")
		}
	} else {
		mcpJSON := att.MCPJSON
		if att.MCPSnapshot == 0 {
			mcpJSON = project.MCPJSON
		}
		mcp := store.UnjObj(mcpJSON)
		if len(mcp) > 0 {
			codexArgs, codexErr := agents.CodexMCPArgs(mcp)
			if codexErr != nil {
				return LaunchOpts{}, codexErr
			}
			extraArgs = append(extraArgs, codexArgs...)
		}
	}
	if task.Agent != "claude" && task.Agent != "" && att.StrictMCP != 0 {
		return LaunchOpts{}, fmt.Errorf("strict_mcp is unsupported for Codex additive configuration")
	}
	return LaunchOpts{ProjectID: &project.ID, TargetID: project.TargetID, Name: task.Title, Agent: task.Agent,
		Model: firstNonEmpty(att.Model, task.Model), Workdir: att.WorktreePath, ResumeID: resumeID, Prime: prime,
		Env: env, ExtraArgs: extraArgs, SkipProjectMCP: true,
		Yolo: task.Agent != "claude" && task.PermissionMode == "bypassPermissions"}, nil
}
