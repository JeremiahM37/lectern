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

`p50 / p95`. "Before" is commit `d3c69ac` (the benchmark alone). "After" adds
the two fixes described below. Both runs use identical settings.

### After the fixes

| Sessions | 10 | 25 | 50 | 100 | 250 ¹ |
|---|---:|---:|---:|---:|---:|
| Launched / failed | 10 / 0 | 25 / 0 | 50 / 0 | 100 / 0 | 250 / 0 |
| Live at window start / end | 10 / 10 | 25 / 25 | 50 / 50 | 100 / 100 | 250 / 250 |
| `GET /api/sessions` | 1.8 / 2.6 ms | 3.1 / 4.2 ms | 5.4 / 6.9 ms | 9.5 / 13 ms | 23 / 36 ms |
| List response size | 10 KB | 26 KB | 53 KB | 105 KB | 266 KB |
| SSE fan-out (update → every subscriber) | 1.1 / 2.4 ms | 1.0 / 1.7 ms | 0.9 / 1.7 ms | 1.0 / 1.5 ms | 0.9 / 9.6 ms |
| SSE events missed / delivered | 0 / 725 | 0 / 725 | 0 / 725 | 0 / 725 | 0 / 2900 |
| Approvals in window (all approved) | 30 | 75 | 150 | 300 | 752 |
| Approval request → visible to operator | 5.0 / 7.3 ms | 4.8 / 6.5 ms | 5.3 / 6.9 ms | 5.0 / 6.9 ms | 5.1 / 7.4 ms |
| Decision → agent unblocked | 1.6 / 2.8 ms | 1.8 / 3.0 ms | 2.0 / 3.1 ms | 1.9 / 3.0 ms | 1.9 / 3.5 ms |
| Approval round trip (excluding human think time) | 6.8 / 9.3 ms | 6.7 / 9.5 ms | 7.5 / 9.5 ms | 7.0 / 9.7 ms | 7.1 / 11 ms |
| Terminal attach → first output | 16 / 18 ms | 16 / 18 ms | 19 / 20 ms | 18 / 19 ms | 17 / 20 ms |
| TUI refresh (fetch + update + render) | 2.3 / 3.0 ms | 3.6 / 4.8 ms | 6.4 / 7.7 ms | 12 / 20 ms | 26 / 41 ms |
| Lectern CPU avg / peak, % of one core | 2 / 7 | 5 / 10 | 9 / 20 | 16 / 33 | 57 / 81 |
| … avg incl. reaped children | 4 | 8 | 16 | 28 | 83 |
| Lectern RSS avg / peak | 42 / 45 MB | 44 / 46 MB | 46 / 48 MB | 47 / 49 MB | 65 / 70 MB |
| HTTP errors · log WARN / ERROR | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 | 0 · 0 / 0 |

¹ Run once, with 100 SSE subscribers instead of 25.

Launching is not a steady-state number, but it was recorded. `POST /api/sessions`
took 263–600 ms p50 with 8 launches in parallel, which is 1.5 s p95 at 250.
All 100 sessions were up in 6.1 s, and all 250 in 21.4 s.

### Before the fixes (same settings)

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

## Reproduce

```bash
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress                 # 10,25,50,100
ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh /tmp/stress-big -tiers 250 -sse-clients 100
```

Useful flags are `-window`, `-approval-every`, `-ssh-targets`, `-sse-clients`,
`-attach-samples` and `-plain-dirs`. `-plain-dirs` puts agents in plain
directories, so no awareness data exists. `ADK_TEST_CPUS` changes the core
cap (default 8). Each run writes `results.json`, `results.md` (the table
above) and each tier's Lectern log. A tier takes about 75–90 s, or longer for
the 250-session tier.

## Caveats

- **One physical host.** "Four targets" means four real SSH servers and tmux
  servers on loopback, not four machines. Network latency to real remote
  targets is not in these numbers. The session status poll visits targets one
  after another, so a slow or unreachable target delays status for the others
  (up to its 45 s command timeout). This benchmark does not exercise that.
- **Stand-in agents, not Claude.** The agents exercise Lectern's real paths:
  hooks, tmux panes, status polling, terminals and awareness. They do not run
  an LLM, write large panes or produce long transcripts. Session list size
  grows with pane previews and history that these agents keep small.
- **Approval latency excludes the human.** The driver approves instantly. In
  real use the round trip is the operator's reaction time plus about 7 ms.
- **Only 21 web terminals can be open at once.** ttyd ports come from
  7710–7730. Opening a 22nd retires the oldest, and its viewers must reconnect.
  This comes from the code (`internal/terminal`), not from a measurement. The
  benchmark attaches 10 per tier.
- **Interactive sessions only.** Task dispatch (worktree, `claude -p`, diff
  capture) and push or Discord notification sinks are not part of this load.
- The shared box was not otherwise idle. Every tier ran once, so treat
  differences of a few milliseconds as noise.
- The saved JSON has one field removed, a per-process CPU breakdown for
  tmux/ttyd/agents. It counted only long-lived processes, so it was misleading.
  The driver no longer collects it.
