# A2A (Agent2Agent)

Lectern speaks the server half of the [Agent2Agent protocol][a2a] v1.0 (Linux
Foundation) over its JSON-RPC 2.0 binding. That makes it a *remote agent* other
orchestrators can discover and drive: a coordinator that speaks A2A finds
Lectern from its Agent Card, sends it a coding task, polls the task, and reads
back the agent's report and diff — without knowing anything about Lectern's
REST API, its board, or how it runs agents.

Everything A2A does lands on the ordinary board. A task filed over the protocol
is a normal Lectern task in a fresh git worktree of a registered project, with
the same review flow, the same worktree janitor, and the same diff review. The
protocol is a door into Lectern, not a second implementation of it: SendMessage,
CancelTask and the read paths call the same functions the REST API and the PWA
call.

Two endpoints:

| Path | Auth | What it is |
|---|---|---|
| `GET /.well-known/agent-card.json` | none | The Agent Card — public discovery metadata |
| `POST /a2a/v1` | Lectern's normal auth | The JSON-RPC 2.0 interface the card points at |

## The Agent Card

An A2A client reads the card first, before it has any credential, so the card
is served unauthenticated and carries nothing about this install beyond its
name, version and the URL the operator configured:

```bash
curl -s "$LECTERN_BASE_URL/.well-known/agent-card.json" | jq
```

```json
{
  "name": "Lectern",
  "description": "Control plane for AI coding agents. Dispatch a coding task ...",
  "version": "2.3.0",
  "supportedInterfaces": [
    {
      "url": "https://lectern.example.com/a2a/v1",
      "protocolBinding": "JSONRPC",
      "protocolVersion": "1.0"
    }
  ],
  "capabilities": { "streaming": false, "pushNotifications": false },
  "defaultInputModes": ["text/plain"],
  "defaultOutputModes": ["text/plain"],
  "skills": [ { "id": "dispatch-task", "...": "..." } ],
  "securitySchemes": { "bearer": { "...": "..." }, "tailscale": { "...": "..." } },
  "security": [ { "bearer": [] }, { "tailscale": [] } ]
}
```

The interface URL is `LECTERN_BASE_URL` plus `/a2a/v1`, so set it to the
address a remote orchestrator reaches this server at, not to a loopback
default.

Both capabilities are `false`, and that is deliberate. A dispatch is a
long-running task you poll; Lectern already has SSE streams and web push for
its own UI, and the protocol's streaming and push bindings are not a substitute
for them. Poll `GetTask` (or watch the board) rather than waiting for a
callback.

The card advertises three skills:

- **`dispatch-task`** — run a coding task with a chosen agent in a fresh git
  worktree of a registered project and return the diff summary.
- **`best-of-n`** — run the task with several agents, or several models, as
  parallel attempts and pick the best.
- **`project-status`** — report the board for a project.

## Auth

The card is public and needs no credential. Everything at `/a2a/v1` is gated by
the same middleware as `/api`: whatever `LECTERN_AUTH` resolves to, A2A gets.

- In **`token`** mode (and `auto` on a listener that is not loopback-only),
  send `LECTERN_AUTH_TOKEN` as `Authorization: Bearer <token>`.
- In **`tailscale`** mode a request from an allow-listed tailnet peer is
  authenticated as that peer with no credential at all — an orchestrator on the
  same tailnet needs to do nothing.

The card declares both schemes, because every accepted credential has to appear
in it.

## Methods

All four methods are POSTed to `/a2a/v1` as JSON-RPC 2.0. Every reply is HTTP
200 carrying either `result` or a JSON-RPC `error` object — a JSON-RPC client
reads the body, not the status. The examples below use `$LECTERN_BASE_URL` and a
bearer `$TOKEN`; drop the header in `tailscale` or `none` mode.

### SendMessage

Files a task and dispatches it. The project is named in the message metadata
(its name is the task's `contextId`); `agent`, `model`, `title` and `variants`
are optional refinements.

```bash
curl -s "$LECTERN_BASE_URL/a2a/v1" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "SendMessage",
    "params": {
      "message": {
        "messageId": "m-1",
        "role": "ROLE_USER",
        "parts": [{ "text": "Fix the failing pagination test." }],
        "metadata": { "project": "lectern", "agent": "claude" }
      }
    }
  }'
```

Returns `{"task": Task}`. The project must be a registered project; an unknown
or missing one is `-32602` and the error lists the valid names. `metadata` keys:

| Key | Meaning |
|---|---|
| `project` | **Required.** A registered project name. |
| `agent` | The agent to run (`claude`, `codex`, `gemini`, or any configured runner). Defaults to the project's own default agent. |
| `model` | A model override for the agent. |
| `title` | The card title. Defaults to the first line of the message text. |
| `permission_mode` | The permission mode to run under. |
| `variants` | Best-of-N: an array of agent names, an array of `{agent, model, permission_mode, launch_profile}` objects, or an integer 2–8 for that many attempts of the task's own variant. |

For a best-of-N run, list the agents to race:

```bash
  -d '{
    "jsonrpc": "2.0", "id": 2, "method": "SendMessage",
    "params": { "message": {
      "messageId": "m-2", "role": "ROLE_USER",
      "parts": [{ "text": "Refactor the cache layer." }],
      "metadata": { "project": "lectern", "variants": ["claude", "codex"] }
    }}
  }'
```

Each variant becomes its own attempt in its own worktree, exactly as an A/B
dispatch from the board does.

### GetTask

Reports one task's state and, once there is something to report, its artifact.
`id` is the id SendMessage returned (Lectern's task id in decimal).

```bash
curl -s "$LECTERN_BASE_URL/a2a/v1" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"GetTask","params":{"id":"42"}}'
```

Returns `{"task": Task}`; an id that names no task is `-32001`. GetTask is not
limited to tasks filed over A2A — an orchestrator holding a board id can read
it, and the auth gate is what protects it.

### ListTasks

Lists the tasks this protocol filed, newest first, optionally narrowed to one
project by `contextId`. This is the read side of the `project-status` skill.

```bash
# every A2A task
curl -s "$LECTERN_BASE_URL/a2a/v1" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"ListTasks"}'

# one project's board, capped
curl -s "$LECTERN_BASE_URL/a2a/v1" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"ListTasks",
       "params":{"contextId":"lectern","limit":20}}'
```

Returns `{"tasks":[...]}`. `limit` defaults to 20 and is capped at 100.
ListTasks reports only tasks filed through A2A (they carry `created_by: a2a`),
not everything a person filed on the board.

### CancelTask

Stops a queued or running task through the same scheduler path the board's
cancel uses, so the agent process and its worktree are released.

```bash
curl -s "$LECTERN_BASE_URL/a2a/v1" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":6,"method":"CancelTask","params":{"id":"42"}}'
```

Returns the task. Cancelling a task that is already past being cancelable is
not an error: the task is returned in the state it is in, because that state is
the answer.

## Task states

Lectern's board columns are not the protocol's task states, so each column maps
onto the closest state A2A defines:

| Lectern | A2A | Meaning |
|---|---|---|
| `queued` (and `backlog`) | `TASK_STATE_SUBMITTED` | Accepted, not started |
| `running` | `TASK_STATE_WORKING` | An attempt is in flight |
| `review` | `TASK_STATE_INPUT_REQUIRED` | The agent stopped and a human owes it a decision |
| `done` | `TASK_STATE_COMPLETED` | Finished |
| `failed` | `TASK_STATE_FAILED` | The attempt failed |
| `cancelled` | `TASK_STATE_CANCELED` | Stopped |

`review` is the state that matters most to a driving orchestrator: a Lectern
attempt always ends by waiting for a human to accept it or send it back, so a
task that finished successfully surfaces as `INPUT_REQUIRED`, not `COMPLETED`.
`COMPLETED` is a task a person has accepted. The status carries a message in
that state (and on failure) explaining what is owed.

A task's artifact carries the newest attempt's report and diff summary as text
parts, so a client that only reads the protocol has the agent's own words and
what changed. When the task has a prompt and a report, both appear in
`history`.

## Errors

Standard JSON-RPC 2.0 error objects:

| Code | When |
|---|---|
| `-32601` | Method not found. `error.data.methods` lists the served methods. |
| `-32602` | Invalid params — missing project, unknown project or agent, a bad `variants` value, a missing task id. |
| `-32001` | Task not found. |
| `-32600` | The request is not a valid JSON-RPC 2.0 envelope. |

## Related

- [Agents](agents.md) — the runners A2A dispatches to.
- [Delegated builds](DELEGATED_BUILDS.md) — Lectern's own lead/worker
  delegation, which is a different mechanism: it is an agent in a session
  handing work to a worker, not an outside orchestrator calling in.

[a2a]: https://a2a-protocol.org/
