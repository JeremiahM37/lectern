# Round-two new-user audit

Candidate: `codex/simple-round-two` (base `69dc2e53`). This audit covers the
implemented candidate on Linux; it is not a report about the deployed server.

| New-user task | Candidate behavior | Evidence |
| --- | --- | --- |
| Start an agent and send a message | Installed agent or Demo; unavailable executable rejected before workspace/session allocation | `test_simple_walkthroughs.py`, `test_pty_backend.py`, launch preflight tests |
| Understand current activity | Stopped + Revive for an exited process; live demo lifecycle events; descriptive session names | restore-gap, PTY and session status tests |
| Approve an action | Demo asks before the first write; subsequent writes do not repeat that introduction; late requests after exit are denied | real PTY demo and permission-hook tests |
| Leave and return | Persistent no-tmux footer and Ctrl+] d; menu, upload, links and scrollback stay on the client; End offers Undo/Restore | portable attachment PTY test, terminal dashboard and walkthrough tests |
| Start work in another folder | Folder picker remains available; simpler cards and four primary navigation items | desktop/phone walkthroughs and mobile session tests |
| Review and commit | Branch-switch warning; automatic diff-based draft; user edits preserved; empty terminal message rejected at that field | commit walkthroughs and console tests |
| Connect a phone | Same runtime/DB, one-time pairing, authenticated existing sessions and approvals; CLI QR route | localruntime phone integration, phone CLI and browser pairing tests |

## Important distinctions

- Changes is the diff against the base branch, including already committed
  work. A successful commit onto a new branch can legitimately leave its count
  unchanged; the panel reloads after Git actions.
- Local Wi-Fi is HTTP on a private interface and says so. Tailscale HTTPS
  remains the remote/secure alternative. The Wi-Fi listener lasts until the
  runtime stops and uses the existing paired-device authentication.
- Windows/macOS binaries compile. Native runtime parity on those operating
  systems is not established by the Linux suite. Keyboard link access is
  implemented without tmux; no new mouse gesture parity claim is made.

Run `verify run .verify-candidate.yaml` for the candidate gate. The production
`.verify.yaml` applies after deployment. Logs and the deployment boundary are
in [round-two-status.md](round-two-status.md).
