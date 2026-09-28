# Simplicity round two: deployment status

Implementation is in `codex/simple-round-two`, based on `69dc2e53`, in
`/mnt/bulk/lectern-simple-round-two`. Implementation commit `da571086` and native-test follow-up `e22c4bc7` are pushed
to main. The implementation was deployed on 2026-09-28.
The old `/home/admin/projects/lectern` checkout was not changed.

## Implemented

- Built-in terminal attachment has a persistent footer, approval indicator,
  Ctrl+] menu/upload/detach, keyboard links and scrollback. Ctrl+backslash opens
  upload rather than sending SIGQUIT. Agent launch secrets are sourced from a
  private temporary file, cleaned after exit, rather than printed in a shell
  command after a crash.
- Launch preflight rejects unavailable executables before allocating session
  records or scratch folders. Revive checks before ending the old process.
  Exited agents show Stopped and Revive; pending approvals expire, including
  late-hook races. The demo requests its first write and emits lifecycle events.
- Phone setup adds a same-network listener to the existing runtime and database.
  Pairing and approval access use the same authentication paths. `lectern phone`
  enables access and prints a single-use QR link. The listener enforces Host and
  Origin checks and excludes local sign-in/shutdown endpoints.
- Web launch picks installed agents or Demo; captured profile command/PATH
  overrides are validated by the server. Sessions receive descriptive names.
  Four primary navigation items, folded card actions, conditional filters and
  Restore tools reduce the initial screen. Approval conversation blanks are trimmed.
- Commit warns about switching the current folder's branch, drafts its message
  from the diff without overwriting user edits, and validates the terminal message
  at its own field. Project previews show readable fields.
- Bare noninteractive invocation shows status. Default server databases use the
  state directory; terminal controls give a useful noninteractive explanation.
  Doctor distinguishes the session browser from the desktop browser opener.
- Linux/macOS/Windows getting-started pages describe the built-in terminal and
  phone flow; Windows Git Bash path and smoke-build fixes are integrated.

## Verification boundary

Run `verify run .verify-candidate.yaml` with Go on PATH. This runs the isolated
Go, frontend and real-browser suites, checks staged assets against a fresh build,
then runs go vet and Windows amd64/macOS arm64 cross-builds. It deliberately does
not use the running production binary as evidence for candidate changes.

The production `.verify.yaml` remains the deployment gate, with visual expectations
updated for the four-item navigation and Chat/Terminal actions. The production restart preserved all 8 session records and all 10 pane process
identities. Native Windows/macOS CI now passes; see the deployment receipt below. Keyboard link actions are covered; no new
mouse link gesture parity claim is made.

The review's Changes count includes committed changes relative to the base branch;
committing on a new branch does not make that review diff empty.

Detailed development history and earlier failed attempts are recorded in
[round-two-progress.md](round-two-progress.md). Final verification output is at
`/mnt/bulk/lectern-round-two-verify-candidate-2.log`.

## Final candidate result (2026-09-28)

`verify run .verify-candidate.yaml` returned **PASS: 3/3 steps passed (backend=web)**.
All three isolated suites passed: frontend 34s, Go 124s, browser E2E 196s.
Static analysis and Windows amd64/macOS arm64 compilation passed. `git diff
--check` is clean. Full suite logs are saved as
`/mnt/bulk/lectern-round-two-final-{go,frontend,e2e}.log`.

This was the pre-deployment candidate result; the deployment result follows.

## Deployment and native verification (2026-09-28)

Full production verification: **PASS: 7/7 steps passed (backend=web)**, including
workshop regressions, Go/frontend/browser suites and actual live UI checks.
The restart preserved all eight session records and ten exact pane processes.
Database backup passed SQLite integrity checking; off-box AIServer snapshot
`e366c129` and the previous immutable binary are available for rollback.
Local deployment evidence is in `/mnt/bulk/lectern-round-two-deploy/`.

[Native CI run](https://github.com/JeremiahM37/lectern/actions/runs/36474792928):
Windows and macOS jobs passed real PTY session/restart, web terminal,
same-runtime phone pairing/approval, and portable no-tmux attachment tests.
The latter covers menu, upload, links, scrollback and detach. The initial macOS
failure was the new test incorrectly requiring a Linux namespace; the follow-up
uses the existing platform-aware test gate. No skip was added.
This verifies disposable native runners, not physical-device GUI or Wi-Fi reachability.
