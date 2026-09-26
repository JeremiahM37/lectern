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
2026-09-26 23:27–23:34 UTC (files `rerun-*.json`). The box was busier than
during the first run: at 250 sessions the list and TUI came out slower than
the first run's 23 ms / 26 ms. The code on `connect-ui`, without these
changes, run straight afterwards gave 37 ms / 52 ms
(`rerun-250-connect-ui-baseline.json`), so the difference is the machine's
load, not a regression.

### Current

| Sessions | 10 | 25 | 50 | 100 | 250 ¹ |
|---|---:|---:|---:|---:|---:|
| Launched / failed | 10 / 0 | 25 / 0 | 50 / 0 | 100 / 0 | 250 / 0 |
| Live at window start / end | 10 / 10 | 25 / 25 | 50 / 50 | 100 / 100 | 250 / 250 |
| `GET /api/sessions` | 1.9 / 2.8 ms | 3.7 / 4.8 ms | 6.6 / 9.7 ms | 12 / 17 ms | 31 / 65 ms |
| List response size | 10 KB | 26 KB | 53 KB | 105 KB | 267 KB |
| SSE fan-out (update → every subscriber) | 0.9 / 1.3 ms | 0.9 / 1.8 ms | 1.0 / 3.3 ms | 1.0 / 2.6 ms | 1.0 / 4.8 ms |
| SSE events missed / delivered | 0 / 725 | 0 / 725 | 0 / 725 | 0 / 725 | 0 / 2900 |
| Approvals in window (all approved) | 30 | 75 | 150 | 300 | 750 |
| Approval request → visible to operator | 4.6 / 5.9 ms | 4.9 / 5.6 ms | 5.5 / 8.0 ms | 5.4 / 7.1 ms | 6.0 / 12 ms |
| Decision → agent unblocked | 1.8 / 3.4 ms | 1.7 / 2.4 ms | 2.1 / 3.3 ms | 2.0 / 3.1 ms | 2.3 / 5.4 ms |
| Approval round trip (excluding human think time) | 6.3 / 8.5 ms | 6.8 / 8.0 ms | 7.7 / 11 ms | 7.5 / 10 ms | 8.4 / 18 ms |
| Terminal attach → first output | 17 / 19 ms | 17 / 18 ms | 20 / 61 ms | 17 / 20 ms | 30 / 88 ms |
| TUI refresh (fetch + update + render) | 2.4 / 3.0 ms | 4.6 / 5.6 ms | 8.9 / 11 ms | 13 / 21 ms | 61 / 118 ms |
| Lectern CPU avg / peak, % of one core | 2 / 6 | 5 / 11 | 10 / 23 | 18 / 31 | 59 / 107 |
| … avg incl. reaped children | 4 | 9 | 18 | 31 | 82 |
| Lectern RSS avg / peak | 43 / 44 MB | 44 / 46 MB | 46 / 48 MB | 48 / 50 MB | 66 / 73 MB |
| HTTP errors · log WARN / ERROR | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 |

¹ Run with 100 SSE subscribers instead of 25. A second run straight after gave
list 31 / 57 ms, TUI 44 / 66 ms, attach 18 / 21 ms, CPU 58%.

The first run (22:50–22:57 UTC, commit `ebbd477`, files `after.json` and
`big.json`) had a quieter machine: list 9.5 / 13 ms and TUI 12 / 20 ms at 100
sessions, and 23 / 36 ms and 26 / 41 ms at 250.

Launching was recorded but is not steady state. `POST /api/sessions` took
294–539 ms p50 with 8 launches in parallel, and up to 2.6 s p95 at 250. All
100 sessions were up in 6.8 s, and all 250 in 24.2 s.

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
| Healthy machines' status age, before the freeze | 2.6 / 6.0 s | 2.4 / 5.5 s |
| Healthy machines' status age, while `ssh-1` is frozen | 8.1 / 16.7 s | 2.0 / 4.0 s |
| Frozen machine's sessions shown as unreachable | never | after 8.9 s |
| Healthy sessions wrongly shown as unreachable | – | 0 |
| Unreachable marker cleared after `ssh-1` answered again | – | 0.3 s |
| 30 terminals held open: opened / still connected | 19 / 10 | 30 / 30 |

Before the change, the freeze made every other machine's status go stale for
seconds at a time: p95 16.7 s, max 19 s. That was one frozen target in the
serial poll. Before the change, 11 of the 30 terminals failed to open and 9 of
the 19 that did open were closed under their viewers, because the 22nd attach
retired a terminal that was in use.

With the old 21-port range forced on the new code
(`-terminal-ports 7710-7730`, `held-terminals-21-ports.json`):
- 21 terminals stayed connected.
- The other 9 were refused with "every web terminal port is in use by an open
  terminal; close one and try again".
- 3 idle terminals left over from the attach samples were retired to make
  room, each with a notice.
- Nothing anyone was viewing was closed.

### Before the first fixes (same settings as "Current")

| Sessions | 10 | 25 | 50 | 100 |
|---|---:|---:|---:|---:|
| `GET /api/sessions` | 3.9 / 5.4 ms | 14 / 16 ms | 44 / 55 ms | 161 / 255 ms |
| TUI refresh | 4.4 / 5.6 ms | 14 / 17 ms | 46 / 69 ms | 227 / 306 ms |
| Terminal attach → first output | 309 / 311 ms | 308 / 310 ms | 308 / 323 ms | 308 / 312 ms |
| Lectern CPU avg / peak, % of one core | 4 / 9 | 10 / 26 | 27 / 72 | 71 / 132 |

Approvals, SSE fan-out and memory were the same as after the fixes, within
noise.

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
   (`0d53bb6`).
   - The default range is now 200 ports (7710–7909); `LECTERN_TERMINAL_PORTS`
     changes it.
   - The proxy counts open websockets. Only a terminal nobody has open is
     retired, and the attach response says which one.
   - When every port is in use by a viewed terminal, a new attach is refused
     with a reason.
   - A retired ttyd is waited for before its port is reused. The benchmark
     suggests this was part of the "failed to open" count above: the new ttyd
     could not bind while the dying one still answered the readiness check. A
     unit test for this passes both with and without the wait, so the
     benchmark is the evidence here.

## Reproduce

```bash
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress                 # 10,25,50,100
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress-big -tiers 250 -sse-clients 100
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress-mm -tiers 50 -hang-at 15s -hold-terminals 30
```

`-hang-at` freezes the first SSH target that far into the window and reports
the status age of the others. `-hold-terminals K` opens K terminals and keeps
them all open. `-terminal-ports LO-HI` sets the instance's port range.

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
  process on one loopback port. Past 200 viewed terminals, new ones are refused
  until one is closed.
- **Interactive sessions only.** Task dispatch (worktree, `claude -p`, diff
  capture) and push or Discord notification sinks are not part of this load.
- The shared box was not otherwise idle. Every tier ran once, so treat
  differences of a few milliseconds as noise.
- The saved JSON has one field removed, a per-process CPU breakdown for
  tmux/ttyd/agents. It counted only long-lived processes, so it was misleading.
  The driver no longer collects it.
