# README demo GIFs

All captures use the disposable demo instance (fake projects `acme-api`,
`storefront-web`, `mobile-app`, `infra`; scripted stand-in `claude`/`codex`/
`gemini` binaries that print a believable session and answer typed input with
a short canned reply — no real model calls, no real credentials, no personal
data). Recorded on the homelab's "agent desk" (Xvfb + openbox), terminal shots
via `ffmpeg -f x11grab` driven by `xdotool`, web-UI shots via `playwright-core`
headless Chromium with `recordVideo`, then converted to GIF with
`ffmpeg` (`palettegen`/`paletteuse`) and optimized with
`gifsicle -O3 --lossy=60 --colors 128`.

| File | Size | Dimensions | Duration | Shows |
|---|---|---|---|---|
| `terminal-dashboard.gif` | 612 KB | 1100x667 | 10.8s | The `lectern` terminal dashboard: arrowing through the session list updates the live preview pane; right-clicking a session opens a new terminal window attached to it while the dashboard stays open; pressing `b` enters multi-select, clicking two more sessions and pressing Enter opens both in new windows. |
| `projects-shell.gif` | 279 KB | 1100x667 | 11.0s | The dashboard's Projects section (key `4`): selecting a project and pressing Enter opens a persistent shell in its repository (`git log --oneline` runs for real against the demo's `infra` git repo); `Ctrl-b d` detaches back to the project list with "Detached. Session keeps running." |
| `any-agent.gif` | 1.2 MB | 1100x688 | 17.9s | Web UI, Settings → Agents: the starter-template catalog (OpenCode, Aider, Goose, Cursor, …) fills in a runner's fields with one click; saving adds it to Agent runners; toggling "Show in menus" moves it out of "Hidden from menus"; the New Session picker's Agent dropdown then offers it. A ~14s wait while the demo's fake CLI is probed for real (it doesn't answer fast) was cut with a jump cut; nothing was faked. |
| `phone.gif` | 705 KB | 390x844 | 14.5s | Mobile-emulated web UI: the Sessions home screen with "NEEDS YOU" badges, opening a session's Chat, typing a message, sending it, and watching the stand-in agent's scripted reply stream in, then returning to the session list where the card now shows "working" live. |

A small red dot follows the mouse in the web-UI recordings (`any-agent.gif`,
`phone.gif`) so clicks are easy to follow; it's injected via `addInitScript`
and never part of the real UI.

## Shots that were planned but skipped (not faked)

Two shots from the original list depend on behavior the demo's scripted
stand-in agents cannot produce, so they were skipped rather than faked:

- **`recently-closed.gif`** — Ending a session and reopening it via
  "Recently closed" routes entirely through **"Choose history" → resume a
  saved conversation**, which reads the real Claude/Codex CLI's own on-disk
  conversation history. The stand-in binaries never write that history, so
  the dialog always reports "No saved conversations found in this workspace."
  The underlying Lectern feature is real; the fixture just can't produce the
  history file it depends on.
- **`switch-agent.gif`** — Switching a session's agent (e.g. Claude → Codex)
  asks the *current* agent to write a structured handoff before starting the
  new one. The stand-ins don't understand that handoff protocol, so the
  "Switching…" progress panel never advances past "Saving context" — it was
  observed stuck for 4+ minutes before the server times it out and reports
  "Handoff failed: the agent did not write a handoff within 4m0s." Also real
  behavior, just not something a snappy demo GIF could show honestly with
  this fixture.

## Reproducing

The desk's demo instance runs as `lectern-demo` (systemd) on
`127.0.0.1:9500` inside the agent-desk LXC, DB at
`/home/agent/demo/data/lectern.db`. After any experimentation, reset with:

```sh
systemctl stop lectern-demo
rm -f /home/agent/demo/data/lectern.db*
systemctl start lectern-demo
python3 /home/agent/demo/seed_demo.py
```

If re-seeding hits `tmux launch failed: duplicate session: lec-sN` (leftover
tmux sessions from a previous run holding the name), end them through the
sessions themselves rather than `tmux kill-session`/`kill-server` — e.g.
`tmux send-keys -t lec-sN C-c` to interrupt the stand-in's foreground prompt
(and, for a plain shell session, `tmux send-keys -t lec-sN 'exit' Enter`) —
which lets tmux close the session on its own once nothing is left running
inside it.
