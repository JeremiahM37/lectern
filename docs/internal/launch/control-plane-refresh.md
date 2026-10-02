# Lectern: control plane refresh

## Announcement draft

Lectern is a self-hosted control plane for coding agents.

Run Claude Code, Codex, Gemini CLI or another configured agent on your own
workstation, over SSH, or in a disposable Proxmox/Docker sandbox. Choose the
machine through the project, queue tasks in separate worktrees, and come back
to the same work from your terminal, browser or phone.

The refreshed walkthrough follows that whole loop: machines → dispatch →
review, with phone approvals alongside it. File previews and uploads make
working across those machines more comfortable, too.

Try it: https://github.com/JeremiahM37/lectern

## Direction, not release claims

Resource-aware automatic placement, a combined worker/resource overview and a
stable worker protocol are possible next steps. They are not part of this
announcement's shipped-feature claims. First validate the placement decisions
people need and the metrics their machines can reliably report.

## Release note draft

- Persistent native terminal controls without tmux, including menu, file upload,
  path opening and safe detach.
- Clearer setup: installed-agent choices, stopped-session recovery, an approval
  in the demo, phone pairing and OS-specific getting-started guides.
- Full desktop sidebar; compact navigation remains available on phones.
- Refreshed README, current screenshots and a workflow video showing Lectern's
  role across machines and agents.

This is draft copy for a release containing these changes, not a declaration
that a new version has been published.
