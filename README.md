<div align="center">

# Lectern

**Run every coding agent you use — Claude Code, Codex, Gemini, OpenCode, Aider, Cursor and more —
on machines you own, and drive them from your terminal, your phone, or a claude.ai chat.**

![version](https://img.shields.io/badge/version-2.3.0-8b5cf6)
![license](https://img.shields.io/badge/license-MIT-blue)
![go](https://img.shields.io/badge/single%20binary-Go-00add8)
![PWA](https://img.shields.io/badge/phone-PWA-19c37d)
![MCP](https://img.shields.io/badge/MCP-server-8b5cf6)

![Lectern terminal dashboard](docs/media/terminal-dashboard.gif)

</div>

```bash
curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh | sh
lectern up          # starts Lectern, finds your agent CLIs, opens the dashboard
lectern claude      # or: lectern codex, lectern opencode, … in any folder
```

## What only Lectern does

### Start work from an ordinary chat

Brainstorm in **claude.ai** (web, desktop or phone). Then say *"start a Lectern
session that builds this"* or *"give my session fixing checkout this PDF"*. The
design you worked out arrives as context, and uploaded files arrive as the real
file. Your agent starts on your machine, and *"end that session"* archives it
when you're done. ChatGPT works the same way with Developer mode. You approve
the connection yourself, and a chat can never approve an agent's actions.
→ [Use Lectern from claude.ai and ChatGPT](docs/use-from-chat.md)

![Starting a Lectern session from claude.ai](docs/media/claude-ai-handoff.gif)

### Every session one keystroke away

Type `lectern` for a live dashboard of every agent on every machine.

- **Click** a session to jump in.
- **Right-click** it to open it in a new terminal window while the list stays put.
- Press **`b`**, tick several sessions, and open them all at once.
- Open **Projects** to get a shell in any repository instantly.

`lectern claude` in any folder starts, or reuses, a tracked session there and
drops you in. In an attached session, **`Ctrl+\`** sends a file to the agent.
→ [Terminal client](docs/terminal-client.md)

![Opening a project shell from the dashboard](docs/media/projects-shell.gif)

### Any agent, without cluttered menus

Claude Code, Codex and Gemini are built in. Settings → Agents adds OpenCode,
Aider, Goose, Amp, Cursor Agent, Copilot CLI, Qwen Code, Crush, Kimi or Cline in
one click, or any CLI as a custom agent. Only the agents you pick appear in
menus, and the rest sit under **More agents…**. Resume, fork, models, approvals,
tasks and `lectern <agent>` work with each agent as far as its CLI allows.
Lectern tells you plainly when a CLI can't do something.
→ [Agents](docs/agents.md)

![Adding an agent from the catalog](docs/media/any-agent.gif)

**Switch a live session to another agent or model** without losing the thread:

![Switching a session from Claude to Codex](docs/media/switch-agent.gif)

### Nothing gets lost

- **Recently closed** brings back a session you closed by mistake, with its
  conversation.
- Sessions stay grouped by project and machine.
- Lectern finds and adopts Claude and Codex sessions you started outside it.
- Every conversation is searchable.

![Restoring a recently closed session](docs/media/recently-closed.gif)

### Your phone is the remote

The same control loop runs in an installable phone app:

- Sessions that need you are flagged on their cards.
- Chat shows tool calls as cards, with real diffs.
- Approvals offer **allow once**, **allow `npm …` this session**, or **deny with
  feedback**, from the app or straight from the notification.
- **Voice mode** lets you talk to your agent and approve out loud, using only
  the browser's free speech engine.
- No Tailscale? **Pair a phone** with a one-time code over any HTTPS tunnel.

→ [Mobile sessions](docs/mobile-sessions.md) · [Remote access](docs/remote-access.md)

![Lectern on a phone](docs/media/phone.gif)

### More than a launcher

- **A board of tasks:** dispatch work to any machine, each task in its own git
  worktree, and review the diff or open a PR when it's done.
- **Best-of-N with a judge, and delegated builds:** a cheap worker builds and a
  lead reviews and integrates.
- **Replay evals:** find out which agent or model is best *for your repo*,
  scored against what actually shipped.
- **Agents that know about each other:** each session sees what its peers are
  editing in the same repo, plus a claim board for who's doing what.
- **Memory:** pairs with [Grimoire](https://github.com/JeremiahM37/grimoire), so
  a new session starts with the project's knowledge.
- **Budgets, usage and quota** at a glance, and checks that run when an agent
  stops.
- **Runs anywhere:** your laptop, anything reachable over SSH, a Proxmox
  container, or an ephemeral sandbox. One static binary, MIT licensed.

## Install

| | |
|---|---|
| **Linux / macOS** | `curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh \| sh` |
| **Homebrew** | `brew install JeremiahM37/tap/lectern` |
| **Windows** | `irm https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.ps1 \| iex` (client; host the server in WSL) |
| **Docker** | `docker run -d -p 127.0.0.1:9110:9110 -v lectern-data:/data ghcr.io/jeremiahm37/lectern:latest` |
| **Go** | `go install github.com/JeremiahM37/lectern/v2/cmd/lectern@latest` |

Agents run on any machine with `git`, `tmux`, `python3` and the agent's own CLI.
`lectern doctor` checks everything and prints a fix next to anything that's
wrong. You can try it with no setup at all, using fake agents:
`LECTERN_MOCK=1 lectern serve`.

## Documentation

| | |
|---|---|
| [Full guide](docs/guide.md) | Everything in depth: sessions, tasks, auth, phone alerts, delegated builds, local models |
| [Use from claude.ai / ChatGPT](docs/use-from-chat.md) | The chat connector: setup, what a chat can do, troubleshooting |
| [Terminal client](docs/terminal-client.md) | Dashboard keys, multi-window, `lectern claude`, send-file |
| [Agents](docs/agents.md) | Catalog, capabilities per agent, custom agents |
| [Mobile sessions](docs/mobile-sessions.md) | Chat cards, approvals, voice mode |
| [Remote access](docs/remote-access.md) | Phone pairing and tunnels without Tailscale |
| [Local / Docker](docs/local.md) · [Docker](docs/docker.md) | Standalone and container setups |

Lectern was called AgentDeck until v2.3. Old `AGENTDECK_*` settings still work.

MIT © Jeremiah Mackey
