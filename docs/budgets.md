# Budgets — spend limits, quota alerts and cost anomalies

Lectern already showed usage (`GET /api/usage`, the Usage tab, the account
quota chip). This closes the competitive gap next to it: **caps**, not just a
dashboard — daily/weekly USD limits (overall and optionally per agent), a
per-task spend cap, Claude 5h/7d quota-threshold alerts, and a cost-anomaly
check, all delivered through the existing push/Discord/ntfy `sinks.Notifier`
(see `docs/agent-events.md` section 3) rather than a second notification
path.

Budgets answer "how much did we spend" and "stop us before we spend too
much." They deliberately do not answer "was the spend worth it" — that is
[docs/outcomes.md](outcomes.md), which ties the same `usage_daily`/
`attempts` history (plus exact OTel telemetry where available) to what each
dollar actually produced.

A provider's own usage limit (Claude's 5-hour and weekly windows, Codex and
Gemini quotas) is a different stop: the Claude quota alerts below warn as it
fills, and [docs/rate-limits.md](rate-limits.md) covers what happens once it
stops an agent — wait for the reset and resume, or hand off.

Package: `internal/budget`. Store helpers: `internal/store/budget.go`.
Enforcement lives where dispatch/launch actually happen
(`internal/api/tasks.go`, `internal/api/sessions.go`,
`internal/scheduler/scheduler.go`), reusing `internal/autonomy`'s pattern of
"pure policy package + callers that read the DB fresh on every decision"
rather than a cached "blocked" flag that could go stale.

## Configuration — `GET`/`PUT /api/budgets`

One JSON blob, stored at settings key `budget_config` (same convention as
`templates`— append-only alongside `sinks.Keys`, not a second settings
registry):

```json
{
  "overall": { "daily_usd": 20, "weekly_usd": 100, "mode": "stop" },
  "per_agent": { "codex": { "daily_usd": 5, "mode": "warn" } },
  "thresholds": [75, 90, 100],
  "quota_thresholds": [75, 90],
  "usage_warn_percent": 80,
  "anomaly_enabled": true,
  "anomaly_multiplier": 3
}
```

A fresh install starts with nothing capped (`daily_usd`/`weekly_usd` both 0)
and `mode: "warn"` — this feature must never silently start blocking an
existing installation. `GET /api/budgets` (and the `budgets` field embedded
in `GET /api/usage`) returns the same config plus **live status**: current
spend, percent of cap, and whether each limit is currently `blocked` (`stop`
mode at or over 100%).

**Daily** resets at UTC midnight; **weekly** resets Monday 00:00 UTC (an ISO
week) — a deterministic boundary, unlike `GET /api/usage`'s own trailing
7-day window, which never has an edge an alert could dedupe against.

## Per-task budget — `tasks.budget_usd`

Settable at create (`POST /api/tasks {"budget_usd": 5}`), at dispatch
(`POST /api/tasks/{id}/dispatch {"budget_usd": 5}`, overriding whatever the
task already had), or by patch (`PATCH /api/tasks/{id} {"budget_usd": 0}`
clears it). Unlike the overall/per-agent limits, a per-task cap has no
separate `warn`/`stop` mode: setting a specific dollar figure on one task
**is** the decision to stop it there.

Enforcement is live, not just at finish: `attempts.live_cost_usd` is updated
by the scheduler every time it parses a streamed `result` event
(`internal/scheduler/scheduler.go`'s `poll`/`StoreEvents`) — Claude's own
`total_cost_usd` on that event is already a running total for the attempt,
so this *sets* rather than accumulates. Once `live_cost_usd` reaches
`budget_usd`, the scheduler cancels the attempt through the same path
`POST /api/tasks/{id}/cancel` uses (`Scheduler.CancelAttempt`) and pushes a
notification — no new kill mechanism.

## Enforcement — `warn` vs `stop`

- **`warn`** (default): alerts fire, nothing is blocked.
- **`stop`**, at or over 100% of either the daily or weekly cap:
  - `POST /api/tasks/{id}/dispatch` is refused (`409`, with the exhausted
    limit's own message as the body) — `internal/budget.Gate`, called before
    a new attempt is created.
  - `POST /api/sessions` (a new interactive session) is refused the same way.
  - **A running task attempt over its own `budget_usd`** is cancelled (see
    above) — this check is unconditional once a per-task cap is set, since
    the cap itself already encodes "stop mode".
  - **A running interactive session is never killed.** Instead, its next
    `UserPromptSubmit` hook response carries an `additionalContext` note —
    the same `hookSpecificOutput` channel cross-agent awareness's briefing
    already uses (`internal/api/hooks_agentevents.go`) — telling the agent a
    budget is exhausted and asking it to stop and summarise rather than
    start new work. Checked fresh on every prompt, not cached.

`budget.Gate(db, agent)` is the one function both dispatch and session-launch
call; it is cheap (at most two `SpendSince` queries) and reads the database
directly rather than trusting a periodic checker's last result, so a limit
that was just raised or just exhausted takes effect on the very next request.

## Alerts

`internal/budget.Checker.Tick` runs on the scheduler's own tick
(`Scheduler.Budgets`, wired in `internal/app/app.go` next to `Routines` and
`Evals` — no separate goroutine or ticker) and evaluates three things:

1. **Spend thresholds** — for every configured limit (overall, each
   per-agent), at each configured percentage (default 75/90/100), once spend
   reaches it. A spend that jumps straight from 0% to 95% in one tick still
   fires *both* 75% and 90% — thresholds are independent crossings, not "only
   the highest".
2. **Claude account quota** — reuses the `rate_limits` settings row
   `internal/agentevents.IngestStatusline` already writes on every
   statusline tick (the same data `GET /api/usage`'s quota chip reads), at
   configurable thresholds (default 75/90 — no 100%, since a quota window
   always eventually reaches it and resets on its own; alerting there would
   just be noise). The window's own `resets_at` is the period key, so a
   fresh window can alert again.
3. **Cost anomalies** — a session or task spending more than `N×` (default
   3×) the account's **trailing 7-day median $/hour** across finished
   sessions and task attempts. This baseline is account-wide, not
   per-entity: a brand-new session or a one-shot task has no history of its
   own to compare against. Runs shorter than 5 minutes are excluded from
   both the baseline and the check — a five-second, ten-cent attempt is not
   meaningfully "$72/hour". Needs at least 3 finished samples in the
   trailing 7 days before it will flag anything. One alert per entity per
   day (not re-pinged every tick while it stays hot).

### Exactly once per period, survives a restart

`budget_alerts_sent(scope_key, period_key, threshold)` — a `UNIQUE` index is
the actual dedup mechanism: `Checker` does a plain `INSERT OR IGNORE` and
only sends a notification when its own insert is the one that lands. A
restart starts a brand-new `Checker` with no in-memory state, but the row
already exists in the database, so nothing resends. Rows older than 90 days
are pruned opportunistically (nothing ever needs to know a long-past
period's alerts were sent).

## Provider usage

`GET /api/usage/providers?days=N` and Settings → Usage & about → **By
provider** show one card per agent CLI: Claude Code, Codex and Gemini CLI
always, and any catalog agent that has usage in the window.

- **Spend and tokens by day** from the same `usage_daily` and task-attempt
  history as `GET /api/usage`. A CLI that reports tokens but no dollars charts
  its tokens instead.
- **Usage windows**: the 5-hour and 7-day windows each login last reported
  (Claude's statusline, Codex's rollout), per machine and account, with the
  reset countdown. A window whose reset has passed is over and not shown.
  Gemini CLI and most catalog agents report no windows, and the card says so.
- **By account**: where a CLI has more than one login
  ([accounts.md](accounts.md)), spend and windows per account.
- **Estimated cost by model**: tokens, the dollars the CLI reported, the
  dollars Lectern booked as estimated, and the same tokens at the model price
  table's list price, so token-only agents can be compared.

**The warning.** Any window at or over `usage_warn_percent` (default 80,
editable on the card) turns its card amber and sends one push per window,
through the same sinks as every budget alert, deduplicated in
`budget_alerts_sent` so a restart does not resend it. Claude's default login
keeps its own `quota_thresholds` ladder (75/90) while the statusline is
reporting, so it is not warned twice; other Claude accounts, Codex, Gemini and
catalog agents use the 80% line.

## Codex spend is estimated

Codex reports tokens but no dollar figure. When the operator has priced it in
the model price table (`GET`/`PUT /api/model-prices`, the same table
[outcomes](outcomes.md) uses), Lectern estimates the spend and treats it as
spend everywhere a budget looks:

- **Tasks.** Before a Codex `result` event is stored, the scheduler prices its
  token usage (`internal/outcomes.EstimateResult`) and writes `cost_usd` plus
  `cost_source: "estimated"` into the event and `attempts.result_json`. That
  also sets `live_cost_usd`, so a per-task `budget_usd` cancels the attempt the
  same way it does for Claude, and finished attempts count toward per-agent
  and overall limits.
- **Interactive sessions.** Each Codex token delta read from the rollout
  (`agentevents.IngestCodexUsage`) is priced and booked into `usage_daily`,
  with the estimated part also recorded in `usage_daily.estimated_usd`.

The price is looked up by model name first, then by the agent name (`codex`),
because a Codex run on its default model reports no model. Codex counts cached
input inside its input tokens and reports how much of it was cached; an entry's
optional `cached_input_per_1m` bills that part at the cached rate, for example
`{"prices":{"codex":{"input_per_1m":1.25,"cached_input_per_1m":0.125,"output_per_1m":10}}}`.
Without it, cached input is billed at the full input rate and the estimate runs
high. Interactive sessions price the rollout's cumulative totals and book only
the difference from what is already booked, so the cached split holds across
readings.

Lectern ships no built-in prices for any model, Claude's included (Claude
reports its own cost), so Codex spend appears only once a price is set.

Prices are edited in **Settings → Budgets → Model prices** (input, optional
cached input, output, USD per 1M tokens; negative rates are refused). It lists
every agent/model that reported tokens without a cost in the last 30 days and
highlights the unpriced ones, e.g. "Codex spend isn't shown until you set a
price for gpt-5-codex". `GET /api/model-prices` returns the same list as `seen`.

Estimates are always labelled: the board and task detail show `~$0.12 est.`,
the Usage page shows the estimated part next to each agent/model total, and
outcomes flag the row as estimated. With no price configured nothing is
estimated, and a Codex budget still sees no spend.

## UI

- **Settings → Budgets**: edit the overall and per-agent limits, mode and
  thresholds, and the model price table used for Codex estimates.
- **Usage page**: a bar per configured limit (daily/weekly, spend vs cap,
  colored by percent), reusing `GET /api/usage`'s embedded `budgets` field —
  no second request.
- **Quota-chip area**: the existing account quota chip gains a compact
  budget indicator when at least one limit is configured.
- **Task create/dispatch**: an optional "Budget (USD)" field.
- **Blocked dispatch**: a `409` from dispatch/launch surfaces through the
  existing toast/notice path (same as every other rejected request), plus an
  inline note near the dispatch buttons when a stop-mode limit is already
  exhausted, so the refusal is not a surprise.

## Tests

- `internal/budget/config_test.go` — `Config.Validate`, `Load`/`Save`
  round-trip, `Gate` (warn never blocks, stop blocks only at/over 100%,
  per-agent independent of overall), period-key stability.
- `internal/budget/checker_test.go` — each threshold fires exactly once per
  period; a fresh `Checker` (simulated restart) does not resend; a new
  period can alert again; quota thresholds; anomaly detection (and that
  disabling it does nothing).
- `internal/api/codex_cost_test.go` — a Codex `turn.completed` result is
  left uncosted without a price, then estimated and labelled once `codex` is
  priced; `live_cost_usd`, a codex stop budget, the Usage page's
  `estimated_usd` and the outcome fact all see it; an interactive Codex
  session's rollout usage is priced into `usage_daily`; cached input is billed
  at `cached_input_per_1m` for both.
- `internal/api/budgets_test.go` — `GET`/`PUT /api/budgets`; stop mode blocks
  dispatch and session launch while warn mode does not; a per-agent limit
  blocks only that agent; a per-task budget cancels a running attempt
  through the normal cancel path; the interactive-session budget note (never
  killed); `budget_usd` settable at create/dispatch/patch.
- `e2e/test_budgets.py` — the model price editor flags unpriced Codex
  usage, refuses a negative rate and saves input/cached/output; and a real server, a tiny overall daily limit, real
  spend posted through the same session statusline hook fixture
  `e2e/test_usage_view.py` uses, and the Settings Budgets UI showing the
  resulting blocked state live.
