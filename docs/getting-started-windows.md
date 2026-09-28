# Getting started on Windows

Install [Git for Windows](https://git-scm.com/download/win), which supplies Git Bash. Use Windows 10 or later with ConPTY support; WSL is optional. Lectern includes its own terminal host and helpers; tmux, Python, and ttyd are not required for local sessions.

## Install

Run in PowerShell:

```powershell
irm https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.ps1 | iex
lectern up
```

If the installer changed PATH, open a new terminal before running Lectern.

## Your first session

Run `lectern up`. It starts a private local runtime and opens your browser.
Choose an installed agent and a project folder. If you have no agent installed,
choose **Demo**: it needs no account and makes a small, reviewable file change.
Send a message, approve the demo's first edit in **Approvals**, then open
**Review & merge** to inspect it. Committing on a new branch switches that
folder to the branch, including any editor or terminal using it.

For terminal access, run `lectern` for the session list, or run `lectern claude`
(or another installed agent's name) in a project folder. In a session, press
**Ctrl+] then m** for the menu, **Ctrl+] then u** to upload a file, or
**Ctrl+] then d** to detach. The agent keeps running after detaching.
Without tmux, **Ctrl+] then e** opens links and **Ctrl+] then [** opens
scrollback (arrows or Page Up/Down; Esc returns to the agent).

## Connect your phone

Put the phone and computer on the same trusted Wi-Fi, then run `lectern phone`.
Scan the pairing QR code with the phone camera. This exposes the same runtime
and sessions on your private network until the runtime stops. Local Wi-Fi
access uses HTTP; use it only on a trusted network. If a firewall asks, allow
Lectern on your private network. Settings → Phone offers the same setup.

A phone on another network needs a reachable configured server address, such
as a Tailscale address. A loopback address (`127.0.0.1`) works only on the
computer running Lectern. Run `lectern doctor` for connection diagnostics.

## Check or stop Lectern

`lectern local status` shows the local runtime state. `lectern local stop`
stops that runtime; it preserves the database. Use `lectern up` to start it
again. See [Local runtime](local.md) for hosted-server selection and storage.
