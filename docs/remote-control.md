# Remote Control: open a session in the Claude app

A Lectern claude session can also be reachable from **claude.ai/code** and the
**Code tab of the Claude mobile app**, through Claude Code's own
[Remote Control](https://code.claude.com/docs/en/remote-control). It is the same
claude process, not a copy. Messages from the terminal, Lectern's chat, the
browser and the phone go into one conversation. `send_to_session`, approvals,
restore and Grimoire priming keep working exactly as before.

## Turning it on

- **Every new session (the default):** new claude sessions open in the Claude
  app without being asked: Settings → Basics → *Open new claude sessions in
  the Claude app*, `lectern remote-control --default on|off`, the
  `remote_control_default` setting (`"0"` is off) or
  `LECTERN_REMOTE_CONTROL_DEFAULT=0|1`, which wins over the setting. A
  session that cannot use it (see below) starts without it rather than
  failing. One session opts out by unticking the box in the New session
  dialog, `lectern claude --no-remote-control`, or `remote_control: false` to
  the API and MCP `start_session`.
- **New session, explicitly:** tick *Also open in the Claude app (Remote
  Control)* in the New session dialog. From the terminal, use `lectern claude
  --remote-control` (or `--rc`) or the *Remote Control* option under *More
  options…*. Over MCP, pass `remote_control: true` to `start_session`. Asked
  for this way, a session that cannot use it is refused with the reason.
- **A running session:** use *Enable Remote Control* in the card's ⋯ menu, the
  session menu in the terminal client, `lectern claude --remote-control` in a
  folder whose session is already running, or the MCP tool
  `enable_remote_control`. Lectern types `/remote-control <name>` into the
  session, so it only does this while the agent is waiting for you. While it
  is working or asking a question, the call is refused and asks you to try
  again.

## Every running session

Settings → Basics → *Turn on in all running sessions*, `lectern
remote-control --all`, or `POST /api/remote-control/all` turns it on in every
live claude session and reports each one:

| Status | Meaning |
|---|---|
| `on` / `already` | reachable from the Claude app now |
| `pending` | the agent was busy; Lectern turns it on after a poll finds it waiting for you |
| `consent` | Claude Code's one-time question (below); answer it, then run it again |
| `skipped` | cannot use it: another agent, an API key, a sandbox, or a Claude Code too old for it, with the reason |

A session restored or reopened later picks the default up again, so a
session from before this default gets it on its next start too.

Claude Duet and other claude.ai apps reach Lectern through its claude.ai
connector ([web-connector.md](web-connector.md)); Remote Control is what puts
each session itself in the Claude app.

## SSH and container machines

Remote Control runs in the claude process, so a session on an SSH or `pct`
machine gets it exactly like a local one, as long as claude there is signed
in with a claude.ai subscription and new enough to have it. Before using it,
Lectern asks the machine whether its `claude --help` lists `--remote-control`
(the answer is kept for ten minutes). An older one would refuse the flag and
the session would not start, so the default leaves it off there and an
explicit request is refused: run `claude update` on that machine.

The session's title on claude.ai is its Lectern name. Cards with Remote Control
show a small ⌁ mark by the name. Once Lectern has seen the session link on the
screen, *Open in Claude* in the ⋯ menu opens it, and `list_sessions`,
`read_session` and the session JSON carry it as `remote_control_url`.

## It stays on

Remote Control is part of the session's saved launch settings, so it carries
over:

| What happens | Result |
|---|---|
| Restore, reopen after a reboot, recovery | the same claude.ai session reconnects |
| Fork | the fork gets its own claude.ai session |
| Quick switch / handoff to a fresh session | the successor connects too, if it can |
| Account swap ([accounts](accounts.md)) | the session reconnects under the **other** account, so it appears in that account's Claude app, not the first one's |

Lectern turns it off for a continuation that cannot use it, such as a switch to
another agent or to a non-Anthropic endpoint. The continuation itself still
starts. Turning Remote Control off is done on the claude.ai side (archive or
disconnect the session), or with `/remote-control` → *Disconnect* in the
terminal.

## Requirements and why it may be unavailable

Remote Control needs Claude Code signed in with a **claude.ai subscription**
(`claude auth login`). An API key does not work. Lectern refuses it, and names
the reason, when:

- the agent is not the built-in `claude` (codex, gemini, custom agents);
- the session's environment sets `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
  `CLAUDE_CODE_OAUTH_TOKEN`, Bedrock/Vertex/Foundry,
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` or `DISABLE_GROWTHBOOK`;
- `ANTHROPIC_BASE_URL` points anywhere but `api.anthropic.com`;
- the session runs in a sandbox ([isolation](isolation.md)).

**One-time consent.** The first time Remote Control is turned on inside a
*running* session, Claude Code asks "Enable Remote Control?" once for that
login (that config directory). Lectern never answers it for you. If it shows
up, Lectern closes it and says so. Open the session's terminal, run
`/remote-control`, choose *Enable Remote Control*, and from then on Lectern can
turn it on by itself. Starting a session with Remote Control on does not ask.
Each extra account in [accounts](accounts.md) is its own config directory, with
its own consent.

## Things to know

- A Remote Control session belongs to the claude.ai account that started it.
  While it is connected its transcript is also stored by Anthropic, and the
  connection is outbound HTTPS only.
- Interactive Lectern sessions start without approval prompts by default
  (`bypassPermissions`). Anyone signed in to that claude.ai account can then
  drive the agent from any device. Protect the account, and keep
  [Trusted Devices](https://code.claude.com/docs/en/remote-control) in mind, or
  start the session with *Ask before running commands*. Claude forwards its own
  permission prompts to the phone.
- Lectern does not use `remoteControlAtStartup`: Lectern's own default and
  per-session opt-out decide, so a claude you start yourself is unaffected.
