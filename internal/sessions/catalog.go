package sessions

// CatalogPreset is a one-click starting point for Settings → Agents: a Spec
// for a popular third-party coding CLI, plus the metadata a reviewer needs to
// trust it. Unlike Builtins(), lectern does not maintain these binaries —
// their flags are only as good as the day someone last read the vendor's own
// docs, and a preset that quietly goes stale is worse than one that says so.
//
// Every preset below cites the exact page(s) it was read from in Source, and
// lists in Unverified any Spec field that could not be confirmed there —
// those fields hold a best guess (usually empty, meaning "not offered"), and
// the UI must present them as unconfirmed rather than as researched fact.
// Re-check Source before trusting a preset whose VerifiedAt has aged.
type CatalogPreset struct {
	Spec
	// DisplayName is UI copy; it need not match the binary name.
	DisplayName string `json:"display_name"`
	// Description is one line of UI copy: what the CLI is, and anything a
	// reviewer should know before adding it (e.g. a flag being experimental).
	Description string `json:"description"`
	// InstallHint is copy-pasteable install guidance shown when Installed()
	// is false for the launch host.
	InstallHint string `json:"install_hint"`
	// Source is where the flags below were read, so a stale entry can be
	// re-checked against the same page instead of re-discovered from scratch.
	Source string `json:"source"`
	// Unverified lists Spec JSON field names (e.g. "model_flag",
	// "resume_args") that are a best guess rather than a confirmed fact as of
	// VerifiedAt. A field absent from this list, including one left at its
	// zero value, was confirmed against Source to genuinely be
	// unsupported — not merely unresearched.
	Unverified []string `json:"unverified,omitempty"`
	// VerifiedAt is when Source was last read for this entry (YYYY-MM-DD).
	// Flags rot; re-verify anything older than a few months before trusting
	// a capability this preset claims.
	VerifiedAt string `json:"verified_at"`
}

// Catalog lists the popular third-party coding CLIs Settings → Agents offers
// as a one-click "Add from catalog" starting point, on top of the "Custom
// command…" escape hatch the editor already has. Adding one here never
// changes behavior for an operator who already saved a custom agent under
// the same name — ParseSpecs only ever reads what is in the "agents"
// setting; a preset is just what pre-fills the editor the first time.
//
// A preset that documents Agent Client Protocol support (agentclientprotocol.com)
// gets an ACP field in addition to its normal interactive launch fields:
// per docs/acp.md, ACP only ever replaces the Task backend for headless work
// (tasks/routines/best-of-N/evals) — interactive sessions stay tmux-based
// through Command/Args/PromptArg exactly like every other agent, so a preset
// can and should carry both.
func Catalog() []CatalogPreset {
	return []CatalogPreset{
		{
			Spec: Spec{Name: "opencode", Command: "opencode",
				ModelFlag: "--model", ResumeArgs: []string{"--continue"},
				ResumeIDArgs:  []string{"--session", "{id}"},
				YoloArgs:      []string{"--auto"},
				ModelsCommand: "{bin} models",
				ACP:           &ACPSpec{Command: "opencode", Args: []string{"acp"}}},
			DisplayName: "OpenCode",
			Description: "Terminal coding agent from the OpenCode/SST team. `--auto` approves permissions " +
				"that are not explicitly denied, which is close to but not identical to a full bypass.",
			InstallHint: "curl -fsSL https://opencode.ai/install | bash  (or: npm i -g opencode-ai)",
			Source:      "https://opencode.ai/docs/cli/ ; https://github.com/anomalyco/opencode",
			Unverified:  []string{"prompt_arg"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "aider", Command: "aider", ModelFlag: "--model",
				YoloArgs: []string{"--yes"},
				Task:     &TaskSpec{PromptTemplate: "--message {prompt}", OutputMode: "plain"}},
			DisplayName: "Aider",
			Description: "AI pair programming in the terminal. Restores its own chat history automatically " +
				"when relaunched in the same directory — there is no separate --resume flag to send.",
			InstallHint: "python -m pip install aider-install && aider-install",
			Source:      "https://aider.chat/docs/scripting.html ; https://github.com/Aider-AI/aider",
			Unverified:  []string{"prompt_arg"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "goose", Command: "goose", Args: []string{"session"},
				ResumeArgs: []string{"--resume"},
				Env:        map[string]string{"GOOSE_MODE": "auto"},
				ACP:        &ACPSpec{Command: "goose", Args: []string{"acp"}}},
			DisplayName: "Goose",
			Description: "Block's open-source terminal agent. Auto-approve is set via the GOOSE_MODE=auto " +
				"environment variable, confirmed in source rather than a flag reference — re-check before relying on it.",
			InstallHint: "curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | bash",
			Source:      "https://goose-docs.ai/docs/guides/goose-cli-commands ; github.com/aaif-goose/goose crates/goose/src/config/mod.rs",
			Unverified:  []string{"args", "model_flag", "resume_id_args", "yolo_args"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "amp", Command: "amp",
				Task: &TaskSpec{PromptTemplate: "-x {prompt}", OutputMode: "plain"}},
			DisplayName: "Amp",
			Description: "Sourcegraph's Amp. Public docs only document -x/--execute for a one-shot prompt; " +
				"model selection, resume and an auto-approve flag are not documented anywhere this was checked.",
			InstallHint: "see https://ampcode.com/manual for installation — no install command found in public docs",
			Source:      "https://ampcode.com/manual ; https://ampcode.com/docs/cli/execute-mode",
			Unverified:  []string{"model_flag", "resume_args", "resume_id_args", "yolo_args", "install_hint"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "cursor-agent", Command: "cursor-agent", PromptArg: true,
				ModelFlag: "--model", ResumeArgs: []string{"--continue"},
				ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs:     []string{"--force"}},
			DisplayName: "Cursor Agent CLI",
			Description: "Cursor's terminal agent (`cursor-agent`). It documents an `agent acp` mode as a " +
				"\"hidden\" advanced command for custom ACP clients, so it is not wired as the task backend here.",
			InstallHint: "curl https://cursor.com/install -fsS | bash",
			Source:      "https://cursor.com/docs/cli/reference/parameters",
			Unverified:  []string{},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "copilot", Command: "copilot",
				ResumeArgs: []string{"--continue"},
				YoloArgs:   []string{"--allow-all"},
				Task:       &TaskSpec{PromptTemplate: "-p {prompt}", OutputMode: "plain"}},
			DisplayName: "GitHub Copilot CLI",
			Description: "GitHub's terminal Copilot agent. `--resume` opens an interactive picker rather than " +
				"taking an id directly, so only resume-last (--continue) is wired; model selection is a /model " +
				"slash command with no documented CLI flag.",
			InstallHint: "npm install -g @github/copilot  (or: curl -fsSL https://gh.io/copilot-install | bash)",
			Source:      "https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli ; https://github.com/github/copilot-cli",
			Unverified:  []string{"model_flag", "resume_id_args", "prompt_arg"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "qwen", Command: "qwen", ModelFlag: "-m",
				YoloArgs: []string{"--yolo"},
				Task:     &TaskSpec{PromptTemplate: "-p {prompt}", OutputMode: "plain"}},
			DisplayName: "Qwen Code",
			Description: "Alibaba's terminal coding agent, originally forked from Gemini CLI. --yolo is " +
				"confirmed in its source; the model flag is carried over from that ancestry but its own docs " +
				"reference (qwenlm.github.io/qwen-code-docs) 404'd, so treat -m as unconfirmed.",
			InstallHint: "npm install -g @qwen-code/qwen-code@latest",
			Source:      "https://github.com/QwenLM/qwen-code (README + code search for --yolo)",
			Unverified:  []string{"model_flag", "prompt_arg"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "crush", Command: "crush",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				YoloArgs: []string{"--yolo"}, ModelsCommand: "{bin} models",
				Task: &TaskSpec{Args: []string{"run"}, PromptTemplate: "{prompt}", OutputMode: "plain",
					ResumeArgs: []string{"--continue"}}},
			DisplayName: "Crush",
			Description: "Charm's terminal agent. The root command has no model flag (only `crush run` and " +
				"`crush models` do); interactive model choice is a runtime picker, not a launch flag.",
			InstallHint: "brew install charmbracelet/tap/crush  (or: npm install -g @charmland/crush)",
			Source:      "github.com/charmbracelet/crush internal/cmd/{root,run,models}.go ; README",
			Unverified:  []string{},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "kimi", Command: "kimi",
				ResumeArgs: []string{"-c"},
				ACP:        &ACPSpec{Command: "kimi", Args: []string{"acp"}}},
			DisplayName: "Kimi Code CLI",
			Description: "Moonshot AI's terminal agent. No CLI model flag or auto-approve flag is documented — " +
				"model choice is a /model slash command, and every edit needs approval unless driven via ACP. " +
				"`-p` is a real one-shot headless flag but is left unwired here since ACP already covers " +
				"background work and the two backends are mutually exclusive.",
			InstallHint: "curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash",
			Source:      "https://moonshotai.github.io/kimi-code/en/guides/getting-started ; https://github.com/MoonshotAI/kimi-code",
			Unverified:  []string{"model_flag", "yolo_args", "prompt_arg"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "cline", Command: "cline", Args: []string{"-i"}, PromptArg: true,
				ModelFlag: "--model", YoloArgs: []string{"--yolo"},
				ACP: &ACPSpec{Command: "cline", Args: []string{"--acp"}}},
			DisplayName: "Cline CLI",
			Description: "Cline's new standalone terminal CLI (separate from the VS Code extension). " +
				"`cline history` lists saved sessions but the exact resume syntax is not detailed in its README.",
			InstallHint: "npm i -g cline",
			Source:      "https://github.com/cline/cline/blob/main/apps/cli/README.md",
			Unverified:  []string{"resume_args"},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "claude-code-acp", Command: "claude-code-acp",
				ACP: &ACPSpec{Command: "npx", Args: []string{"-y", "@zed-industries/claude-code-acp"}}},
			DisplayName: "Claude Code (ACP)",
			Description: "Zed's ACP adapter for Claude Code — needs a Claude Code login exactly like the " +
				"built-in claude agent. See docs/acp.md.",
			InstallHint: "needs npx on the launch host; no separate global install",
			Source:      "docs/acp.md",
			Unverified:  []string{},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "codex-acp", Command: "codex-acp",
				ACP: &ACPSpec{Command: "npx", Args: []string{"-y", "@zed-industries/codex-acp"}}},
			DisplayName: "Codex (ACP)",
			Description: "Zed's ACP adapter for Codex — ships a platform-specific native binary via " +
				"optionalDependencies. See docs/acp.md.",
			InstallHint: "needs npx on the launch host; no separate global install",
			Source:      "docs/acp.md",
			Unverified:  []string{},
			VerifiedAt:  "2026-09-26",
		},
		{
			Spec: Spec{Name: "gemini-acp", Command: "gemini",
				ACP: &ACPSpec{Command: "gemini", Args: []string{"--experimental-acp"}}},
			DisplayName: "Gemini CLI (ACP)",
			Description: "Gemini CLI's own native ACP mode — no adapter package, but gemini itself must " +
				"already be installed (it is also lectern's built-in `gemini` agent). See docs/acp.md.",
			InstallHint: "npm install -g @google/gemini-cli",
			Source:      "docs/acp.md ; https://github.com/google-gemini/gemini-cli",
			Unverified:  []string{},
			VerifiedAt:  "2026-09-26",
		},
	}
}

// FindCatalogPreset looks up one catalog entry by name for the "Add from
// catalog" flow.
func FindCatalogPreset(name string) (CatalogPreset, bool) {
	for _, p := range Catalog() {
		if p.Name == name {
			return p, true
		}
	}
	return CatalogPreset{}, false
}
