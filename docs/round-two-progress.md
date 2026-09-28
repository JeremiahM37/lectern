# Simplicity round two — implementation in progress

Candidate: `/mnt/bulk/lectern-simple-round-two`, branch `codex/simple-round-two`,
based on `69dc2e53`. The historical `/home/admin/projects/agentdeck` alias points
to an older checkout; do not merge or deploy from it. Production is unchanged.

Scope: `/mnt/bulk/ux-audit/after/reaudit.md` and the original audit beside it.

Implemented in this candidate (not yet fully verified):

- Portable native attachment footer, controls menu, upload, detach and pending
  approval status; Ctrl-backslash is consumed instead of killing the agent.
- Private launch environment files keep hook credentials out of shell crash
  diagnostics. Cleanup after exit; isolation binds the private file.
- Target-side executable preflight; server-generated descriptive session names.
- Approval expiry on lifecycle cleanup; Stopped label for exited agents; late
  permission requests refused/expired. The latest race fix needs tests.
- Same-runtime private-network listener, phone pairing cookie support, QR CLI,
  and Settings button. Does not start another DB or replace live sessions.
- Demo Go helper requests approval before first write.
- Four primary navigation items, Restore grouping, less card clutter, trim
  trailing conversation whitespace, explicit branch-switch warning.
- OS getting-started guides, quickstart links, corrected local prerequisites,
  noninteractive bare CLI status rather than a database in the current folder.
- Windows worktree fixes integrated as diffs (Git Bash temp path, smoke build,
  Python compatibility variants).

Evidence so far (logs `/mnt/bulk/lectern-round-two-*`):

- Full Go suite `go-3.log`: pass before the latest stopped-label and late-hook
  updates. Earlier failures from changed naming and staged env were corrected.
- Frontend tests `frontend-tests-2.log`: 419 pass, 0 fail.
- Build `frontend-build-2.log`: pass; subsequent Restore adjustment building.
- Focused real PTY interaction test passed (menu, upload, typing, detach).
- `safety.log`: real crashing process received env without printing credential;
  private staged env removed after exit. Wi-Fi cookie/auth regression passed.
- `race.log`: selected attachment, demo, Wi-Fi, env and status tests pass with
  race detector. Windows cross-build succeeded (`windows-build.log`). This is
  not proof of native Windows runtime behavior.
- Browser suites are IN PROGRESS. Fixtures using bare executable were changed
  to explicit `serve`. Do not mistake the initial setup errors for UI results.
  Full latest started log `e2e-3.log`, tool session 13437. It snapshots code
  before the newest Restore adjustment and late-hook guard. Focused walkthrough
  found Restore was hidden behind a dropdown; implementation now restores one
  click access to its panel. Saved search/discovery tests updated to open it.

Remaining before completion:

1. Finish browser failures and new-user walkthroughs on staged current assets.
2. Prove default-install demo/phone/agent death through actual local runtime;
   current focused pairing test tests HTTP cookie policy, not a physical phone.
3. Finish no-tmux parity: native file/link actions and scroll behavior need
   review; current wrapper preserves OSC8 links but lacks tmux path-click menu.
4. Verify default naming uniqueness, missing-agent failure before side effects,
   and the late-hook cleanup race with regression tests.
5. Check same-Wi-Fi interface selection (multiple interfaces) and listener
   cleanup; latest shutdown patch avoids holding mutex while waiting handlers.
6. Review card/menu usability and CSS, required missing-agent/error UI states,
   commit message/stale count, docs remaining platform caveats.
7. Run appropriate full checks and project `verify` honestly against candidate;
   current .verify.yaml mixes candidate tests with old live service checks.
   No verify PASS yet. No commit/push/deploy performed.

All real process tests MUST use reviewed `tools/run-isolated-tests.sh` with
`ADK_ISOLATION_REVIEWED=1`, PATH `/usr/local/go/bin:$PATH`. Do not touch host tmux.
Current assets are staged with `python3 frontend/scripts/stage.py` after a
successful `npm run build --prefix frontend`; e2e runner builds Go but does NOT
build frontend. Native test source is `internal/ptyhost/interactive_test.go`.

## Next continuation checkpoint

The previous turn made concrete changes and obtained new evidence:

- Browser run `e2e-4.log` reduced 66 failures + 5 setup errors to THREE
  failures. All three were old interactions/fixture contracts; focused rerun
  `browser-remaining.log` passed phone card sizing, adopted-release and unique
  names. The final React fixture needed a real-shaped `/sessions/restorable`
  mock response; `react-fixture.log` now passes.
- Navigation test helper `e2e/navigation.py` uses More, opens filters and
  Restore only when closed, and opens card actions. It preserves underlying
  terminal persistence/lifecycle assertions; no skips were added.
- Portable attachment now has Ctrl+] e using the existing path/link detector,
  hint overlay and action UI. `TerminalControls.Links` receives the actual
  screen; copy uses OSC52, send uses the attachment writer, file/web opens
  reuse existing restrictions. Mouse interception and scrollback still need
  review before claiming full no-tmux parity.
- Portable PTY controls including link picker PASSED under race detector in
  `portable-links.log` (the command pattern now correctly matches TestPortable).
  An existing tmux hint timing test failed in that race run, then passed in
  `native-regressions.log` without race; no data race was reported. Do not
  claim the whole race run passed.
- `native-regressions.log` passed focused native hints, portable attach, revive
  and late-permission rejection. New late hook regression is in bugfixes_test.
- Real PTY demo test uncovered 20-second exit grace and missing lifecycle
  events. Grace reduced to 2 seconds; demo now reports prompt, tool completion,
  Stop and SessionEnd events. `pty-flows.log` proves real approval -> first
  write -> second write without another approval -> Stopped + Revive. Its last
  naming test initially used the unsupported shell API; corrected to demo and
  passed in `browser-remaining.log`. Missing agent left NO session or scratch
  allocation in that run.
- Revive now probes the captured executable BEFORE stopping/releasing the old
  terminal. A missing executable previously ended it first. Need a specific
  regression that verifies preserving that process on refusal.
- Profiles with command/PATH overrides no longer inherit a false "missing"
  result from the default-agent probe. Backend still validates exact launch.
- Wi-Fi address selection now prefers the default route over private virtual
  interfaces. UDP connect to reserved TEST-NET selects route without sending
  data. Listener shutdown releases mutex before waiting on handlers.
- Terminal commit form now explains the folder/editor branch switch, and
  validates empty message on Tab/Enter at that field. These latest changes
  are UNTESTED; `dashboardForm.help` is rendered with `muted.Width` (initial
  build used an unimported lipgloss symbol and was corrected).

Latest current staged frontend matches frontend-build-4.log (success).
Subsequent source edit was only sessions/harness.tsx (test fixture, not bundle).
The backend has changed since the broad browser run: demo lifecycle, probe
2-second grace, portable link actions, Wi-Fi route preference, terminal commit
form. Run full Go/frontend/e2e again when remaining work is ready.

No live deploy/restart/commit/push, no `verify` PASS yet. Broad suite sessions
from prior turn finished. Last focused React run session 94684 finished pass.
No external blocker; goal remains active.

## Completed candidate checkpoint (2026-09-28)

Supersedes the outstanding-work lists above. See [round-two-status.md](round-two-status.md)
and [round-two-audit.md](round-two-audit.md). Candidate verification passed 3/3,
including all Go/frontend/browser suites, go vet and both portable cross-builds.
Added same-runtime phone integration + CLI tests, scrollback + real portable
attachment coverage, revive refusal preservation, commit field validation,
automatic commit-message drafting and reproducible service-worker packaging.
No commit, push, deployment or production restart performed.
