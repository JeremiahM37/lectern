# ACP agents

Zed, Toad, Devin Desktop and OpenHands can all drive any agent that speaks
the **Agent Client Protocol** ([agentclientprotocol.com](https://agentclientprotocol.com)) —
JSON-RPC 2.0 over stdio. Lectern needed a custom runner per CLI before this;
now any ACP agent gets a live timeline, mid-run steering and gated approvals
— the same capabilities `codex-appserver` already gives Codex — with **no
backend code per agent**. `internal/drivers/acp.go` implements the client
side; `internal/agents.ACPDefinition` / `internal/sessions.ACPSpec` are the
`acp: {command, args, env}` registry field (see
[docs/agents.md](agents.md#acp-agents-any-cli-no-backend-code)).

## Adding an agent

Settings → **Agents** → Add agent → the "ACP agent" starter template, or the
terminal dashboard's `Q` shortcut, offer three presets — each is only a
starting point; they are not pre-registered, and Lectern never assumes the
binary exists until you save and dispatch against it:

| Preset | Command | Notes |
|---|---|---|
| Claude Code (ACP) | `npx -y @zed-industries/claude-code-acp` | Zed's own adapter; needs a Claude Code login exactly like the built-in `claude` agent |
| Codex (ACP) | `npx -y @zed-industries/codex-acp` | Zed's own adapter; ships a platform-specific native binary via `optionalDependencies` |
| Gemini CLI (ACP) | `gemini --experimental-acp` | Gemini CLI's own native ACP mode — no adapter package, but `gemini` itself must already be installed |

Or by hand:

```bash
curl -X PUT .../api/agents -d '[{
  "name": "claude-code-acp",
  "command": "claude-code-acp",
  "acp": {"command": "npx", "args": ["-y", "@zed-industries/claude-code-acp"]}
}]'
```

A task dispatched on this agent picks the `acp` driver automatically —
visible as `attempt.driver == "acp"` on the task view, exactly like
`attempt.driver == "codex-appserver"` for a gated Codex run. Interactive
sessions are unaffected: they stay tmux-based, same as every other agent —
ACP is for **headless** work (tasks, routines, best-of-N, evals), where a
protocol built for a client to observe and drive a turn structurally is a
better fit than screen-scraping a TUI.

## Protocol facts this driver relies on

Read directly off `@agentclientprotocol/sdk@0.14.1`'s `schema/schema.json`
and `dist/{acp,stream}.js` (the exact dependency
`@zed-industries/claude-code-acp@0.16.2` ships with, via `npm pack`, never
executed against a real model) — **not** the docs site's prose, which proved
lossy when cross-checked against the shipped SDK (its own summariser invented
methods that do not exist, such as `session/list` and `$/cancel_request`).

- **Framing**: newline-delimited JSON-RPC 2.0 over stdio — one
  `JSON.stringify(message) + "\n"` per message, **not** Content-Length/LSP
  framing. `protocolVersion` is an int; `PROTOCOL_VERSION` is `1`.
- **Agent methods** (Lectern → agent): `initialize`, `session/new`,
  `session/prompt`; notification `session/cancel`.
- **Client methods** (agent → Lectern): `fs/read_text_file`,
  `fs/write_text_file`, `session/request_permission`; notification
  `session/update`.
- `session/prompt`'s **response** is the turn's own completion
  (`{stopReason, usage?}`) — unlike Codex's `turn/start`, which acks
  immediately and reports completion via a separate notification. This
  driver issues it from a dedicated background goroutine rather than
  blocking `Start`/`Send` on it, since a real turn can run far longer than
  any other request this codebase makes.
- `terminal/*` (`terminal/create`/`output`/`wait_for_exit`/`kill`/`release`)
  exists in the schema but this driver declines it outright
  (`clientCapabilities.terminal: false` in `initialize`) — see **Limits**.

## Mapping onto Lectern's timeline

| ACP | Lectern event |
|---|---|
| `session/update` → `agent_message_chunk` / `agent_thought_chunk` | `text` |
| `session/update` → `tool_call` (first sight of a `toolCallId`) | `tool_use` (`name` from `ToolKind`: execute→Bash, edit/delete/move→Edit, read/search→Read, fetch→Fetch, else Tool) |
| `session/update` → `tool_call`/`tool_call_update` with `status: completed\|failed` | `tool_result` (`is_error` on `failed`; content is the tool's rendered text/diff-path/`[terminal output]` parts, or its title if none) |
| `session/update` → `plan` | `text`, rendered as a `- [ ]`/`- [x]` checklist |
| `session/prompt`'s `{stopReason, usage?}` response | `result` (`subtype: "success"` for `end_turn` or an operator-requested `cancelled`; `"error"` otherwise — `max_tokens`, `max_turn_requests`, `refusal`, an unrequested `cancelled`) |
| `session/update` → `available_commands_update`, `current_mode_update`, `config_option_update`, `session_info_update`, `usage_update` | not surfaced (accepted and ignored) |

## Permissions

`session/request_permission` is routed through `internal/broker` exactly like
Codex's `execCommandApproval`/`applyPatchApproval` — the tool name/input
shape it produces is the same one the approvals UI already renders for
claude/codex, so it needed no ACP-specific case there.

| Lectern permission mode | Behaviour |
|---|---|
| `default` (gated) | Every `session/request_permission` call creates a real pending approval and waits for a decision, exactly like a Codex gated task |
| `acceptEdits`, `plan`, `bypassPermissions`, `steerable`, unset | Auto-allow: the protocol's `allow_always` option is selected (falling back to `allow_once` if the agent did not offer one) |

The protocol requires the client to answer every still-pending permission
request with `{outcome:{outcome:"cancelled"}}` once it has sent
`session/cancel` — this driver's `Cancel` sets that flag first, so any
request arriving after cancellation is answered that way instead of being
routed anywhere.

## Steering

`session/prompt` is request/response — its response **is** the turn ending,
so there is no way to fold a message into an already-in-flight call the way
Codex's `turn/steer` or Claude's streaming-input fifo can. `Send` while a
turn is active **queues** the message; the driver's own loop starts it as a
fresh `session/prompt` on the same session the moment the current one's
response arrives — "a follow-up `session/prompt` after the current turn
ends, or queue," exactly as specified.

## Filesystem confinement

The client capability this driver advertises (`fs.readTextFile`,
`fs.writeTextFile`) is scoped to the attempt's own worktree, not the whole
target: `fs/read_text_file`/`fs/write_text_file` calls are resolved with
`filepath.Rel` against the worktree root, and anything that is not an
absolute path *inside* it — a relative path, a `..` escape, an absolute path
elsewhere — is refused with a JSON-RPC error before `executor.Executor` ever
sees it. `line`/`limit` (1-based line window) are supported for a partial
read.

## Limits (not done)

- **`terminal/*` is declined**, not implemented. A well-behaved ACP agent
  falls back to its own tool-call flow (asking for permission to run a
  command, same as it would for any client with no terminal capability) —
  which this driver already handles — so nothing is lost for a
  correctly-written agent; a non-compliant one calling it anyway gets a
  clean protocol error rather than a hang.
- **No `session/load` / resume.** A follow-up attempt starts a fresh ACP
  process and session, the same as every other driver's follow-up today —
  there is no cross-process continuation of an ACP session.
- **No `session/set_mode` / `session/set_model`.** Lectern's own `model`
  field is not translated onto an ACP agent.
- **`plan` update field names are a best-effort read**, not independently
  re-verified against the SDK schema during this work — a shape mismatch
  silently emits no plan text rather than erroring, so this is safe to be
  wrong about, just possibly quiet.
- **`usage` field names on `session/prompt`'s response** are similarly
  best-effort (`input_tokens`/`output_tokens`); an agent that reports usage
  under different keys just shows no token count, not an error.
- **Interactive sessions do not use ACP.** They stay tmux-based; ACP is
  wired for headless tasks/routines/best-of-N/evals only (see GAP context in
  `docs/agent-events.md` §4 for the same split on `codex-appserver`).
