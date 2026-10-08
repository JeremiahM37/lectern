# Paste a screenshot into an agent session, from any device

Agents (Claude Code, Codex) run on the Lectern host, usually a machine with no
screen. When you press Ctrl+V in one, it looks for an image on the host's
clipboard and finds nothing. Lectern gives every session a clipboard that
holds what you copied on the device you are driving it from.

There are three paths. They add up; none replaces another.

| Path | What it covers | When the content arrives |
|---|---|---|
| **Shims** | Programs that run `wl-paste` or `xclip`: Claude Code. | On demand, at the moment of the paste |
| **Headless clipboard** | Programs that read the clipboard natively through X11: Codex (arboard), toolkits, `xclip` itself. | Already there when they read: mirrored when you copy |
| **Upload and inject** | The web terminal and the phone, where the browser itself intercepts the paste. | The file's path is typed into the prompt, which Claude Code attaches |

## What runs where

* **Shims.** Every agent session Lectern launches gets `wl-paste` and `xclip`
  first on its `PATH` (`<state dir>/lectern-clipboard/bin`, written by
  `lectern clipboard ensure`). They understand exactly the read forms the
  agents use: list the types (`wl-paste -l`, `xclip -selection clipboard -t
  TARGETS -o`), read `image/png|jpeg|gif|webp|bmp` or `text/plain`. They ask
  the Lectern server for the clipboard of the client driving *that* session.
  Anything else (a copy, `--watch`, the primary selection, an unknown type) is
  passed to the real tool when the machine has a display, and otherwise does
  nothing (a copy is swallowed; a read fails like an empty clipboard).
* **Headless clipboard.** Each machine that runs sessions gets a private
  Xvfb and a clipboard owner (`lectern clipboard daemon`), started by the
  first session launch and found again by every later one. Sessions get
  `DISPLAY` and `XAUTHORITY` for it. Wayland-only programs are not covered;
  Codex and every toolkit fall back to X11 when only `DISPLAY` is set.
  Needs `Xvfb` on that machine (`apt install xvfb`); without it the shims still
  work.
* **Mirroring.** An attached `lectern` client watches its computer's clipboard
  and copies **images** to the session's machine as they appear (on attach, and
  within two seconds of a copy). Text is **not** mirrored unless you ask:
  `lectern clipboard serve --text` or `LECTERN_CLIPBOARD_MIRROR_TEXT=1`,
  because copying your text clipboard to a server is a privacy choice. Turn the
  whole thing off with `LECTERN_CLIPBOARD_BRIDGE=0`, or only the mirror with
  `LECTERN_CLIPBOARD_MIRROR=0`. Mirrored content expires after 10 minutes.
* **Every target.** A session on an SSH target or container gets the same
  thing, provisioned through that target's own `lectern` binary (the same way
  its other helpers run). A target with no `lectern` binary, or no Xvfb, gets
  what it can: nothing without the binary, only the shims without Xvfb. Disposable
  sandbox tasks have none.

## Your devices

* **`lectern` on Linux, macOS, Windows** (`lectern claude`, `lectern attach`, the
  dashboard): registers while attached. Reads with `wl-paste`/`xclip` (Linux),
  `pbpaste`/`osascript` (macOS), PowerShell `Get-Clipboard -Format Image`
  (Windows). A computer with no clipboard tool does not register, so it is
  never the device that gets asked.
* **Web and PWA terminal:** a browser may not read the clipboard without a
  gesture, but it does receive your paste. The page uploads the pasted image,
  types its quoted path into the prompt (Claude Code attaches an image path),
  and also mirrors it to the headless clipboard. As a clipboard client it
  answers "unavailable" at once, so a shim never waits for it.
* **Installed web app (Chrome on Android or desktop):** it is a share target.
  Share an image to "lectern" from any app; `/share.html` lists your running
  sessions, and the one you tap gets the image uploaded, mirrored to that
  machine's clipboard and its path typed to the agent, exactly like a paste.
  (A shared link or text is typed as text.) The held item expires after 10
  minutes. Pasting inside the web app works as described above.
* **Android app:** pasting an image in the terminal or the composer, and the
  system Share sheet ("Lectern" -> choose a session), use the same
  upload-and-inject path. While the app is in the foreground it also answers
  the server's clipboard requests from the real clipboard. See
  [Android](android.md#clipboard-and-screenshots).
* **Plain SSH** (you are not attached through Lectern): run
  `lectern clipboard serve --session ID` on the computer you are sitting at,
  in a second terminal. It cannot see you type in another program, so it is an
  explicit bridge and answers while it runs. Without it, `lectern upload` or
  Ctrl+\ in an attachment sends a file, and you paste its path.

## Sessions that were already running

On start the server adds `DISPLAY`, `XAUTHORITY` and the shim directory to the
tmux environment of every running session, so new windows and panes (Ctrl+]
splits, shells) have them. **A process that is already running keeps the
environment it started with: restart the agent (resume it) to pick the
clipboard up.** Everything launched after the upgrade has it from the start.

## Security model

What is being protected: the contents of a person's clipboard (passwords,
tokens, private images), on their devices, from a process on the Lectern host
that should not be able to read it - an agent, its tools, another session.

**Who may do what.**

| Action | Who | How it is checked |
|---|---|---|
| Ask for a clipboard (`POST /api/hook/session/{id}/clipboard`) | The agent of session `{id}`, only | That session's own hook token (`LECTERN_HOOK_TOKEN`, random per launch, held in a private file, never on a command line). Another session's token, no token, or a made-up one gets 401. There is no route on the general API to ask. |
| Register as a clipboard, report activity, answer a request, mirror | A signed-in person: Tailscale identity, access token, or paired device | Same gate as approving a request (`CanDecide`). A host process is `KindLocal` and not human under Tailscale identity, so it gets 403 - an agent cannot register itself as "the user's clipboard" or answer for one. |
| Answer a specific request | The client that was asked, with the login that registered it | The request id is unguessable, bound to client id and login. |

**Which client is asked.** Only the clients attached to the asking session; if
none, a session-less client (the Android app). Never a client attached to a
different session, so an agent in session B cannot read what you copied while
driving session A. Among eligible clients, the one that most recently typed.

**When.** A client is asked only if its person typed in it within 20 seconds
(a CLI bridge started on purpose with `clipboard serve` counts as always
typing). So an agent cannot poll your clipboard while you are away from it: it
gets an answer only while you are at the keyboard of a client attached to its
session - that is, in the window in which you are plausibly pressing Ctrl+V.
It also cannot ask for anything but images and plain text, at most 30 times a
minute per session, and an answer is cut off after 3 seconds and 20 MB.

**Audit.** Every request writes a `clipboard request` log line: session,
operation, type, client, kind, login, bytes, milliseconds, outcome. Every mirror
writes `clipboard mirror`.

**The mirrored (headless) clipboard.** It holds only what your signed-in
clients sent, images by default. It lives in an X server that listens on no
network port (`-nolisten tcp`) and requires an Xauthority cookie that is in a
mode-0600 file inside a mode-0700 directory. So it is readable by processes
that run as the same OS user as the sessions, and by no other user on the
machine. All of one person's sessions on a machine share it: a session can
read what you last copied, even while you are driving another. That is the
trade for programs (Codex) that read it without asking; it is why the item
expires after 10 minutes, why text is opt-in, and why the on-demand path (which
does enforce per-session routing and recent typing) remains for Claude Code.
Lectern runs agents as the person who owns the session, so "the person's own
processes" is the boundary Lectern already has everywhere (it is also the
boundary of their `~/.ssh`). If you run untrusted agents under the same OS
user, do not enable mirroring (`LECTERN_CLIPBOARD_MIRROR=0` on your clients)
and rely on the on-demand path.

**What is not covered.** With `LECTERN_AUTH=none` (single-user localhost)
everyone who reaches the port is the operator, as everywhere else in Lectern.
Anything that can read the hook token of a session (that session's own
processes) can ask for its clipboard while you are typing in it - which is the
point of the feature.
