// Package delegation is the "Delegated builds" feature: a lead agent plans a
// piece of work and reviews the result, and a cheaper worker agent does the
// implementation in between, as a Lectern task in its own worktree.
//
// The lead never edits during the build and never watches it; it writes a
// brief, waits once, reads the diff once, and accepts or sends findings back
// for one correction cycle. What makes that safe is what Lectern already had:
// the worker runs as a task in a worktree, its diff is reviewable on its own,
// and a follow-up resumes the same worker in the same tree.
package delegation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// SettingKey is the settings-table key the configuration is stored under.
const SettingKey = "delegation"

// PresetAgent is the registry name of the worker the preset installs.
const PresetAgent = "flash-builder"

// PresetModel is the worker model the preset pins.
const PresetModel = "deepseek-flash"

// Settings is the whole feature's configuration. Off by default: a worker
// costs money per token and needs a credential, and no install should start
// sending a project's code to a second provider until someone chose that.
type Settings struct {
	Enabled bool `json:"enabled"`
	// WorkerAgent is a registry agent name; it must have a task definition.
	WorkerAgent string `json:"worker_agent"`
	// WorkerModel overrides the agent's default model when set.
	WorkerModel string `json:"worker_model"`
	// PermissionMode is the task permission mode the worker runs under.
	PermissionMode string `json:"permission_mode"`
	// CorrectionCycles is how many follow-ups the lead should send before it
	// reassesses scope. The tools do not enforce it; the guide states it.
	CorrectionCycles int `json:"correction_cycles"`
}

// Defaults are what an untouched install reports.
func Defaults() Settings {
	return Settings{PermissionMode: "acceptEdits", CorrectionCycles: 1}
}

// Load reads the stored settings, falling back to defaults field by field.
func Load(get func(key string) string) Settings {
	s := Defaults()
	raw := strings.TrimSpace(get(SettingKey))
	if raw == "" {
		return s
	}
	var stored Settings
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return s
	}
	if stored.PermissionMode == "" {
		stored.PermissionMode = s.PermissionMode
	}
	if stored.CorrectionCycles <= 0 {
		stored.CorrectionCycles = s.CorrectionCycles
	}
	return stored
}

// Validate rejects a configuration that could not run a build.
func (s Settings) Validate() error {
	switch s.PermissionMode {
	case "acceptEdits", "plan", "bypassPermissions":
	default:
		return fmt.Errorf("permission_mode must be acceptEdits, plan or bypassPermissions")
	}
	if s.CorrectionCycles < 0 || s.CorrectionCycles > 3 {
		return fmt.Errorf("correction_cycles must be 0-3")
	}
	if s.Enabled && strings.TrimSpace(s.WorkerAgent) == "" {
		return fmt.Errorf("choose a worker agent before enabling delegated builds")
	}
	return nil
}

// Encode is the stored form.
func (s Settings) Encode() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// PresetSpec is a worker that runs Codex against DeepSeek's Responses API
// with the Flash model, configured entirely through -c overrides so the
// operator's own ~/.codex/config.toml is never touched. The API key lives in
// the agent's environment, where the registry masks and retains it.
func PresetSpec(apiKey string) sessions.Spec {
	provider := []string{
		"-c", "model_provider=deepseek",
		"-c", `model_providers.deepseek.name="DeepSeek"`,
		"-c", `model_providers.deepseek.base_url="https://api.deepseek.com"`,
		"-c", `model_providers.deepseek.env_key="DEEPSEEK_API_KEY"`,
		"-c", `model_providers.deepseek.wire_api="responses"`,
		"-c", `model_reasoning_effort="high"`,
	}
	env := map[string]string{}
	if apiKey != "" {
		env["DEEPSEEK_API_KEY"] = apiKey
	}
	return sessions.Spec{
		Name:      PresetAgent,
		Command:   "codex",
		Args:      append([]string(nil), provider...),
		ModelFlag: "-m",
		PromptArg: true,
		Env:       env,
		Task: &sessions.TaskSpec{
			Command:        "codex",
			Args:           append(append([]string(nil), provider...), "exec", "--json"),
			PromptTemplate: "{prompt}",
			OutputMode:     "codex",
			PermissionArgs: map[string][]string{
				"acceptEdits":       {"--sandbox", "workspace-write"},
				"plan":              {"--sandbox", "read-only"},
				"bypassPermissions": {"--dangerously-bypass-approvals-and-sandbox"},
			},
			ResumeArgs: []string{"resume", "{id}"},
		},
	}
}

// WorkerPrompt is what the worker is asked to do: the lead's brief, framed so
// the worker owns discovery, implementation and verification within it and
// returns one report the lead can review without reading a transcript.
func WorkerPrompt(brief string) string {
	return strings.TrimSpace(workerPreamble) + "\n\n---\n\n" + strings.TrimSpace(brief) + "\n"
}

const workerPreamble = `
You are the implementation worker for a brief written by a lead agent. The
lead owns scope, architecture, acceptance and integration; you own everything
needed to complete the brief inside its stated file scope: repository
discovery, implementation, tests, debugging and the verification commands it
names. Do not send routine exploration back to the lead, and do not redesign
the system: a missing contract or a contradiction in the brief is a blocker to
report, not an invitation.

Rules:
- Only modify paths inside the brief's scope. Do not weaken tests, types,
  validation, linting or security checks to make verification pass.
- Do not introduce undeclared dependencies, edit credentials or production
  configuration, or run migrations against real data.
- Do not commit, push, merge or deploy. Do not spawn other agents.
- Run every verification command the brief names. Do not claim a command ran
  when it did not, and do not report success only because a command exited 0.
- Stop after repeated failures of the same approach and report the blocker
  with evidence instead of widening the brief.

Finish with ONE completion report, nothing after it:

STATUS: ready_for_review | blocked | failed
CHANGED: each path and what changed in it, one line each
VERIFIED: each command, its exit status and the salient result
RISKS: anything the lead should look at first
DECISIONS FOR THE LEAD: questions only the lead can answer, if any
`
