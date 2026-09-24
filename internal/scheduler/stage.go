package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/ctxbundle"
	"github.com/JeremiahM37/lectern/v2/internal/delegation"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// taskLaunchConfig resolves the configured task adapter once, or returns the
// immutable snapshot stored when this attempt was queued.
func (s *Scheduler) taskLaunchConfig(att *store.Attempt, c *runCtx) (agents.TaskLaunchConfig, error) {
	if att.LaunchConfigJSON != "" {
		var saved agents.TaskLaunchConfig
		if err := json.Unmarshal([]byte(att.LaunchConfigJSON), &saved); err != nil || saved.Version != 1 || saved.Agent == "" {
			return saved, fmt.Errorf("attempt task launch configuration is unreadable")
		}
		if saved.Agent != effAgent(c, att) {
			return saved, fmt.Errorf("attempt task launch agent no longer matches its task")
		}
		return saved, nil
	}
	agent := effAgent(c, att)
	var def agents.TaskDefinition
	if s.AgentDefinitions != nil {
		var ok bool
		def, ok = s.AgentDefinitions()[agent]
		if !ok {
			return agents.TaskLaunchConfig{}, fmt.Errorf("agent %q has no non-interactive task definition", agent)
		}
	} else {
		// Legacy test schedulers and old databases still use the built-in adapters.
		switch agent {
		case "claude", "codex", "gemini":
			def = agents.TaskDefinition{Name: agent, Builtin: true}
		default:
			return agents.TaskLaunchConfig{}, fmt.Errorf("agent %q has no non-interactive task definition", agent)
		}
	}
	env := map[string]string{}
	if s.Creds != nil {
		for k, v := range s.Creds.BaseAgentEnv() {
			env[k] = v
		}
	}
	for k, v := range def.Env {
		env[k] = v
	}
	for k, v := range projectEnv(c.Project) {
		env[k] = v
	}
	return agents.TaskLaunchConfig{Version: 1, Agent: agent, Definition: def, Env: env}, nil
}

// launchKW is what stageRuntime tells the launcher about the files it wrote.
type launchKW struct {
	SettingsPath string
	MCPConfig    string
	StrictMCP    bool
	ExtraArgs    []string
	Agent        string
	Env          map[string]string
	Definition   *agents.TaskDefinition
	// Prompt is the fully composed prompt this call staged to prompt.md
	// (memory recall, project notes, context bundle, task footer). A
	// structured-driver launch needs it verbatim rather than re-reading the
	// file back, since a standalone drivers.Run caller has no stageRuntime.
	Prompt string
}

// EffectiveMCPServers lists the server names this attempt can actually reach, so
// the parity profile grants exactly those.
//
// A local agent runs as the control plane user and inherits that user's MCP
// servers; a remote one only ever has what the project ships in `mcp`. Granting
// a name the agent does not have is harmless, but claiming it would be a lie —
// so remote targets get the project's own servers and nothing more.
func EffectiveMCPServers(hostConfigPath string, project *store.Project, target *store.Target,
	mcp map[string]any) []string {
	names := []string{}
	if inner, ok := mcp["mcpServers"].(map[string]any); ok {
		for k := range inner {
			names = append(names, k)
		}
	} else {
		for k := range mcp {
			names = append(names, k)
		}
	}
	if HostLocalKinds[target.Kind] && project.StrictMCP == 0 {
		names = append(names, agents.HostMCPServers(hostConfigPath)...)
	}
	return names
}

// stageRuntime writes everything the agent reads at startup and returns the
// launch flags that point at it.
//
// Shared by the worktree and sandbox paths. They drifted apart once — sandbox
// runs silently skipped project memory — and a context or permission feature
// landing on only one of them shows up as nothing but worse output.
func (s *Scheduler) stageRuntime(ctx context.Context, ex executor.Executor, workdir string,
	att *store.Attempt, c *runCtx) (launchKW, error) {
	var kw launchKW
	launchConfig, err := s.taskLaunchConfig(att, c)
	if err != nil {
		return kw, err
	}
	kw.Agent = launchConfig.Agent
	kw.Env = launchConfig.Env
	if !launchConfig.Definition.Builtin {
		def := launchConfig.Definition
		kw.Definition = &def
	}
	rt := agents.RuntimeDir(workdir)
	isReviewer := c.Task.CreatedBy == "reviewer-gate"

	prompt := firstNonEmpty(att.Prompt, c.Task.Prompt, c.Task.Title)
	if !isReviewer {
		if recalled := memory.Automatic(ctx, s.Memory, c.Project.Name, prompt, nil, c.Project.MemoryTopic); recalled.Context != "" {
			prompt = recalled.Context + "\n" + prompt
		}
		prompt = memory.ProjectHint(s.Memory, c.Project.Name, c.Project.MemoryTopic) + prompt
		notes, err := s.DB.ProjectNotes(c.Project.ID, 12)
		if err == nil && len(notes) > 0 {
			texts := make([]string, 0, len(notes))
			for _, n := range notes {
				texts = append(texts, n.Note)
			}
			prompt = BuildNotesPrefix(texts) + prompt
		}
		prompt += AgentTaskFooter
	}

	// context bundle: target-wide files first (host conventions), then the
	// project's own. Staged from the CONTROL PLANE's filesystem, so a remote
	// target gets exactly what a local one does.
	patterns := append(store.UnjStrings(c.Target.ContextJSON),
		store.UnjStrings(c.Project.ContextJSON)...)
	files, skipped := ctxbundle.Collect(patterns)
	for _, f := range files {
		if err := ex.WriteFile(ctx, fmt.Sprintf("%s/%s/%s", rt, ctxbundle.Subdir, f.Name),
			f.Data); err != nil {
			return kw, err
		}
	}
	if len(files) > 0 {
		if err := ex.WriteFile(ctx, fmt.Sprintf("%s/%s/%s", rt, ctxbundle.Subdir, ctxbundle.IndexName),
			[]byte(ctxbundle.IndexMarkdown(files, skipped))); err != nil {
			return kw, err
		}
	}
	if len(skipped) > 0 {
		s.Log.Warn("context not staged", "attempt", att.ID, "notes", skipped)
	}
	prompt = ctxbundle.PromptPrefix(files, skipped) + prompt

	if err := ex.WriteFile(ctx, rt+"/prompt.md", []byte(prompt)); err != nil {
		return kw, err
	}
	kw.Prompt = prompt
	// task-filing kit: lets the agent put follow-up cards on the board
	if err := ex.WriteFile(ctx, rt+"/lec.py", hooks.ADK); err != nil {
		return kw, err
	}
	if err := ex.WriteFile(ctx, rt+"/env", []byte(fmt.Sprintf(
		"ADK_URL=%s\nADK_TOKEN=%s\n", s.Cfg.BaseURL, att.Token))); err != nil {
		return kw, err
	}

	// per-project MCP: the host user's config is absent on ssh/pct/sandbox
	// targets, so without this a remote agent has strictly fewer tools than a
	// local one
	mcp := store.UnjObj(c.Project.MCPJSON)
	agent := launchConfig.Agent
	if delegation.IsLead(store.UnjStrings(c.Task.LabelsJSON)) {
		// An orchestrated attempt runs the lead, which needs delegate_build and
		// friends. They come from this binary in MCP mode, aimed at this server,
		// so the operator never has to register anything for it to work.
		mcp = withLeadMCP(mcp, agent, s.leadExecutable(), s.Cfg.BaseURL, s.Cfg.AuthToken)
		for k, v := range delegation.LeadEnv(agent) {
			if kw.Env == nil {
				kw.Env = map[string]string{}
			}
			kw.Env[k] = v
		}
	}
	if !launchConfig.Definition.Builtin && (len(mcp) > 0 || c.Project.StrictMCP != 0) {
		return kw, fmt.Errorf("agent %q has no MCP capability mapping; configure MCP flags in its task definition or use a built-in agent", agent)
	}
	// Snapshot the complete project launch policy before branching by agent.
	// Takeover must continue the attempt even if the project is edited later;
	// the explicit marker distinguishes a captured empty declaration from an
	// old row that predates this snapshot feature.
	if err := s.DB.Update("attempts", att.ID, map[string]any{
		"mcp_json": nz(c.Project.MCPJSON, "{}"), "strict_mcp": c.Project.StrictMCP,
		"mcp_snapshot": 1,
	}); err != nil {
		return kw, err
	}
	if agent == "codex" && c.Project.StrictMCP != 0 {
		return kw, fmt.Errorf("strict_mcp is unsupported for Codex additive configuration")
	}
	if agent == "claude" && (len(mcp) > 0 || c.Project.StrictMCP != 0) {
		payload := mcp
		if _, ok := mcp["mcpServers"]; !ok {
			payload = map[string]any{"mcpServers": mcp}
		}
		raw, _ := json.Marshal(payload)
		nonce, err := randomToken()
		if err != nil {
			return kw, err
		}
		stateEnv, err := agents.MCPStateEnvPrefix(projectEnv(c.Project))
		if err != nil {
			return kw, err
		}
		result, err := ex.Run(ctx, stateEnv+agents.MCPInstallCommand(agents.TaskMCPRel(att.ID, nonce), raw), executor.RunOpts{Timeout: 20})
		if err != nil || !result.OK() {
			if err != nil {
				return kw, err
			}
			return kw, fmt.Errorf("could not secure task MCP runtime")
		}
		configPath, err := agents.PrivateMCPPath(result.Stdout)
		if err != nil {
			return kw, err
		}
		kw.MCPConfig = configPath
		kw.StrictMCP = c.Project.StrictMCP != 0
	} else if agent == "codex" && len(mcp) > 0 {
		var err error
		kw.ExtraArgs, err = agents.CodexMCPArgs(mcp)
		if err != nil {
			return kw, err
		}
	}

	// settings.json is written for EVERY permission mode, not just the gated one:
	// headless has no prompt, so a tool the rules don't grant is denied outright
	// and the operator never learns why. Rules are how acceptEdits gets Bash.
	gated := effPermissionMode(c, att) == "default"
	if gated {
		if err := ex.WriteFile(ctx, rt+"/hook.py", hooks.Hook); err != nil {
			return kw, err
		}
	}
	memoryDir := c.Target.MemoryDir
	if agent != "claude" {
		memoryDir = "" // the memory layout is Claude Code's; nobody else reads it
	}
	perms, err := agents.ParsePermissions(c.Project.PermissionsJSON)
	if err != nil {
		return kw, err
	}
	settings, err := agents.BuildSettings(agents.SettingsInput{
		BaseURL:       s.Cfg.BaseURL,
		Token:         att.Token,
		Gated:         gated,
		Permissions:   perms,
		Matcher:       firstNonEmpty(c.Project.GateMatcher, agents.DefaultGateMatcher),
		Profile:       firstNonEmpty(c.Project.CapabilityProfile, "restricted"),
		MCPServers:    EffectiveMCPServers(s.Cfg.HostClaudeConfig, c.Project, c.Target, mcp),
		MemoryDir:     memoryDir,
		ExpireSeconds: int(s.Cfg.ApprovalExpire.Seconds()),
	})
	if err != nil {
		return kw, err
	}
	raw, _ := json.Marshal(settings)
	if err := ex.WriteFile(ctx, rt+"/settings.json", raw); err != nil {
		return kw, err
	}

	// reused worktrees (follow-ups, reviewer gate) carry the PREVIOUS attempt's
	// runtime files — a stale exit_code finalises this attempt instantly with the
	// previous run's output, which mock-only tests happily hide
	if _, err := ex.Run(ctx, fmt.Sprintf("rm -f %s/exit_code %s/events.jsonl %s/stderr.log",
		rt, rt, rt), executor.RunOpts{Timeout: 20}); err != nil {
		return kw, err
	}

	if memoryDir != "" {
		r, err := ex.Run(ctx, agents.MemoryLinkCommand(workdir, memoryDir),
			executor.RunOpts{Timeout: 30})
		// a broken link degrades the agent's knowledge; it is never worth failing
		// the run over
		if err != nil {
			s.Log.Warn("memory link failed", "attempt", att.ID, "err", err)
		} else if !r.OK() {
			s.Log.Warn("memory link refused", "attempt", att.ID,
				"detail", firstNonEmpty(r.Stderr, r.Stdout))
		}
	}

	kw.SettingsPath = agents.SettingsRel
	return kw, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// withLeadMCP adds the Lectern server to a project's MCP declaration for an
// orchestrated attempt. A project that declares its own "lectern" server keeps
// it: the operator may have pointed it somewhere deliberately.
func withLeadMCP(mcp map[string]any, agent, executable, baseURL, token string) map[string]any {
	out := map[string]any{}
	servers := out
	if inner, ok := mcp["mcpServers"].(map[string]any); ok {
		servers = map[string]any{}
		for k, v := range inner {
			servers[k] = v
		}
		out["mcpServers"] = servers
	} else {
		for k, v := range mcp {
			out[k] = v
		}
	}
	if _, declared := servers[delegation.MCPServerName]; declared {
		return out
	}
	for k, v := range delegation.LeadMCP(agent, executable, baseURL, token) {
		servers[k] = v
	}
	return out
}

// leadExecutable is the binary the lead's MCP server runs: this process,
// which is the server the lead talks to. The path is resolved once at startup
// by the caller through LeadBinary when the executable cannot be trusted
// (tests), and falls back to os.Executable.
func (s *Scheduler) leadExecutable() string {
	if s.LeadBinary != "" {
		return s.LeadBinary
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "lectern"
}
