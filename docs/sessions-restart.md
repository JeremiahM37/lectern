# Restart all sessions

Settings → Basics → *Restart all sessions*, the sessions list's ⋯ menu,
`lectern restart --all`, `POST /api/restart-all`, or the MCP tool
`restart_all_sessions` restarts every session in place: each agent is stopped
and started again in the **same** session, resuming the **same** conversation
by its id, with the same name, project, group, worktree, account and launch
settings. Its Grimoire briefing is part of that conversation, so nothing is
typed into it again. It is the path an account swap ([accounts](accounts.md))
already takes, for every session at once; use it after updating Claude Code,
or to bring every claude session into the Claude app (a restarted claude
session comes back with [Remote Control](remote-control.md) when it can use
it, unless it was started with `remote_control: false`).

Before anything is stopped, the confirm dialog (or `lectern restart --all
--dry-run`, or `GET /api/restart-all`) lists what will happen to each
session:

| Status | Meaning |
|---|---|
| `restarting` | idle: restarted now, one at a time, each waiting for its prompt before the next |
| `pending` | working, asking a question, or with text typed at its prompt: restarted once it waits for you, never mid-turn |
| `skipped` | a shell, a session Lectern did not start, or one whose conversation id is not known yet (a restart would start a new conversation), with the reason |
| `restarted` / `failed` | how it went |

`POST /api/restart-all` returns at once with that list; `GET` shows the run
(`last`) as it goes, and the CLI follows it until no session is still
`restarting`. A session's terminal is stopped by Lectern's own per-session
stop, which checks it is the exact process the session started; nothing is
stopped by name. Pending restarts live in the server's memory: a server
restart forgets them, and running it again picks them up.

The MCP tool is not offered over the claude.ai connector
([web-connector.md](web-connector.md)): restarting the whole board is the
operator's own maintenance.

## Update and restart

When a newer Lectern release is out, the same dialog (Settings → Basics, or
the sessions list's ⋯ menu → *Restart all sessions*) says so and offers
**Update and restart all**. `GET /api/update` reports `current`, `latest`,
`available`, and whether this server can update itself (`can_update`,
`reason`). `POST /api/update` (optionally `{"version": "v2.10.6"}` or
`{"restart_sessions": false}`) then:

1. downloads the release archive for this platform, checks it against the
   release's `checksums.txt`, and runs the new program once before swapping
   it in, the same steps as `lectern update`; nothing changes if any of
   these fail;
2. answers, then restarts the server **in place** (same process ID, arguments
   and environment, so a service manager sees no exit). Sessions run in their
   own tmux or PTY host and keep running through it;
3. the new server restarts every session, as above: idle ones one at a
   time, busy ones once they wait for you, each into its own conversation.

The dialog follows each step and then the sessions' progress. Like editing
files, it needs a signed-in person (a token or tailnet identity), not a
process on the server.

The server can only replace a program it is allowed to write. It cannot
update itself when a package manager owns it (Homebrew, Scoop, .deb, .rpm),
when its folder belongs to another user (for example a root-owned
`/usr/local/bin/lectern` run by a service user), or on Windows. The dialog
then names the command to use instead.

This updates the server. Update the `lectern` program on each computer you
attach from with `lectern update`.
