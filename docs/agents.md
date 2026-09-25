# Agents

Lectern drives more than one coding CLI. Everything CLI-specific lives in
`internal/agents/` (`launch.go` and `parse.go`, plus `claude.go` for Claude
Code's settings and permission rules), so the run
protocol — worktree, tmux, events file, exit code — is identical whichever agent
you pick.

| Agent | Status | Gated approvals | Session resume | Credentials pushed to remote targets |
|---|---|---|---|---|
| `claude` | first-class | ✅ | ✅ | `~/.claude/.credentials.json` |
| `codex` | first-class | ❌ | ✅ | `~/.codex/auth.json` |
| `gemini` | experimental | ❌ | ❌ | — |

## Choosing one

The web Settings → **Agents** page is the place to add a runner. **Add agent**
offers OpenCode, Aider and a custom runner starter, then keeps the command,
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
including when the declaration is empty. Gemini and custom agents receive no
automatic MCP translation unless their custom launch definition supplies it.

Project MCP is managed from PWA project settings, the terminal dashboard's MCP
action, or `PUT /api/projects/ID/mcp` through the CLI API. Responses redact
credential values; the target-side runtime document is private to its session or
task. The declaration is snapshotted before a background attempt starts, so
routine takeover keeps the original MCP policy when project settings change.
See [context-parity.md](context-parity.md).

Claude and Codex support exact-ID resume and fork in the interactive web and
terminal clients. Gemini and custom agents expose those actions only when their
definition provides the corresponding argument templates; Lectern never falls
back to an unrelated last conversation.

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
  "acp": {"command": "npx", "args": ["-y", "@zed-industries/claude-code-acp"]}
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
