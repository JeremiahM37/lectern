# Standalone local Lectern

Use the standalone local runtime when the computer where you run the agent is
also the computer where you want the terminal workspace. It does not require a
separately hosted Lectern server, an SSH alias, a hosted URL, or Grimoire.
Lectern starts a private local helper on demand; no manual server setup is
needed. Your existing agent CLI and its provider/model environment remain the
source of truth.

## Install

Follow [Getting started](getting-started.md): the installer for Linux, macOS
or Windows, then `lectern up`. The machine needs Git and an agent CLI. Agent
terminals are kept by Lectern's built-in PTY host, so tmux and Python are not
needed; on Linux an installed tmux is used instead, which also lets Lectern
adopt agent sessions you started by hand ([how](ptyhost.md)).

### Build from source

`tools/install-local.sh` builds a checkout (Go 1.25) or installs a binary you
already have, into `~/.local/bin`:

```sh
git clone https://github.com/JeremiahM37/lectern.git
cd lectern
bash tools/install-local.sh                     # or: --binary ./lectern
```

If the remote client already owns the name `lectern`, it installs
`lectern-local` instead, leaving the remote launcher unchanged.

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
the `lectern` binary you run, `lectern local status` says so (with
`--json`, `"outdated": true`), `lectern doctor` says so, and interactive local commands
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

Stopping the helper does not discard the local database or durable tmux
sessions; later local commands can start it again and resume them. A stop is
refused while a task is still active, so inspect or finish that task first.
Local state defaults to `~/.local/state/lectern/local`, or to
`$XDG_STATE_HOME/lectern/local` when `XDG_STATE_HOME` is set.

`lectern local` opens the local dashboard/console, where you choose the
configured coding-agent command and its project. Sessions keep running when
you close it, in the PTY host (or tmux). Configure the coding-agent command and its
provider/model settings in Lectern; a model API endpoint alone is not an
executable coding agent.

Use the normal Lectern terminal dashboard and session controls exposed by the
local runtime. Project paths are local paths on this machine; no target SSH
connection is involved. For a blank room, omit a project when prompted and let
the local runtime create its workspace.

## Windows and WSL

Native Windows runs the private runtime too (`lectern up`); agents need Git for
Windows. Inside WSL, use the Linux installer. The two are separate installs:
each sees only the agent CLIs and files on its own side.

## Phones

`lectern phone` (or Settings → Connect your phone) lets a phone on the same
Wi-Fi reach this private runtime: the same sessions, on this computer's network
address, for paired devices only. It is unencrypted on the LAN, so use it on a
network you trust; `lectern phone --off` stops it. Away from home, use
Tailscale or a relay ([Remote access](remote-access.md)).

## Local versus remote

| | Standalone local | Remote client |
|---|---|---|
| Agent process | Same machine as the terminal | Lectern server or a registered target |
| Setup | Git and an agent CLI | SSH alias/key and reachable control plane |
| Command | `lectern local` | `lectern` or `lectern console` |
| Server URL | Not required | `LECTERN_API`, installer `--api`, or a service on this machine |
| Grimoire | Optional/not required | Optional; configured by the control plane |

Both paths preserve the agent CLI's own provider and model settings. Choose
the remote client when one board should manage agents on several machines;
choose local when the terminal workspace should stay on this computer.
