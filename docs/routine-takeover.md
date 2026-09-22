# Take over a routine run

Open **Routines**, choose a run under **Started routine runs**, then choose
**Take over as session**. The same action is on a task's detail sheet.
When the handoff completes, **Open session** opens Chat; Attach opens its terminal.

Takeover stops the background attempt and starts an interactive CLI in that
attempt's existing worktree. Claude and Codex resume its exact conversation ID.
If an agent cannot resume that ID, the session gets a written handoff containing
the original request, last result and the path to the complete event log. Files
and uncommitted changes stay in place. The routine's future schedule is unchanged.

The task retains its history with a link to the session; its background attempt
is marked cancelled. Dispatch and task follow-ups are disabled after takeover;
send further instructions to the session. Deleting or clearing the task preserves
the session's worktree, including after the session has ended.

Requests persist before interruption. Repeated clicks reuse one session, and
Lectern resumes an unfinished handoff after a restart. A failed handoff shows
its error and offers **Retry takeover**. Preflight failures leave the original
process alive. Claude's permissions, MCP configuration and project environment
carry over; approval hooks tied to the cancelled attempt are removed.

A run must have a persistent worktree. Ephemeral sandbox runs are unsupported.
Wait for parallel attempts to finish or a queued follow-up to start before
requesting takeover, so there is only one background writer to transfer.

`internal/api/takeovers_test.go` uses a real routine, git worktree and tmux process
to verify exact resume, interruption, continued input, restart recovery, retries,
context fallback and cleanup protection. `e2e/test_routine_takeover.py` exercises
the same flow through the browser.
