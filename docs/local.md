# Standalone local Lectern

Use the standalone local runtime when the computer where you run the agent is
also the computer where you want the terminal workspace. It does not require a
separately hosted Lectern server, an SSH alias, a hosted URL, or Grimoire.
Lectern starts a private local helper on demand; no manual server setup is
needed. Your existing agent CLI and its provider/model environment remain the
source of truth.

## Install on Linux or macOS

For a first install, follow the [Linux](getting-started-linux.md),
[macOS](getting-started-macos.md), or [Windows](getting-started-windows.md)
guide. The local runtime includes a durable terminal host and Go helpers;
tmux and Python are optional. Git and an installed agent are needed for real
project work, or use the built-in demo without an agent account.

To build and install from a checkout:

```sh
git clone https://github.com/JeremiahM37/lectern.git
cd lectern
bash tools/install-local.sh
```

The installer builds the checkout with Go and installs `lectern` in
`~/.local/bin`. If the remote client already owns that name, it installs
`lectern-local` instead, leaving the remote launcher unchanged. Add
`~/.local/bin` to `PATH` if your shell does not already include it.

To install a binary you built or received through a release process:

```sh
bash tools/install-local.sh --binary ./lectern --prefix "$HOME/.local/bin"
```

`tools/install-local.sh` accepts a checkout or an already-built binary.
The root `install.sh` downloads checksummed GitHub release assets instead.
The `up` workflow ships in v2.4.1 and later.

## Hosted service recovery

The hosted systemd unit uses `KillMode=process`. Lectern's graceful shutdown
closes its terminal proxies and stops scheduling, then leaves local agent tmux
sessions alive across a binary replacement or manual service restart. The next
process adopts surviving sessions from SQLite and recovery metadata.

This applies only to processes launched on the control-plane host. SSH, PCT,
and sandbox targets keep their tmux processes in the target's own service or
user scope. Before the first restart, checkpoint each target's boot identity
and native conversation identity; adoption must require exact target/session
matches. A deployment replaces the binary atomically, validates this unit
setting, runs `systemctl daemon-reload`, and stops for operator review. It must
not issue `systemctl restart` automatically.

Source builds require Go 1.25.x in addition to the runtime prerequisites.

## Start an agent locally

The installed command has the same local command surface as the hosted client:

```sh
lectern local
lectern local api --help
```

If the installer selected `lectern-local` to preserve an existing remote
launcher, substitute that name in the commands above.

With no `LECTERN_API`, ordinary Lectern commands first look for a Lectern
service already running on this machine — `lectern serve`, for example a
systemd unit — on `127.0.0.1` at `LECTERN_PORT` (default 9110; a service's
`/etc/lectern.env` or `/etc/default/lectern` is also read when you have not
set `LECTERN_PORT` and the file is readable). A private local runtime is
never mistaken for one. If a service answers, plain commands use it; otherwise
they use the private local runtime. When both are running, an interactive
command prints one line saying so, and `lectern doctor` reports the choice on
its `server` line. Commands run inside a local runtime session stay on that
runtime.

A service in token auth mode refuses a CLI without its token. Then plain
commands use the private runtime and print a line asking for
`LECTERN_AUTH_TOKEN` (or `LECTERN_API`). In tailscale mode, processes on the
service's own machine need no token.

`lectern local` explicitly selects local mode even when a service is running
or `LECTERN_API` points at a remote control plane. Conversely, keep
`LECTERN_API` set when an ordinary command should use a particular hosted
server; an unreachable explicit remote does not silently fall back to local
state.

The helper starts privately when the first local command needs it. Check or
stop it with:

```sh
lectern local status
lectern local stop
```

A running helper is reused whatever build started it. When it is older than
the `lectern` binary you run, `lectern local status` reports `"outdated":
true` with a note, `lectern doctor` says so, and interactive local commands
print a reminder. Nothing stops it for you; run `lectern local stop` and the
next local command starts the current build.

### Opening it in a browser

The helper answers only its own signed-in browser. Any web page can send
requests to `127.0.0.1`, so the helper refuses requests from other origins
and host names, and everything else needs its token (the CLI has it) or a
browser cookie. `lectern up` opens your browser through a one-time sign-in
link (it works once, for ten minutes, and prints it when it cannot open a
browser). The browser stays signed in across restarts. Opening the plain
address without signing in shows a page saying to run `lectern up`.

`lectern up` also hands the running helper your shell's `PATH`, so an agent
you installed after it started is found without restarting it.

Stopping the helper does not discard the local database or durable terminal
sessions; later local commands can start it again and resume them. A stop is
refused while a task is still active, so inspect or finish that task first.
Local state defaults to `~/.local/state/lectern/local`, or to
`$XDG_STATE_HOME/lectern/local` when `XDG_STATE_HOME` is set.

`lectern local` opens the local dashboard/console, where you choose the
configured coding-agent command and its project. The local command keeps the
interactive workspace. Configure the coding-agent command and its
provider/model settings in Lectern; a model API endpoint alone is not an
executable coding agent.

Use the normal Lectern terminal dashboard and session controls exposed by the
local runtime. Project paths are local paths on this machine; no target SSH
connection is involved. For a blank room, omit a project when prompted and let
the local runtime create its workspace.

## Windows and WSL

Windows uses the built-in ConPTY terminal host and Git Bash from Git for
Windows. WSL is optional. Follow the [Windows guide](getting-started-windows.md).

## Local versus remote

| | Standalone local | Remote client |
|---|---|---|
| Agent process | Same machine as the terminal | Lectern server or a registered target |
| Setup | Git, agent CLI (or demo); Go for source builds | SSH alias/key and reachable control plane |
| Command | `lectern local` | `lectern` or `lectern console` |
| Server URL | Not required | `LECTERN_API`, installer `--api`, or a service on this machine |
| Grimoire | Optional/not required | Optional; configured by the control plane |

Both paths preserve the agent CLI's own provider and model settings. Choose
the remote client when one board should manage agents on several machines;
choose local when the terminal workspace should stay on this computer.

The built-in terminal uses a Unix PTY on Linux/macOS and ConPTY on Windows;
tmux is optional. Linux has full runtime/browser regression coverage. Windows/macOS CI exercises
real PTY sessions, server restarts, web terminal streams, phone pairing and
no-tmux controls on disposable native runners. Physical-device desktop and
network configurations can still differ from CI.
