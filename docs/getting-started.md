# Getting started

Three steps on any computer. You need **git** and at least one agent CLI
(Claude Code, Codex, Gemini, or another from Settings → Agents). Nothing else:
Lectern keeps your agents' terminals alive itself, so tmux, Python and ttyd are
not needed.

## Linux and macOS

1. **Install.**

   ```sh
   curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh | sh
   ```

   It checks the download against the release's checksums and installs
   `lectern` into `~/.local/bin`. If that folder is not on your PATH it asks
   to add it, or prints the exact line to add. With Homebrew,
   `brew install JeremiahM37/tap/lectern` does the same.

2. **Start it.** In a project folder:

   ```sh
   cd ~/myapp
   lectern up
   ```

   Your browser opens, signed in, on **Start an agent**, with this folder
   already chosen as the project. Over SSH or without a desktop, `lectern up`
   prints a sign-in link instead (it works once, for ten minutes).

3. **Start an agent.** Press **Start** in the browser, or stay in the
   terminal:

   ```sh
   lectern claude        # or: lectern codex, lectern gemini
   ```

   No agent installed yet? **Try a demo agent** in the browser needs nothing
   installed and shows chat, approvals and review.

## Windows

1. **Install.** In PowerShell:

   ```powershell
   irm https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.ps1 | iex
   ```

   Agents need [Git for Windows](https://git-scm.com/download/win); the
   installer says so if it is missing. Open a new terminal afterwards so the
   updated PATH applies.

2. **Start it:** `cd` into a project folder and run `lectern up`.

3. **Start an agent:** press **Start** in the browser, or run `lectern claude`.

Inside WSL, follow the Linux steps instead. A WSL install and a Windows install
are separate: each sees only the agent CLIs installed on its own side.

## Next

- **Your phone.** On the same Wi-Fi, run `lectern phone` (or Settings →
  Connect your phone) and scan the QR code. Away from home, use Tailscale or a
  relay; see [Remote access](remote-access.md) and [Relay](relay.md).
- **Something not working?** `lectern doctor` checks everything and prints the
  command that fixes each problem.
- **Updates:** `lectern update`.
- **Every command:** `lectern help`.

## Other ways to run it

- A shared server for several people or machines, with SSH targets:
  [Run a shared server](quickstart.md).
- Docker: [Docker](docker.md).
- Building from source, and how the private local runtime works:
  [Local runtime](local.md).
