# Usage limits — detect, wait, resume or hand off

When an agent's provider stops it for a usage limit, Lectern notices, shows
it on the card with the reset time, and does what the project's policy says:
tell you, wait and resume the same agent after the reset, or hand the work
to another agent now. Headless tasks get the same treatment.

Package: `internal/limits`. Task side: `internal/scheduler/limits.go`.
Store: `internal/store/limits.go` (`limit_holds`, `limit_policies`).

## Detection

| Source | Agents | What it reads |
|---|---|---|
| Pane | any | The bottom 15 non-blank lines of every session poll. A limit message counts only at the start of a line (after bullets and box edges) and only while the agent is not working below it. |
| `StopFailure` hook | Claude | `error: "rate_limit"` plus the message Claude showed. A transient 429 is ignored unless the statusline already shows the window at 100%. |
| Statusline | Claude | Supplies the reset time when a message did not state one (the 5h or 7d window at 100%). It does not open a hold by itself: an idle session on a full account is not stopped. |
| Task stream | Claude, Codex, text agents | Claude's `rate_limit_event` with `status: "rejected"`; Codex `error` / `turn.failed`; the result or output text for everything else. |

All message patterns live in one table, `limits.Table`, each with the real
text it was taken from. `internal/limits/patterns_test.go` matches every
sample and a list of near misses that must not match (Claude's fast-mode
notice, a transient "Rate limit reached", context limits, source code that
quotes the message).

| Pattern | Example |
|---|---|
| `claude-limit` | `You've hit your session limit · resets 3:40pm (America/Chicago)` |
| `claude-auto-continue` | `Usage limit reached · continuing automatically at 3:40pm · esc to cancel` |
| `claude-out-of-credits` | `You're out of usage credits · resets Oct 3, 9am (UTC)` |
| `claude-legacy` | `Claude AI usage limit reached\|1790200800` |
| `codex-usage-limit` | `You've hit your usage limit. Try again at 3:40 PM.` |
| `gemini-usage-limit` | `Usage limit reached for gemini-2.5-pro.` / `Access resets at 3:40 PM PST.` |
| `gemini-quota-exhausted` | `You have exhausted your capacity on this model. Your quota will reset after 19h14m47s.` |

Samples come from Claude Code 2.1.283's bundle, the Codex 0.157.0 binary and
gemini-cli 0.61.0's source and tests. Catalog and custom agents are checked
against the same table: they usually wrap one of these CLIs or print the
provider's message as-is. An agent that words its limit differently is not
detected until a pattern is added.

## Policy

`notify` (default), `wait` or `handoff`, set globally, per project or per
session; the narrowest one set wins.

```bash
# every project: resume after the reset
curl -X PUT .../api/limits/policy -d '{"policy":{"mode":"wait"}}'
# one project: hand off to Codex straight away
curl -X PUT .../api/limits/policy -d '{"project_id":3,"policy":{"mode":"handoff","fallback_agent":"codex","fallback_model":"gpt-5"}}'
# one session: back to the project's policy
curl -X PUT .../api/limits/policy -d '{"session_id":42,"policy":null}'
```

A fallback can also be a launch profile (`fallback_profile_id`). Settings →
Budgets edits the global policy, and each project card in Settings has its
own editor.

- **notify** — a push and a card banner with one-tap choices. Nothing is typed
  into the session. When the reset passes, one more push says so.
- **wait** — at the reset (plus 45 s and up to 90 s of jitter) Lectern types
  `Your usage limit has reset. Please continue where you left off.` and checks
  that the agent really resumed: working below the nudge, a tool or Stop hook
  after it, or no limit message within 90 s. If the limit comes back, it
  reschedules for the new reset, or backs off (10 min doubling to 2 h when no
  reset is known), up to 4 tries, then gives up and tells you. If the CLI is
  already counting down to continue by itself (Claude's own banner), Lectern
  leaves it alone and only steps in if it does not resume.
- **handoff** — starts the fallback agent in the same workspace through the
  switch machinery. The limited agent cannot write a handoff, so Lectern
  builds one from the limit, the last prompt and the end of the screen, and
  primes the successor to continue. It cancels the CLI's own auto-continue
  countdown if one is showing, so two agents never work in one workspace. The
  original session stays open. **Switch** on a limited session takes the same
  path.

The push and the card offer **Resume at reset** (or **Resume now** once the
reset has passed), **Hand off** and **Dismiss** —
`POST /api/limits/{id}/choose {"action":"wait|resume_now|handoff|notify|dismiss"}`,
optionally with `agent`/`model`/`profile_id` for the handoff. `GET
/api/limits` lists open holds (`?all=true` for recent ones).

## Tasks

A limit in a task's stream opens a hold on the attempt. When the attempt
exits, the project's policy applies:

- **wait** — a new attempt is queued with `not_before` set to the reset. It
  resumes the agent's own conversation in the same worktree; the board
  shows the task as queued with "Limit — resumes 3:41pm".
- **handoff** — a new attempt on the fallback agent, now, in the same worktree,
  with the original task and a note about the partial work.
- **notify** — the task fails as before, with a "Task stopped by usage limit"
  push offering the same choices.

On a sandbox target the container is gone, so the continuation starts fresh
from the task prompt.

## Restarts

Every hold lives in `limit_holds`, and every automatic action is a
compare-and-swap on its state. A nudge is recorded before it is sent, so a
restarted Lectern finds the hold "resuming" and verifies the earlier nudge
instead of sending another. If the nudge never reached the pane, it is
retried once, which is still a single delivered nudge. A handoff interrupted
by a restart is detected and reported rather than restarted, and a task
continuation that was created but not recorded is adopted instead of
duplicated. Tests: `TestNoDoubleResumeAfterRestart`,
`TestConcurrentTicksNudgeOnce`, `TestUndeliveredNudgeIsRetried`,
`TestLimitedTaskIsRequeuedForTheResetOnce`.

## Limits of this feature

- Patterns come from CLI source and bundles, not from a live limited session.
  A CLI release that rewords its message fails the pattern tests only when the
  samples are refreshed.
- Codex and Gemini sessions are detected from the pane only; neither exposes a
  hook for this. Codex's own rate-limit percentages (in its rollout files) are
  not read.
- There is no account hot-swap: a handoff goes to another agent or launch
  profile, not another login of the same CLI.
