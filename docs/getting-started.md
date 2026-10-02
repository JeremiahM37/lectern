# Getting started

This page walks through your first session, start to finish:

1. Install Lectern.
2. Start an agent in a project folder.
3. Watch it and approve its actions in the browser.
4. Do the same from your phone.
5. Review what it changed and commit it.

You need **git** and, for real work, an agent CLI (Claude Code, Codex, Gemini,
or another from Settings → Agents). The built-in demo agent needs neither an
install nor an account, so you can follow every step without one. Nothing else
is required: Lectern keeps your agents' terminals alive itself (a Unix PTY on
Linux and macOS, ConPTY on Windows), so tmux, Python and ttyd are not needed.

Git: your distribution's package manager on Linux, `xcode-select --install` on
macOS, [Git for Windows](https://git-scm.com/download/win) (which brings Git
Bash) on Windows 10 or later.

## 1. Install

### Linux and macOS

```sh
curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh | sh
```

It checks the download against the release's checksums and installs `lectern`
into `~/.local/bin`. If that folder is not on your PATH it asks to add it, or
prints the exact line to add. On macOS with Homebrew,
`brew install JeremiahM37/tap/lectern` does the same.

### Windows

In PowerShell:

```powershell
irm https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.ps1 | iex
```

With Scoop instead:
`scoop bucket add jeremiahm37 https://github.com/JeremiahM37/scoop-bucket && scoop install lectern`.

Agents need [Git for Windows](https://git-scm.com/download/win); the installer
says so if it is missing. Open a new terminal afterwards so the updated PATH
applies.

Inside WSL, follow the Linux steps instead. A WSL install and a Windows install
are separate: each sees only the agent CLIs installed on its own side.

**Check it:** `lectern doctor` lists what it found (git, agent CLIs) and prints
the command that fixes anything missing.

## 2. Start your first agent

In a project folder (a git repository works best):

```sh
cd ~/myapp
lectern claude        # or: lectern codex, lectern gemini
```

**What you see:** Lectern starts in the background the first time, adds a git
folder as a project, prints one line naming the new session, and then shows
the agent's own screen, exactly as if you had run `claude` yourself. Talk to
it as usual.

Press **Ctrl+]** then **d** to leave. The agent keeps running. Run
`lectern claude` in the same folder again to come back to it.

**No agent CLI yet?** Skip to step 3 and press **Try a demo agent** in the
browser. It runs a scripted stand-in that makes a small file change, so you
can try approving and reviewing.

## 3. Watch and approve from the browser

```sh
lectern up
```

**What you see:** your browser opens on Lectern, already signed in. Over SSH or
without a desktop, `lectern up` prints a sign-in link instead (it works once,
for ten minutes); `lectern up --no-browser` does the same on purpose.

The **Sessions** page lists your agent with its status: **Working**, **Needs
you**, **Idle** or **Ended**. Open it with **Chat** for a conversation view,
or **Terminal** for the same screen you saw in step 2. To start another agent
from here, press **Start an agent**, pick a folder and an agent, and press
**Start**.

Ask the agent for a small change, such as "add a line to the README". A new
install has **Ask before risky actions** on, so before the agent edits a file
or runs a command it stops and asks:

- The session card turns to **Needs you**, and **Approvals** in the sidebar
  shows how many requests are waiting.
- The request shows exactly what the agent wants to do: the command, or the
  diff of the edit.
- Choose **Allow once** to let this one action through, **Allow for this
  session** to stop asking about this kind of action until the session ends,
  or **Deny…** to refuse it. A deny opens a short note that is sent back to
  the agent, so you can tell it what to do instead.

The agent waits until you answer, wherever you answer from.

## 4. Approve from your phone

On the computer running Lectern:

```sh
lectern phone
```

(or in the browser: Settings → Connect your phone)

**What you see:** a QR code in the terminal. Scan it with your phone's camera
and the link opens Lectern on the phone, paired and signed in. The link works
once, for five minutes. If your computer's firewall asks, allow Lectern on
your private network.

The phone shows the same sessions, with a bottom bar of **Sessions**,
**Approvals**, **Tasks**, **Settings** and **More**. A waiting request looks
the same as on the desktop. On a session card you can also swipe right to
approve what it is waiting on.

Things to know:

- **Same Wi-Fi only, unencrypted.** This pairing uses your computer's address
  on the local network over plain HTTP. Only paired devices get in, but use it
  on a network you trust. `lectern phone --off` stops it; so does stopping
  Lectern.
- **Away from home** you need Tailscale (installed on the computer and the
  phone) or a relay. See [Remote access](remote-access.md) and
  [Relay](relay.md).
- **Notifications** need an HTTPS address, which Tailscale or a relay gives
  you. Then tap **Enable phone alerts** and you are told when an agent needs
  you. On an iPhone, add Lectern to the Home Screen first (Share → Add to Home
  Screen) and enable alerts from there.

## 5. Review and commit

When the agent has finished, open **Review & merge**: from the session card's
**More** menu, from the conversation view, or with **Review changes** on the
terminal page.

**Changes** shows everything the session changed compared with the base
branch, including new files. Click or tap any line to leave a comment, then
press **Send to agent**: all your comments go to the agent in one message, so
you can ask for fixes without leaving the page. After it works, each comment
shows whether the agent changed that code.

**Commit** shows the files split into **Staged** and **Changes**:

1. Stage what you want to keep (**Stage all**, per file, or per hunk). If you
   stage nothing, Commit takes every change.
2. Write a commit message, or press **✨ Write message** to have one drafted
   from the diff.
3. Press **Commit**.

If the folder is on its default branch (for example `main`), Commit offers
**Commit on a new branch** first, named after the session (such as
`lectern/fix-login`). Committing there switches that folder to the new branch,
including any editor or terminal using it. Committing straight to `main` needs
an extra tick. **Push to origin** is off unless you tick it.

The same page works on the phone. More about review, including side-by-side
diffs, conflicts and checks: [Review](review.md).

## In the terminal

`lectern` alone lists your sessions; `lectern claude` (or another installed
agent's name) in a project folder starts or reattaches one. Inside a session,
**Ctrl+] then m** opens the menu, **Ctrl+] then u** sends a file,
**Ctrl+] then e** picks a path or link the agent printed, **Ctrl+] then [**
scrolls back and **Ctrl+] then d** leaves; the agent keeps running. The same
keys work with or without tmux ([Terminal client](terminal-client.md)).

## Next

- **Check or stop it.** `lectern local status` shows the private runtime;
  `lectern local stop` stops it and keeps its database. `lectern up` starts it
  again.
- **Start with your computer.** `lectern up --service` also starts Lectern when
  you log in.
- **Something not working?** `lectern doctor` checks everything and prints the
  command that fixes each problem.
- **Updates:** `lectern update`.
- **Every command:** `lectern help`.
- **Everything else:** the [Full guide](guide.md).

## Other ways to run it

- A shared server for several people or machines, with SSH targets:
  [Run a shared server](quickstart.md).
- Docker: [Docker](docker.md).
- Building from source, and how the private local runtime works:
  [Local runtime](local.md).
