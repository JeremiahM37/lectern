# Agents

Lectern drives more than one coding CLI. Everything CLI-specific lives in
`internal/agents/` (`launch.go` and `parse.go`, plus `claude.go` for Claude
Code's settings and permission rules), so the run
protocol — worktree, tmux, events file, exit code — is identical whichever agent
you pick.

| Agent | Status | Gated approvals (interactive session) | Session resume | Credentials pushed to remote targets |
|---|---|---|---|---|
| `claude` | first-class | ✅ | ✅ | `~/.claude/.credentials.json` |
| `codex` | first-class | ✅ (hooks.json, confirmed against 0.156.1) | ✅ | `~/.codex/auth.json` |
| `gemini` | experimental | ❌ | ❌ | — |

An **interactive session's** "ask" permission mode holds an approval on your
phone for both claude and codex, via each CLI's own hook mechanism — see
docs/agent-events.md sections 2 and 3 for how codex's was confirmed and
wired. This is a different mechanism from the **background task** "gated"
mode in the table below, which is driver-specific (see
[context-parity.md](context-parity.md) and docs/agent-events.md section 4 for
`codex-appserver`, the one task driver that does support gated codex
approvals via JSON-RPC rather than a hook).

## Catalog: one-click presets for other CLIs

Settings → **Agents** → **Add agent** opens a searchable catalog populated from
`GET /api/agents/catalog` (`internal/sessions/catalog.go`), not a hardcoded
list. It covers every agent [Orca](https://github.com/stablyai/orca) supports
plus Aider and the ACP adapters — 33 presets in four groups (Popular, Vendor
agents, Open source & community, ACP adapters). Search matches the product,
binary and vendor. Each row shows a monogram, the command, whether it is
installed on the Lectern host, and chips for what it can and cannot do
(resume, fork, model, auto-approve, background tasks, ACP, project MCP,
project skills), with the reason as a tooltip. "Custom runner" is still there
for any other CLI. Picking a preset fills every field of the form; nothing is
saved until you do, and a newly added agent stays out of the pickers until you
show it (see below). An entry already added is shown disabled.

Every preset records how it was checked: `verified_by` is the installed CLI
version whose `--help` (and, for ACP, a real `initialize` handshake) it was
read from, or `docs` when the CLI cannot be installed without an account;
`source` cites the help output or page; `unverified` names any field that is
still a guess. Settings shows all of these under the list, with the install
command and where the CLI lists its session ids. **Never treat an
`unverified` field as documented behaviour.**

Two definition fields exist for these CLIs:

- `prompt_args` passes the opening message through arguments, with `{prompt}`
  replaced — `["-i", "{prompt}"]` for Copilot and Qwen Code (whose bare
  positional runs one-shot and exits), `["--prompt", "{prompt}"]` for the
  OpenCode family, `["--", "{prompt}"]` for Grok and Devin. It takes precedence
  over `prompt_arg`. With neither, the message is typed once the pane settles.
- `yolo_env` is an auto-approve switch that only exists as an environment
  variable (Goose's `GOOSE_MODE=auto`). Like `yolo_args` it is applied only
  when the session is launched in yolo mode.

`GET /api/agents/catalog` also reports `installed` (whether the preset's own
binary resolves on `PATH` for the Lectern host — there is no per-target remote
check), `added` (an agent by that name is already registered) and
`capabilities`. The MCP and skills entries depend on the agent keeping the
preset's name: Lectern wires both by agent name.

## Exact conversations for catalog agents

Restore, Recent, the saved-conversation picker, fork and `lectern restore`
continue one exact conversation, never "the latest one in this folder".
Claude and Codex are bound by process evidence. A catalog agent is bound in
one of two ways, both set in its definition:

- **Named at launch** (`session_id_args`, e.g. `["--session-id", "{id}"]`):
  Lectern generates a UUID, records it on the session and passes it to the
  CLI. `fork_session_id` also names a fork's new conversation. Used for Qwen
  Code, OpenClaude, Copilot CLI and Grok. An id named this way is used as is
  on Restore — Qwen Code, for one, leaves a conversation out of its own
  listing until the process has exited — so a session closed before its first
  message restores to a conversation the CLI never saved, and the CLI says so.
- **Matched afterwards** (`sessions`): Lectern reads where the CLI lists its
  conversations — a JSON listing command (`opencode session list --format
  json`), session files (Pi's JSONL headers, Vibe's `meta.json`) or a SQLite
  table (Hermes) — and binds the one conversation in this folder that did not
  exist when the session launched and has been written since. It binds
  nothing when more than one qualifies, when another session already holds
  it, or when another unbound session of the same agent was running in the
  same folder. A session left unbound gets the picker, not a guess. Capture
  runs while the session is alive and once more after it ends, for CLIs that
  list a conversation only then. The listing that records what existed runs
  before the CLI starts, and capture waits a few seconds after the launch:
  run beside a starting CLI, a listing command can race it initialising the
  same store (Crush once failed to start that way).

The same listing validates an id before Restore, Recent or fork uses it, and
feeds the saved-conversation picker. Lectern lists these conversations but
does not read their messages, so the picker shows titles and times only.
`sessions_hint` says where each CLI lists them for anyone resuming by hand.

| Agent | How it is bound | Checked live |
|---|---|---|
| OpenCode, Kilo Code, MiMo Code | `session list --format json` | bind, Restore, fork, `lectern restore` |
| Qwen Code | named at launch; forks from `sessions list --json` | bind, Restore, fork, `lectern restore` |
| OpenClaude | named at launch, forks too | bind, Restore, fork, `lectern restore` |
| Goose | `session list --format json` | bind, Restore, fork, `lectern restore` |
| Pi, oh-my-pi | session file headers | bind, Restore, fork (Pi), `lectern restore` |
| Crush | `session list --json` (per folder) | bind, Restore, `lectern restore` |
| Cline | `history --json` | bind, Restore, `lectern restore` |
| Hermes | `~/.hermes/state.db` | bind, Restore, `lectern restore` |
| Mistral Vibe | `logs/session/*/meta.json` | bind, Restore, `lectern restore` |
| Copilot CLI, Grok | named at launch | flags from `--help` only (need an account) |

"Checked live" means: each CLI ran in Lectern against a local stand-in
OpenAI-compatible server (no account), answered an opening prompt, was bound,
stopped, restored through the API and again through `lectern restore`, and the
restored CLI's next request to the model carried the earlier reply
(`tools/catalog-probe/live_exact.py`, with `fake_openai.py` as the server). Every
other catalog agent resumes only its most recent conversation in the folder,
or by an id you give it: their session stores could not be read without an
account (Kimi, Devin, Kiro, Cursor, Auggie, Amp, Droid, Antigravity, Continue,
Codebuff, Command Code, Autohand, Rovo Dev) or are an internal format Lectern
does not read (Muse). Cursor's `create-chat` returns a new chat id and would
suit naming at launch, but it needs a Cursor login to test.

Two first-run screens got in the way of those checks and will greet a real
user once: MiMo Code asks to accept `--yolo` the first time, and OpenClaude
asks to accept bypass mode (its folder-trust prompt is answered by Lectern
now, like Claude Code's). Crush's input box is not one Lectern recognises as
a prompt, so its opening message is not typed in automatically.

## Capability degradation

`GET /api/agents` and `GET /api/agents/capabilities` report, per agent, a
`capabilities` map keyed by `resume`, `exact`, `fork`, `model`, `models_list`,
`yolo`, `acp` and `task` (`internal/sessions.Spec.Capabilities`); `exact` is
resuming one exact conversation later (see above). Each entry is
`{"available": true}` or `{"available": false, "reason": "<Feature> isn't
available for <Agent>"}` — driven by which registry fields are populated, so
a picker can grey out a Resume button with an explanatory reason ("Resume isn't
available for Aider") instead of a disabled control nobody can explain, or a
button that silently does nothing. `task` is available for any built-in
(claude/codex/gemini get their non-interactive backend from Go code, not from
`task`/`acp`) or for a custom/catalog agent with either field set.

## Shown agents ("More agents…")

Every agent picker — new session, Switch, task create/quick-dispatch,
best-of-N, delegate, and the terminal dashboard's own forms — reads
`GET /api/agents/menu` (`{"agents": ["claude", "codex", …]}`) and renders that
ordered list first, with a **More agents…** entry that opens the full
registry instead of growing into a long dropdown. `PUT /api/agents/menu`
saves the shown/ordered list from Settings → Agents' "Show in menus" toggle
and up/down reorder controls; an unknown name is rejected outright. The
default, before anything is saved, is **installed built-ins** — the built-in
agents (claude/codex/gemini) whose binary resolves on `PATH`, or every
built-in if none are installed (so a fresh box never shows an empty picker).
A newly added catalog or custom agent is **not** shown by default; toggle it
on in Settings once you want it in every picker. This is one shared ordering,
not a per-account preference — Lectern has no multi-user model for this
setting, the same as the `agents` setting itself.

The web pickers show this order in a native `<select>`/button list plus a
"More agents…" entry that opens a small picker of every registered agent; the
terminal dashboard's own agent select is already a scrollable list, so it
sorts shown agents to the top instead of hiding anything.

## Choosing one

The web Settings → **Agents** page is the place to add a runner. **Add agent**
offers the catalog above and a custom runner starter, then keeps the command,
model flag, provider endpoint and environment together. Aider's endpoint uses
`OPENAI_API_BASE`; OpenCode uses its configured provider settings (add
`OPENCODE_CONFIG_CONTENT` under Environment when configuring one). Custom
runners can name the endpoint variable their CLI expects. The same editor is available
in the terminal dashboard from **All actions → Manage agent runners** or the
`Q` shortcut. A target is chosen when the session or task is launched.

The command is the runner boundary. A provider URL or model name configures that
command; it does not make an API endpoint executable. Keep provider credentials
in the target environment. Existing masked values are retained by the editor
unless you replace them.

Set a project's default and every task inherits it:

```bash
curl -X PATCH .../api/projects/3 -d '{"default_agent":"codex"}'
```

In the PWA, the **Agent** toggle in the new-task sheet picks per task, starting
from the project default. A task's explicit `agent` always wins.

For scripts, `lectern agent list` shows the registry and
`lectern agent save @agents.json` updates it. The JSON form is useful for
repeatable deployments; Settings and the TUI expose the required command fields
without requiring JSON for ordinary setup.

An agent is interactive-only unless its definition also has a `task` object.
Enable background tasks in the editor to set a separate one-shot command,
arguments, prompt template (`{prompt}`, `{prompt_file}`, or `stdin`), plain/JSONL
output, and permission flag mappings. This keeps a CLI's interactive and batch
invocations explicit; a session command is never guessed as a task command.

The toggle reshapes the form, because the options are not interchangeable:
gated approvals and the Claude model aliases (`fable`/`opus`/`sonnet`/`haiku`)
are Claude-only, so picking Codex disables gated mode and hides the A/B row
rather than letting you build a dispatch that fails later. If the target's last
probe didn't find the binary, the toggle says so instead of failing at dispatch.

## Permission modes

Lectern's modes map onto each CLI's own sandboxing:

| Mode | claude | codex |
|---|---|---|
| `plan` | `--permission-mode plan` | `--sandbox read-only` |
| `acceptEdits` | `--permission-mode acceptEdits` | `--sandbox workspace-write` |
| `bypassPermissions` | `--permission-mode bypassPermissions` | `--dangerously-bypass-approvals-and-sandbox` |
| `default` (gated) | PreToolUse hook → approval on your phone | **rejected** — codex has no hook |

Codex requires **≥ 0.140**: the older `--full-auto` flag was removed upstream,
and `item.started` events (which make the timeline live rather than
after-the-fact) only appear in newer builds.

## Context and tools

The staged context bundle reaches every agent — it is prepended to the prompt, so
it needs no CLI support. Per-project MCP servers and permission rules are written
for Claude (`--mcp-config`, `--settings`) and MCP servers are passed additively
to Codex with `-c` overrides; Codex keeps its normal `~/.codex` home. The same
project MCP declaration reaches fresh, resumed, and forked interactive sessions
for Claude and Codex. Claude receives a private absolute MCP document and can
enforce `strict_mcp`; Codex receives additive overrides and rejects `strict_mcp`,
including when the declaration is empty. Interactive OpenCode, Qwen Code,
GitHub Copilot CLI, Kilo, MiMo Code and Amp sessions get the servers through
one private file their CLI reads beside the user's own config, and every ACP
agent's tasks (Gemini CLI, OpenCode, Goose, Kimi, Cline, Grok, Devin, Hermes
and the other catalog agents with an `acp` block) get them in ACP
`session/new`. Interactive Gemini CLI sessions get them merged into
`.gemini/settings.json` only in a Lectern-created worktree or scratch workspace,
never in the project's own checkout. Other interactive sessions and non-ACP
custom tasks get no automatic translation; see
[context-parity.md](context-parity.md#mcp-servers) for which agent gets what.

Project skills (Agent Skills `SKILL.md` directories) are linked into
`.claude/skills` for Claude and into the shared `.agents/skills` for Codex,
Gemini CLI, Qwen Code, OpenCode, GitHub Copilot CLI, Kilo, MiMo Code, Muse,
Devin, Command Code, Pi and Amp. Other agents have no confirmed skills
directory and are refused; see
[terminal-client.md](terminal-client.md#project-skills).

Project MCP is managed from PWA project settings, the terminal dashboard's MCP
action, or `PUT /api/projects/ID/mcp` through the CLI API. Responses redact
credential values; the target-side runtime document is private to its session or
task. The declaration is snapshotted before a background attempt starts, so
routine takeover keeps the original MCP policy when project settings change.
See [context-parity.md](context-parity.md).

Claude and Codex support exact-ID resume and fork in the interactive web and
terminal clients, and so do catalog agents that name or list their
conversations (above). Gemini and other custom agents expose those actions only
when their definition provides the corresponding argument templates; Lectern
never falls back to an unrelated last conversation.

## Binary not found

Agents installed under `~/.local/bin` are invisible to a systemd unit, whose
`PATH` doesn't include it — the probe then reports the agent as missing. Point
Lectern at the real path:

```ini
# /etc/systemd/system/lectern.service.d/override.conf
[Service]
Environment=LECTERN_CODEX_BIN=/home/you/.local/bin/codex
```

`LECTERN_CLAUDE_BIN` and `LECTERN_GEMINI_BIN` work the same way.

## Any CLI, for sessions and tasks

An interactive session can use any configured CLI. A background task can use one
too when its definition includes a non-interactive `task` invocation. Keeping
the two invocations separate matters for CLIs whose interactive UI and batch
runner have different commands (for example, an interactive TUI versus a
`run`/`--message` command).

Define a session-only CLI:

```bash
curl -X PUT .../api/agents -d '[{
  "name": "aider",
  "command": "aider",
  "args": ["--no-auto-commits"],
  "model_flag": "--model",
  "prompt_arg": true,
  "env": {"OPENAI_API_BASE": "http://ollama-host:11434/v1"}
}]'
```

`prompt_arg` says the CLI accepts an opening message as a positional argument.
When it does, the project briefing rides on the command line; otherwise
lectern types it after the pane settles. A definition sharing a built-in's
name overrides it, which is how a CLI whose flags have drifted gets fixed
without a release.

Add a task definition when the CLI supports a bounded one-shot command:

```json
{
  "name": "aider",
  "command": "aider",
  "model_flag": "--model",
  "env": {"OPENAI_API_BASE": "http://ollama-host:11434/v1"},
  "task": {
    "command": "aider",
    "args": ["--no-auto-commits"],
    "prompt_template": "--message {prompt}",
    "output_mode": "plain",
    "permission_args": {"bypassPermissions": ["--yes"]}
  }
}
```

`task.command` falls back to the interactive command when omitted. `args` are
individual tokens. `prompt_template` is either `stdin`, or an individual-token
template containing `{prompt}` or `{prompt_file}`; arbitrary shell text is
rejected. Prompt-argument tasks receive `< /dev/null` so a CLI cannot wait on a
tmux pane's open stdin. `plain` captures ordinary text and `jsonl` normalizes
recognized event objects while preserving unknown records.

The task field is also the capability declaration: omitted `task` means
session-only and task/routine creation explains how to fix it. `default` gated
approvals are only available to Claude. `plan` and `bypassPermissions` require
explicit `permission_args` for a custom CLI; `acceptEdits` uses the CLI's native
default when no mapping is supplied. Project MCP is translated only for the
built-in Claude and Codex adapters; a custom task with MCP fails early with an
actionable capability error instead of silently claiming tools it cannot pass.

Agent-wide and project environment values are layered for both sessions and
tasks. This is the provider/local-model configuration door: Aider's OpenAI
compatible endpoint uses `OPENAI_API_BASE`/`OPENAI_API_KEY`; other CLIs use
their own documented variables. OpenCode uses its provider configuration, which
can be supplied through `OPENCODE_CONFIG_CONTENT`, while keeping a runnable
CLI command in the definition. An HTTP endpoint by itself is not an executable
agent. The `/api/agents` response redacts credential-shaped environment values
with typed retention markers; send those markers back when editing another
field so the stored secret is retained without entering the browser response.

A project's `env` is layered over the agent's, so pointing one project at a local
model does not require redefining the agent.

Built-in adapters still handle Claude, Codex and Gemini's provider-specific
flags and event formats. Custom tasks use the generic invocation above, so no
backend code change is needed for each additional CLI. A custom JSONL event
with `type` set to `init`, `text`, `tool_use`, `tool_result` or `result` is
normalized directly; other JSON records and plain output remain visible in the
timeline.

One trap worth inheriting: both codex and gemini read stdin even when the prompt
is passed as an argument, and a tmux pane's stdin never reaches EOF — so the
launcher redirects `< /dev/null`. Without it the agent waits forever and the
attempt looks alive but never moves.

## ACP agents (any CLI, no backend code)

A third way to give a configured agent a non-interactive capability: `acp`
instead of `task`.

```json
{
  "name": "claude-code-acp",
  "command": "claude-code-acp",
  "acp": {"command": "npx", "args": ["-y", "@agentclientprotocol/claude-agent-acp"]}
}
```

`acp` and `task` are mutually exclusive — an agent is either a plain-text/JSONL
CLI (`task`) or an Agent Client Protocol agent (`acp`), never both. Unlike
`task`, `acp` needs no `prompt_template`, `output_mode` or `permission_args`:
the protocol carries the prompt and every permission decision itself, so a
task dispatched on an `acp` agent supports **every** Lectern permission mode
(`default`, `acceptEdits`, `plan`, `bypassPermissions`) with no capability
mapping to configure. See [docs/acp.md](acp.md) for the protocol, the mapping
onto Lectern's timeline, and its limits.

## Catalog verification

Checked on 2026-09-27. Each CLI was installed under a throwaway prefix
(`/mnt/bulk/cli-probe`, a private `HOME`, nothing logged in) and every flag
below was read from its `--help`; every ACP command was started and answered
an ACP `initialize` request (Kiro's needs a login first, so only its
`acp --help` was read). "Interactive context" is what Lectern passes to an
interactive session by agent name; every ACP agent also gets project MCP
servers for its tasks through `session/new`.

| Agent | Verified by | Opening prompt | Model | Resume | Auto-approve | Background tasks | Interactive context |
|---|---|---|---|---|---|---|---|
| OpenCode (`opencode`) | opencode 1.18.32 | `--prompt {prompt}` | `--model` | last, by id, fork | `--auto` | ACP `opencode acp` | MCP, skills |
| Cursor Agent CLI (`cursor-agent`) | cursor-agent 2026.09.26-dd393fe | positional | `--model` | last, by id | `--force` | ACP `cursor-agent acp` | — |
| GitHub Copilot CLI (`copilot`) | copilot 1.0.88 | `-i {prompt}` | `--model` | last, by id | `--yolo` | ACP `copilot --acp` | MCP, skills |
| Amp (`amp`) | amp 0.0.1790496040 | typed | — | last, by id | — | task | MCP, skills |
| Qwen Code (`qwen`) | qwen 0.24.6 | `-i {prompt}` | `-m` | last, by id, fork | `--yolo` | ACP `qwen --acp` | MCP, skills |
| Kimi Code CLI (`kimi`) | kimi 2.1.1 | typed | `-m` | last, by id | `--auto` | ACP `kimi acp` | — |
| Goose (`goose`) | goose 1.52.0 | typed | `--model` | last, by id, fork | env `GOOSE_MODE=auto` | ACP `goose acp` | — |
| Aider (`aider`) | aider 0.86.2 | typed | `--model` | last | `--yes-always` | task | — |
| Crush (`crush`) | crush v0.96.1 | typed | — | last, by id | `--yolo` | task | — |
| Cline CLI (`cline`) | cline 3.0.65 | positional | `-m` | by id | `--auto-approve true` | ACP `cline --acp` | — |
| Grok CLI (`grok`) | grok 1.0.41 | `-- {prompt}` | `-m` | last, by id, fork | `--permission-mode bypassPermissions` | ACP `grok agent stdio` | — |
| Antigravity CLI (`antigravity`) | agy 1.2.12 | `--prompt-interactive {prompt}` | `--model` | last, by id | `--dangerously-skip-permissions` | task | — |
| Muse Code (`muse`) | muse 1.4.0 | typed | `--model` | last, by id | `--yolo` | task | skills |
| MiMo Code (`mimo`) | mimo 0.1.15 | `--prompt {prompt}` | `-m` | last, by id, fork | `--yolo` | ACP `mimo acp` | MCP, skills |
| Devin CLI (`devin`) | devin 3000.11.3 | `-- {prompt}` | `--model` | last, by id | `--permission-mode dangerous` | ACP `devin acp` | skills |
| Droid (`droid`) | droid 0.228.0 | positional | — | last, by id, fork | `--auto high` | task | — |
| Kiro CLI (`kiro`) | kiro-cli 2.24.1 | positional | `--model` | last, by id | `--trust-all-tools` | ACP `kiro-cli acp` | — |
| Auggie (`auggie`) | auggie 0.36.0 | positional | `-m` | last, by id | — | ACP `auggie --acp` | — |
| Continue CLI (`cn`) | cn 1.5.47 | positional | — | last, fork | `--auto` | task | — |
| Kilo Code CLI (`kilo`) | kilo 7.8.1 | `--prompt {prompt}` | `-m` | last, by id, fork | `--auto` | ACP `kilo acp` | MCP, skills |
| Mistral Vibe (`vibe`) | vibe 2.25.8 | positional | — | last, by id | `--auto-approve` | ACP `vibe-acp` | — |
| Rovo Dev CLI (`rovodev`) | docs | typed | — | last, by id | `--yolo` | task | — |
| Codebuff (`codebuff`) | codebuff 1.0.688 | positional | — | last, by id | — | — | — |
| Command Code (`command-code`) | command-code 1.66.0 | positional | `-m` | last, by id, fork | `--yolo` | task | skills |
| Autohand Code (`autohand`) | autohand 0.9.8 | typed | `--model` | by id, fork | `--unrestricted` | ACP `autohand --acp` | — |
| ZCode (`zcode`) | zcode 0.16.9 (desktop bundle) | typed | — | last, by id | `--mode yolo` | task | — |
| Pi (`pi`) | pi 0.73.1 | positional | `--model` | last, by id, fork | — | task | skills |
| oh-my-pi (`omp`) | omp 18.3.4 | positional | `--model` | last, by id | `--auto-approve` | ACP `omp acp` | — |
| Hermes Agent (`hermes`) | hermes 0.21.5 | `-q {prompt}` | `-m` | last, by id | `--yolo` | ACP `hermes acp` | — |
| OpenClaude (`openclaude`) | openclaude 0.31.0 | positional | `--model` | last, by id, fork | `--dangerously-skip-permissions` | task | — |
| Claude Code (ACP) (`claude-code-acp`) | claude-agent-acp 0.81.2 | typed | — | — | — | ACP `npx -y @agentclientprotocol/claude-agent-acp` | — |
| Codex (ACP) (`codex-acp`) | codex-acp 1.13.1 | typed | — | — | — | ACP `npx -y @agentclientprotocol/codex-acp` | — |
| Gemini CLI (ACP) (`gemini-acp`) | gemini 0.61.0 | typed | — | — | — | ACP `gemini --experimental-acp` | — |

What the table cannot show, and why some entries are thinner than others:

- **ZCode**: Z.ai publishes only the desktop app. Its bundled runtime
  (`zcode` 0.16.9 inside the 3.14.3 AppImage) has the flags above, but no
  terminal UI and it cannot start outside the app's layout; no standalone
  install is documented. The preset marks its command and install hint
  unverified.
- **Rovo Dev**: `acli` 1.3.39 installs, but `acli rovodev` refuses even `--help`
  before an Atlassian login, so its flags come from Atlassian's command
  reference.
- **Amp**: no model flag (`--mode` picks model and tools together) and no
  auto-approve flag — only the `amp.dangerouslyAllowAll` setting. Amp accepts
  any unknown flag silently, so `--dangerously-allow-all` (Orca's choice) could
  not be confirmed and is not used.
- **Auggie** and **Codebuff** have no auto-approve flag; **Pi** has no approval
  prompts at all; **Codebuff** has no headless mode.
- **Devin** approves everything with `--permission-mode dangerous`, not
  `bypass` as Orca has it. **Aider**'s flag is `--yes-always`, and it restores
  chat history only with `--restore-chat-history` (off by default).
- Model lists: only Kiro prints JSON (`chat --list-models --format json`,
  unverified without a login). The other CLIs' `models` commands print text,
  which Lectern's model picker cannot parse, so they are not wired.
- Not translated yet: project MCP for OpenClaude (`--mcp-config` behaves like
  Claude Code's, but could not be confirmed without an account), Grok, Devin,
  Muse, Droid and the other CLIs whose only MCP config is their own settings
  file; project skills for CLIs whose skill listing could not be checked.
- Usage-limit detection (`internal/limits`) still recognises only Claude,
  Codex and Gemini messages; none of the new CLIs documents its limit text.

Built-in adapters and ACP adapters keep their own rows elsewhere in this
document.

## `lectern <agent>` for any registered agent

`lectern claude`, `lectern codex` and `lectern gemini` (the three built-ins)
resolve to a one-command session launch/attach at zero extra cost — they are
known at compile time, exactly like any other fixed subcommand. Any other
name — a custom or catalog agent you added in Settings — still works the same
way: an argument that matches no fixed subcommand falls through to a registry
check (`GET /api/agents`) before being reported as an unknown command, so
`lectern aider` (once `aider` is registered) behaves identically to `lectern
claude`. Existing subcommands always win: `console`, `attach`, `mcp`, and
every other name main.go or `lectern local` already claims can never be
shadowed by an agent of the same name (see `TestUnknownVerbCollisionSafety`
in `cmd/lectern/agent_dynamic_test.go`) — an agent registered under one of
those names is simply unreachable via this one-command shortcut, though it
still works everywhere else (the web UI, the MCP `start_session` tool,
`POST /api/sessions`). A genuine typo still fails as "unknown command", just
after one round trip to the registry instead of none.

## MCP `start_session` agent validation

The `start_session` MCP tool's `agent` parameter (default `claude`) is
checked against the live registry before it ever reaches `POST /sessions`: an
unregistered name fails with `unknown agent "X" — have: claude, codex, gemini,
…`, listing every currently valid name, rather than the generic 422 the
session endpoint itself gives. If the registry cannot be listed at all (a
transient failure), the check is skipped rather than failing the tool call —
the dispatch below still validates and reports whatever is actually wrong.
