# Comparative workflow evidence — 2026-09-10

This is a bounded evidence record for Lectern, Agent of Empires (AoE), and
upstream Agent Deck. It records tested cells and known limits. It makes no
overall web superiority claim.

## Versions and evidence roots

- Lectern mobile release candidate: `178bc00d406a893da8b7d4c76d5c5533021ea568`
  (artifact SHA256 `282f53a5f7157c851953c0b7bb90cb6bced2e546d0d22745928e741ad97643d6`).
  Full release verification passed `6/6` web steps; the run manifest is
  `/tmp/lectern-mobile-verify-178bc00.manifest.json`.
- The deployed live `178bc00` build passed post-deploy verification `6/6`
  (exit 0), handle `49422`, PID `739182`; log:
  `/tmp/lectern-postdeploy-178bc00.log`.
- The C1/C2/C5/C6 browser evidence was executed against the preceding e84
  feature run; the mobile follow-up and atomic fixture correction are recorded
  separately in `docs/parity/evidence-2026-09-10.json`.
- Historical Lectern comparison baseline: `bdc4685`.
- Upstream Agent Deck: `61cc4d6`.
- AoE: `5687bbd`.
- Exact rubric and bounded matrix:
  `/tmp/lectern-parity-proof-v2/rubric.md` and
  `/tmp/lectern-parity-proof-v2/bounded-matrix.md`.
- Checked-in compact rubric, inline evidence ledger, and rerun instructions:
  [docs/parity/rubric.md](parity/rubric.md),
  [docs/parity/evidence-2026-09-10.json](parity/evidence-2026-09-10.json),
  and [docs/parity/RERUN.md](parity/RERUN.md).
- The original before-fix TUI raw capture was overwritten. The surviving
  observation and limitation are recorded in
  `/tmp/lectern-parity-proof-v2/tui-pyte/prefix-observation.md`.

## Current browser cells

Lectern and AoE each have current, isolated browser runs covering C1, C2,
C5, and C6. These sessions were created through each product's web UI; API
setup was limited to registering the fixture project/target needed by the
wizard.

Lectern evidence is under
`/tmp/lectern-parity-proof-v3/ui/backend-e84-r4/`. It shows:

- C1: four UI-created sessions using configured `fake-proof`, with the
  readiness marker visible.
- C2: four sessions and their project/target context are captured before
  search; while the palette is open, exactly one `UI Gamma` result is visible
  with `Sessions`, agent, project, target, and repository context. The result
  is then opened.
- C5: desktop and 390px mobile attach, sentinel input, dashboard return, and
  reattach, with mobile horizontal-overflow check.
- C6: a synthetic 503 leaves the agent setup form open. The observed form,
  exact PUT body, error, and retained values are in
  `phone-c6-observed-form.json`, `phone-c6-request.json`, and
  `phone-c6-error-draft.txt`. The same run captures the multiline session
  creation POST in `desktop-c6-session-create-request.json`.

The Lectern fake runner is deterministic and makes no model/provider call.
Its log contains startup and sentinels only. The multiline value is therefore
proven at the browser request boundary and in the retained form; automatic
consumption by an agent is deliberately not claimed.

AoE evidence is under
`/tmp/lectern-parity-proof-v3/ui/backend-aoe-5687-run-03/`. It shows the
same C1/C2/C5/C6 cells with a configured custom agent. Its C6 request captures
the exact multiline custom instruction, repository, and custom-agent name at
the UI boundary. The fake process only proves local wiring/readiness, not model
or provider behavior.

## C3 status

Lectern's C3 cell is accepted PASS from the actual ttyd evidence:

- `/tmp/lectern-parity-proof-v3/c3/real-ttyd/ours-final/03-copy-scroll.txt`
- `/tmp/lectern-parity-proof-v3/c3/real-ttyd/ours-final/07-reattached-unique.txt`
- `/tmp/lectern-parity-proof-v3/c3/real-ttyd/ours-final/04-resize.png`

The recorded evidence includes fresh same-PID `669016`, the marker, tmux scrollback containing `C3_KNOWN_01`, readable `30x110` output with the status row, and reattachment.
Equivalent upstream Agent Deck C3 is accepted by the real ttyd receipt
`upstream-c3-final-cancel-resume-receipt.json`. AoE C3 is accepted as a
composite of the documented modes in `docs/parity/receipts/aoe-c3-final-evidence.json`:
the one-session LIVE preview proves attach, actual 27x71 pane resize, `Ctrl-Q`
dashboard exit, Tab reentry, same shell identity, and a fresh sentinel; native
attach proves tmux copy-mode PageUp scroll (`C3_NATIVE_SCROLL_01..33`) and Escape
cancellation. LIVE preview Shift+PageUp did not change visible markers in this
run, so that mode-specific limitation remains explicit.

## C4 child isolation and diff review

C4 is complete for the three compared products. Each run created a child
workspace/branch, edited a tracked file, verified distinct identities, kept the
parent unchanged, and checked the exact patch. The aggregate ledger is
`/tmp/lectern-parity-proof-v3/c4-summary.json`.

- Lectern e84 build: `/tmp/lectern-parity-proof-v3/current-web/c4-evidence.json`;
  the web Review changes surface showed the exact patch at 1440x900 and
  390x900.
- AoE: `/tmp/lectern-parity-proof-v3/aoe-web/c4-evidence.json` and
  `/tmp/lectern-parity-proof-v3/aoe-web/c4-web-evidence-restored.json`;
  the desktop Diff pane and mobile right-panel picker showed the exact patch.
- Upstream Agent Deck: `/tmp/lectern-parity-proof-v3/upstream-web/c4-evidence.json`.
  The CLI sent `git diff --no-color main` to the session and captured the resulting output.
  The user-facing path was `session send` followed by terminal capture, but
  `session send` returned a confirmation warning (code 1) saying submission
  was not confirmed; the captured terminal still contains the diff. This is
  recorded as PASS via CLI send-plus-capture workflow, with no integrated viewer
  claimed.

All C4 harness attempts, including failed/recovery attempts, remain preserved
under the referenced artifact roots. No failed attempt is silently replaced by
a green result.

## Retry and limitation ledger

The current browser results are the final bounded runs, with earlier attempts
retained for audit:

- Lectern: earlier named-attach/xterm-focus failure is retained in
  `/tmp/lectern-parity-proof-v3/ui/lectern-e84-backend-result-failed-named.json`
  and `backend-run-named-failed.log`. Receipt runs that exposed a transient
  launch failure and an overlong tmux socket path are retained in
  `backend-lectern-final-receipts-run.log`,
  `backend-lectern-final-receipts-retry-run.log`, and
  `backend-lectern-final-receipts-retry2-run.log`. The short-path final
  receipt is `backend-e84-r4`.
- AoE: run 01 preserved the first-session keyboard focus mistake; run 02
  preserved the mobile new-session icon being outside the viewport; run 03
  passed after using the visible command-palette action and a short artifact
  path. Logs are `aoe-ui-backend-run.log`,
  `aoe-ui-backend-run-02.log`, and `aoe-ui-backend-run-03.log`.

These retries are harness history, not product failure counts.

## Overall status

The Lectern `178bc00` candidate has completed its full web release
verification. The current Lectern/AoE browser C1/C2/C5/C6 cells, all three
C4 paths, and the composite C3 workflows for Lectern, upstream Agent Deck,
and AoE are evidenced. The full cross-product rubric remains a bounded evidence
record rather than a performance ranking, and broader claims about web
superiority remain unverified.
