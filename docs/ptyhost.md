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
| Web terminal | `lectern term-server` running `tmux attach` | `lectern term-server`, straight from the PTY host |

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

`backend.Tmux` produces the command lines the code produced before (one
capture's argument order aside) — the refactor is behaviour-neutral, and the
existing suite and a golden test are its proof.

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

The resolved backend is reported by the target probe (`session_backend`);
`lectern doctor` says when tmux is not needed.

Changing the setting does not move sessions. Sessions started under one
backend are only visible while that backend is selected; switch back to reach
them.

**Adoption is tmux-only.** Finding and adopting an agent you started by hand
relies on tmux being a shared, discoverable server that any terminal can
create sessions in. The PTY host only holds what Lectern started, so on a
pty-backend target *Find running sessions* finds nothing to adopt.

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
| `paste-file` (not tmux) | `-t T FILE`: tmux's load-buffer + paste-buffer -p in one command |
| `display-message` | `-p -t T FORMAT` |
| `list-panes`, `list-sessions` | `-a -F FORMAT` |
| `set-option` / `show-options` | `[-o] -t T @name VALUE`, `-qv -t T @name` |
| `show-environment` | `-t T NAME` |
| `if-shell` | `-F -t T COND CMD` (CMD may be `kill-session …`) |
| `resize-window` | `-t T -x W -y H` |
| `attach-session` | `-t T` (interactive) |

Plus the native `poll`, `probe`, and `lectern ptyhost status|stop|serve`.

## 4. Web and native terminals

The browser terminal speaks ttyd's WebSocket protocol (`/token`, `/ws`,
subprotocol `tty`, `0`/`1`/`2`/`3` message prefixes). Lectern now serves that
protocol itself, for both backends, with `lectern term-server` (package
`internal/terminal/webterm`), which takes ttyd's own arguments. ttyd is no
longer needed anywhere; a Manager built without a binary (the Go tests) still
uses it, so the protocol stays checked against the real thing.

- A pty-backend attachment on this machine (`lectern pty attach-session …` or
  the project-shell form `lectern pty new-session -A …`) runs the same client
  code in the terminal server's process and streams the host's frames straight
  to the WebSocket: no second pseudo-terminal, no re-encoding.
- Anything else — `tmux attach`, an `ssh -tt …` to another machine, `pct exec`
  — runs on a pseudo-terminal of its own per connection, as with ttyd.
- A terminal server that dies is replaced on the next request, even before
  it has been reaped.

The native client (`lectern attach`) runs the argv the server returns, which
for the pty backend is `lectern pty attach-session -t =NAME`: raw mode, the
session's snapshot, then the live stream. Ctrl-] then `d` detaches, as does
closing the window. Lectern's Ctrl-] controls overlay still needs a local
tmux; without one the attachment runs directly, as it already did.

## 5. Python on the agent machine

Every target-side Python helper has a Go port run as `lectern helper NAME …`,
with the same arguments, output and exit status (parity tests run both on the
same fixtures). Lectern uses the Go port when the target has a `lectern`
binary that lists the helper: always for the local target, and for an SSH or
pct target whose probe (`lectern helper --capabilities`) lists it — a remote
binary older than the server is only asked for what it has. Otherwise the
Python runs as before.

Still Python: the desktop and computer-use tools (Linux-only, need Xvfb and
AT-SPI), the autonomous workshop's host helpers under `/usr/local/libexec`,
plugin hook templates written by users, importing cookies from a Chrome
profile (its SQLite database), and the relay a sandbox hook starts.

## 6. Windows server

- Command lines run through Git for Windows' `bash.exe` (found beside
  `git.exe`, never `System32\bash.exe`, which is WSL), so Lectern's POSIX
  command lines work unchanged; paths they print in `/c/…` form are read back
  as `C:\…`. Git for Windows is already what Claude Code needs on Windows.
  `LECTERN_BASH` names another bash.
- The local target's session backend is the PTY host; `/bin/sh`-style program
  paths map to Git's own programs.
- The local runtime (`lectern up`, the TUI) runs on Windows: its singleton
  lock is `LockFileEx` and the engine inherits the locked handle and the token
  pipe as handles.
- `lectern up --service` writes a per-user logon entry
  (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Lectern`, no
  administrator rights) running `lectern up --no-browser`, which starts the
  runtime detached and exits; a console flashes briefly at logon. A logon
  entry does not restart a crashed runtime; the next `lectern` command does.
- State lives under `%LOCALAPPDATA%\lectern` (the PTY host's socket) and the
  usual `XDG_STATE_HOME` fallback (`~/.local/state/lectern`).

## 7. Testing

- `internal/sessions/backend`: golden tests pin every tmux command to what
  Lectern sent before the seam; the pty backend's commands and the resolver
  are unit-tested.
- `internal/ptyhost`: the format language, targets, flags and keys; and real
  tests that start a detached host, type, capture, resize, attach, detach and
  reattach, poll and probe, and stop a session only when its tracking identity
  matches.
- `internal/terminal/webterm`: a program on its own terminal, and a PTY-host
  session streamed straight from the host, over the real WebSocket protocol.
- `internal/api` real-process tests run once per backend: dispatch, failing
  and custom tasks, interactive launch and priming, typed text, release, kill
  and handoff.
- `internal/smoke`: builds `lectern`, serves it with the pty backend, opens a
  shell, types through the API and the web terminal, kills and restarts the
  server, and finds the same shell.
- The browser suite runs `e2e/test_pty_backend.py` against a real server with
  `LECTERN_SESSION_BACKEND=pty`; the rest of it now uses `term-server` instead
  of ttyd.
- CI: `windows-latest` and `macos-latest` jobs run the backend, PTY host, web
  terminal, Git Bash, local runtime and smoke tests. The reviewed isolated
  Linux runner is unchanged, except that it no longer requires ttyd.

## Known gaps

- Windows and macOS are exercised by CI only; nothing here was run on those
  systems by hand.
- Windows has no public API for another process's working directory: the
  pane's directory is the shell's last OSC 7 report, else where the session
  started. The foreground process is the newest one in the session's tree.
- Agent-exit detection on Windows reads the newest process's command line; it
  is best-effort.
- Native conversation identity reads Linux `/proc`; elsewhere it is
  inconclusive, as it was.
- The worktree helpers (git worktrees for sessions and tasks) refuse on
  Windows, as the Python they replace never ran there: sessions on Windows run
  in the project directory itself.
- The screen model does not reflow on resize, and `capture-pane -J` does not
  join wrapped lines.
- Sessions on the PTY host end when the user logs out if the OS ends the
  user's processes (systemd `KillUserProcesses`), exactly as tmux's do.

## 8. Python helper inventory

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

### Native identity, conversations and search

| helper | replaces | used by |
|---|---|---|
| `native-identity` | `native_records.py` + `native_identity.py` | `sessions.CaptureNativeID`, `sessions.CaptureNativeEvidence` |
| `native-conversations` | `conversations.py` | conversation history (`api/native_conversations.go`), adopted-session matching |
| `native-conversation-live` | `conversation_live.py` | the structured live conversation view |
| `catalog-sessions` | `catalog_sessions.py` | catalog agents' conversation lists |
| `configured-home` | the configured-home probe | `sessions.ProbeConfiguredHome` |
| `claude-fork-path` | `claude_fork_path.py` | forking a Claude conversation |
| `native-search`, `native-search-read` | the native search scripts | native search; the Go and Python versions read each other's SQLite index |

These read Linux `/proc` as the scripts did, and ask the target's session
backend about a pane through `helpers.Mux`.

### Workspace, files, review and the rest

| helper | replaces | used by |
|---|---|---|
| `workspace-files` | `api/workspace_files.py` | the file explorer, reads and edits (`runWorkspaceScript`) |
| `review`, `review-git` | `api/scripts/review.py`, `review_git.py` | the terminal's changes view and review actions |
| `worktree`, `worktree-cancel`, `worktree-preflight`, `worktree-group` | the interactive worktree scripts | `worktree.RunInteractiveWithTimeout` |
| `realpath` | the realpath one-liner | workspace allocation |
| `mcp-install`, `gemini-workspace` | the MCP and Gemini workspace installers | sessions and the scheduler |
| `skills`, `workflow-stage` | their staging scripts | project skills and workflows |
| `ports` | the ports script | the browser pane's port list |
| `browser-cookies` | `cookies.py` (file imports) | importing a cookies file |
| `bridge` | the stdio-to-TCP relay | pct targets' `DialTarget` |

A folder download's zip has the same entries but Go's compressed bytes, and
text search without ripgrep or git uses Go's regular expressions.
