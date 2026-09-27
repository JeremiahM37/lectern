# README demo GIFs

All captures use the disposable demo instance (fake projects `acme-api`,
`storefront-web`, `mobile-app`, `infra`; scripted stand-in `claude`/`codex`/
`gemini` binaries that print a believable session and answer typed input with
a short canned reply — no real model calls, no real credentials, no personal
data). The `claude` stand-in registers itself like real Claude Code
(`~/.claude/sessions/<pid>.json`) and writes a real Claude-format JSONL
transcript, so Lectern's own history/handoff/resume code paths run for real
against it — that's what makes `recently-closed.gif` and `switch-agent.gif`
possible below. Recorded on the homelab's "agent desk" (Xvfb + openbox),
terminal shots via `ffmpeg -f x11grab` driven by `xdotool`, web-UI shots via
`playwright-core` headless Chromium with `recordVideo`, then converted to GIF
with `ffmpeg` (`palettegen`/`paletteuse`) and optimized with
`gifsicle -O3 --lossy=60 --colors 128`.

| File | Size | Dimensions | Duration | Shows |
|---|---|---|---|---|
| `terminal-dashboard.gif` | 655 KB | 1100x667 | 11.0s | The `lectern` terminal dashboard (footer hints include `C restore`): arrowing through the session list updates the live preview pane; right-clicking a session opens a new terminal window attached to it while the dashboard stays open; pressing `b` enters multi-select, clicking two more sessions and pressing Enter opens both in new windows. |
| `projects-shell.gif` | 279 KB | 1100x667 | 11.0s | The dashboard's Projects section (key `4`): selecting a project and pressing Enter opens a persistent shell in its repository (`git log --oneline` runs for real against the demo's `infra` git repo); `Ctrl-b d` detaches back to the project list with "Detached. Session keeps running." |
| `recently-closed.gif` | 1.2 MB | 1100x688 | 14.3s | Web UI, Sessions: ending a `claude` session via **More → End** raises an **Undo** toast; ending a second one stacks another; **↺ Restore** opens the restore list grouped by project (`storefront-web`, `mobile-app`) with each row's last message; typing `checkout` into its search narrows it to one row, and **Resume** reopens the session in a live terminal with its real conversation replayed (`> Let shoppers apply a coupon...` / `Read(src/checkout/Cart.tsx)` / `(resumed conversation …)`). |
| `switch-agent.gif` | 1.2 MB | 1100x773 | 14.9s | Web UI: opening a `claude` session's **Switch** dialog, picking Codex's default model, and watching the handoff run (Saving context → Starting Codex → Ready) into a live embedded terminal for the new Codex session — a "Switched. The original session is still available in Sessions." toast confirms it, and the tab shows the `← Push notification…  Clau…` link back to the original. The embedded terminal renders real UTF-8 (✻, ●, ↳, ✓, the box-drawing corners, the em dash in "Done — changes") correctly. A ~14s wait while the dialog probes each agent's real model list was cut with a jump cut. |
| `any-agent.gif` | 1.3 MB | 1100x688 | 17.8s | Web UI, Settings → Agents: the starter-template catalog (OpenCode, Aider, Goose, Cursor, …) fills in a runner's fields with one click; saving adds it to Agent runners; toggling "Show in menus" moves it out of "Hidden from menus"; the New Session picker's Agent dropdown then offers it. A ~14s wait while the demo's fake CLI is probed for real (it doesn't answer fast) was cut with a jump cut; nothing was faked. |
| `phone.gif` | 800 KB | 390x844 | 17.0s | Mobile-emulated web UI: opening a `claude` session's Chat, which now renders real structured tool **cards** (🔍 Search, ✏️ file edit, ⌘ Terminal) instead of raw terminal text, typing a message, sending it, watching the stand-in's reply stream in, then returning to the session list where the card shows "working" live. |

A small red dot follows the mouse in the web-UI recordings so clicks are easy
to follow; it's injected via `addInitScript` and never part of the real UI.

## Previously skipped, now recorded

`recently-closed.gif` and `switch-agent.gif` were originally skipped because
the stand-in agents couldn't produce the real CLI behavior (on-disk
conversation history, a written handoff) those features read. The stand-ins
were upgraded to actually register sessions, write real transcripts, and
answer a handoff request, so both now record honestly against the real code
paths — nothing here is faked or scripted around. `switch-agent.gif` also
needed `ttyd` installed on the desk (`/usr/local/bin/ttyd`, on the
`lectern-demo` service's `PATH`) — without it, the new session Switch opens
failed to attach a live terminal and surfaced `ApiError: ttyd is not
installed on the control plane` toasts. It then needed `LANG=C.UTF-8
LC_ALL=C.UTF-8` added to `lectern-demo.service`'s `Environment=` — without a
UTF-8 locale, tmux (and the ttyd client attached to it) substituted every
non-ASCII byte with `_`, turning `●`/`↳`/the box corners/the em dash into
underscores. Both are fixed now: the new Codex session opens cleanly with the
real glyphs intact and no error toasts anywhere in the clip (checked across
5+ frames spanning the whole recording). Getting the locale fix to actually
take required a full tmux server restart — the running tmux server keeps the
environment it was started with, so a locale change to the service doesn't
reach already-open sessions until every pane exits and the server itself
exits, letting the next session spawn a fresh server under the new env.

## Reproducing

The desk's demo instance runs as `lectern-demo` (systemd) on
`127.0.0.1:9500` inside the agent-desk LXC, DB at
`/home/agent/demo/data/lectern.db`. Since the `claude` stand-in also writes
into `~/.claude/sessions/` and `~/.claude/projects/` (so real history/resume
code runs against it), a full reset needs those cleared too:

```sh
systemctl stop lectern-demo
rm -f /home/agent/demo/data/lectern.db*
rm -rf /home/agent/.claude/sessions /home/agent/.claude/projects
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

## `trackers/` — Tasks hub screenshots

Still screenshots of the Tasks hub ([trackers.md](../trackers.md)) at 1440x900
and 390x844 (2x): demo mode's scripted GitHub repository (`mock/repo`), plus a
Linear, a Jira and an Azure DevOps connection pointed at a small local stand-in API serving
made-up issues. Captured with headless Chromium through Playwright; nothing is
edited.
