# Per-agent-process isolation (bwrap / Docker)

`internal/isolation` gives a session or task a fast, no-clone sandbox tier,
alongside the two isolation mechanisms Lectern already had:

| Tier | What it is | Start cost | Guarantee |
|---|---|---|---|
| none (default) | the agent runs directly on the target | — | none |
| **bwrap / docker** (this doc) | the agent process runs inside a container/namespace on the *same* target | sub-second | filesystem + process containment; network is best-effort (see below) |
| `sandbox` target (`internal/sandbox`) | a whole disposable Proxmox LXC clone | tens of seconds | the strongest: a separate machine |
| `tools/autonomy-runner.py` (`docs/autonomy-isolation.md`) | a privileged, cgroup- and BPF-filtered worker for unattended jobs | seconds | kernel-level network denial, resource limits, UID 65534 |

This is the gap competitors close by running every agent turn in a
container (agent-deck, Sculptor: Docker per agent; Codex/Cursor: a microVM).
Lectern's `sandbox` tier is stronger but heavy — a full LXC clone per
attempt. This tier is the lightweight, sub-second one: reuse it for the
common case, reach for `sandbox` or the autonomy runner when the stronger
guarantee is worth the cost.

## Configuring it

`internal/isolation.Config`:

```json
{"mode": "bwrap", "network": "deny", "docker_image": "", "allow_hosts": ["example.com"]}
```

- `mode`: `""` (none), `"bwrap"`, or `"docker"`.
- `network`: `"allow"` (default — unrestricted, identical to an unisolated
  launch) or `"deny"` (routed through the built-in allowlist proxy; bwrap
  only — see below).
- `docker_image`: only for `mode: "docker"`; defaults to
  `debian:bookworm-slim`.
- `allow_hosts`: extra hostnames the egress proxy accepts under
  `network: "deny"`, layered on `isolation.DefaultAllowHosts` (the launched
  agent's own model API, common package registries, and the Lectern host
  itself so hooks keep working).

**Per-project default, per-launch override**: a project's `default_isolation_json`
(`PATCH /api/projects/{id}` body `{"isolation": {...}}`) is what a new
session or task launch gets when it doesn't say otherwise. `POST /api/sessions`
accepts `"isolation"` to override it for one launch — `null`/absent means
"use the default", `{"mode": ""}` is an explicit opt-out even when the
project's default is sandboxed. A resumed or handed-off session keeps
exactly the Isolation it was launched with (`sessions.LaunchConfiguration.Isolation`),
not a later edit of the project's default. The session card shows a 🔒 badge
naming the running tier when one is active.

## bwrap

Linux only, no daemon. The tmux pane stays **outside** the sandbox — the
pane runs `bwrap ... -- bash -c '<agent command>'`, so attach/detach and the
terminal client work exactly as they do for an unsandboxed session.

Filesystem (`internal/isolation.BuildBwrapArgv`, mirroring the reviewed
profile in `tools/run-isolated-tests.sh`):

- `/usr`, `/etc` read-only (required); `/bin`, `/lib`, `/lib64`, `/sbin`
  read-only via `--ro-bind-try` (tolerated missing, for non-usrmerged /
  non-x86_64 hosts).
- `/dev`, `/proc` private to the sandbox; `/tmp` a fresh, empty tmpfs.
- `$HOME` is a fresh tmpfs (hidden), except the launched agent's own config
  directories, bound **read-write** so an already-authenticated CLI keeps
  working and a token refresh persists:

  | Agent | Bound |
  |---|---|
  | claude | `~/.claude`, `~/.claude.json` |
  | codex | `~/.codex` |
  | gemini | `~/.gemini`, `~/.config/gemini` |

  A custom agent name not in this list gets **no** extra bind — it runs with
  no home directory at all beyond what it puts in the sandboxed `/tmp`. This
  is a fixed, reviewed list on purpose: the whole point of the sandbox is
  that it is *not* "the rest of home too".
- The working directory is bind-mounted **read-write** at its real path and
  is where the sandboxed shell starts (`--chdir`).
- `--unshare-pid --unshare-ipc --unshare-uts --die-with-parent`: the
  sandboxed process cannot see or signal other host processes.

`$HOME`-relative binds use a literal `"$HOME/..."` shell token, expanded by
bash on whichever machine actually runs the command — this package never
assumes it knows the target's home directory at Go build time, so the same
generated command works on a local or an ssh target's own `$HOME`.

### Network: `allow` vs `deny`

`allow` (default) leaves the sandbox's network namespace exactly as the
target already has it — filesystem/process isolation only, same reachability
as an unisolated launch.

`deny` adds `--unshare-net`: the sandbox gets an empty network namespace
with only its own private loopback, no route to anything else. The **one**
way out is a bridge to Lectern's own allowlist proxy
(`internal/isolation.AllowlistProxy`, an HTTP(S) forward/CONNECT proxy that
403s anything not in its allowlist):

1. Before launch, `sessions.Manager` (via `isolation.ProxyRegistry`) opens
   the proxy on a **unix-domain socket** on the target's own filesystem —
   AF_UNIX sockets are a filesystem object, not a network one, so they cross
   the `--unshare-net` boundary intact when bind-mounted in.
2. `bwrap` bind-mounts that socket into the sandbox at a fixed path.
3. Inside the sandbox, before the agent starts, `socat` bridges a loopback
   TCP port (private to the sandbox's own netns) onto that socket:
   `socat TCP-LISTEN:<port>,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:<socket>`
   — **the listening/forking address must come first**. `socat` only reopens
   the *other* address per forked child when the forking address drives the
   loop; reversed, the unix side is dialed once at process start and every
   TCP client after the first shares — and breaks, the moment the first one
   closes — that one connection. This was a real bug caught by the real-run
   test below, not a hypothetical: every connection after the first failed
   "Broken pipe" until the arguments were reordered to match
   `internal/autonomy`'s own bridge, which already had it right.
4. `HTTP_PROXY`/`HTTPS_PROXY`/`http_proxy`/`https_proxy` are exported
   pointing at that loopback port before the agent process execs.
5. The proxy is a live process for the session's **whole lifetime**, not
   just the launch call — `ProxyRegistry` keeps it running and
   `sessions.Manager` tears it down from every path that can end a session
   (`Kill`, archive, poll-detected death, launch-time abort, interrupted
   setup recovery), not only a clean stop.

This design is exactly what the reviewed `tools/autonomy-runner.py` /
`docs/autonomy-isolation.md` bridge already does for its own, heavier tier
(`bridges/network.sock` + an in-sandbox `socat`) — reused here rather than
invented twice.

**`network: deny` requires a `local` target.** The proxy's unix socket lives
on whichever machine `sessions.Manager` itself runs on; an `ssh` or `pct`
target has no way to reach it. `network: allow` bwrap still works on `ssh`
targets (filesystem/process containment only, network untouched) — see
`isolation.ValidateForTarget`. Isolation on a `sandbox` target is refused
outright: the cloned container already **is** the isolation.

## Docker

`docker run --rm -it` with the same workdir and per-agent config mounts as
bwrap, `-w`/`--chdir` at the workdir. `network: allow` leaves Docker's
default bridge network in place; `network: deny` is `--network none` — real,
kernel-enforced, and total. **There is no egress-proxy bridge for Docker in
this release**: a denied Docker session has no network at all, not even its
own model API. That is a deliberate v1 limit, not an oversight — use bwrap
on a local target for network-restricted-but-reachable, or `network: allow`
under Docker for filesystem/process containment with full network.

Docker mode is only usable where the target actually has a Docker daemon;
there is no separate capability probe; a target without `docker` on `PATH`
simply fails the tmux launch with docker's own "command not found", the same
way an unconfigured agent binary already fails today.

## Task attempts (background/queued, not interactive)

`agents.LaunchSpec.Isolation` and `internal/scheduler`'s `taskIsolation`
apply the project's default the same way, with two differences from an
interactive session:

- Never applied when the attempt already runs inside the `sandbox` tier
  (`internal/sandbox`) — that container already is the isolation.
- `network: deny` is **clamped to `allow`** for a queued attempt: a session
  has `sessions.Manager` watching its whole lifecycle to start and tear down
  the proxy; a queued attempt does not yet have that hook. Filesystem/process
  containment still applies. This is logged, not silently dropped.

## Hooks

A launched agent's hook callbacks (`docs/agent-events.md` — statusline,
Stop, PermissionRequest) must still reach Lectern's own HTTP host.
`isolation.DefaultAllowHosts(agent, hookURL)` always includes the parsed
hook host in the `network: deny` allowlist, so this is automatic — a project
does not need to name its own control plane in `allow_hosts`. Docker's
`network: none` has no hooks at all, by the same limit as its model API
above.

## Threat model — what this buys, and what it does not

**Real:**

- A sandboxed agent cannot read or write anything on the host outside its
  workdir and its own explicit agent-config binds — not other projects,
  not credentials, not the rest of `$HOME`, not the Lectern database.
- It cannot see or signal other host processes (`--unshare-pid`).
- `network: deny` on bwrap is a genuine kernel-level network partition: the
  only socket the sandboxed process can reach at all is the one bridged
  unix-domain path, verified by a real nested-bwrap test (below) that
  confirms a direct connection attempt to a real host listener fails while
  the bridged path succeeds.
- `network: deny` on Docker (`--network none`) is a genuine, total, kernel-
  enforced cutoff.

**Not promised — read before relying on this instead of `sandbox`/the
autonomy runner:**

- **This is a process/mount-namespace boundary, not a separate kernel or
  VM.** A kernel vulnerability is out of scope, same as `docs/autonomy-isolation.md`
  says for its own, heavier tier.
- **bwrap here is not privilege-dropped or resource-limited.** Unlike the
  autonomy runner (UID 65534, no capabilities, cgroup RAM/CPU/task caps,
  BPF IP filters, `NoNewPrivileges`), this tier runs as the same user
  Lectern's own process runs as, with no capability drop and no resource
  ceiling. It buys filesystem/process containment for an interactive
  session someone is watching, not a hard multi-tenant boundary for
  unattended code.
- **The egress allowlist is a host check, not TLS inspection.** The proxy
  allows or refuses a CONNECT/forward request by the hostname the client
  names. It does not inspect the SNI inside a CONNECT tunnel against that
  same name, so a process that lies about its CONNECT target and then
  speaks TLS to a different host inside the tunnel is not caught. It is an
  allowlist against accidental/incidental exfiltration to the wrong host,
  not a defense against an agent actively trying to evade it.
- **Docker's `network: allow` (the default) is exactly as open as an
  unisolated launch** — Docker's default bridge reaches the internet like
  any other container. Only `network: deny` (`--network none`) changes that,
  and see above: it also removes the model API.
- **A custom agent's own config paths are never bound.** If you configure a
  CLI Lectern doesn't know (`AgentHomePaths`), it runs isolated with *no*
  home directory beyond the sandbox's private `/tmp` — which may simply
  break a CLI that expects to find its own state there. This is intentional
  (nothing is bound by guesswork), but worth knowing before pointing a new
  agent at this tier.
- **Task attempts never get `network: deny`** (see above) — a project whose
  default asks for it silently runs attempts with `network: allow` instead,
  with a log line, not a failure.

For a hard multi-tenant boundary around an *unattended* job — no
credentials on disk beyond one provider token, cgroup-enforced CPU/RAM/time,
BPF-filtered egress even at the kernel level — use
`tools/autonomy-runner.py` (`docs/autonomy-isolation.md`). For the strongest
guarantee of all — a genuinely separate machine — use the `sandbox` target.

## Testing

`internal/isolation`'s tests split the same way the code does:

- **Pure argv/profile generation** (`bwrap_test.go`, `docker_test.go`,
  `isolation_test.go`): `BuildBwrapArgv`/`BuildDockerArgv` are functions of
  their inputs only — no file- or environment-probing — so every agent ×
  network-policy combination is asserted directly, including that
  `$HOME`-relative binds use the literal, unexpanded `"$HOME/..."` token.
- **The allowlist proxy** (`proxy_test.go`): a real `AllowlistProxy` on a
  real loopback listener — allowed CONNECT tunnels a plain HTTP request
  through and gets the real response back; a denied CONNECT gets 403 before
  a byte reaches the upstream; the plain-HTTP forward path (how a hook
  callback actually reaches HTTP_PROXY) is covered the same way; wildcard
  matching (`*.example.com`) is covered directly.
- **A real, nested bwrap run** (`real_bwrap_test.go`): gated by
  `requireBwrap`, which builds the actual `Wrap()` output for a trivial
  command and runs it, skipping (not failing) if nested bwrap does not work
  in the current environment — `tools/run-isolated-tests.sh` itself already
  runs the whole suite inside one bwrap sandbox, so this is deliberately a
  *second*, nested layer, per that script's own comment that nesting may or
  may not work and must be probed rather than assumed. Where it does work
  (confirmed under `tools/run-isolated-tests.sh` on this repo's own CI
  profile), two real sandboxes run: one proves a stub agent can write inside
  its bind-mounted workdir but cannot read a file placed outside it; the
  other proves, under `network: deny`, that a direct connection to a real
  host listener fails outright while the one bridged path to an allowlisted
  host succeeds — the exact mechanism above, exercised for real, not
  asserted from the argv alone.
- **Launch configuration persistence** (`internal/sessions/launch_configuration_test.go`,
  `isolation_launch_test.go`): a saved `LaunchConfiguration.Isolation`
  round-trips through the same JSON path as `ProfileName`/`Yolo`; a fresh
  launch inherits the project's default; a continuation keeps its own
  captured choice rather than picking up a later default edit; `Launch`
  rejects a target/isolation combination `ValidateForTarget` cannot honestly
  support *before* leaving a live session behind; a `network: deny` launch
  starts this session's own proxy and `Kill` tears it down.
