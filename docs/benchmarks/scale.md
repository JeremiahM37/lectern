# Scale: many interactive sessions on one control plane

Measured 2026-09-26 with `tools/stress/run.sh`. Every number below is copied
from the run's `results.json`, kept in [`scale-2026-09-26/`](scale-2026-09-26/).

## Hardware and limits

- AIServer: AMD Ryzen AI MAX+ 395 (16 cores / 32 threads), 123 GB RAM, Linux 6.17.
- The whole benchmark — Lectern, the SSH fixtures, the stand-in agents, tmux,
  ttyd and the driver — ran in one bubblewrap namespace capped at **8 cores**
  (`CPUQuota=800%`) and 16 GB, on a box shared with other work. Lectern built
  with Go 1.26.6; tmux 3.5a.

## Method

Each tier starts a fresh, private Lectern (`lectern serve`, own port, own
`LECTERN_DB`, own `HOME`, own tmux servers) inside the reviewed isolation
namespace (`tools/run-isolated-tests.sh`, mode `stress`). It cannot reach the
live service on 9110 or the host's tmux server.

- **Targets**: one `local` target plus three `ssh` targets. Each SSH target is
  a separate real SSH server on loopback (`e2e/ssh_fixture_server.py`) with its
  own `HOME` and tmux server, reached through Lectern's normal SSH executor.
  These are four "machines" in the sense Lectern sees them, but all run on one
  host.
- **Agents**: N interactive sessions (`agent: claude`, `permission_mode: ask`),
  round-robin over the targets. The Claude binary is a shell stand-in
  (`tools/stress/fake-claude.sh`) that uses the hooks Lectern installs through
  `--settings`. Every 20 s it asks for approval through the `PermissionRequest`
  hook and blocks until Lectern answers. It also reports a Bash and an Edit
  tool call through `PreToolUse`/`PostToolUse` and appends to one of five
  shared files. Each target has one git repository, and each agent works in
  its own worktree of it. That means agents in the same repository overlap,
  which feeds cross-agent awareness.
- **Operator**: the driver approves each approval as soon as it arrives on
  `/api/stream`. During the 60 s steady-state window:
  - one client polls `GET /api/sessions` four times a second;
  - 25 other clients hold `/api/stream` open (100 at 250 sessions);
  - every 2 s a probe renames a session, and the driver times its arrival at
    every subscriber.
- **Terminal attach**: for 10 sessions spread over the targets, the driver
  opens a web terminal the way the browser does:
  1. `POST /api/sessions/{id}/terminal`
  2. the terminal token
  3. the ttyd websocket through Lectern's proxy

  The time is measured until the first byte of terminal output arrives.
- **TUI refresh**: `internal/console`'s own dashboard model is used against the
  live instance. One refresh is the fetch, `Update` and `View`, repeated 20 times.
- **Resources**: CPU and RSS of the Lectern process come from `/proc` once a
  second. "Incl. reaped children" adds the short-lived `bash`/`tmux`/`ssh`
  commands Lectern runs and waits for.
- **Approval timing**: the agent stamps nanosecond wall-clock times around its
  hook call. The driver stamps when the approval event arrived and when it sent
  the decision. Only requests that began inside the window count.

## Results

`p50 / p95`. The first table is the current code (`stress-test-2`), re-run
2026-09-27 00:34–00:41 UTC (files `rerun-*.json`). An earlier re-run that
evening, on a busier machine, measured 31 ms (list) and 61 ms (TUI) at 250
sessions. The code on `connect-ui` without these changes, run straight after
it, measured 37 ms and 52 ms (`rerun-250-connect-ui-baseline.json`). Load on
the shared box moves these numbers by that much.

### Current

| Sessions | 10 | 25 | 50 | 100 | 250 ¹ |
|---|---:|---:|---:|---:|---:|
| Launched / failed | 10 / 0 | 25 / 0 | 50 / 0 | 100 / 0 | 250 / 0 |
| Live at window start / end | 10 / 10 | 25 / 25 | 50 / 50 | 100 / 100 | 250 / 250 |
| `GET /api/sessions` | 2.0 / 4.8 ms | 3.9 / 5.3 ms | 6.2 / 7.6 ms | 11 / 14 ms | 27 / 41 ms |
| List response size | 10 KB | 26 KB | 52 KB | 105 KB | 267 KB |
| SSE fan-out (update → every subscriber) | 1.0 / 2.4 ms | 1.2 / 1.5 ms | 0.7 / 2.3 ms | 0.9 / 1.8 ms | 0.9 / 1.9 ms |
| SSE events missed / delivered | 0 / 725 | 0 / 725 | 0 / 725 | 0 / 725 | 0 / 2900 |
| Approvals in window (all approved) | 30 | 75 | 150 | 300 | 750 |
| Approval request → visible to operator | 5.1 / 11 ms | 4.7 / 5.9 ms | 4.3 / 6.0 ms | 4.3 / 5.8 ms | 4.6 / 8.0 ms |
| Decision → agent unblocked | 1.9 / 3.6 ms | 1.7 / 2.3 ms | 1.7 / 2.3 ms | 1.6 / 2.3 ms | 1.8 / 4.4 ms |
| Approval round trip (excluding human think time) | 7.2 / 15 ms | 6.4 / 8.2 ms | 6.1 / 7.8 ms | 6.1 / 7.7 ms | 6.6 / 13 ms |
| Terminal attach → first output | 28 / 71 ms | 16 / 17 ms | 16 / 19 ms | 16 / 55 ms | 16 / 17 ms |
| TUI refresh (fetch + update + render) | 2.6 / 7.5 ms | 4.4 / 5.9 ms | 7.3 / 8.9 ms | 13 / 17 ms | 34 / 66 ms |
| Lectern CPU avg / peak, % of one core | 3 / 18 | 5 / 11 | 9 / 18 | 16 / 30 | 57 / 98 |
| … avg incl. reaped children | 5 | 9 | 16 | 27 | 79 |
| Lectern RSS avg / peak | 43 / 45 MB | 44 / 46 MB | 46 / 48 MB | 48 / 50 MB | 65 / 68 MB |
| HTTP errors · log WARN / ERROR | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 |

¹ Run once, with 100 SSE subscribers instead of 25.

The first run (22:50–22:57 UTC, commit `ebbd477`, files `after.json` and
`big.json`) had a quieter machine. At 100 sessions it measured list 9.5 /
13 ms and TUI 12 / 20 ms. At 250 it measured 23 / 36 ms and 26 / 41 ms.

Launching was recorded but is not steady state. `POST /api/sessions` took
354–433 ms p50 with 8 launches in parallel, and 2.1 s p95 at 250. All 100
sessions were up in 5.3 s, and all 250 in 20.9 s.

### Multi-machine: one machine stops answering, and many terminals stay open

Measured at 50 sessions over the same four targets.
- **Frozen machine:** 15 s into the window, the driver SIGSTOPs the SSH server
  of `ssh-1`, and resumes it at the end of the window. While it is stopped the
  kernel still accepts connections but nothing answers, which is what a hung or
  partitioned machine looks like.
- **Status age:** the stand-in agents redraw a clock every second, so a
  session's `last_activity_at` moves on every successful status poll. "Status
  age" is how old that timestamp is in the list the dashboard polls, measured
  over the 37 sessions on the three other targets.
- **Held terminals:** 30 web terminals are opened on those sessions, and all 30
  websockets are held open at once, like 30 browser tabs. After 3 s the driver
  counts how many are still connected.
- **Before:** `connect-ui` at `a8776ad`. **After:** this branch. Files:
  `multi-machine-before.json`, `multi-machine-after.json`.

| | Before | After |
|---|---:|---:|
| Healthy machines' status age, before the freeze | 2.6 / 6.0 s | 2.5 / 5.9 s |
| Healthy machines' status age, while `ssh-1` is frozen | 8.1 / 16.7 s | 2.1 / 3.9 s |
| Frozen machine's sessions shown as unreachable | never | after 8.4 s |
| Healthy sessions wrongly shown as unreachable | – | 0 |
| Unreachable marker cleared after `ssh-1` answered again | – | 0.3 s |
| 30 terminals held open: opened / still connected | 19 / 10 | 30 / 30 |

Before the change, the freeze made every other machine's status go stale for
seconds at a time: p95 16.7 s, max 19 s. That was one frozen target in the
serial poll. Also before the change, 11 of the 30 terminals failed to open and
9 of the 19 that did open were closed under their viewers. From the 22nd
attach onward, each new terminal retired the oldest one, in use or not, and
took over its port.

With the limit set to the old 21 (`-terminals-max 21`,
`held-terminals-limit-21.json`):
- 21 terminals stayed connected.
- The other 9 were refused with "every web terminal is open somewhere; close
  one and try again".
- 3 idle terminals left over from the attach samples were retired to make
  room, each with a notice.
- Nothing anyone was viewing was closed.

At the default limit, 100 terminals held open at once on 100 sessions
(`held-terminals-100.json`) all stayed connected. During that run Lectern
used 17% of a core and 54 MB on average, peaking at 72 MB.

## What the benchmark found and fixed

1. **The session list was quadratic when agents share a repository**
   (`0a56317`). Every row looked up its "overlaps #N" awareness chip on its
   own. That meant a full scan of live sessions plus one query per peer, so N
   sessions in one repository cost O(N²) queries per list. The web UI, TUI and
   phone all poll this list. The chips are now computed for the whole list from
   two queries. A randomized test checks the new code against the old per-row
   computation. In-process benchmark (`BenchmarkListSessionsSharedRepo`), 200
   sessions: 824 ms → 12 ms. In this benchmark at 100 sessions: list p50
   161 → 9.5 ms, TUI refresh 227 → 12 ms, Lectern CPU 71% → 16% of a core.
2. **Opening a web terminal slept a fixed 300 ms** (`ebbd477`). That was about
   300 of the 308 ms. Attach now returns as soon as ttyd accepts connections,
   and 300 ms is only the upper bound. It now takes about 17 ms to first
   output. The old "ttyd exited immediately" check could never fire. It now
   works.

3. **One unreachable machine stalled everyone's status** (`d5040d9`). The
   status poll visited targets one after another, and the scheduler waited
   for it. Now:
   - Each machine is polled by its own worker, at most 8 at once, each with its
     own timeout (`LECTERN_TARGET_POLL_TIMEOUT`, default 20 s).
   - A round waits at most 500 ms. A machine still being polled carries on in
     the background and is skipped until it finishes.
   - A failing machine is retried with backoff, from 2 s up to 60 s.
   - Its sessions keep their last status and show "<machine> unreachable" on
     the web card and in the TUI. That appears as soon as a poll fails, or
     after the poll has run for 8 s.
4. **The 22nd web terminal closed the oldest, even while it was being viewed**
   (`0d53bb6`, `0e74a74`).
   - ttyd now listens on a Unix socket in a directory private to the
     Lectern instance (mode 0700), not on a loopback port from 7710–7730.
     That removes the port range and its 21-terminal cap. It also stops
     other local users from reaching these unauthenticated shells, and
     stops two Lectern instances on one host from picking the same port
     and serving each other's terminals.
   - The browser suite's parallel workers are such instances. A different
     terminal test failed in each of three `verify` runs before this change;
     each passed when run alone. Two `verify` runs after it passed.
   - What remains is a limit on the number of ttyd processes,
     `LECTERN_TERMINALS_MAX` (default 200).
   - The proxy counts open websockets. At the limit only a terminal nobody
     has open is retired, and the attach response says which one. With every
     terminal viewed, a new attach is refused with a reason.

## Reproduce

```bash
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress                 # 10,25,50,100
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress-big -tiers 250 -sse-clients 100
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress-mm -tiers 50 -hang-at 15s -hold-terminals 30
```

`-hang-at` freezes the first SSH target that far into the window and reports
the status age of the others. `-hold-terminals K` opens K terminals and keeps
them all open. `-terminals-max N` sets the instance's terminal limit.

Useful flags are `-window`, `-approval-every`, `-ssh-targets`, `-sse-clients`,
`-attach-samples` and `-plain-dirs`. `-plain-dirs` puts agents in plain
directories, so no awareness data exists. `ADK_TEST_CPUS` changes the core
cap (default 8). Each run writes `results.json`, `results.md` (the table
above) and each tier's Lectern log. A tier takes about 75–90 s, or longer for
the 250-session tier.

## Caveats

- **One physical host.** "Four targets" means four real SSH servers and tmux
  servers on loopback, not four machines. Network latency to real remote
  targets is not in these numbers. The unreachable-machine test freezes one
  SSH server; it does not simulate packet loss or a slow link.
- **Status polling limits.** Up to 8 machines are polled at once. With more
  than 8 hung machines the rest wait for a free slot, bounded by the 20 s
  timeout. A hung machine is retried on its backoff, and each retry can hold
  that scheduler tick for up to 500 ms.
- **Task runs are still polled one after another.** Task attempts (`claude -p`
  runs) are still tailed serially in the scheduler tick. A hung machine with
  running *tasks* on it still delays the tick; this change covers interactive
  sessions. This benchmark does not exercise tasks.
- **Stand-in agents, not Claude.** The agents exercise Lectern's real paths:
  hooks, tmux panes, status polling, terminals and awareness. They do not run
  an LLM, write large panes or produce long transcripts. Session list size
  grows with pane previews and history that these agents keep small.
- **Approval latency excludes the human.** The driver approves instantly. In
  real use the round trip is the operator's reaction time plus about 7 ms.
- **Web terminals: 200 at once by default.** Each open terminal is one ttyd
  process. Past 200 viewed terminals, new ones are refused until one is closed;
  raise `LECTERN_TERMINALS_MAX` if that is too few. The benchmark held 100.
- **Interactive sessions only.** Task dispatch (worktree, `claude -p`, diff
  capture) and push or Discord notification sinks are not part of this load.
- The shared box was not otherwise idle. Every tier ran once, so treat
  differences of a few milliseconds as noise.
- The first run's saved JSON has one field removed, a per-process CPU breakdown for
  tmux/ttyd/agents. It counted only long-lived processes, so it was misleading.
  The driver no longer collects it.
