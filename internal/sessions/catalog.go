package sessions

import "strings"

// CatalogPreset is a one-click starting point for Settings → Agents: a Spec
// for a popular third-party coding CLI, plus the metadata a reviewer needs to
// trust it. Unlike Builtins(), lectern does not maintain these binaries —
// their flags are only as good as the day someone last checked them, and a
// preset that quietly goes stale is worse than one that says so.
//
// Every preset below says how it was checked in VerifiedBy (the installed
// CLI version whose --help it was read from, or "docs" when the CLI could not
// be installed without an account), cites the page(s) it was read from in
// Source, and lists in Unverified any Spec field that could not be confirmed
// — those fields hold a best guess and the UI presents them as unconfirmed.
// Re-check before trusting a preset whose VerifiedAt has aged.
type CatalogPreset struct {
	Spec
	// DisplayName is UI copy; it need not match the binary name.
	DisplayName string `json:"display_name"`
	// Vendor is who ships the CLI; the catalog search matches it.
	Vendor string `json:"vendor"`
	// Group sections the catalog list so ~30 entries stay scannable.
	Group string `json:"group"`
	// Icon is a monogram badge. Lectern ships no third-party logos: they are
	// trademarks, and fetching favicons would call out to a third party
	// every time Settings opens.
	Icon CatalogIcon `json:"icon"`
	// Homepage is the CLI's own documentation.
	Homepage string `json:"homepage"`
	// Description is one line of UI copy: what the CLI is, and anything a
	// reviewer should know before adding it — especially what it cannot do.
	Description string `json:"description"`
	// InstallHint is copy-pasteable install guidance shown when Installed()
	// is false for the launch host.
	InstallHint string `json:"install_hint"`
	// SessionsHint says where the CLI's own session ids are listed, for an
	// exact resume or fork. Lectern captures native ids itself only for the
	// built-in agents.
	SessionsHint string `json:"sessions_hint,omitempty"`
	// Source is where the flags below were read, so a stale entry can be
	// re-checked against the same page instead of re-discovered from scratch.
	Source string `json:"source"`
	// VerifiedBy is "<binary> <version>" when the flags were checked against
	// that installed CLI's --help (and, for ACP, a real initialize handshake),
	// or "docs" when only the vendor's documentation could be read.
	VerifiedBy string `json:"verified_by"`
	// Unverified lists Spec JSON field names (e.g. "model_flag",
	// "resume_args") that are a best guess rather than a confirmed fact as of
	// VerifiedAt. A field absent from this list, including one left at its
	// zero value, was confirmed to genuinely be unsupported — not merely
	// unresearched.
	Unverified []string `json:"unverified,omitempty"`
	// VerifiedAt is when the entry was last checked (YYYY-MM-DD).
	VerifiedAt string `json:"verified_at"`
}

// CatalogIcon is a one- or two-letter badge on a brand-neutral color.
type CatalogIcon struct {
	Glyph string `json:"glyph"`
	Color string `json:"color"`
}

// Catalog groups, in display order.
const (
	CatalogGroupPopular   = "Popular"
	CatalogGroupVendor    = "Vendor agents"
	CatalogGroupCommunity = "Open source & community"
	CatalogGroupAdapters  = "ACP adapters"
)

// CatalogGroups is the display order of the groups above.
func CatalogGroups() []string {
	return []string{CatalogGroupPopular, CatalogGroupVendor, CatalogGroupCommunity, CatalogGroupAdapters}
}

const catalogVerifiedAt = "2026-09-27"

// Catalog lists the coding CLIs Settings → Agents offers as a one-click
// "Add from catalog" starting point, on top of the "Custom command…" escape
// hatch the editor already has. Adding one here never changes behavior for an
// operator who already saved a custom agent under the same name — ParseSpecs
// only ever reads what is in the "agents" setting; a preset is just what
// pre-fills the editor the first time.
//
// A preset whose CLI speaks the Agent Client Protocol gets an ACP field in
// addition to its interactive launch fields: per docs/acp.md, ACP only ever
// replaces the Task backend for headless work (tasks/routines/best-of-N/evals)
// — interactive sessions stay tmux-based through Command/Args/PromptArg(s)
// exactly like every other agent. ACP is preferred over a Task command
// whenever both exist, because it carries every permission mode and project
// MCP servers itself.
// openclaudeTrust is claudeTrust for OpenClaude's own config file: the same
// projects[dir].hasTrustDialogAccepted key, in ~/.openclaude.json (or
// $OPENCLAUDE_CONFIG_DIR). Without it the first launch in a folder stops at
// the trust dialog before the opening message.
var openclaudeTrust = strings.NewReplacer("CLAUDE_CONFIG_DIR", "OPENCLAUDE_CONFIG_DIR", ".claude.json", ".openclaude.json").Replace(claudeTrust)

func Catalog() []CatalogPreset {
	return []CatalogPreset{
		// ---- Popular -----------------------------------------------------
		{
			Spec: Spec{Name: "opencode", Sessions: &SessionsSpec{Command: "{bin} session list --format json", ID: "id", Dir: "directory", Created: "created", Updated: "updated", Title: "title"}, Command: "opencode", ModelFlag: "--model",
				PromptArgs: []string{"--prompt", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				ForkArgs: []string{"--session", "{id}", "--fork"},
				YoloArgs: []string{"--auto"},
				ACP:      &ACPSpec{Command: "opencode", Args: []string{"acp"}}},
			DisplayName: "OpenCode", Vendor: "SST / Anomaly", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"OC", "#3f3f46"}, Homepage: "https://opencode.ai/docs/cli/",
			Description: "Open-source terminal agent. `--auto` approves every permission that is not explicitly denied. " +
				"Project MCP servers and skills are passed to interactive sessions.",
			InstallHint:  "curl -fsSL https://opencode.ai/install | bash  (or: npm i -g opencode-ai)",
			SessionsHint: "opencode session list",
			Source:       "opencode --help ; https://opencode.ai/docs/cli/",
			VerifiedBy:   "opencode 1.18.32", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "cursor-agent", Command: "cursor-agent", PromptArg: true,
				ModelFlag: "--model", ResumeArgs: []string{"--continue"},
				ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs:     []string{"--force"},
				ACP:          &ACPSpec{Command: "cursor-agent", Args: []string{"acp"}}},
			DisplayName: "Cursor Agent CLI", Vendor: "Cursor", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"Cu", "#18181b"}, Homepage: "https://cursor.com/docs/cli/overview",
			Description: "Cursor's terminal agent. `cursor-agent acp` is not listed in --help (Cursor documents it as an " +
				"advanced mode) but answers the ACP handshake.",
			InstallHint:  "curl https://cursor.com/install -fsS | bash",
			SessionsHint: "cursor-agent ls",
			Source:       "cursor-agent --help ; https://cursor.com/docs/cli/reference/parameters",
			VerifiedBy:   "cursor-agent 2026.09.26-dd393fe", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "copilot", SessionIDArgs: []string{"--session-id", "{id}"}, Command: "copilot", ModelFlag: "--model",
				PromptArgs: []string{"-i", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume={id}"},
				YoloArgs: []string{"--yolo"},
				ACP:      &ACPSpec{Command: "copilot", Args: []string{"--acp"}}},
			DisplayName: "GitHub Copilot CLI", Vendor: "GitHub", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"GH", "#24292f"}, Homepage: "https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli",
			Description: "GitHub's terminal agent. `-i` opens the session with the first prompt; `-p` would run one-shot " +
				"and exit. Project MCP servers and skills are passed to interactive sessions.",
			InstallHint:  "npm install -g @github/copilot",
			SessionsHint: "`copilot --resume` opens a picker; ids live under ~/.copilot/session-state/",
			Source:       "copilot --help ; https://github.com/github/copilot-cli",
			VerifiedBy:   "copilot 1.0.88", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "amp", Command: "amp",
				ResumeArgs:   []string{"threads", "continue", "--last"},
				ResumeIDArgs: []string{"threads", "continue", "{id}"},
				Task:         &TaskSpec{Args: []string{"--stream-json"}, PromptTemplate: "-x {prompt}", OutputMode: "claude"}},
			DisplayName: "Amp", Vendor: "Amp (Sourcegraph)", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"A", "#be123c"}, Homepage: "https://ampcode.com/manual",
			Description: "Amp has no model flag (`--mode low|medium|high|ultra` picks model and tools together) and no " +
				"auto-approve flag: approvals are off only with `amp.dangerouslyAllowAll` in its settings file. " +
				"Tasks use `-x` with Claude-compatible stream JSON. Project MCP servers and skills are passed through.",
			InstallHint:  "curl -fsSL https://ampcode.com/install.sh | bash",
			SessionsHint: "amp threads list",
			Source:       "amp --help ; https://ampcode.com/docs/cli ; https://ampcode.com/docs/customize/skills",
			VerifiedBy:   "amp 0.0.1790496040", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "qwen", SessionIDArgs: []string{"--session-id", "{id}"}, Sessions: &SessionsSpec{Command: "{bin} sessions list --json --limit 200", ID: "sessionId", Dir: "cwd", Created: "startTime", Updated: "mtime", Title: "prompt"}, Command: "qwen", ModelFlag: "-m",
				PromptArgs: []string{"-i", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				ForkArgs: []string{"--resume", "{id}", "--fork-session"},
				YoloArgs: []string{"--yolo"},
				ACP:      &ACPSpec{Command: "qwen", Args: []string{"--acp"}}},
			DisplayName: "Qwen Code", Vendor: "Alibaba Qwen", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"Q", "#6d28d9"}, Homepage: "https://github.com/QwenLM/qwen-code",
			Description: "Alibaba's terminal agent. A bare positional prompt runs one-shot, so the first message goes " +
				"through `-i`. Project MCP servers and skills are passed to interactive sessions.",
			InstallHint:  "npm install -g @qwen-code/qwen-code@latest",
			SessionsHint: "qwen sessions list",
			Source:       "qwen --help ; https://github.com/QwenLM/qwen-code",
			VerifiedBy:   "qwen 0.24.6", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "kimi", Command: "kimi", ModelFlag: "-m",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				YoloArgs: []string{"--auto"},
				ACP:      &ACPSpec{Command: "kimi", Args: []string{"acp"}}},
			DisplayName: "Kimi Code CLI", Vendor: "Moonshot AI", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"K", "#0f766e"}, Homepage: "https://moonshotai.github.io/kimi-code/",
			Description: "Moonshot's terminal agent. `--auto` is its never-ask mode (`--yolo` still asks for risky " +
				"actions). No opening-prompt flag keeps the session interactive, so the first message is typed in.",
			InstallHint:  "curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash  (or: npm i -g @moonshot-ai/kimi-code)",
			SessionsHint: "kimi session list (sessions are scoped to the working directory)",
			Source:       "kimi --help ; https://moonshotai.github.io/kimi-code/",
			VerifiedBy:   "kimi 2.1.1", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "goose", Sessions: &SessionsSpec{Command: "{bin} session list --format json", ID: "id", Dir: "working_dir", Created: "created_at", Updated: "updated_at", Title: "name"}, Command: "goose", Args: []string{"session"}, ModelFlag: "--model",
				ResumeArgs:   []string{"--resume"},
				ResumeIDArgs: []string{"--resume", "--session-id", "{id}"},
				ForkArgs:     []string{"--resume", "--session-id", "{id}", "--fork"},
				YoloEnv:      map[string]string{"GOOSE_MODE": "auto"},
				ACP:          &ACPSpec{Command: "goose", Args: []string{"acp"}}},
			DisplayName: "Goose", Vendor: "Block / AAIF", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"G", "#171717"}, Homepage: "https://goose-docs.ai/docs/guides/goose-cli-commands",
			Description: "Open-source agent. Auto-approve is the GOOSE_MODE=auto environment variable, set only " +
				"when the session is launched in yolo mode. `goose session` takes no opening prompt, so it is typed in.",
			InstallHint:  "curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | bash",
			SessionsHint: "goose session list",
			Source:       "goose session --help ; https://goose-docs.ai/docs/guides/goose-cli-commands",
			VerifiedBy:   "goose 1.52.0", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "aider", Command: "aider", ModelFlag: "--model",
				ResumeArgs: []string{"--restore-chat-history"},
				YoloArgs:   []string{"--yes-always"},
				Task: &TaskSpec{PromptTemplate: "--message {prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{"bypassPermissions": {"--yes-always"}}}},
			DisplayName: "Aider", Vendor: "Aider", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"Ai", "#15803d"}, Homepage: "https://aider.chat/docs/",
			Description: "AI pair programming in the terminal. Its positional arguments are files, so the opening " +
				"message is typed in. It keeps one chat history per directory: resume restores it, and there are " +
				"no session ids.",
			InstallHint: "python -m pip install aider-install && aider-install",
			Source:      "aider --help ; https://aider.chat/docs/config/options.html",
			VerifiedBy:  "aider 0.86.2", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "crush", Sessions: &SessionsSpec{Command: "{bin} session list --json", ID: "id", Created: "created", Updated: "modified", Title: "title"}, Command: "crush",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				YoloArgs: []string{"--yolo"},
				Task: &TaskSpec{Args: []string{"run"}, PromptTemplate: "{prompt}", OutputMode: "plain",
					ResumeArgs: []string{"--continue"}}},
			DisplayName: "Crush", Vendor: "Charm", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"Cr", "#db2777"}, Homepage: "https://github.com/charmbracelet/crush",
			Description: "Charm's terminal agent. The interactive command has no model flag and takes no opening " +
				"prompt; model choice is a runtime picker.",
			InstallHint: "brew install charmbracelet/tap/crush  (or: npm install -g @charmland/crush)",
			Source:      "crush --help ; github.com/charmbracelet/crush",
			VerifiedBy:  "crush v0.96.1", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "cline", Sessions: &SessionsSpec{Command: "{bin} history --json --limit 200", ID: "sessionId", Dir: "cwd", Created: "startedAt", Updated: "updatedAt", Title: "metadata.title"}, Command: "cline", Args: []string{"-i"}, PromptArg: true,
				ModelFlag: "-m", ResumeIDArgs: []string{"--id", "{id}"},
				YoloArgs: []string{"--auto-approve", "true"},
				ACP:      &ACPSpec{Command: "cline", Args: []string{"--acp"}}},
			DisplayName: "Cline CLI", Vendor: "Cline", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"Cl", "#1d4ed8"}, Homepage: "https://docs.cline.bot/cline-cli/overview",
			Description: "Cline's standalone CLI (`-i` opens its TUI). Auto-approve is Cline's own default; with yolo " +
				"off it still auto-approves unless you add `--auto-approve false`. There is no resume-last flag.",
			InstallHint:  "npm i -g cline",
			SessionsHint: "cline history",
			Source:       "cline --help ; https://github.com/cline/cline",
			VerifiedBy:   "cline 3.0.65", Unverified: []string{"prompt_arg"}, VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "grok", SessionIDArgs: []string{"--session-id", "{id}"}, ForkSessionID: true, Command: "grok", ModelFlag: "-m",
				PromptArgs: []string{"--", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				ForkArgs: []string{"--resume", "{id}", "--fork-session"},
				YoloArgs: []string{"--permission-mode", "bypassPermissions"},
				ACP:      &ACPSpec{Command: "grok", Args: []string{"agent", "stdio"}}},
			DisplayName: "Grok CLI", Vendor: "xAI", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"X", "#000000"}, Homepage: "https://x.ai/cli",
			Description: "xAI's terminal agent. `grok agent stdio` speaks ACP. The opening prompt follows `--` so " +
				"text starting with a dash is not read as a flag.",
			InstallHint:  "curl -fsSL https://x.ai/cli/install.sh | bash",
			SessionsHint: "grok sessions list",
			Source:       "grok --help ; grok agent --help ; https://x.ai/cli/install.sh",
			VerifiedBy:   "grok 1.0.41", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "antigravity", Command: "agy", ModelFlag: "--model",
				PromptArgs: []string{"--prompt-interactive", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--conversation", "{id}"},
				YoloArgs: []string{"--dangerously-skip-permissions"},
				Task: &TaskSpec{PromptTemplate: "--print {prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{
						"plan":              {"--mode", "plan"},
						"acceptEdits":       {"--mode", "accept-edits"},
						"bypassPermissions": {"--dangerously-skip-permissions"},
					}}},
			DisplayName: "Antigravity CLI", Vendor: "Google", Group: CatalogGroupPopular,
			Icon: CatalogIcon{"Ag", "#1a73e8"}, Homepage: "https://antigravity.google/docs/cli/overview",
			Description: "Google's Antigravity terminal agent (`agy`). No ACP mode; background tasks use `--print`. " +
				"Needs a Google sign-in or GEMINI_API_KEY.",
			InstallHint:  "curl -fsSL https://antigravity.google/cli/install.sh | bash",
			SessionsHint: "/resume inside agy lists conversations",
			Source:       "agy --help ; https://antigravity.google/docs/cli/install",
			VerifiedBy:   "agy 1.2.12", VerifiedAt: catalogVerifiedAt,
		},
		// ---- Vendor agents -----------------------------------------------
		{
			Spec: Spec{Name: "muse", Command: "muse", Args: []string{"--trust-workspace"}, ModelFlag: "--model",
				ResumeArgs:   []string{"resume", "--last"},
				ResumeIDArgs: []string{"resume", "{id}"},
				YoloArgs:     []string{"--yolo"},
				Task: &TaskSpec{Args: []string{"exec"}, PromptTemplate: "{prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{"bypassPermissions": {"--yolo"}}}},
			DisplayName: "Muse Code", Vendor: "Meta", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"M", "#0668e1"}, Homepage: "https://dev.meta.ai/docs/muse-code",
			Description: "Meta's terminal agent. Launch trusts the workspace for that run only. The opening message " +
				"is typed in because Muse reads a subcommand-shaped prompt as a command. No ACP (its `muse serve` " +
				"speaks Meta's own protocol); MCP servers live only in its settings file.",
			InstallHint:  "curl -fsSL https://dev.meta.ai/install.sh | sh",
			SessionsHint: "`muse resume` opens a picker; ids are session UUIDs or names",
			Source:       "muse --help ; muse resume --help ; muse exec --help ; https://dev.meta.ai/docs/muse-code/extending",
			VerifiedBy:   "muse 1.4.0", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "mimo", Sessions: &SessionsSpec{Command: "{bin} session list --format json", ID: "id", Dir: "directory", Created: "created", Updated: "updated", Title: "title"}, Command: "mimo", Args: []string{"--trust"}, ModelFlag: "-m",
				PromptArgs: []string{"--prompt", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				ForkArgs: []string{"--session", "{id}", "--fork"},
				YoloArgs: []string{"--yolo"},
				ACP:      &ACPSpec{Command: "mimo", Args: []string{"acp"}}},
			DisplayName: "MiMo Code", Vendor: "Xiaomi", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Mi", "#ff6900"}, Homepage: "https://mimo.xiaomi.com/coder",
			Description: "Xiaomi's terminal agent, built on OpenCode. Project MCP servers (MIMOCODE_CONFIG) and " +
				"skills are passed to interactive sessions.",
			InstallHint:  "curl -fsSL https://mimo.xiaomi.com/install | bash",
			SessionsHint: "mimo session list",
			Source:       "mimo --help ; https://mimo.xiaomi.com/coder",
			VerifiedBy:   "mimo 0.1.15", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "devin", Command: "devin", ModelFlag: "--model",
				PromptArgs: []string{"--", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs: []string{"--permission-mode", "dangerous"},
				ACP:      &ACPSpec{Command: "devin", Args: []string{"acp"}}},
			DisplayName: "Devin CLI", Vendor: "Cognition", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"D", "#0e7490"}, Homepage: "https://docs.devin.ai/cli",
			Description: "Cognition's local agent. Its approve-everything mode is `--permission-mode dangerous` " +
				"(the modes are auto, accept-edits, smart and dangerous). Needs a Devin account.",
			InstallHint:  "curl -fsSL https://cli.devin.ai/install.sh | bash",
			SessionsHint: "devin list",
			Source:       "devin --help ; https://docs.devin.ai/cli",
			VerifiedBy:   "devin 3000.11.3", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "droid", Command: "droid", PromptArg: true,
				ResumeArgs:   []string{"--resume", "--last"},
				ResumeIDArgs: []string{"--resume", "{id}"},
				ForkArgs:     []string{"--fork", "{id}"},
				YoloArgs:     []string{"--auto", "high"},
				Task: &TaskSpec{Args: []string{"exec"}, PromptTemplate: "{prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{
						"acceptEdits":       {"--auto", "low"},
						"bypassPermissions": {"--skip-permissions-unsafe"},
					}}},
			DisplayName: "Droid", Vendor: "Factory", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Dr", "#ea580c"}, Homepage: "https://docs.factory.ai/cli/getting-started/quickstart",
			Description: "Factory's terminal agent. The interactive command has no model flag (only `droid exec -m`) " +
				"and there is no ACP mode. `droid exec` is read-only unless given an autonomy level, so plan-mode " +
				"tasks are not offered.",
			InstallHint:  "curl -fsSL https://app.factory.ai/cli | sh",
			SessionsHint: "`droid resume` opens a picker; `droid search <text>` finds sessions",
			Source:       "droid --help ; droid exec --help",
			VerifiedBy:   "droid 0.228.0", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "kiro", Command: "kiro-cli", Args: []string{"chat"}, PromptArg: true,
				ModelFlag:     "--model",
				ModelsCommand: "{bin} chat --list-models --format json",
				ResumeArgs:    []string{"--resume"}, ResumeIDArgs: []string{"--resume-id", "{id}"},
				YoloArgs: []string{"--trust-all-tools"},
				ACP:      &ACPSpec{Command: "kiro-cli", Args: []string{"acp"}}},
			DisplayName: "Kiro CLI", Vendor: "AWS", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Ki", "#7c3aed"}, Homepage: "https://kiro.dev/docs/cli/",
			Description:  "AWS's Kiro terminal agent. Needs a Kiro login before anything, including `kiro-cli acp`, starts.",
			InstallHint:  "curl -fsSL https://cli.kiro.dev/install | bash",
			SessionsHint: "kiro-cli chat --list-sessions",
			Source:       "kiro-cli chat --help ; kiro-cli acp --help ; https://kiro.dev/docs/cli/",
			VerifiedBy:   "kiro-cli 2.24.1", Unverified: []string{"models_command"}, VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "auggie", Command: "auggie", PromptArg: true, ModelFlag: "-m",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				ACP: &ACPSpec{Command: "auggie", Args: []string{"--acp"}}},
			DisplayName: "Auggie", Vendor: "Augment Code", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Au", "#16a34a"}, Homepage: "https://docs.augmentcode.com/cli/overview",
			Description: "Augment's terminal agent. There is no approve-everything flag — tool policy is per tool " +
				"(`--permission tool:allow`), so yolo is not offered. Needs an Augment account.",
			InstallHint:  "npm install -g @augmentcode/auggie",
			SessionsHint: "auggie session list",
			Source:       "auggie --help ; auggie session --help",
			VerifiedBy:   "auggie 0.36.0", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "cn", Command: "cn", PromptArg: true,
				ResumeArgs: []string{"--resume"}, ForkArgs: []string{"--fork", "{id}"},
				YoloArgs: []string{"--auto"},
				Task: &TaskSpec{PromptTemplate: "-p {prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{
						"plan":              {"--readonly"},
						"bypassPermissions": {"--auto"},
					}}},
			DisplayName: "Continue CLI", Vendor: "Continue", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Co", "#0891b2"}, Homepage: "https://docs.continue.dev/guides/cli",
			Description: "Continue's terminal agent (`cn`). `--resume` reopens only the last session; a session id " +
				"can only be forked. `--model` adds a Continue Hub model rather than choosing one, so it is not " +
				"wired as the model flag. No ACP mode.",
			InstallHint:  "npm i -g @continuedev/cli",
			SessionsHint: "cn ls",
			Source:       "cn --help ; https://docs.continue.dev/guides/cli",
			VerifiedBy:   "cn 1.5.47", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "kilo", Sessions: &SessionsSpec{Command: "{bin} session list --format json", ID: "id", Dir: "directory", Created: "created", Updated: "updated", Title: "title"}, Command: "kilo", ModelFlag: "-m",
				PromptArgs: []string{"--prompt", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				ForkArgs: []string{"--session", "{id}", "--fork"},
				YoloArgs: []string{"--auto"},
				ACP:      &ACPSpec{Command: "kilo", Args: []string{"acp"}}},
			DisplayName: "Kilo Code CLI", Vendor: "Kilo Code", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Kc", "#ca8a04"}, Homepage: "https://kilo.ai/docs/cli",
			Description: "Kilo Code's terminal agent, built on OpenCode. Project MCP servers (KILO_CONFIG) and " +
				"skills are passed to interactive sessions.",
			InstallHint:  "npm install -g @kilocode/cli",
			SessionsHint: "kilo session list",
			Source:       "kilo --help ; https://kilo.ai/docs/cli",
			VerifiedBy:   "kilo 7.8.1", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "vibe", Sessions: &SessionsSpec{Files: []string{"$VIBE_HOME/logs/session/session_*/meta.json", "~/.vibe/logs/session/session_*/meta.json"}, Whole: true, ID: "session_id", Dir: "origin_directory", Created: "start_time", Updated: "bumped_at", Title: "title"}, Command: "vibe", Args: []string{"--trust"}, PromptArg: true,
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs: []string{"--auto-approve"},
				ACP:      &ACPSpec{Command: "vibe-acp"}},
			DisplayName: "Mistral Vibe", Vendor: "Mistral AI", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"V", "#fa520f"}, Homepage: "https://github.com/mistralai/mistral-vibe",
			Description: "Mistral's terminal agent. There is no model flag (the model is VIBE_ACTIVE_MODEL or its " +
				"config). ACP is the separate `vibe-acp` binary installed alongside it. `--trust` trusts the " +
				"directory for that run only.",
			InstallHint:  "curl -LsSf https://mistral.ai/vibe/install.sh | bash  (or: uv tool install mistral-vibe)",
			SessionsHint: "`vibe --resume` opens a picker",
			Source:       "vibe --help ; https://github.com/mistralai/mistral-vibe",
			VerifiedBy:   "vibe 2.25.8", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "rovodev", Command: "acli", Args: []string{"rovodev", "run"},
				ResumeArgs: []string{"--restore"}, ResumeIDArgs: []string{"--restore", "{id}"},
				YoloArgs: []string{"--yolo"},
				Task: &TaskSpec{Args: []string{"rovodev", "run"}, PromptTemplate: "{prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{"bypassPermissions": {"--yolo"}}}},
			DisplayName: "Rovo Dev CLI", Vendor: "Atlassian", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"R", "#0052cc"}, Homepage: "https://support.atlassian.com/rovo/docs/install-and-run-rovo-dev-cli-on-your-device/",
			Description: "Atlassian's agent, run as `acli rovodev run`. A positional instruction runs once and exits, so " +
				"the opening message is typed in. Model choice is the /models command. Needs a Rovo Dev API token; " +
				"its --help is not readable before login, so flags come from Atlassian's command reference.",
			InstallHint:  "install the Atlassian CLI (acli): https://developer.atlassian.com/cloud/acli/guides/install-acli/ , then acli rovodev auth login",
			SessionsHint: "/sessions inside Rovo Dev",
			Source:       "https://support.atlassian.com/rovo/docs/rovo-dev-cli-commands/",
			VerifiedBy:   "docs", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "codebuff", Command: "codebuff", PromptArg: true,
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--continue", "{id}"}},
			DisplayName: "Codebuff", Vendor: "Codebuff", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Cb", "#65a30d"}, Homepage: "https://www.codebuff.com/docs/help/quick-start",
			Description: "Codebuff's terminal agent. Interactive only: no model flag (`--lite`/`--max` pick a tier), " +
				"no auto-approve flag, no headless or ACP mode.",
			InstallHint: "npm install -g codebuff",
			Source:      "codebuff --help",
			VerifiedBy:  "codebuff 1.0.688", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "command-code", Command: "command-code", Args: []string{"--trust"}, PromptArg: true,
				ModelFlag:  "-m",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				ForkArgs: []string{"--resume", "{id}", "--fork-session"},
				YoloArgs: []string{"--yolo"},
				Task: &TaskSpec{PromptTemplate: "-p {prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{
						"plan":              {"--permission-mode", "plan"},
						"acceptEdits":       {"--permission-mode", "accept-edits"},
						"bypassPermissions": {"--yolo"},
					}}},
			DisplayName: "Command Code", Vendor: "Command Code", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Cm", "#475569"}, Homepage: "https://commandcode.ai/docs/quickstart",
			Description: "Command Code's terminal agent. Launch passes `--trust` so the first-run trust prompt does not " +
				"swallow the opening message. No ACP mode.",
			InstallHint:  "npm i -g command-code",
			SessionsHint: "`command-code --resume` opens a picker; /session-file shows the current id",
			Source:       "command-code --help",
			VerifiedBy:   "command-code 1.66.0", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "autohand", Command: "autohand", ModelFlag: "--model",
				ResumeIDArgs: []string{"resume", "{id}"},
				ForkArgs:     []string{"--fork", "{id}"},
				YoloArgs:     []string{"--unrestricted"},
				ACP:          &ACPSpec{Command: "autohand", Args: []string{"--acp"}}},
			DisplayName: "Autohand Code", Vendor: "Autohand", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Ah", "#9333ea"}, Homepage: "https://github.com/autohandai/code-cli",
			Description: "Autohand's terminal agent. A positional prompt runs once and exits, so the opening message " +
				"is typed in. `autohand resume` without an id opens a picker, so there is no resume-last.",
			InstallHint:  "curl -fsSL https://autohand.ai/install.sh | bash  (or: npm i -g autohand-cli)",
			SessionsHint: "`autohand resume` opens the project picker",
			Source:       "autohand --help ; https://github.com/autohandai/code-cli",
			VerifiedBy:   "autohand 0.9.8", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "zcode", Command: "zcode",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs: []string{"--mode", "yolo"},
				Task: &TaskSpec{PromptTemplate: "--prompt {prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{
						"plan":              {"--mode", "plan"},
						"acceptEdits":       {"--mode", "edit"},
						"bypassPermissions": {"--mode", "yolo"},
					}}},
			DisplayName: "ZCode", Vendor: "Z.ai", Group: CatalogGroupVendor,
			Icon: CatalogIcon{"Z", "#2563eb"}, Homepage: "https://zcode.z.ai/en/docs",
			Description: "Z.ai's agent. Z.ai publishes only the desktop app; its bundled runtime (0.16.9) has these " +
				"flags but no terminal UI, and no standalone `zcode` install is documented. Add this only if you " +
				"already have a `zcode` CLI with its TUI. No model flag or ACP mode.",
			InstallHint:  "no standalone CLI is published — see https://zcode.z.ai/en/docs/install",
			SessionsHint: "/resume inside ZCode (ids look like sess_…)",
			Source:       "zcode --help from the ZCode 3.14.3 AppImage runtime ; https://zcode.z.ai/en/docs",
			VerifiedBy:   "zcode 0.16.9 (desktop bundle)",
			Unverified:   []string{"command", "install_hint"}, VerifiedAt: catalogVerifiedAt,
		},
		// ---- Open source & community ------------------------------------
		{
			Spec: Spec{Name: "pi", Sessions: &SessionsSpec{Files: []string{"$PI_CODING_AGENT_SESSION_DIR/*/*.jsonl", "$PI_CODING_AGENT_DIR/sessions/*/*.jsonl", "~/.pi/agent/sessions/*/*.jsonl"}, Header: "session", ID: "id", Dir: "cwd", Created: "timestamp"}, Command: "pi", PromptArg: true, ModelFlag: "--model",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--session", "{id}"},
				ForkArgs: []string{"--fork", "{id}"},
				Task: &TaskSpec{PromptTemplate: "-p {prompt}", OutputMode: "plain",
					PermissionArgs: map[string][]string{"plan": {"--tools", "read,grep,find,ls"}}}},
			DisplayName: "Pi", Vendor: "pi.dev", Group: CatalogGroupCommunity,
			Icon: CatalogIcon{"π", "#44403c"}, Homepage: "https://pi.dev",
			Description: "Minimal open-source agent. Pi has no approval prompts at all, so there is nothing for yolo " +
				"to switch off; plan-mode tasks restrict it to read-only tools. No MCP by design, no ACP.",
			InstallHint:  "npm install -g @mariozechner/pi-coding-agent",
			SessionsHint: "session files under ~/.pi/agent/sessions/; --session takes a partial UUID",
			Source:       "pi --help ; @mariozechner/pi-coding-agent README",
			VerifiedBy:   "pi 0.73.1", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "omp", Sessions: &SessionsSpec{Files: []string{"~/.omp/agent/sessions/*/*.jsonl"}, Header: "session", ID: "id", Dir: "cwd", Created: "timestamp"}, Command: "omp", PromptArg: true, ModelFlag: "--model",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs: []string{"--auto-approve"},
				ACP:      &ACPSpec{Command: "omp", Args: []string{"acp"}}},
			DisplayName: "oh-my-pi", Vendor: "omp.sh", Group: CatalogGroupCommunity,
			Icon: CatalogIcon{"ω", "#57534e"}, Homepage: "https://omp.sh",
			Description:  "Batteries-included fork of Pi (`omp`), with approvals, MCP and an ACP mode. Runs on Bun.",
			InstallHint:  "curl -fsSL https://omp.sh/install | sh",
			SessionsHint: "session files under ~/.omp/agent/sessions/; --resume takes an id prefix or path",
			Source:       "omp --help ; https://omp.sh",
			VerifiedBy:   "omp 18.3.4", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "hermes", Sessions: &SessionsSpec{SQLite: []string{"$HERMES_HOME/state.db", "~/.hermes/state.db"}, Query: "SELECT id, cwd, started_at, last_activity_at, title FROM sessions WHERE coalesce(archived, 0) = 0 AND coalesce(hidden, 0) = 0", ID: "id", Dir: "cwd", Created: "started_at", Updated: "last_activity_at", Title: "title"}, Command: "hermes", Args: []string{"chat"}, ModelFlag: "-m",
				PromptArgs: []string{"-q", "{prompt}"},
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				YoloArgs: []string{"--yolo"},
				ACP:      &ACPSpec{Command: "hermes", Args: []string{"acp"}}},
			DisplayName: "Hermes Agent", Vendor: "Nous Research", Group: CatalogGroupCommunity,
			Icon: CatalogIcon{"H", "#b45309"}, Homepage: "https://hermes-agent.nousresearch.com/docs/",
			Description: "Nous Research's agent. `hermes chat -q` seeds an interactive session on a terminal. Its " +
				"`-z` one-shot mode bypasses every approval, so background tasks use ACP instead.",
			InstallHint:  "curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash",
			SessionsHint: "hermes sessions list (the id is also printed on exit)",
			Source:       "hermes --help ; hermes chat --help",
			VerifiedBy:   "hermes 0.21.5", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "openclaude", TrustCommand: openclaudeTrust, SessionIDArgs: []string{"--session-id", "{id}"}, ForkSessionID: true, Sessions: &SessionsSpec{Files: []string{"~/.openclaude/projects/{slug}/*.jsonl"}, ID: "@stem", Dir: "cwd", Created: "timestamp"}, Command: "openclaude", PromptArg: true, ModelFlag: "--model",
				ResumeArgs: []string{"--continue"}, ResumeIDArgs: []string{"--resume", "{id}"},
				ForkArgs: []string{"--resume", "{id}", "--fork-session"},
				YoloArgs: []string{"--dangerously-skip-permissions"},
				Task: &TaskSpec{Args: []string{"--output-format", "stream-json", "--verbose"},
					PromptTemplate: "-p {prompt}", OutputMode: "claude",
					PermissionArgs: map[string][]string{
						"plan":              {"--permission-mode", "plan"},
						"acceptEdits":       {"--permission-mode", "acceptEdits"},
						"bypassPermissions": {"--permission-mode", "bypassPermissions"},
					}}},
			DisplayName: "OpenClaude", Vendor: "Gitlawb", Group: CatalogGroupCommunity,
			Icon: CatalogIcon{"Oc", "#c2410c"}, Homepage: "https://openclaude.gitlawb.com/",
			Description: "Open-source fork of Claude Code that talks to other providers (`--provider`). Same flags " +
				"and stream JSON as Claude Code; project MCP servers are not yet translated for it.",
			InstallHint: "npm install -g @gitlawb/openclaude",
			Source:      "openclaude --help",
			VerifiedBy:  "openclaude 0.31.0", VerifiedAt: catalogVerifiedAt,
		},
		// ---- ACP adapters ------------------------------------------------
		{
			Spec: Spec{Name: "claude-code-acp", Command: "claude-code-acp",
				ACP: &ACPSpec{Command: "npx", Args: []string{"-y", "@agentclientprotocol/claude-agent-acp"}}},
			DisplayName: "Claude Code (ACP)", Vendor: "Agent Client Protocol", Group: CatalogGroupAdapters,
			Icon: CatalogIcon{"C", "#d97757"}, Homepage: "https://github.com/agentclientprotocol/claude-agent-acp",
			Description: "The ACP adapter for Claude Code (formerly Zed's @zed-industries/claude-code-acp, now " +
				"deprecated) — needs a Claude Code login exactly like the built-in claude agent. For background " +
				"tasks only. See docs/acp.md.",
			InstallHint: "needs npx on the launch host; no separate global install",
			Source:      "npm @agentclientprotocol/claude-agent-acp ; docs/acp.md",
			VerifiedBy:  "claude-agent-acp 0.81.2", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "codex-acp", Command: "codex-acp",
				ACP: &ACPSpec{Command: "npx", Args: []string{"-y", "@agentclientprotocol/codex-acp"}}},
			DisplayName: "Codex (ACP)", Vendor: "Agent Client Protocol", Group: CatalogGroupAdapters,
			Icon: CatalogIcon{"Cx", "#404040"}, Homepage: "https://github.com/agentclientprotocol/codex-acp",
			Description: "The ACP adapter for Codex (formerly Zed's @zed-industries/codex-acp, now deprecated) — " +
				"ships a platform-specific native binary. For background tasks only. See docs/acp.md.",
			InstallHint: "needs npx on the launch host; no separate global install",
			Source:      "npm @agentclientprotocol/codex-acp ; docs/acp.md",
			VerifiedBy:  "codex-acp 1.13.1", VerifiedAt: catalogVerifiedAt,
		},
		{
			Spec: Spec{Name: "gemini-acp", Command: "gemini",
				ACP: &ACPSpec{Command: "gemini", Args: []string{"--experimental-acp"}}},
			DisplayName: "Gemini CLI (ACP)", Vendor: "Google", Group: CatalogGroupAdapters,
			Icon: CatalogIcon{"Ge", "#4285f4"}, Homepage: "https://github.com/google-gemini/gemini-cli",
			Description: "Gemini CLI's own ACP mode — gemini itself must be installed (it is also lectern's built-in " +
				"`gemini` agent). 0.61 calls the flag deprecated in favour of `--acp`; both still answer.",
			InstallHint: "npm install -g @google/gemini-cli",
			Source:      "gemini --help ; docs/acp.md",
			VerifiedBy:  "gemini 0.61.0", VerifiedAt: catalogVerifiedAt,
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
