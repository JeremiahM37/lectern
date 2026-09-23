# Terminal workspace

Attach opens an internal tab in **Terminals**, connected to the existing ttyd
WebSocket and tmux session. Session attachments, running-task terminals and
project shells all use this workspace. Switching to the board or another tab
keeps its terminal connection and output; attaching the same session reuses its
tab. Closing a tab only disconnects that view. **Pop out** opens a separate
browser tab when wanted. Open tabs restore after a page reload (inactive tabs
connect when selected), and the workspace supports links such as
`/#terminals/session/42`. ttyd still owns the PTY bridge and reconnects resolve by attachment
identity. All JavaScript, fonts, PDF rendering, and terminal add-ons ship in the
binary; clients do not contact a CDN.

- Drop files or paste screenshots to upload original bytes into the session
  directory (or the current attempt's worktree). A successful upload pastes a
  shell-quoted target path with **no Enter**. Transfer failures stay visible.
- Wheel scrolling in plain terminals retrieves retained tmux history when you
  reach the top of the browser buffer. Scrolling back to the bottom or typing
  returns to live output automatically; scrolling never activates Pause view.
  Touch swipes scroll local output or send wheel gestures to a full-screen app
  using the mouse protocol it negotiated. Full-screen chats may own their entire
  transcript; tmux history cannot recover lines the app never retained there.
- Find searches the terminal buffer. History captures up to 100,000 retained
  tmux lines (final 8 MiB cap), with search and a text download. It works for
  output generated before the browser attached, including alternate-screen CLIs.
- Pause view keeps a selectable snapshot while the live buffer continues to
  consume output. Live returns to current output. It does not stop the agent.
- Split shell starts a separate persistent tmux session in the same workspace.
  Hiding the shell closes only its browser connection. Reopening attaches to it.
- Files lists the workspace with parent-folder navigation, path insertion,
  raster-image/PDF/text previews, and byte-preserving downloads. Symlinks out of
  the workspace, nonregular files, and files over 25 MiB are refused. HTML/SVG
  render as source text. PDF.js renders pages without a browser plugin.
- Reconnect view reopens only the selected browser connection and redraws it.
  The tmux session and agent keep running. Resizing returns the live viewport to
  the current screen; Pause view remains a fixed snapshot.
- Appearance preferences (font size, spacing, theme) persist per device.
- Desktop offers an `lectern://attach/<kind>/<id>` link, a manual command,
  and the Windows setup script. `lectern attach <kind> <id>` on the control
  plane resolves the same target and invokes the same terminal command.

The desktop launcher uses the user's `lectern` SSH alias to the control plane.
It accepts only known attachment kinds and a positive numeric ID. It neither
accepts arbitrary commands/hosts in URLs nor copies control-plane SSH keys to
clients. Installation instructions are served at `/desktop/README.txt`.

SSH attachment respects the target's port and command prefix. Wrappers that
consume stdin (Windows SSH → WSL in particular) use `script -qefc` and
`/dev/tty` to relay a real Unix PTY to tmux. A plain `tmux attach` inside the
base64 pipe fails with `not a terminal`; redirecting to `/dev/tty` alone fails
with `can't use /dev/tty`. `script` is provided by util-linux on these targets.

Token mode gates terminal tokens/WebSockets as well as workspace APIs. The
terminal reads the same `lec-token` local storage value as the board. Terminal
pages and live terminal traffic bypass the PWA cache.

## Verification

`e2e/test_terminal_workspace.py` starts a real, isolated Lectern, ttyd, tmux
server and filesystem. It covers drop/paste without submission, exact upload and
download bytes, shell persistence, history search, appearance persistence,
phone layout, frozen views, simultaneous clients, the desktop CLI over a real
PTY, image/PDF rendering, failed-upload recovery, and ttyd death/reconnection.
A full-screen process also exercises phone rotation, grid reflow, rapid
reconnections and delayed callbacks from discarded WebSockets.

Go tests cover real filesystem reads, symlink/special-file/size boundaries,
attempt worktree routing, token enforcement, and SSH command quoting/wrappers.
The existing terminal proxy tests also cover service restart and two clients.
`e2e/test_terminal_tabs.py` checks switching/reuse without reconnecting, two
independent terminals, reload, mobile geometry, pop-out and non-destructive close.
`e2e/test_terminal_scroll.py` verifies wheel and real touch gestures against a
mouse-driven full-screen process, plus history from before attachment.

## Deployment verification — 2026-09-08

Deployed on AIServer; all 10 pre-existing tmux sessions survived the restart.
`verify`: **PASS: 6/6 steps passed (backend=web)**, including the full Go suite
and 68 browser tests. No error-priority service journal entries after deployment.

A portable Windows WezTerm 20240203-110809-5046fc22 trial displayed a tmux
session also attached through the deployed browser terminal. Its native
`wezterm cli get-text` returned the marker entered in the browser. Windows
PowerShell parsed the setup and launcher scripts and rejected malformed links.
The native desktop connection still requires the operator's SSH alias/key setup;
the trial did not install WezTerm or the URI handler permanently.

The desktop's WSL instance subsequently stopped and returned
`Wsl/Service/E_UNEXPECTED` to new invocations. No WSL restart or configuration
change was performed. The production local-target browser check separately
verified keyboard input and upload/path insertion. Existing remote-upload tests
remain in the suite; do not mistake a stopped WSL instance for a ttyd failure.

## Shared native and browser terminals

Opening Kitty or WezTerm keeps the browser attached to the same tmux session.
Lectern sets `window-size latest` on the attached window (never globally), so
the shared screen fits whichever client last attached, typed or resized. The
browser re-announces its size when a terminal tab is shown, when the page
becomes visible or is restored, and when the window or the terminal gains
focus. The Linux PTY only signals real size changes, so the engine steps one
row down and back, which tmux counts as a resize from that client even when
the final size is unchanged.

Lectern used `window-size smallest` until 2026-09-23. That kept a small browser
from being cropped around a larger desktop's cursor, but any forgotten client
(a sleeping phone, a hidden tab, a narrow split pane) then pinned every other
view to its size. The larger view showed a sliver of the agent's input box
padded with tmux's dots until the session was restarted. With `latest`, the
view you are using always wins, and a passive second viewer is the one that is
cropped until it is shown, focused or typed in.

Regression coverage joins a real native PTY and a real browser, changes both
terminal sizes, and checks complete, uncorrupted rows without reopening either.
The browser also refreshes its renderer on focus and page restoration. This
addresses reproduced attachment and redraw failures; it does not establish
that every possible Codex or terminal font rendering bug is gone.

The toolbar keeps files and desktop access visible; Tools contains search,
history, split shell, appearance, pause, and reconnect. Scrolling alone never
pauses the live stream. Scrolling back to the bottom or typing returns to live
output. Pause is an explicit action.

Scrolling follows the negotiated mouse protocol, not the alternate-screen flag:
tmux uses the alternate screen even for a plain shell. Without mouse reporting,
Lectern can retrieve retained tmux history. End-to-end tests run with an empty
tmux config and cover both its default alternate screen and a normal-screen
configuration, so host customizations cannot hide this behavior.

## Default terminal

Open in terminal follows the lectern:// link directly. The installed desktop
handler asks Linux's default-terminal helper or Windows's default console host
to launch SSH. It does not choose a brand from the browser's OS, or change fonts
or themes. Minimal Linux environments without a default helper use TERMINAL or
an installed terminal. Setup remains under Tools → Terminal connection setup.

Middle-click inside a terminal to start directional autoscroll. Move above or
below the marker to scroll; farther away means faster. Escape, another click,
typing, or leaving the window stops it. This also works through retained tmux
history and negotiated application mouse scrolling. A slim draggable scrollbar
remains on the terminal viewport and retained history. Opening a desktop terminal
keeps the browser terminal alive: external links can fire beforeunload without
leaving the page, so cleanup waits for an actual pagehide instead.
