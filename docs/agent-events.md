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
- Structured drivers: tasks and delegated builds already run headless
  (`claude -p --output-format stream-json`, `codex exec --json`, parsed in
  `internal/agents/parse.go`). What is added: one Go interface in
  `internal/drivers` that the scheduler and eval runner use; a Claude driver
  with `--input-format stream-json` so a running task can be steered (send a
  message mid-run) instead of cancel-and-redispatch; a Codex driver on
  `codex app-server` (JSON-RPC) whose approval requests go through the broker
  like Claude's PreToolUse hook, with `codex exec --json` kept as fallback.
