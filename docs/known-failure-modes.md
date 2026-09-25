# Known failure modes

Failures this repository has already hit, and what now prevents or detects each
one. Each entry starts from the symptom, because that is what an operator or a
future contributor actually has to work backwards from.

## Agent or test commands killing the live tmux server

- **Symptom** — every agent session on the host disappears at once, and Lectern
  shows a wall of ended sessions, after an agent test run or a debugging step.
- **Cause** — `tmux kill-server` / `kill-session` aimed at the operator's own
  tmux server instead of a test one. Two traps make that easy: a test that
  never pins its socket inherits the host server, and `$TMUX` beats
  `$TMUX_TMPDIR` — tmux reads the client socket from `$TMUX` before any default
  derived from `TMUX_TMPDIR`, so setting `TMUX_TMPDIR` alone still talks to the
  host server.
- **Prevention** — tmux-dependent tests run only through
  `tools/run-isolated-tests.sh`, which runs them in an unprivileged bubblewrap
  namespace against a private tmux server (`tools/tmux-isolated-wrapper` unsets
  `TMUX` and rewrites `kill-server` into exact `kill-session` calls on a
  validated test socket). Real-process tests call `testutil.RequireIsolated`,
  which verifies the marker *and* the kernel namespace rather than trusting the
  environment; `newRealRig` and the other real-process fixtures all go through
  it. On top of that, every contributor using Claude Code gets the committed
  hook `.claude/settings.json` → `tools/claude-guard/block-host-tmux-kill.py`,
  which refuses a host tmux kill command before the shell runs it.
- **Where** — `tools/run-isolated-tests.sh`, `tools/tmux-isolated-wrapper`,
  `internal/testutil/tmux.go`, `internal/api/e2e_real_test.go`,
  `tools/claude-guard/`.

## A logout killing a tmux server

- **Symptom** — a burst of sessions ending with no workload change, at a moment
  that lines up with an SSH disconnect.
- **Cause** — the tmux server was started from a login shell. With systemd user
  lingering off, logging out terminates that user session scope and the tmux
  server inside it.
- **Detection** — correlate the burst of `session ended` log lines with sshd
  disconnects.
- **Prevention / mitigation** — start the server from `lectern.service`, whose
  cgroup survives logout, or enable lingering for the account
  (`loginctl enable-linger`). `tools/upgrade-lectern.sh` installs a
  `KillMode=process` drop-in so a service restart does not take the panes with
  it.
- **Where** — `internal/sessions/poll.go`, `tools/upgrade-lectern.sh`,
  `docs/testing/mobile-terminal.md`.

## Ctrl-C hanging in a test

- **Symptom** — a test that presses Ctrl-C in a child's PTY waits forever for a
  process that never reacted.
- **Cause** — a non-interactive shell starts background jobs (`cmd &`) with
  SIGINT and SIGQUIT ignored, and an ignored disposition survives every `exec`
  after it, so the suite behaves differently depending on how it was launched.
- **Prevention** — `e2e/conftest.py` restores the ordinary dispositions at
  import, before anything is spawned; `tools/test-all-parallel.sh` runs under
  `set -m` so its jobs get their own process group and default signal
  dispositions.
- **Where** — `e2e/conftest.py`, `tools/test-all-parallel.sh`.

## A terminal shrunk to a sliver padded with dots

- **Symptom** — a session's terminal is only a few columns wide and padded with
  dots, even in a large window.
- **Cause** — tmux sizes a shared window for one client. With
  `window-size smallest` a hidden tab, a phone, or a native terminal attached to
  the same session wins that race for every other viewer.
- **Prevention** — sessions are created with `window-size latest`, so the last
  client to claim the size sets it, and the browser reclaims the size on show
  and focus by re-announcing its dimensions and stepping the row count down and
  back — which tmux counts as a real resize from that client.
- **Where** — `internal/terminal/terminal.go`,
  `frontend/src/terminal/engine.ts`.

## An upgrade replacing the live binary symlink with a plain file

- **Symptom** — after `tools/upgrade-lectern.sh --apply`, a later release
  install does not reach the running service, and re-pointing the symlink has
  no effect.
- **Cause** — the install step writes the candidate over the live path
  (`/usr/local/bin/lectern`), which is meant to be a symlink into the release
  directory. Replacing that path with a regular file breaks the indirection the
  release layout depends on.
- **Detection / recovery** — check `ls -l /usr/local/bin/lectern` after an
  upgrade; when it is not a symlink, re-point it to the release directory
  before restarting:

  ```bash
  sudo ln -sfn /opt/lectern/releases/<sha>/lectern /usr/local/bin/lectern.new && sudo mv -T /usr/local/bin/lectern.new /usr/local/bin/lectern
  ```

- **Where** — `tools/upgrade-lectern.sh`.

## CI e2e not starting after a `go.sum` change

- **Symptom** — the e2e job fails immediately with a bubblewrap bind error and
  no test output at all.
- **Cause** — the isolated runner bind-mounts the Go module cache read-only,
  and the sandbox has no network. A cache miss (any `go.sum` change) left
  `GOMODCACHE` absent, and bwrap cannot bind a source path that does not exist.
- **Prevention** — the workflow runs `go mod download` before the isolated run,
  so the modules exist before the sandbox starts.
- **Where** — `.github/workflows/ci.yml`, `tools/run-isolated-tests.sh`.

## Every client appearing to come from loopback

- **Symptom** — with `tailscale serve` in front, every request arrived from
  127.0.0.1, so identity had to be read from `Tailscale-User-Login` /
  `X-Forwarded-For` — ordinary headers that any process on the host could
  forge, a dispatched agent included. Identity, and therefore approvals, could
  be spoofed from the same box.
- **Cause** — `tailscale serve` proxies from loopback, and a loopback listener
  cannot tell a real tailnet peer from a local
  `curl -H 'Tailscale-User-Login: ...' 127.0.0.1:PORT/...`.
- **Prevention** — Lectern serves the PWA itself: a native TLS listener bound to
  this node's tailnet addresses, so requests arrive with real peer addresses
  and a whois against tailscaled is authoritative. `tailscale serve` identity
  headers are untrusted by default and only honoured with an explicit opt-in.
- **Where** — `internal/auth/tls.go`, `internal/auth/resolver.go`,
  `cmd/lectern/main.go`.
