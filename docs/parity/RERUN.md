# Re-running the bounded evidence

Run from a clean checkout with a local fake executable and isolated temporary
HOME/XDG/tmux state. Do not provide real provider keys or invoke a hosted model.
The original harnesses used short-lived fixture roots under `/tmp`; those roots
are intentionally not part of the repository. The commands pass `HOME` through
a per-command environment wrapper, so they do not change the caller's shell.

## Minimal isolated fixture

The following creates the same kind of local repository and fake runner used by
the comparison. Each product must be run in its own `RUN` directory:

```bash
RUN=$(mktemp -d /tmp/lectern-parity-XXXXXX)
mkdir -p "$RUN"/{home,config,data,cache,state,tmp,tmux,bin,repo}
REPO="$RUN/repo"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email fixture@example.invalid
git -C "$REPO" config user.name Fixture
printf 'before\n' > "$REPO/tracked.txt"
git -C "$REPO" add tracked.txt
git -C "$REPO" commit -qm baseline
cat > "$RUN/bin/fixture-agent" <<'SH'
#!/bin/sh
printf 'FIXTURE_AGENT_MARKER\n'
printf 'C3_PID:%s\n' "$$"
exec /bin/bash --noprofile --norc
SH
chmod 755 "$RUN/bin/fixture-agent"

FIXTURE_ENV=(
  "HOME=$RUN/home" "XDG_CONFIG_HOME=$RUN/config"
  "XDG_DATA_HOME=$RUN/data" "XDG_CACHE_HOME=$RUN/cache"
  "XDG_STATE_HOME=$RUN/state" "TMPDIR=$RUN/tmp"
  "TMUX_TMPDIR=$RUN/tmux" "TERM=xterm-256color" "LANG=C.UTF-8"
  "PATH=$RUN/bin:$PATH" "LECTERN_NO_UPDATE_CHECK=1" "DO_NOT_TRACK=1"
)
run_fixture() { env "${FIXTURE_ENV[@]}" "$@"; }
```

Pin and record the binary/source revision before launching. The successful C3
launch forms used in this comparison were (the fake command is a fixture, not
the terminal UI):

```bash
# upstream Agent Deck 61cc4d6
UPSTREAM_BIN=/tmp/lectern-parity-proof-v2/agent-deck-upstream
run_fixture "$UPSTREAM_BIN" launch "$REPO" -c "$RUN/bin/fixture-agent" \
  -t "Alpha synthetic" -g Work/Backend --no-wait --quiet

# Agent of Empires 5687bbd (repeat with distinct titles for four sessions)
AOE_BIN=/mnt/bulk/lectern-comparison/aoe-target/debug/aoe
run_fixture "$AOE_BIN" add "$REPO" --title "Alpha synthetic" --tool codex \
  --cmd-override "$RUN/bin/fixture-agent" --trust-hooks --launch --yolo
```

For Lectern, create the fixture project/target through its Settings wizard,
choose the fake command, and create four sessions through the New session form;
the checked-in mobile test is the executable local example. When exposing a
browser terminal through ttyd, launch the pinned product TUI itself. Running the
fake executable directly under ttyd bypasses the product and is not evidence:

```bash
UPSTREAM_PORT=17961
run_fixture ttyd --writable --port "$UPSTREAM_PORT" "$UPSTREAM_BIN"

AOE_PORT=17963
run_fixture ttyd --writable --port "$AOE_PORT" "$AOE_BIN"
```

Attach the selected row in the product TUI and capture the live terminal. Send
the marker plus 70 known lines with `printf 'C3_KNOWN_%02d\n' {1..70}` and
record a visible `C3_KNOWN_*` line. A plain PageUp is not a tmux copy-mode
check: record the actual bindings first:

```bash
run_fixture tmux list-keys -T root
run_fixture tmux list-keys -T copy-mode
run_fixture tmux list-keys -T copy-mode-vi
```

In the pinned upstream run, `Ctrl-b [` entered copy mode, PageUp scrolled, and
Escape cancelled it; the successful detach was the product's `Ctrl-q` action.
In the pinned AoE LIVE preview, `Ctrl-q` returned to the dashboard and Tab
reentered the same pane. AoE's native attach path uses `Ctrl-b d` only while
directly attached to the tmux pane. Use the bindings printed above when a
build differs, and record the exact key sequence.
After reattaching, expect the same pane/session identity and a fresh
`C3_REATTACHED_UNIQUE_<token>` from the same shell PID. Resize to 110 columns ×
30 rows for upstream and expect `stty size` to report `30:110`. Record both the
outer terminal and the actual tmux pane for AoE: its 110×31 LIVE terminal
produced a 71×27 pane and `stty size` reported `27:71` after the UI chrome was
accounted for. Verify copy mode is gone (`pane_in_mode=0`) before sending shell
input. Any missing observation is `UNVERIFIED`.

For the C4 child-isolation check, the pinned upstream CLI workflow was:

```bash
UPSTREAM="$UPSTREAM_BIN"
run_fixture "$UPSTREAM" launch "$REPO" -c "$RUN/bin/fixture-agent" \
  -t 'C4 upstream parent' --no-wait --quiet
run_fixture "$UPSTREAM" launch "$REPO" -c "$RUN/bin/fixture-agent" \
  -t 'C4 upstream child' \
  -w c4-upstream-child -b --no-wait --quiet
CHILD_JSON=$(run_fixture "$UPSTREAM" worktree info 'C4 upstream child' --json)
CHILD=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["worktree_path"])' \
  <<< "$CHILD_JSON")
printf 'after\n' > "$CHILD/tracked.txt"
git -C "$CHILD" diff --no-color main
run_fixture "$UPSTREAM" session send 'C4 upstream child' \
  'git diff --no-color main'
```

The successful run queried a path ending in
`.worktrees/feature-c4-upstream-child`; do not construct that path from the
branch name. Confirm the parent still contains `before\n`, the queried child
contains `after\n`, and the attached product terminal shows the exact
`-before` / `+after` diff.

The equivalent Lectern/AoE web path creates the child from the product’s
workspace/fork control, edits `tracked.txt` in the displayed child directory,
and captures the rendered Review/Diff surface. Expected observations are a
distinct child identity, unchanged parent `before\n`, and the exact two-line
patch `-before` / `+after`. Do not count a filesystem diff without the
user-facing product surface as full C4 proof.

For the checked-in Lectern mobile behavior, run the real PTY/browser tests:

```bash
python -m pytest -q \
  e2e/test_terminal_swipe_tabs.py \
  e2e/test_mobile_terminal_focus.py
```

The swipe test creates two local tmux sessions, uses CDP touch events at
390×900, sends shell markers, checks xterm selection and mouse-reporting mode,
and cleans its fixture sessions. The focus test checks the compact Tools →
Files path, keybar behavior, rotation/resizing, offline recovery, and that
Tools/status do not overlap the xterm viewport.

For the broader C1–C6 comparison, use the fixture above separately per product
and the rubric in `rubric.md`; record the exact revision, viewport, fixture
root, observable marker/sentinel, and strict status. If a harness fails to
expose a criterion, record `UNVERIFIED` and preserve the failed attempt. A
single authorized retry is allowed only when the harness itself errors. The
2026-09-10 historical harness exceeded that retry budget while recovering
selector, xterm-focus, socket-path, and clipboard issues; those attempts are
disclosed in the comparison ledger and were not counted as product passes.

The 2026-09-10 evidence JSON records the exact fake-agent values and request
boundaries from the completed runs. Temporary screenshots/logs mentioned in
older review notes are audit material, not required inputs to this rerun.
