<div align="center">

# Lectern

**Run every coding agent you use — Claude Code, Codex, Gemini, OpenCode, Aider, Cursor and more —
on machines you own, and drive them from your terminal, your phone, or a claude.ai chat.**

![version](https://img.shields.io/badge/version-2.6.2-8b5cf6)
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

## What makes Lectern different

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

**Click any file path or link an agent prints** — even one wrapped across
lines or outside the project — and it opens. In `lectern attach`, double-click
opens PDFs in your PDF viewer and links in your browser, on your own machine
even when the session runs on a server; right-click to download it or send the
path back to the agent; **`Ctrl+] e`** labels every path on screen so you can
open one from the keyboard. In the browser terminal and the chat, paths are
underlined on hover and open in Lectern's viewer (read-only outside the
project).
→ [Paths and links](docs/terminal-client.md#paths-and-links-the-agent-prints)

![Opening a project shell from the dashboard](docs/media/projects-shell.gif)

### Any agent, without cluttered menus

Claude Code, Codex and Gemini are built in. Settings → Agents adds 30 more from a
searchable catalog in one click — OpenCode, Cursor, Copilot CLI, Grok, Amp,
Antigravity, Qwen Code, Kimi, Goose, Aider, Droid, Kiro, Devin, Pi and the rest —
or any CLI as a custom agent. Only the agents you pick appear in
menus, and the rest sit under **More agents…**. Resume, fork, models, approvals,
tasks and `lectern <agent>` work with each agent as far as its CLI allows.
Lectern tells you plainly when a CLI can't do something.
→ [Agents](docs/agents.md)

![Adding an agent from the catalog](docs/media/any-agent.gif)

**Switch a live session to another agent or model** without losing the thread:

![Switching a session from Claude to Codex](docs/media/switch-agent.gif)

### Nothing gets lost

- **Restore** brings back anything that ended: closed, archived, crashed, or
  cut off by a reboot, with its exact conversation when one was saved — for
  Claude, Codex and the catalog agents that name or list their sessions
  (OpenCode, Qwen Code, Goose, Pi and more; see docs/agents.md). Ending a
  session offers **Undo**, an agent that exits shows **Revive**, and
  `lectern restore` does the same from any terminal.
- Sessions stay grouped by project and machine.
- Lectern finds and adopts Claude and Codex sessions you started outside it.
- Every saved Claude and Codex conversation on your machines is searchable,
  including ones Lectern never launched.

![Restoring a session: ending one offers Undo, and Restore brings its conversation back](docs/media/recently-closed.gif)

### Your phone is the remote

The same control loop runs in an installable phone app:

- Sessions that need you are flagged on their cards.
- Chat shows tool calls as cards, with real diffs.
- Approvals offer **allow once**, **allow `npm …` this session**, or **deny with
  feedback**, from the app or straight from the notification.
- **Voice mode** lets you talk to your agent and approve out loud, using only
  the browser's free speech engine.
- No Tailscale? Run **`lectern relay`** on any small server and pair a phone
  by QR code. Traffic is **end-to-end encrypted** (Noise, the protocol behind
  WireGuard); the relay only passes sealed frames and never serves app code.
- An **Android app prototype** bundles the same web app:
  [download the signed 0.2.1 APK](https://github.com/JeremiahM37/lectern/releases/download/v2.6.0/lectern-android-0.2.1.apk) or
  [build it from source](docs/android.md). It
  keeps the relay key in Keystore, and takes Approve / Deny / Reply from the
  notification with no Lectern screen open, via UnifiedPush and ntfy.

→ [Mobile sessions](docs/mobile-sessions.md) · [Relay](docs/relay.md) · [Remote access](docs/remote-access.md) · [Android app](docs/android.md)

![Lectern on a phone](docs/media/phone.gif)

### More than a launcher

- **A board of tasks:** dispatch work to any machine, each task in its own git
  worktree, and review the diff or open a PR when it's done.
- **CI loop:** when a PR's checks fail, Lectern sends the failing jobs and a
  trimmed log back to the agent that wrote the code, up to 3 tries, and pings
  your phone when it goes green. → [CI loop](docs/ci-loop.md)
- **Usage limits:** when Claude, Codex or Gemini hits its limit, the card and
  your phone show when it resets. Lectern can resume the same agent after the
  reset, hand the work to another agent in the same workspace, or (opt-in)
  continue the same conversation under another of your signed-in accounts of
  that CLI. → [Rate limits](docs/rate-limits.md), [Accounts](docs/accounts.md)
- **Best-of-N with a judge, and delegated builds:** a cheap worker builds and a
  lead reviews and integrates.
- **A browser beside every session:** watch the agent drive a real browser on
  its machine and take over at any time, or click any element of your dev
  server to send its HTML, CSS and a cropped screenshot to the agent.
  → [Browser](docs/browser.md)
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
- **Scales:** one Lectern process ran 250 concurrent sessions across 4
  targets with 65 MB of memory; approvals round-trip in about 7 ms.
  → [Benchmark](docs/benchmarks/scale.md)

## Install

| | |
|---|---|
| **Linux / macOS** | `curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh \| sh` |
| **Homebrew** | `brew install JeremiahM37/tap/lectern` |
| **Windows** | `irm https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.ps1 \| iex` (needs [Git for Windows](https://git-scm.com/download/win)) |
| **Docker** | `docker run -d -p 127.0.0.1:9110:9110 -e LECTERN_INSECURE_LISTEN=1 -v lectern-data:/data ghcr.io/jeremiahm37/lectern:latest` |
| **Go** | `go install github.com/JeremiahM37/lectern/v2/cmd/lectern@latest` |

On the machine that runs Lectern, agents need only `git` and the agent's own
CLI: Lectern keeps their terminals alive itself, on Linux, macOS and Windows
([how](docs/ptyhost.md)). Other machines reached over SSH need the `lectern`
binary installed, or `tmux` and `python3`. The web terminal is built in too;
nothing else to install.
`lectern doctor` checks everything and prints a fix next to anything that's
wrong, and `lectern update` installs a new release. You can try it with no
setup at all, using fake agents: `LECTERN_MOCK=1 lectern serve` (it listens on
127.0.0.1 only).

## Documentation

| | |
|---|---|
| [Getting started](docs/getting-started.md) | Install, start, first agent: three steps on Linux, macOS and Windows |
| [Full guide](docs/guide.md) | Everything in depth: sessions, tasks, auth, phone alerts, delegated builds, local models |
| [Use from claude.ai / ChatGPT](docs/use-from-chat.md) | The chat connector: setup, what a chat can do, troubleshooting |
| [Terminal client](docs/terminal-client.md) | Dashboard keys, multi-window, `lectern claude`, send-file |
| [Agents](docs/agents.md) | Catalog, capabilities per agent, custom agents |
| [Mobile sessions](docs/mobile-sessions.md) | Chat cards, approvals, voice mode |
| [Browser](docs/browser.md) | Browser pane, Design Mode, agent browser tools, computer use |
| [Remote access](docs/remote-access.md) | Phone pairing and tunnels without Tailscale |
| [Local runtime](docs/local.md) · [Shared server](docs/quickstart.md) · [Docker](docs/docker.md) | How the private runtime works, building from source, a server for several machines, containers |

Lectern was called AgentDeck until v2.3. Old `AGENTDECK_*` settings still work.

MIT © Jeremiah Mackey
