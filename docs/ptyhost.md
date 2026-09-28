# Session backends and `lectern ptyhost`

Lectern keeps every agent and shell in a terminal that outlives the web page,
the phone and the Lectern server itself. Until 2.7 that terminal was always a
tmux session, and a handful of target-side helpers were Python scripts. That
made `tmux` and `python3` hard requirements on every agent machine and kept
the server off native Windows.

This document describes the replacement: a **session backend** seam with two
implementations, and a small PTY host that ships inside the `lectern` binary.

| | tmux backend | pty backend |
|---|---|---|
| Needs on the agent machine | `tmux`, `python3` for a few helpers | nothing but the `lectern` binary |
| Platforms | Linux, macOS, WSL | Linux, macOS, Windows 10 1809+ |
| Session survives a Lectern restart/upgrade | yes (tmux server) | yes (ptyhost process) |
| Adopt sessions you started by hand | yes | no — tmux only (see below) |
| Web terminal | ttyd wrapping `tmux attach` | built-in, straight to the PTY host |

## 1. The seam

`internal/sessions/backend` holds the `Backend` interface. Every place that
used to build a `tmux …` command line now asks the target's backend for it:
create, send text and keys, capture, the batched status poll, the agent-exit
probe, list, kill (plain, quiet, and "only if the tracking identity still
matches"), has-session, times, pane cwd, show-environment, tracking-identity
options, and the attach argv for the web and native terminals.

The interface returns **shell command strings**, not results. That is the
existing contract of the executor layer (`Executor.Run` runs a command on a
target — locally, over SSH, or in a container), so both backends work on any
target kind without new plumbing, and every parser (the framed poll, the
agent probe, discovery) is shared.

`backend.Tmux` produces exactly the strings the code produced before — the
refactor is behaviour-neutral, and the existing suite is its proof.

`backend.Pty` produces the same commands with the program word replaced:
`'<lectern>' pty capture-pane -p -t '=lec-3:' -S -40` instead of
`tmux capture-pane …`. `lectern pty` implements **the subset of tmux's
command language Lectern uses**, including its target syntax (`=name`,
`=name:`), its format language (`#{pane_pid}`, `#{?a,b,c}`, `#{==:a,b}`,
`#{@option}`) and its error texts (`can't find session: NAME`, `no server
running on …`, `unknown variable: NAME`), because callers key on those.
Keeping the language identical is what lets one table-driven test suite run
against both backends and compare answers.

Where the tmux form relies on the target's shell tools, the pty backend uses
a native subcommand instead, so nothing beyond `lectern` is needed (and so it
works in Git Bash on Windows):

| operation | tmux backend | pty backend |
|---|---|---|
| batched status poll | `tmux capture-pane` + `base64` per pane | `lectern pty poll` (same framing) |
| agent-exit probe | `tmux display-message` + `ps` | `lectern pty probe` (same framing) |
| discovery of hand-started agents | `tmux list-panes -a` + `ps` | not offered |
| extended keys | `set-option -s extended-keys` | not needed: bytes pass through untouched |

### Which backend a target uses

`LECTERN_SESSION_BACKEND=auto|tmux|pty` (default `auto`), resolved **per
target** when its executor is made:

- `tmux` / `pty`: every target uses that backend.
- `auto`, local target: `pty` on Windows and macOS; on Linux `tmux` when it is
  installed, else `pty`. A local ptyhost that already holds sessions keeps
  `pty` in use, so installing tmux later never hides running sessions.
- `auto`, SSH/pct targets: `tmux`, unless the target's probe found no tmux but
  found a `lectern` binary, in which case `pty`.
- Mock mode (`LECTERN_MOCK=1`) scripts the tmux dialect, so mock targets stay
  on tmux; real terminals opened in mock mode (project shells) follow the
  setting.

The resolved backend is reported by the target probe (`session_backend`) and
by `lectern doctor`.

Changing the setting does not move sessions. Sessions started under one
backend are only visible while that backend is selected; switch back to reach
them.

**Adoption is tmux-only.** Finding and adopting an agent you started by hand
relies on tmux being a shared, discoverable server that any terminal can
create sessions in. The PTY host only holds what Lectern started. With the
pty backend, *Find running sessions* reports that adoption needs tmux.

## 2. `lectern ptyhost`

One small process per user per machine owns the PTYs. It is started on
demand (by the first `lectern pty new-session`) and is deliberately separate
from the server so a server restart or upgrade never touches it.

### Building blocks

- **PTYs:** `github.com/charmbracelet/x/xpty` — creack/pty on Unix and ConPTY
  on Windows behind one interface with `Resize`, plus a `WaitProcess` that
  works around Go's broken `Wait` for ConPTY children (go#62708). It is
  maintained with the rest of charmbracelet/x (v0.1.4, July 2026).
  `aymanbagabas/go-pty` is equivalent and by the same author, but xpty shares
  its module family and release cadence with `x/vt` and `x/ansi`, which
  Lectern already depends on. `UserExistsError/conpty` is Windows-only and
  unmaintained since 2024.
- **Screen state:** `github.com/charmbracelet/x/vt`, a Go terminal emulator
  with a main/alternate screen, scrollback and mode tracking. Every byte a
  session writes goes to the emulator and to any attached clients.
  `capture-pane` reads the emulator (screen plus scrollback, 5000 lines by
  default, `LECTERN_PTY_HISTORY`).

### Socket and permissions

- Unix: `$XDG_RUNTIME_DIR/lectern/ptyhost.sock`, else
  `$TMPDIR/lectern-<uid>/ptyhost.sock`; the directory is created `0700` and
  its owner is checked before use.
- Windows: an AF_UNIX socket (supported since Windows 10 1803, and by Go's
  `net` package) at `%LOCALAPPDATA%\lectern\ptyhost.sock`. The profile
  directory's ACL already limits it to the user, SYSTEM and Administrators.
  This keeps one transport on every platform instead of a second named-pipe
  implementation.
- `LECTERN_PTYHOST_SOCKET` overrides the path (tests, multiple instances).
- A lock file beside the socket (`flock` / `LockFileEx`) is held for the
  host's lifetime. A starting host takes the lock first; only the lock holder
  may remove a stale socket. That is the rule the research on Orca's daemon
  handover came to: never delete a name you did not create.

### Protocol

Length-prefixed frames on the socket: 1 byte kind, 4 bytes big-endian length,
payload. Control frames carry JSON.

1. **Handshake.** The client sends `{"hello":"lectern-ptyhost","protocol":N,
   "min":M}`; the host answers with its own protocol version, the operations
   it supports, its build and pid. A client refuses a host outside `[min,N]`
   with a clear message; within the range it only uses operations the host
   listed. New operations are additive, so an older host keeps serving a
   newer client for everything it knows.
2. **Requests.** One JSON request, one JSON response:
   `new`, `list`, `has`, `kill`, `write`, `paste`, `capture`, `resize`,
   `info` (pid, tty, cwd, foreground command, created/activity times,
   environment, options), `set-option`, `poll`, `probe`, `shutdown`.
3. **Attach.** After an `attach` request the connection becomes a stream:
   the host sends a snapshot of the screen (rendered with styles, cursor
   position and the private modes the program has set — alternate screen,
   bracketed paste, mouse, application cursor keys, focus events), then raw
   output frames; the client sends input and resize frames. Any number of
   clients may attach; the latest client to resize sets the size, as tmux's
   `window-size latest` does.

A program that queries the terminal (cursor position, device attributes) is
answered by the attached client when there is one and by the emulator when
there is none, so programs never hang on an unattended session and are never
answered twice.

### Lifetime and upgrades

- The host detaches from whoever started it: `setsid` on Unix; on Windows
  `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_BREAKAWAY_FROM_JOB`
  (falling back without breakaway when the job forbids it), so closing the
  console that started Lectern does not end the agents. On Windows a
  session is exactly as durable as the process holding its pseudoconsole —
  `ClosePseudoConsole` ends the attached process tree — which is why the host
  must not be the server.
- Lectern's own systemd unit uses `KillMode=process`, so a host started by the
  service survives `systemctl restart lectern`, as tmux does.
- The host is never replaced while it holds sessions. It exits by itself when
  its last session has ended and no client is connected (tmux behaves the
  same), and the next start runs the current binary. Handshake versioning
  covers the window in between. `lectern ptyhost status` shows the running
  host's build, protocol and sessions.
- `lectern ptyhost stop` ends the host **and every session in it**; it asks
  for `--force` when sessions are live.

### Foreground process and working directory

Used by the agent-exit probe, the terminal split's "open in the same
directory", and native identity.

- **Linux:** `tcgetpgrp` on the PTY → process group leader; `/proc/<pid>/cwd`,
  `/proc/<pid>/cmdline`; the TTY is the real `/dev/pts/N`, so `ps -t` and
  discovery-style joins also work.
- **macOS:** `tcgetpgrp`; `kern.proc.pid` sysctl for the command name; cwd via
  `lsof -a -p PID -d cwd -Fn` (part of the base system).
- **Windows:** ConPTY has no process groups. The foreground is the most
  recently started descendant of the session's root process (Toolhelp
  snapshot). The cwd of another process is not exposed by a public API, so
  the pane cwd is the last OSC 7 / OSC 9;9 directory the shell reported, else
  the session's starting directory. Git Bash's prompt reports OSC 7 when
  configured; PowerShell and cmd do not by default.

## 3. `lectern pty` command subset

| tmux command | options used by Lectern |
|---|---|
| `new-session` | `-d -s NAME -c DIR -e K=V -x W -y H [--] CMD…` (a single CMD word is run by the shell, as tmux does) |
| `has-session`, `kill-session` | `-t =NAME` |
| `capture-pane` | `-p -t T [-S -N] [-J] [-e]` |
| `send-keys` | `-t T [-l] KEY…` (tmux key names: `Enter`, `Escape`, `C-c`, `Up`, `Tab`, …) |
| `load-buffer` / `paste-buffer` | `-b NAME FILE`, `-b NAME -t T [-d] [-p]` |
| `display-message` | `-p -t T FORMAT` |
| `list-panes`, `list-sessions` | `-a -F FORMAT` |
| `set-option` / `show-options` | `[-o] -t T @name VALUE`, `-qv -t T @name` |
| `show-environment` | `-t T NAME` |
| `if-shell` | `-F -t T COND CMD` (CMD may be `kill-session …`) |
| `resize-window` | `-t T -x W -y H` |
| `attach-session` | `-t T` (interactive) |

Plus the native `poll`, `probe`, and `lectern ptyhost status|stop|serve`.

## 4. Web and native terminals

The web terminal speaks ttyd's WebSocket protocol (`/token`, `/ws`,
subprotocol `tty`, `0`/`1`/`2`/`3` message prefixes). For a pty-backend
attachment Lectern serves that protocol itself (`internal/terminal/ptyweb`)
and connects the WebSocket straight to the PTY host — no ttyd, no extra PTY.
For a remote pty target the same server runs `ssh -tt … lectern pty
attach-session` in a local PTY. tmux attachments keep using ttyd.

The native client (`lectern attach`) runs the attachment argv the server
returns, which for the pty backend is `lectern pty attach-session -t NAME`
(put the local terminal in raw mode, detach with Ctrl-] then `d`... or by
closing the window). Lectern's Ctrl-] controls overlay still needs a local
tmux; without one the attachment runs directly, as it already does.

## 5. Python on the agent machine

Every target-side Python helper has a Go port reachable as `lectern helper
NAME …`, with byte-identical output. When the target has a `lectern` binary
(always for the local target; probed for SSH/pct targets) Lectern runs the
Go helper; otherwise it runs the existing Python, and `lectern doctor` and
the target probe say that Python is then required. The inventory is in
§8.

## 6. Windows server

- Commands run through Git for Windows' `bash.exe` (found beside `git.exe`),
  so Lectern's shell command lines, and Claude Code, work unchanged. Git for
  Windows is already a requirement for Claude Code on Windows.
- The session backend is `pty`.
- `lectern up --service` registers a per-user logon entry
  (`HKCU\…\Run\Lectern`, no administrator rights) that starts
  `lectern local supervise --detach`; the process re-launches itself
  detached, so no console window stays open and closing one ends nothing.
- State lives under `%LOCALAPPDATA%\lectern`.

## 7. Testing

- Session-level Go tests run table-driven against both backends on Linux.
- `internal/ptyhost` has unit tests for the protocol, the format language and
  capture, and a real smoke test (spawn a shell, send text, capture, resize,
  reattach after the client — standing in for a server — restarts).
- CI adds `windows-latest` and `macos-latest` jobs running the backend and
  ptyhost tests, including the smoke test. The reviewed isolated Linux runner
  is unchanged.
- A subset of the browser suite runs with `LECTERN_SESSION_BACKEND=pty`.

## 8. Python helper inventory

See the table maintained at the end of this file once the ports land.

### Agent hooks, drivers and trust

These run on the agent machine, some of them launched by the agent itself
(hook commands, codex's `notify`), so the Go form is written into the
agent's configuration rather than only run by Lectern.

| helper | replaces | used by |
|---|---|---|
| `approval-hook` | `internal/hooks/hook.py` | the PreToolUse gate in a task's settings.json (`agents.HookSettingsFor`) |
| `lec` | `internal/hooks/lec.py` | the task-filing kit named in the task prompt footer (`scheduler.AgentTaskFooterFor`); reads `.lectern/env` from the working directory, as `python3 .lectern/lec.py` did |
| `claude-settings-install` | agentevents' Claude settings installer | interactive Claude sessions (`agentevents.ClaudeSettingsInstall`) |
| `claude-statusline` | the default status line's `python3 -c` | the status line script the installer writes |
| `codex-notify` | `CodexNotifyScript` | codex `-c notify=[…]` (nothing is written to disk) |
| `codex-hook` | `CodexHookScript` | the hooks.json entries the installer merges |
| `codex-hooks-install` | agentevents' hooks.json merge | interactive codex sessions (`agentevents.CodexHooksInstall`); replaces entries naming the Python handler |
| `pump` | the drivers' `pump.py` | streaming drivers (claude-steer, codex-appserver, ACP) |
| `claude-trust`, `codex-trust` | the built-in trust commands (and the legacy Claude one) | `Spec.TrustProbeOn`; `codex-trust` carries a port of tomllib so it reads config.toml exactly as the script did |

Differences from the scripts, all deliberate: the approval hook, `lec` and
the codex handlers make their HTTP requests themselves (urllib's and curl's
proxy rules, including proxying loopback, which the network=deny sandbox
needs), so a curl-less target works too; `lec`'s usage text names the Go
command. Operator- or catalog-defined trust commands (e.g. openclaude's) run
as declared.
