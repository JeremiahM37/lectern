# Agent events, identity and review — design contract

This file is the shared contract for the work on branch `feat/agent-events`.
Every worker implements against it; change it only by editing this file in
the same commit as the code that needs the change.

Reference payloads captured from Claude Code 2.1.281 live in
`/mnt/bulk/lectern-events-ref/` (SessionStart, UserPromptSubmit, Stop,
statusline). Use them as test fixtures; do not invent field names.

## 1. Identity and auth (`internal/auth`)

`LECTERN_AUTH` = `auto` (default) | `none` | `token` | `tailscale`.

- `auto`: listen host is loopback (`127.0.0.1`, `::1`, `localhost`) → `none`.
  Otherwise, if tailscaled's LocalAPI answers → `tailscale`; otherwise, if
  `LECTERN_AUTH_TOKEN` is set → `token`; otherwise `none` with a loud startup
  warning. A single-machine user therefore never configures anything.
- `LECTERN_AUTH_TOKEN`, when set, is accepted in every mode except `none`
  (Bearer header, `lectern_token` cookie, or `?token=`), for scripts and for
  clients that are not on the tailnet.

Principal resolution for one request (`auth.Principal{Kind, Login, Node, Human bool}`):

1. Remote address is loopback:
   - `X-Forwarded-For` whose first address is a tailnet IP (tailscale serve,
     which proxies from loopback) → whois that address.
   - else `Tailscale-User-Login` header → that login.
   - else → `Kind: "local"`, `Human: false` (a process on this machine,
     possibly an agent).
   - **Both header paths require `LECTERN_TRUST_SERVE_HEADERS=1`** (default
     off — see the second deviation below). Without it, loopback is always
     `Kind: "local"`, `Human: false`, headers or not.
2. Remote address is a tailnet IP (100.64.0.0/10, fd7a:115c:a1e0::/48) →
   LocalAPI `GET /localapi/v0/whois?addr=IP:PORT` (socket
   `/var/run/tailscale/tailscaled.sock`, overridable `LECTERN_TAILSCALE_SOCKET`).
   Cache results for 60s.
3. Anything else (LAN) → token required.

Allowed tailnet identities: `LECTERN_TAILSCALE_USERS` (comma list of login
names); default is the login that owns this node (LocalAPI status → Self.UserID
→ User[..].LoginName). Tagged nodes are denied unless a tag is listed in
`LECTERN_TAILSCALE_TAGS`. This tailnet has other people on it; "on the tailnet"
is NOT authorisation.

`local` principals are allowed for ordinary API use in every mode (the MCP
server, CLI and agents on this host use it), but **cannot decide approvals**
unless the mode is `none`. Approval decisions need a human principal
(tailscale identity, token, or mode `none`).

**Deviation (implemented in `internal/auth`):** the loopback-bypass above does
not extend to mode `token`. `token` is chosen specifically when there's no
tailscale identity to fall back on, so unlike `none`/`tailscale` it doesn't
also relax loopback: every request, including one from a process on this
machine, needs the token. This is what `e2e/test_ui.py::test_token_auth_ui_works`
already encoded (a browser hitting `http://127.0.0.1:PORT` on a token-configured
server must get 401 without the token) — a literal "every mode" reading would
have broken it, since a same-host browser is indistinguishable from a trusted
local CLI at the TCP layer. `none` and `tailscale` mode are unaffected and
match the text above exactly.

**Deviation 2 (security fix, found in review before merge):** the loopback
`X-Forwarded-For`/`Tailscale-User-Login` path is gated behind
`LECTERN_TRUST_SERVE_HEADERS=1` (default off), not on unconditionally in
`tailscale` mode as originally written. Loopback can't tell `tailscale serve`
proxying a real tailnet client from any other process on the box — including a
dispatched agent — sending the same headers to `127.0.0.1` directly, which
would otherwise let an agent forge a human principal and approve its own
permission request. `internal/auth.Resolver.Authenticate` only honors the
headers on loopback when the flag is set; otherwise loopback in `tailscale`
mode is unconditionally `Kind: "local"`, `Human: false`.

**Addition: a native TLS listener**, so the flag above has a real alternative
instead of being the only way to get a phone a secure context.
`LECTERN_TLS=tailscale` (or auto-on when the resolved auth mode is `tailscale`
and `LECTERN_TLS_PORT` is set) brings up a second listener on
`LECTERN_TLS_PORT`, bound to this node's tailnet addresses — both v4 and v6,
from LocalAPI `status.Self.TailscaleIPs` — serving the same handler as the
plain HTTP listener. Its `tls.Config.GetCertificate` (`internal/auth.CertCache`)
fetches the cert/key pair from LocalAPI `GET
/localapi/v0/cert/<dnsname>?type=pair` (`dnsname` = `status.Self.DNSName` minus
its trailing dot; the response is the leaf cert chain PEM immediately followed
by the key PEM — `internal/auth.splitCertPair` separates them by PEM block
type rather than assuming that order), cached in memory and refetched once
within 7 days of the cached cert's expiry. `LocalAPI.Cert` is an interface
method for exactly this: tests fake it instead of touching a real tailscaled.
Requests on this listener carry the real tailnet peer address, so whois there
is trustworthy without `tailscale serve` and without
`LECTERN_TRUST_SERVE_HEADERS`. An existing `tailscale serve --https=8443
http://127.0.0.1:9110` should be replaced with `LECTERN_TLS_PORT=8443` and
`serve` turned off — Lectern now serves that port itself. (Not done as part of
this change: the live AIServer `serve` config stays as-is until someone
deliberately cuts it over.)

Everything is gated: API, SSE, `/term/*` proxy, static assets may stay open.
`GET /api/whoami` → `{mode, kind, login, node, human}`.

## 2. Agent hooks (`internal/agentevents`)

Each session gets a random hook secret (column `sessions.hook_token`) and the
env `LECTERN_HOOK_TOKEN`, `LECTERN_HOOK_URL` (base, e.g.
`http://100.96.103.31:9110/api/hook/session/<id>`). `LECTERN_HOOK_BASE`
configures the host part for remote targets (default `LECTERN_BASE_URL`).

Endpoint: `POST /api/hook/session/{id}/{event}` (inside the existing
`/api/hook/*` group that `withAuth` exempts, like the task approval hook),
authenticated ONLY by
`Authorization: Bearer <hook_token>` for that session (not by the general
auth), body = the agent's hook JSON. Unknown events are accepted and ignored.

Claude Code sessions are launched with `--settings <file>` (a file written per
session on the target, under `~/.lectern/hooks/`), merging with the user's own
settings. It registers `type: "http"` hooks with
`headers: {"Authorization": "Bearer $LECTERN_HOOK_TOKEN"}` and
`allowedEnvVars: ["LECTERN_HOOK_TOKEN"]` for: SessionStart, UserPromptSubmit,
PreToolUse (async-safe, short timeout), PostToolUse, Notification, Stop,
StopFailure, PreCompact, SessionEnd, and (only in `ask` permission mode)
PermissionRequest. It also sets `statusLine` to a small POSIX sh script that
POSTs its stdin to `.../statusline` in the background (curl, 2s max) and then
execs the user's own statusLine command if they had one (read from their
settings at launch), else prints a short line.

Codex sessions: `-c notify=[...]` for agent-turn-complete plus whatever
hooks.json events codex 0.155 supports (probe it), and usage from its rollout
JSONL. Anything not covered falls back to screen state.

> **Implementation note (2026-09-23, section 2 worker) — what probing codex
> 0.155.1 found**, so a later worker does not have to redo it. `codex --help`
> / `codex exec --help` list `--dangerously-bypass-hook-trust` ("Run enabled
> hooks without requiring persisted hook trust for this invocation.
> DANGEROUS. Intended only for automation that already vets hook
> sources") — hooks are a real, shipped feature, gated by a trust dialog like
> MCP servers. `strings` on the installed binary
> (`~/.codex/packages/standalone/releases/0.155.1-*/bin/codex`) turns up full
> embedded JSON Schemas (title `pre-tool-use.command.input` etc.) for
> `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
> `PreCompact`, `PostCompact`, `SessionEnd`, `SubagentStart`, `SubagentStop`,
> `PermissionRequest`, `Stop` and `Interrupt` — byte-for-byte Claude Code's
> own wire format: `hook_event_name`, `session_id`, `cwd`, `permission_mode`
> enum (`default|acceptEdits|plan|dontAsk|bypassPermissions`),
> `hookSpecificOutput`, `permissionDecision`
> (`allow|deny|ask`/`updatedInput`/`permissionDecisionReason`), `decision:
> block`, `continue`, `stopReason`, `suppressOutput`. A `PermissionRequest`
> hook's answer is exactly this contract's
> `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":
> {"behavior":"allow"|"deny","message":"..."}}}` — so `internal/agentevents`'s
> ingest path needs no per-agent branching for that shape. Handlers are
> **command**-based (`codex_hooks::engine::command_runner`, invoked via
> `sh -lc`), not Claude's `type: "http"` — no evidence of a native HTTP
> handler for codex hooks was found. What was **not** pinned down in this
> pass: the exact on-disk hooks.json registration schema (event name → list
> of handlers) and its project-level discovery path — a plugin manifest can
> point `"hooks": "./hooks.json"`, and a
> `.tmp/plugins/.../hooks/hooks.json` path turned up in an installed plugin
> tree, but nothing here proves a *standalone* project's `.codex/hooks.json`
> (or an equivalent `-c hooks...=` override) is discovered the way
> `.claude/settings.json` is, and doing so needs a real ChatGPT-authenticated
> turn to observe, which this offline pass could not run. **What IS wired**
> (`internal/agentevents/codex_settings.go`,
> `internal/sessions.codexNotifyArg`): the independently-confirmed `notify`
> mechanism — `-c 'notify=["python3","<script>"]'` was accepted outright by
> `--strict-config`, and the binary's strings list the exact notify payload
> field names (`thread-id`, `turn-id`, `cwd`, `client`, `input-messages`,
> `last-assistant-message`) passed as that script's last argv element on
> agent-turn-complete. Lectern's notify script POSTs a synthetic
> `AgentTurnComplete` event to the same `/api/hook/session/{id}/{event}`
> endpoint, mapped to `idle` — covering this section's headline mapping
> without depending on the unconfirmed hooks.json shape. The cost: a codex
> session gets no PreToolUse/PostToolUse/Notification-equivalent signal
> between turns, so it falls back to screen scraping there exactly as an
> agent with no hooks at all does (poll.go's 10-minute rule). Wiring
> hooks.json for the richer signals is a good next step and needs no ingest
> changes — `MapEventState`/`IngestEvent` already accept every event name
> above.

Both the claude and codex installers write into `~/.lectern/hooks/` on the
session's TARGET (not the control plane) via the same `ex.Run` seam
`internal/sessions/agents.go`'s `claudeTrust`/`codexTrust` probes already use,
and are only attempted when the launched spec is the true, unmodified
built-in (`spec.Builtin`) — an operator's own agent definition that happens
to reuse the name `claude` or `codex` (as
`e2e/test_session_continuity.py`'s scripted stand-in does) is a different
program and is not assumed to understand `--settings` or `-c notify=[...]`.

### Session state

New columns on `sessions`: `agent_state` (`working` | `waiting_input` |
`waiting_permission` | `idle` | `error` | `ended`), `state_source` (`hook` |
`screen`), `state_at`, `hook_seen_at`.

Mapping: UserPromptSubmit/PreToolUse/PostToolUse → working;
Notification(permission_prompt) → waiting_permission;
Notification(idle_prompt) → waiting_input; Stop → idle; StopFailure → error;
SessionEnd → ended. Screen scraping keeps running but only writes state when
no hook event arrived for that session in the last 10 minutes, or the agent has
no hooks. Every change publishes bus event `session.state`
`{id, state, previous, source, at}`.

### Usage (from statusline, transcript, codex rollout, stream-json)

Table `usage_samples`-free design: latest values on the row, history in
`usage_daily`. Columns on `sessions`: `model`, `context_pct`, `context_tokens`,
`context_size`, `cost_usd`, `lines_added`, `lines_removed`, `rate_5h_pct`,
`rate_5h_reset`, `rate_7d_pct`, `rate_7d_reset`, `usage_at`. Rate limits are
per account, so also keep the latest in a `settings` key `rate_limits`.
Bus event `session.usage`. Tasks keep their existing cost accounting and gain
the same context fields where the driver reports them.

> **Correction (2026-09-23, cards/usage-view worker):** `context_pct` above
> collided with a column that already existed before this section — the
> screen-scraped "percent left until auto-compact" ContextPct
> (`internal/sessions.ContextPct`, low is bad), which `poll.go`'s `applyPane`
> keeps writing on every tick regardless of hooks. The statusline path was
> writing `used_percentage` (high is bad) into that same column, so the two
> fought over one field with opposite meanings. Fixed by giving the hook/
> rollout-sourced reading its own column, `context_used_pct` (int, nullable),
> documented on the column itself in `store/schema.go`. `context_pct` keeps
> its original screen-scraped meaning and is untouched; every card renders
> `context_used_pct` (amber ≥70, red ≥85) when present, since that is what
> "how full is the context window" means for the UI, and falls back to the
> legacy bar only when a session has never reported it.
>
> **Codex usage**, since codex has no statusline/http-hook push for it: a
> session's `codex_thread_id` column is learned from the `AgentTurnComplete`
> notify payload's `thread-id` (validated against
> `agentevents.ValidCodexThreadID` before it is ever stored or shell-quoted
> into a command — see `codex_rollout.go`). `internal/sessions.Poll` then
> batches one `ls`+`tail` per target, per poll tick, over every codex
> session's exact `~/.codex/sessions/*/*/*/rollout-*-<thread_id>.jsonl`, and
> `agentevents.ParseCodexRolloutUsage` reads the latest `token_count`
> `event_msg` record (real shape confirmed against live 0.155.1 rollout files
> on this machine — `total_token_usage`/`model_context_window`, genuinely
> cumulative for the whole thread, unlike Claude's confusingly-named
> `total_input_tokens`). Feeds the same `context_used_pct`/`context_tokens`/
> `context_size`/`model` fields and the same `usage_daily` delta-booking
> (`Ingester.IngestCodexUsage`) as the statusline path; `cost_usd` is left
> alone, since codex reports no dollar figure — the UI shows tokens instead
> for a codex session, per this section's original text above.
>
> **`precompact_at`** (float, nullable) records the last time a `PreCompact`
> hook reached a session, independent of `agent_state` (PreCompact still maps
> to `ok=false` in `MapEventState`, so it never changes `agent_state` itself —
> only `hook_seen_at` moved before this addition). It exists so a card can
> show a brief "compacting" note in the window before the next statusline/
> rollout tick reports the resulting drop in `context_used_pct`. This is a
> read model only; the section 3 push on `PreCompact(auto)` is a different
> worker's responsibility and is untouched here.

## 3. Alerts and approvals

Push (existing Web Push) on session transitions: → waiting_permission,
→ waiting_input, working → idle ("done"), → error, PreCompact(auto)
("compacting"). One push per transition, never repeated for the same state;
suppressed if a browser typed into that session within the last 30s. Per-kind
toggles in settings.

Session permission mode: `bypass` (today's default, unchanged) | `ask`.
Global default setting + per-launch override. In `ask`, the PermissionRequest
hook creates an approval (reuse the broker's storage), pushes a notification
with Approve / Deny actions, and holds the HTTP request until decided or
`LECTERN_APPROVAL_HOLD` (default 120s) passes; on timeout it answers `{}` so
Claude falls back to its own terminal dialog. Response on decision:
`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"|"deny","message":"..."}}}`.

## 4. Checks on stop, review, best-of-N, evals, drivers

- Checks: project setting `check_command` (default: `verify` when the repo has
  `.verify.yaml` and `verify` is on PATH on the target; otherwise none). On a
  session's Stop, if the worktree diff changed since the last check, run it on
  the target (timeout, one at a time per session), store in `session_checks`,
  show on the card, push on failure. Task auto-verify uses the same setting.
- Review: sessions get diff (merge-base with the base branch, including
  uncommitted), commit, push and PR, reusing the task implementation. Inline
  diff comments `{file, line, side, text}` are batched and sent to a session
  as one prompt (SendText) or to a task as request-changes feedback. PR
  description generated by a headless cheap-model call on the diff.
- Best-of-N: generalise A/B to N attempts across agent/model profiles, each in
  its own worktree, each checked; compare view (diff, check result, cost,
  time); pick one, archive the rest; optional judge agent.
- Evals ("agent tests"): suites of cases `{name, project, base_ref, prompt,
  check_command, timeout}` (UI or `.lectern/evals/*.yaml`); a run is cases ×
  profiles × repeats executed headless through the structured drivers; results
  store pass/fail, duration, cost, tokens, diff size; leaderboard + matrix UI.
- Structured drivers (`internal/drivers`, implemented): `drivers.Driver` starts
  a run and returns a `drivers.Handle` (`Events`, `Send`, `Cancel`, `Wait`);
  `drivers.Select(agent, builtin, permissionMode)` is the one place that turns
  "which agent, launched how" into a driver kind, and `drivers.Run(ctx, ex,
  spec)` is the standalone entry point for a future best-of-N/eval worker —
  no scheduler, database or board required, just an `executor.Executor`.
  `attempts.driver` (new column) records the choice, exposed as
  `attempt.driver` on the task view.
  - `claude-exec` / `codex-exec` / `gemini-exec` (`exec.go`) are the EXISTING
    tmux + redirected-events.jsonl path, unchanged: they call the same
    `agents.Launcher.Command` and `agents.ParseStreamLines` scheduler.go
    already used, just behind the `Handle` interface. Not steerable.
  - `claude-steer` (permission_mode `steerable`, claude only): claude launches
    with `--input-format stream-json --output-format stream-json` instead of
    `-p <prompt>`, reading stdin from a FIFO a small python3 pump
    (`.lectern/pump.py`) keeps open for the run's whole life — see `fifo.go`'s
    package comment for why a plain FIFO redirect breaks after the first
    message. `Send` appends another `{"type":"user","message":{"role":"user",
    "content":[{"type":"text","text":...}]}}` line (verified against `claude
    --help`'s `--input-format` and Claude Code's streaming-input docs) to the
    fifo at any time; `Cancel` sends an end sentinel that closes the pump,
    which closes claude's stdin, which lets it exit normally.
  - `codex-appserver` (permission_mode `default` on codex): runs
    `codex app-server` over the same fifo/pump substrate, speaking JSON-RPC
    (initialize, thread/start, turn/start, turn/steer for `Send`,
    turn/interrupt for `Cancel`). Its `execCommandApproval`/
    `applyPatchApproval` server requests are routed through
    `internal/broker` exactly like claude's PreToolUse hook — this is what
    lets a codex task run gated instead of only bypass, which was rejected
    outright before this driver existed. Falls back to `codex exec --json`
    (bypass mode, with a visible timeline notice) if the `initialize`
    handshake fails. Protocol shapes were read directly off `codex
    app-server generate-json-schema --out DIR` (codex-cli 0.155.1, the stable
    non-experimental subset), not guessed — see `codex_appserver.go`'s doc
    comment for the exact method list.
  - The scheduler wires the two streaming kinds through `driverRuns` (a live
    `Handle` per attempt); every other attempt is completely unaffected — the
    map is only ever populated for `claude-steer`/`codex-appserver`, and
    `poll()`/`CancelAttempt` skip straight past anything in it. Ending a
    steerable/gated run is the existing Cancel action: it closes gracefully
    and is finalised (diff capture, auto-verify) exactly like a naturally
    completed attempt, instead of the bare 'cancelled' status a tmux kill
    leaves behind. There is deliberately no auto-close grace timer — these
    runs stay open, like an interactive session, until the operator ends them.
  - `POST /api/tasks/{id}/steer {text}` delivers a follow-up to a running
    attempt's driver (`Scheduler.Steer`); the task detail view shows a "Send
    to running agent" input only while `task.status === "running" &&
    task.attempt.driver === "claude-steer"`.
  - Not done: `codex-appserver` is not wired into the frontend steer input
    (its approvals surface through the existing approvals UI instead); there
    is no driver for a configured custom CLI (`task-generic` stays on the
    scheduler's existing generic-task tmux path); no auto-close/idle-timeout
    policy for a steerable run left unattended.
