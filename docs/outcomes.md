# Cost per outcome — tying every dollar to what it produced

`GET /api/usage` (and now `GET /api/budgets`) answer "how much did we
spend." Neither answers "was it worth it." This feature does: for every
agent and model, per project, over a period, it reports $ per passing task,
$ per accepted change, $ per 100 kept lines, passes per $10, and median time
to a passing check.

Packages: `internal/outcomes` (derivation, aggregation, the optional price
table). Telemetry ingest: `internal/agentevents/otel.go` and
`otel_ingest.go`. Receiver endpoints: `internal/api/hooks_otel.go`. API:
`internal/api/outcomes.go`. UI: `frontend/src/settings/OutcomesPanel.tsx`
(Settings → Usage & about → Outcomes), plus a `$/pass` column on eval
leaderboards (`internal/evals/aggregate.go`'s `VariantStats.CostPerPass`)
and Best-of-N compare cards (`frontend/src/board/CompareView.tsx`).

## What is measured

An **outcome fact** is one row per finished task attempt or per checked
interactive session (`outcome_facts`, see `internal/store/schema.go`), each
carrying: agent, model, project, date, cost, its cost's source, whether its
check passed, whether it was accepted, lines kept (accepted changes only),
whether an eval graded it a pass, and time to a passing check. The table is
a **recomputed cache**, not its own source of truth — `outcomes.Rebuild`
derives it fresh from `attempts`/`tasks`/`session_checks`/`eval_results`/
`otel_attempt_usage` on every `GET /api/outcomes` call (cheap at homelab
scale: hundreds to low thousands of rows), so a stale or missing row
self-heals on the next read instead of needing a migration or a backfill
job.

### Check passed

From `attempts.verify_json`'s existing `{"cmd","rc","output"}` shape
(`internal/checks.RunForTask`) for attempts, or a session's latest
`session_checks` row for sessions. `rc == 0` / `status == "passed"` is a
pass; a non-zero rc or `failed`/`error` is a fail; no check ever having run
is `null` (unknown) — never coerced to false, since "never checked" and
"checked and failed" are different facts.

### Accepted

An attempt counts as accepted when its task's status is `done` **and** it
is that task's highest-`N` attempt at read time — the same signal
`pickAttemptTask` (Best-of-N "pick this one") already produces by swapping
`N` so the winner becomes `LatestAttempt`, and the same attempt every
existing single-attempt endpoint (`commit`, `integrate`, `taskReport`)
already treats as canonical. This is a pure read of existing state: nothing
had to change in `bestofn.go` or `task_build.go` for "accepted" to track
picks and integrations correctly. Sessions have no accept/merge concept and
are not marked accepted.

### Lines kept

Only computed for accepted attempts: the sum of `additions`+`deletions`
across `attempts.diff_stat_json`'s file list. A rejected or still-running
attempt's diff is not "kept" by any outcome-relevant definition, so its
`lines_kept` is `null`, not `0`.

### Eval pass

Joined from `eval_results.status` where `eval_results.attempt_id` matches —
evals already grade attempts through the same Best-of-N machinery, so this
is a lookup, not a new signal.

### Time to passing check

`finished_at - started_at` for an attempt whose check passed; for a
session, its latest-passing-check's `finished_at` minus the session's
`created_at`. `null` when the check never passed.

## Telemetry: exact cost via OpenTelemetry

Claude Code can export its own usage as OpenTelemetry metrics and logs (see
[Claude Code's monitoring docs](https://code.claude.com/docs/en/monitoring-usage)).
Lectern points that exporter at itself:

```
CLAUDE_CODE_ENABLE_TELEMETRY=1
OTEL_METRICS_EXPORTER=otlp
OTEL_LOGS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http/json
OTEL_EXPORTER_OTLP_ENDPOINT=<lectern base>/api/hook/otel/session/<id>   # or .../attempt/<id>
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer <the session's/attempt's own hook token>
OTEL_RESOURCE_ATTRIBUTES=lectern.session_id=<id>                        # or lectern.attempt_id=<id>
```

built by `agentevents.OTelEnv` and injected for every **Claude** interactive
session launch (`internal/sessions.Manager.launch`) and every **Claude**
headless task attempt (`internal/scheduler.stageRuntime`) — gated on the
`otel_telemetry` setting (default on; set to `"0"` to opt out, the same
"explicit `0` opts out" convention `internal/alerts`/`internal/awareness`
already use). It reuses the session's/attempt's own existing hook/approval
bearer token rather than minting a second secret. Only `http/json` is
offered: Lectern's receiver is a small JSON decoder
(`internal/agentevents/otel.go`), not a full OTLP/gRPC or protobuf server —
`http/json` is one of Claude Code's three documented exporter protocols, and
the OTel SDK appends `/v1/metrics`/`/v1/logs` to `OTEL_EXPORTER_OTLP_ENDPOINT`
itself, which is why the injected endpoint has no `/v1/...` suffix of its
own.

The receiver parses exactly five metric names and one log event, matching
Claude Code's own documented names verbatim:

| Metric | What Lectern reads |
|---|---|
| `claude_code.cost.usage` | cumulative session/attempt cost, USD |
| `claude_code.token.usage` | cumulative tokens by `type` (input/output/cacheRead/cacheCreation — the latter two fold into "input", matching how Claude's own statusline and result payloads are already treated) |
| `claude_code.lines_of_code.count` | cumulative lines added/removed |
| `claude_code.pull_request.count` | cumulative PRs opened (parsed; not yet surfaced in the UI — see Caveats) |
| `claude_code.commit.count` | cumulative commits (same) |

and `claude_code.api_request` log records, opportunistically, for any
`cost_usd`/`model`/`input_tokens`/`output_tokens` attributes a given Claude
Code build attaches to them (not documented as standard attributes today,
so this is a bonus source when present, never a requirement).

**Sessions** (`agentevents.Ingester.IngestOTelMetrics`/`IngestOTelLogs`):
these metrics are cumulative for the session's whole process lifetime, so
the delta math is identical to the existing statusline path
(`IngestStatusline`) — diff against the session's own previously-stored
`cost_usd`/booked `usage_daily` totals, then book the difference. The first
successful OTel ingest sets `sessions.otel_active_at`, which is also the
precedence switch (below).

**Attempts** (`otel_attempt_usage`, `internal/store/otel_usage.go`): a
headless attempt's process is one-shot and never resumed, so there is no
baseline to diff — each export simply overwrites the row with the freshest
cumulative reading. `internal/outcomes.Rebuild` reads this ahead of
`attempts.result_json`'s `cost_usd` whenever a row exists.

## Precedence — nothing is double-counted

1. **OTel**, when the session has ever reported in (`otel_active_at` set) or
   the attempt has an `otel_attempt_usage` row. Exact, per Claude Code's own
   accounting.
2. **The agent's own reported cost** — a session's statusline
   (`IngestStatusline`) or an attempt's `result_json.cost_usd`
   (`resultUsageTokens`/`resultUsage`). This is what every installation had
   before this feature and remains the default until OTel is configured.
3. **Estimated** — only when an agent reports tokens but no dollar figure at
   all (Codex-style), and only when the operator has configured that
   model's `$/1M` input+output rate via `GET`/`PUT /api/model-prices`
   (`internal/outcomes/prices.go`, settings key `model_prices`). A fresh
   install estimates nothing: an unconfigured model's cost is reported as
   unknown (`cost_source: ""`), never silently invented.

Once a session's OTel exporter has reported in even once,
`IngestStatusline` **stops** booking its own `usage_daily` deltas and
stops overwriting `cost_usd`/`lines_added`/`lines_removed`/`model` for that
session — it keeps updating everything OTel does not cover (context-window
percentage, account-wide 5h/7d rate limits) unconditionally. This is the
whole double-counting guard: exactly one source ever writes a given dollar
into `usage_daily`, decided per session, and it only ever hands off from
statusline to OTel, never back.

For attempts there is no shared ledger to guard: `result_json`'s cost is
read only when no `otel_attempt_usage` row exists at all, so the two can
never both contribute to the same fact's `cost_usd`.

## Metrics reported (`GET /api/outcomes?days=30&group=agent|model|project`)

- **`cost_usd`** — total spend in the window.
- **`cost_per_pass`** — `cost_usd / passed`. What a passing outcome actually
  costs, including the failed attempts that spend also paid for.
- **`cost_per_accepted`** — `cost_usd / accepted`.
- **`cost_per_100_lines`** — `cost_usd * 100 / lines_kept`.
- **`passes_per_10usd`** — `passed / cost_usd * 10`.
- **`median_time_to_pass_s`** — median, not mean, so one slow outlier does
  not dominate the headline number.

Every derived field is `null` (rendered "—" in the UI) when its denominator
is zero — never a divide-by-zero artifact.

## Partial data

A row is flagged:

- **`partial`** when any contributing fact has no cost figure at all
  (`cost_source == ""` — no OTel, no reported cost, no price table entry).
  Its `cost_usd` undercounts.
- **`estimated`** when any contributing fact's cost came from the price
  table rather than a measured source.

The UI shows both as inline badges next to the group's label, and the panel
footer restates the precedence order, so a number is never presented as more
certain than it is.

## Caveats

- `claude_code.pull_request.count`/`claude_code.commit.count` are parsed
  (and covered by the OTLP fixture tests) but not yet wired into
  `outcome_facts` or the UI — a real "did this attempt ship a PR/commit"
  signal is a natural follow-up but was left out of this pass to keep
  "accepted" to one unambiguous definition (task done + latest attempt).
- `claude_code.api_request`'s `cost_usd`/token attributes are not part of
  Claude Code's documented event schema as of this writing; the parser
  reads them opportunistically and degrades to nothing when absent, so this
  never depends on an undocumented field actually showing up.
- Only Claude Code ships an OTLP exporter today. Codex and other
  token-only agents fall through to the price-table estimate, or report as
  partial data with no configured price.
- `outcome_facts` is a cache rebuilt on every read, not a historical
  ledger: renaming an agent or moving a task between projects changes what
  older facts report the next time they are recomputed, the same way
  `GET /api/usage`'s own on-the-fly aggregation already behaves.

## Tests

- `internal/agentevents/otel_test.go` — OTLP/HTTP JSON metrics and logs
  parsing against fixtures built from the documented metric/event names
  (including OTLP's int64-as-JSON-string encoding).
- `internal/agentevents/otel_ingest_test.go` — session delta math, the
  precedence hand-off in `IngestStatusline`, attempt overwrite semantics.
- `internal/api/hooks_otel_test.go` — auth (session hook token, attempt
  token, wrong/missing token on both).
- `internal/outcomes/outcomes_test.go` — fact derivation: cost precedence,
  accepted-only-for-the-winning-latest-attempt, check pass/fail/unknown,
  eval pass, session latest-check-wins.
- `internal/outcomes/aggregate_test.go` — the five derived metrics'
  arithmetic, grouping by agent/model/project, ranking, partial/estimated
  flags, window cutoff.
- `internal/outcomes/prices_test.go` — price table validation and
  save/load round-trip.
- `internal/evals/aggregate_test.go` — leaderboard `cost_per_pass`.
- `e2e/test_outcomes.py` — two stub attempts with different costs and check
  outcomes dispatched through a real server; the Outcomes table ranks them
  correctly and the API groups by agent/model/project.
