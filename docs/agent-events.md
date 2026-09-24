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

> **Implementation note (2026-09-23, section 3 worker).**
>
> **Alerts** (`internal/alerts`): wired as `agentevents.Ingester.OnHookEvent`
> (a new synchronous, best-effort callback fired at the end of every
> `IngestEvent`), not a generic bus subscription — PreCompact's `trigger` and
> Stop's `last_assistant_message` never reach the bus as structured fields,
> only the raw hook body, so a bus-only design could not read them. This
> means alerts only cover **hook-driven** sessions; a session with no hooks
> at all, or one past poll.go's 10-minute hook-silence fallback window, gets
> no separate screen-derived alert in this pass. Given every builtin
> claude/codex session installs hooks at launch (section 2), this covers the
> overwhelming majority of real sessions; extending it to the screen-scrape
> path is a reasonable follow-up, not required here.
>
> **Deviation**: a `PermissionRequest` event does **not** also fire the
> generic "needs permission" text alert when the session is in `ask` mode —
> the approval flow below already sends a dedicated, actionable
> `kind:"approval"` push (tool name + input summary + Approve/Deny) for that
> exact tool call, and a second, read-only push for the same event would
> just double-ping the phone. Claude's `Notification(permission_prompt)`
> hook — the only signal a **non**-`ask` session's rare permission prompt
> produces — still gets the generic alert, using its `message` field (there
> is no discrete tool name on `Notification`, unlike `PermissionRequest`).
>
> Per-kind toggles are `alert_waiting_permission` / `alert_waiting_input` /
> `alert_idle` / `alert_error` / `alert_compacting`, appended to `sinks.Keys`
> (default ON; `"0"` turns one off) rather than standing up a second settings
> registry. Per-session cooldown is 60s (`alerts.DefaultCooldown`); the
> suppress window is exactly the 30s above.
>
> **Suppression / "browser typed" signal**: NOT read from the ttyd reverse
> proxy. `internal/api/termproxy.go`'s `termProxy` hands an `Upgrade` request
> straight to `net/http/httputil.ReverseProxy`, which hijacks the TCP
> connection and pipes it byte-for-byte in both directions with no per-frame
> hook — adding one would mean replacing that proxy with a hand-rolled
> hijacking implementation across every attachment kind
> (session/attempt/project/companion shells), which is a lot of surface and
> regression risk for this alone. Instead, per the contract's own fallback,
> the terminal page sends an explicit heartbeat: `frontend/src/terminal/
> engine.ts`'s `input()` — called for every real keystroke, on-screen key,
> snippet and paste — POSTs `POST /api/term/{kind}/{id}/activity` (only
> meaningful for `kind==="session"`), recorded in `internal/alerts.Activity`,
> an in-memory per-session last-input map with no persistence (a restart
> losing 30s of "just typed" state is immaterial).
>
> **Session permission mode**: `sessions.permission_mode` (`""|"bypass"|
> "ask"`) is resolved once at launch (`internal/sessions/manager.go`'s
> `askPermission := m.AskPermission || o.PermissionMode == "ask"`) and stored
> both on the session row and in its `LaunchConfiguration`, so a native
> continuation of the same session keeps the same mode without the caller
> re-specifying it. The primary `POST /api/sessions` handler resolves the
> effective mode as: an explicit `permission_mode` field wins outright; else
> an explicit legacy `yolo` boolean derives it (`false` → `ask` — this is a
> **behavior addition**, not a break: "let the agent ask" always meant
> `yolo:false`, and now that lectern can actually receive and act on that
> hook, wiring it up here is the feature, not a regression); else the
> `session_permission_mode` global default setting (`"bypass"` when unset).
> Internal launch paths that predate this feature and compute their own
> `Yolo` independently (task takeover, handoff, scratch shells) are left
> alone: they never set `LaunchOpts.PermissionMode`, so they get exactly
> their pre-existing behavior (no `PermissionRequest` hook registered),
> avoiding a surprise multi-minute hold on an automated flow nobody is
> watching a phone for.
>
> **Approvals schema** (docs' "extend it so an approval can belong to a
> session instead of an attempt, schema append-only"): `approvals` gained a
> nullable `session_id` column and `attempt_id` was relaxed from `NOT NULL`
> to nullable — SQLite has no `ALTER COLUMN` to drop a `NOT NULL`
> constraint, so this is a one-time table rebuild
> (`internal/store/migrate_approvals.go`, guarded on `PRAGMA table_info`
> still showing the old `NOT NULL` shape, so it is a no-op on a fresh
> database and every later boot of an already-migrated one) rather than a
> plain entry in the `migrations` `[]string` list. Every existing row keeps
> its id and data; only the new column is added. `store.Approval.AttemptID`/
> `SessionID` keep the pre-existing "0 means absent" Go-level convention
> (matching `TaskID`), while the underlying DB column is genuine `NULL` for
> whichever side does not apply — required for foreign-key correctness,
> since a `0` sentinel would need a real `attempts.id`/`sessions.id` row 0 to
> satisfy the FK, which never exists.
>
> `broker.CreateForSession`/`WaitOnce` are new, parallel to `Create`/`Wait`:
> a session's `PermissionRequest` hook is **one held HTTP request**
> (`internal/api/hooks_agentevents.go`'s `holdSessionPermissionRequest`), not
> the task hook's long-poll loop, so `WaitOnce` resolves in a single
> decide-or-timeout window and does not consult the unrelated, much longer
> `ApprovalExpire` (900s) the task path uses — `LECTERN_APPROVAL_HOLD`
> (`config.SessionApprovalHold`, default 120s) is its own, independent knob.
> A `PostToolUse` or `Notification` hook arriving for a session expires any
> approval still `pending` for it (`broker.ExpireForSession`) — the
> contract's "if the session's terminal answers first ..., expire it".
> Deciding a session approval reuses the existing `POST
> /api/approvals/{id}/decision` endpoint and its human-principal check
> unchanged; "always allow" (a project policy rule) is a no-op for a
> session-scoped approval, which has no attempt to resolve a project from.
>
> **Frontend**: `NeedsYou.tsx`'s approval row and a new `SessionCard.tsx`
> banner both show a session approval's tool name, input summary and
> Approve / Deny / "Deny with reason…" (a shared `approval-summary.ts`
> renders the input line). `Sessions.tsx` polls `/approvals?status=pending`
> on its own 15s cadence (separate from `NeedsYou`'s identical poll — two
> small requests to the same cheap endpoint was judged simpler and more
> robust than threading a shared cache through both) and passes the
> per-session row down to `SessionCard`. `service-worker.ts`'s
> `notificationclick` handler answers an `approve`/`deny` action with a
> same-origin `fetch` (no bearer token — the phone is authenticated by
> Tailscale identity, or the `lectern_token` cookie in token mode) and shows
> a confirmation or failure notification; the decision logic itself lives in
> a new sibling module, `sw-actions.ts`, purely so it is unit-testable
> (`sw-actions.test.ts`) — a real `ServiceWorkerGlobalScope` isn't available
> to a plain Node test, so nothing inside `service-worker.ts` itself ever
> was. This required upgrading `serviceWorkerPlugin.ts` from a single-file
> `tsc.transpileModule` call to an `esbuild` bundle (`format:'iife'`): the
> old approach could not resolve a same-directory `import` at all, and a
> classic-script service worker cannot use a bare `import` statement either.
>
> **Codex**: interactive codex sessions keep their approvals in codex's own
> UI, as directed — no `PermissionRequest`/hold wiring was added for the
> `codex` agent name, matching the existing per-agent gate in
> `internal/api/agent_registry.go`.
>
> **Not done in this pass**: alerts for a session that only has
> screen-derived state (no hooks, or past the 10-minute hook-fallback
> window); a global "quiet hours" setting (explicitly out of scope per the
> contract).

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

## 5. Cross-agent awareness

The owner routinely runs several agents (Claude Code, Codex) against the
SAME repository at once, in separate worktrees or the same one, and they
duplicate or collide with each other's work — two agents independently
building the same feature is the motivating case. This section makes each
agent aware of what the others are doing, entirely through the SessionStart/
UserPromptSubmit/PreToolUse hook responses section 2 already wires up, plus
a read API and an MCP tool for agents that get no context injection.

**repo identity** (`internal/awareness`): a session's `repo_key` is
`"<target id>:<git common dir>"`, resolved by running `git -C workdir
rev-parse --path-format=absolute --git-common-dir --show-toplevel` — the
common dir is identical for every worktree of one repository, so two
sessions in two different worktrees of the same checkout compare equal, and
the target id keeps two different hosts with an identical absolute path from
colliding. `rel_path` (what is actually stored per edit) is the edited
file's absolute path made relative to THAT session's own `--show-toplevel`
line, so it also compares equal across worktrees for the same logical file.

This resolution is the only thing in the whole feature allowed to shell out,
and it NEVER runs on a hook's response path: `Tracker.EnsureRepoKeyAsync`
kicks off a background goroutine (deduped per session id) the first time an
awareness-relevant hook fires for a session whose `sessions.repo_key` column
is still `''`, and the hook responds immediately with whatever is already
cached — `''` (not resolved yet), the row's real key, or the sentinel
`"none"` (resolved once, confirmed not a git checkout, never retried). A
session belonging to a project backfills that project's `projects.repo_key`/
`repo_toplevel` at no extra git cost, since an attempt's worktree is always
cut from its project's repository — this is how a RUNNING TASK ATTEMPT
becomes visible as a peer with zero git calls of its own (matched via
`ProjectForAttempt(attempt).RepoKey`).

**activity tracking**: `session_file_edits(session_id, repo_key, rel_path,
at)` keeps exactly one row per `(session_id, rel_path)` (`INSERT ... ON
CONFLICT DO UPDATE`) from PostToolUse — and, as a lighter "intent" signal,
from PreToolUse too, for `Edit`/`Write`/`MultiEdit`/`NotebookEdit`
(`tool_input.file_path`, or `notebook_path` for `NotebookEdit`). Rows older
than 24h are pruned opportunistically on every write. `sessions.
last_prompt_excerpt`/`last_prompt_at` hold the latest `UserPromptSubmit`
`prompt`, clipped to ~200 chars — the "what is this session working on"
signal a peer summary shows (latest value only, like `pane_tail`; no history
table, since only "right now" is needed).

**peers** (`Tracker.Peers`): every other LIVE session sharing the calling
session's `repo_key`, plus every running task attempt whose project shares
it, each with id/name/agent/branch/agent_state/last prompt excerpt and its
files edited in the last 60 minutes with ages. Never shells out — every
`repo_key` it compares is whatever is already cached, which is what keeps it
safe to call from inside a hook response.

**briefing** (SessionStart/UserPromptSubmit): when there are peers, the hook
response's body is
`{"hookSpecificOutput":{"hookEventName":"<event>","additionalContext":"<text>"}}`
— verified on real Claude Code 2.1.281 to land in the agent's context
verbatim for both these event names (and for PreToolUse, used below), since
these hooks are `type:"http"` and the HTTP response body IS the hook
response. The text lists each peer ("Session #143 'scratch terminals'
(branch feat/x, working): last asked '…'; edited
`frontend/src/sessions/SessionCard.tsx` 4m ago, …") and ends with "Coordinate
before duplicating their work: check their branch or ask the operator.",
clipped to ~1200 chars. No peers → `{}`, exactly as before this feature.
UserPromptSubmit re-sends the SAME unchanged text only after
`sessions.awareness_briefing_at` is ≥30 minutes old — otherwise it hashes the
rendered text against `sessions.awareness_briefing_hash` and answers `{}`
when nothing has changed, so a chatty session is not re-briefed every turn.

**edit warning** (PreToolUse, `Edit`/`Write`/`MultiEdit`/`NotebookEdit`
only): if any OTHER session touched the exact same `rel_path` within the
last 30 minutes, the response carries advisory `additionalContext` — never a
denial, never a question, purely informational. The wording depends on
whether the two sessions share the same `workdir` ("edited X Ns ago in this
SAME working directory — your changes may collide or be overwritten") or are
in separate worktrees of the same repository ("edited X Ns ago in a separate
worktree of this repository — a merge conflict is likely later, not an
immediate collision"). PreToolUse's existing behaviour (state → `working`,
approval-hold in `ask` mode, etc.) is unchanged; awareness only adds to the
response when nothing else already claimed it.

The per-session Claude settings file's `PreToolUse`/`PostToolUse` hooks
already use `matcher: "*"` (section 2) — a superset of
`Edit|Write|MultiEdit|NotebookEdit`, so no settings-file change was needed;
`internal/awareness.FilePathFromToolInput` is what filters to the tracked
tools before recording or warning.

**for agents with no context injection** (Codex today — section 2's probing
found no confirmed hooks.json wiring, only the `notify`-driven
`AgentTurnComplete`, so a codex session never sees PreToolUse/PostToolUse):
- `GET /api/sessions/{id}/peers` → `{"peers":[...], "self_files":[...]}`,
  normal API auth. `self_files` is the calling session's own recent edits,
  included so a caller (or the frontend) can compute "which of MY files did
  a peer also touch" without a second round trip.
- `GET /api/peers?common_dir=<path>` matches by the git-common-dir component
  of `repo_key` alone (any target) — the fallback for a caller with no
  Lectern session context at all.
- MCP tool `active_work` (`internal/mcp/tools.go`): with no arguments it
  resolves the calling session via `LECTERN_SESSION_ID` (the same env every
  builtin session already carries) and calls the peers endpoint above; with
  `repo_path`, it instead runs `git -C repo_path rev-parse
  --path-format=absolute --git-common-dir` LOCALLY (this MCP process's own
  machine — the one place in `internal/mcp` that shells out at all, since
  everywhere else it is purely an HTTP client of lectern's own API) and
  calls `GET /api/peers?common_dir=...`.
- Not done: no launch-time prime/prompt hint was added for codex sessions.
  Section 2 found no confirmed project-level hooks.json discovery path to
  hang a one-line hint off, and codex's only other launch-time text
  (`-c notify=[...]`) is a shell command, not agent-visible prompt text —
  there is no existing "launch-time prompt mechanism" for codex this could
  append to without inventing one, which is out of scope here. A codex
  session gets awareness exclusively through `active_work` today.

**operator visibility**:
- Session cards and the Conversation header show a small `⚠ overlaps #N`
  chip (`AwarenessOverlapChip.tsx`) when another LIVE session touched at
  least one of THIS session's own recently-edited files (same 30-minute
  window as the edit warning above) — computed server-side, for free, as
  part of the ordinary session row (`sessionView.AwarenessOverlap` in
  `internal/api/sessions.go`), so the chip needs no extra fetch and updates
  live over the existing session SSE/refresh path. Tapping it expands the
  shared file list and the peer's name from data already in hand.
- The Needs-you list gets a "Possible duplicate work" row
  (`GET /api/awareness/duplicate-prompts`) for any two live sessions in one
  repo whose last prompts (within the last hour) have Jaccard word-overlap
  ≥0.5 (`Tracker.DuplicatePrompts`) — a best-effort signal that does not mark
  the whole Needs-you section stale if it fails, unlike approvals/tasks.

**settings**: `awareness_briefing`, `awareness_edit_warning` (`internal/
sinks.Keys`), default ON, off only when explicitly set to `"0"` — the same
convention `alert_*`/`session_permission_mode` already use.

**tests**: `internal/awareness/real_test.go` (real git, two worktrees:
`repo_key` matches across them and differs across target ids; `rel_path`
matches across them too; full record→peer→warning path, including the
same-workdir vs separate-worktree wording); `internal/awareness/
awareness_test.go` (briefing dedup/TTL, edit-warning window, pruning, MCP
fallback matching, Jaccard duplicate-prompt detection — all DB-only, no
git); `internal/api/awareness_test.go` (the hook wire contract: briefing
appears only with peers and is deduplicated, edit warning same-dir vs
worktree wording, settings toggle, the peers endpoint's JSON shape);
`e2e/test_awareness.py` (two real sessions, real hook tokens, a real
PostToolUse→PreToolUse round trip asserting the actual injected warning
text, and the overlap chip rendering live with no reload).

**not done**: no MCP tool test (covered indirectly through the HTTP
endpoints it calls); no frontend unit test for `AwarenessOverlapChip`
(covered by the e2e test, which exercises it against a real server); no
`GET /api/awareness/overlaps` board-wide endpoint — the chip's data rides
free on the existing session list instead, which turned out to need no
separate endpoint.
