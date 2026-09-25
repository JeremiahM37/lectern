# Triggers

A trigger source lets a project pick up work on its own — the way Devin,
Cursor, Codex, Tembo and Charlie already do from GitHub issues/PR comments,
Slack and Linear — instead of always waiting for a human to press dispatch or
a routine's clock to tick. Implementation: `internal/triggers` (the polling
and Socket Mode engines) plus `internal/api/triggers.go` (the HTTP surface and
the one adapter that turns a matched event into a real task).

## The tailnet note — why this is polling/outbound, not inbound webhooks

Lectern is typically reachable only on a private tailnet, so an inbound
webhook from GitHub, Slack or Linear usually cannot reach it at all — there is
nothing at the address those services would try to call. Every source here is
designed around that constraint:

- **GitHub** is polled: lectern periodically asks GitHub what changed, using
  the target's own `gh` CLI login (the same one `gh pr create` already uses
  for a human's Commit/PR button).
- **Linear** is polled over its GraphQL API with an API key.
- **Slack** uses **Socket Mode** — an *outbound* websocket lectern opens to
  Slack, which Slack then uses to carry events, slash commands and
  interactivity back over. No inbound HTTP endpoint is exposed for Slack
  either.

Nothing in this package listens for an inbound webhook. If you run lectern
somewhere that genuinely is publicly reachable, none of this changes — polling
and Socket Mode still work fine, they are just not the fastest possible path
(GitHub events wait for the next poll instead of arriving instantly).

## Safety model (read this before enabling a source)

A trigger source is a door a stranger can knock on if it watches a public
repo, channel or team. Four things make that safe by default:

1. **An author allowlist is mandatory.** A source with an empty allowlist acts
   on nothing — every event is recorded as skipped with the reason "author
   not on this source's allowlist". You add logins/user ids/emails you trust
   explicitly; there is no "trust everyone" setting.
2. **The permission mode is never configurable per trigger.** Every task a
   trigger creates uses the *project's own* `default_permission_mode`
   (falling back to `acceptEdits`) — the same gating a human's task already
   gets on that project. A trigger source's config cannot elevate this; the
   field does not exist to set.
3. **Per-source rate limiting.** `max_per_hour` in a source's config (default
   10) caps how many tasks one source can create in a rolling hour, checked
   before every task creation. A misconfigured label match or a chatty bot can
   only spend so much before it hits the wall.
4. **Every event is recorded exactly once.** `trigger_events` has a unique
   index on `(source_id, external_id)` — a second delivery of the same GitHub
   webhook-equivalent, Slack envelope or Linear issue update is a no-op, not a
   second task. See "Recent events" below for how to read this ledger.

None of this is a substitute for scoping the credential itself: a GitHub
`gh` login, Slack bot token or Linear API key should have the narrowest access
that lets the trigger do its job.

## Setup

Configuration lives in a project's **Settings → that project's card →
Triggers** section (works down to 390px). Add a source, fill in its config and
secrets, save, then use **Test connection** to check credentials before
turning it on for real traffic.

### GitHub

Fields (`config`):

| Field | Default | Meaning |
|---|---|---|
| `repo` | — (required) | `owner/repo` |
| `label` | `lectern` | issue label that creates a task |
| `queued_label` | `<label>:queued` | label swapped in once filed, so the issue is never re-evaluated |
| `mention_handle` | `@lectern` | text in a PR/issue comment that creates a follow-up task |
| `allowed_authors` | `[]` | GitHub logins allowed to trigger work |
| `base_branch` | project default | branch new task worktrees are cut from |
| `agent` / `model` | project default | which agent runs the task |
| `max_per_hour` | 10 | rate limit |

No secrets are stored for GitHub. Both polling and posting back run through
the project's own target and its `gh` CLI login — `gh auth status` on that
machine is what "Test connection" actually exercises reading `repos/<repo>`.

Behavior:

- An open issue carrying `label` files a task (title, body and the issue URL
  become the prompt), then the issue is relabelled `label` → `queued_label` —
  this is the durable "seen" marker on GitHub's own side, independent of
  lectern's event ledger.
- A PR or issue comment mentioning `mention_handle` files a follow-up task.
- When a **label-triggered** task finishes with changes, lectern commits,
  pushes and opens a pull request with `Closes #N` in the body, then comments
  the PR link on the issue. A task that finished with nothing to commit, or
  that failed/was cancelled, gets a comment explaining that instead — never a
  second silent attempt.
- When a **mention-triggered** task finishes, lectern replies on the
  originating comment's issue/PR with a result summary and diff stats (file
  count, `+adds -dels`) rather than opening a second pull request.
- Rate-limit awareness: every `gh api` call is read with `--include`, and the
  `x-ratelimit-remaining` header is checked before the (more numerous) comment
  poll; below 5 remaining, that poll is skipped for the tick rather than
  spending the budget a relabel or a human might need.
- Cursors are timestamps (`since`), not conditional-request ETags — see the
  comment on `githubCursor` in `internal/triggers/github.go` for why 304
  handling through `gh api` was not something this could verify against live
  GitHub without a real token in this environment, and a since-cursor is a
  fully documented, reliable substitute at homelab polling volumes.

### Slack

Fields (`config`): `channel` (empty = any channel the bot is in),
`allowed_users` (Slack user ids, e.g. `U0123ABCD`), `agent`/`model`,
`max_per_hour`.

Secrets: `app_token` (`xapp-...`, app-level, used to open the Socket Mode
connection) and `bot_token` (`xoxb-...`, used to post messages).

Slack app setup, once, in api.slack.com:

1. Create an app. Under **Socket Mode**, turn it on and generate an
   app-level token with the `connections:write` scope — that is `app_token`.
2. Under **OAuth & Permissions**, add bot scopes `chat:write`, `app_mentions:read`
   and `commands` (if you want the slash command), install the app to your
   workspace, and copy the **Bot User OAuth Token** — that is `bot_token`.
3. Under **Event Subscriptions**, turn events on (Socket Mode does not need a
   Request URL) and subscribe to the bot event `app_mention`.
4. Optionally add a **Slash Command** named `/lectern` — no Request URL needed
   either; Socket Mode carries it.
5. Invite the bot to whichever channel(s) you want it watching.

Behavior:

- A message that `@mentions` the bot, or a `/lectern <text>` command, creates
  a task from the message/command text (the bot mention itself is stripped).
- Lectern posts an immediate acknowledgement in the same channel/thread —
  "On it — created task #N…" — or, if the author was not allowlisted or the
  rate limit was hit, a short reply saying why nothing happened. Slack users
  expect *some* reply; a silent drop reads as broken.
- When the task finishes, lectern replies in the same thread with the result
  summary and diff stats.
- The connection is held open for as long as the source stays enabled, with
  reconnect-with-backoff on any drop, and is torn down on shutdown
  (`Manager.StopSlack`, called from `App.Close`). Editing a source's config or
  secrets restarts its connection on the next tick rather than running on
  stale credentials until the socket happens to drop on its own.

If Socket Mode is not an option for your workspace, note that Slack Events
also supports classic HTTP webhooks — but that needs a public URL, which is
exactly the thing most lectern deployments do not have (see the tailnet note
above). Socket Mode is deliberately the only implementation here; adding a
webhook endpoint later is straightforward (an HTTP handler that verifies
Slack's signing secret and forwards to the same intake path) if a deployment
ever has a public origin and prefers it.

### Linear

Fields (`config`): `team_key` (e.g. `ENG`), `label` (default `lectern`),
`allowed_users` (emails or display names of the issue's creator),
`done_state_name` (default `Done`), `agent`/`model`, `max_per_hour`.

Secrets: `api_key` (a personal or workspace API key from Linear's Settings →
API).

Behavior:

- An issue in `team_key` carrying `label`, created or updated after the source
  was enabled, files a task from its title, description and URL.
- When the task finishes, lectern posts a comment on the issue with the result
  summary and diff stats, then — unless the task failed or was cancelled —
  moves the issue to the workflow state named `done_state_name`. A missing
  state name is reported rather than guessed at; the issue is left where a
  human put it.
- Cursor: `since` (an updatedAt timestamp), same reasoning as GitHub's.

## Freshly configured sources start from "now"

Enabling a GitHub or Linear source seeds its cursor to the moment it was
created, not the beginning of time — otherwise the first poll would treat
every historical labelled issue as brand new and open one task per issue.
Only issues/comments/updates from after that moment are ever considered.

## Recent events

Every event a source sees — matched or not — is one row in `trigger_events`,
visible in the project's Triggers section: timestamp, author, what happened
(`task_created`, `skipped` with a reason, or nothing recorded at all for
traffic that plainly did not match). This is also the dedup ledger: a second
delivery of an event already recorded is a no-op, checked by a database
unique constraint on `(source_id, external_id)`, not by application logic
remembering to check first.

## API

- `GET /api/projects/{id}/triggers` — list a project's sources.
- `POST /api/projects/{id}/triggers` — create one (`kind`, `name`, `config`,
  `secrets`, `interval_s`).
- `PATCH /api/triggers/{id}` — update name/enabled/interval/config/secrets.
  An empty `secrets` body leaves stored secrets untouched.
- `DELETE /api/triggers/{id}` — remove a source and its event history.
- `POST /api/triggers/{id}/test` — a cheap, read-only connectivity check.
- `GET /api/projects/{id}/trigger-events` — recent events, newest first, with
  each event's task title/status inlined.

A source's `secrets` are never returned raw: the API redacts them to
`{"field_name": true|false}` (configured or not), the same pattern
`project.mcp_json` already uses for MCP server credentials.

## Testing

`internal/triggers` is tested with:

- a fake `gh` CLI script placed on `PATH` and driven through a real
  `executor.Local` (so it genuinely shells out, exercising the same code path
  production uses), plus a real local git repository with a `file://`-style
  local bare remote for the commit/push/PR postback path — no network needed;
- an `httptest` GraphQL server for Linear;
- a from-scratch minimal RFC 6455 server (`ws_test.go`) driving the real
  `wsDial` client, plus a full Socket Mode round trip (connect → app_mention
  envelope → ack → task creation → acknowledgement posted back) against that
  server and an `httptest` REST server for `apps.connections.open` /
  `chat.postMessage`.

Run the whole suite through the reviewed isolated runner (see
`CONTRIBUTING.md`), never directly against the host.
