# Decision log

Lectern records every decision a person, a policy or a witness check made about
an agent's work, and how long the thing waited for that decision. Most of these
decisions never touched the approvals table: a permission prompt answered in
the agent's own terminal, a reply to an agent that stopped, an interrupt, or a
Review action. They are all written to the `decisions` table now.

The log is for studying how supervision works: approval habits, how many agents
one person can keep up with, and how long work waits on a human. It adds no
service and no screen. It is read through the API only.
Implementation: `internal/decisionlog`, the table in `internal/store/schema.go`,
the routes in `internal/api/decisions.go`.

## What is recorded

| kind | when | decision values | latency |
|---|---|---|---|
| `turn_response` | a prompt arrives after the agent stopped | `reply` (via `terminal`) | time from the Stop to the prompt |
| `interrupt` | a prompt arrives while the agent is still working | `interrupt` | none |
| `permission_prompt` | an agent asked permission for a tool | `allowed` (via `inferred`), `denied_or_interrupted` (via `inferred`) | time from the request to the outcome |
| `approval` | a broker decision that stood (the Lectern approval card) | `approved`, `denied`, `expired`, `stale` | from creation to decision |
| `review_commit` | a commit from the Review panel | `done`, `refused_stale` | from the session's last Stop |
| `review_push` | a push from the Review panel | `done`, `refused_stale` | from the session's last Stop |
| `review_comments_sent` | Review comments sent to the agent | `sent` | from the session's last Stop |
| `review_discard` | discarding changes in the Review panel | `done` | from the session's last Stop |
| `task_review` | a task review: request changes, or accept | `request_changes`, `accept` | none |

Each row also has the time it happened, the session, attempt and project when
known, the agent, how many sessions were live and how many approvals were
pending at that moment, and a small `detail` object.

## What is never recorded

- Prompt text, command text, tool input, agent messages, review comment text and
  commit messages. The `detail` object only holds lengths (`prompt_chars`), tool
  names, file and comment counts, and task ids.
- Anything in the approval input. Only the tool name is copied from it.

## Reading it

- `GET /api/decisions?days=30&kind=` lists rows, newest first. `days` is 1 to
  365 and defaults to 30. An unknown `kind` is a 422.
- `GET /api/decisions/summary?days=30` returns, for each kind, the count, the
  count of each decision value, latency p50 and p90 (nearest rank), and the
  latency p50 for each active-session bucket: 1, 2, 3–4, 5–7 and 8+.

Both routes need the same sign-in as the other read endpoints.

## Turning it off

Set `LECTERN_DECISION_LOG=0` (or `false` or `off`) in Lectern's environment.
Nothing is written while it is off. Existing rows stay in the database and the
read routes keep working on them. The default is on.

## What the inferred values mean

Lectern does not see a terminal permission prompt being answered. It infers the
outcome from the hooks:

- `allowed` means the same tool ran (`PostToolUse`) after the request. It does
  not prove the person clicked yes. A tool that a rule allowed silently is not
  recorded, because no request was made.
- `denied_or_interrupted` means the agent stopped, or the person sent a new
  prompt, before that tool ran. It can also mean the agent gave up on the call
  for some other reason.

A request that is replaced by another before it resolves is dropped without a
row, because Claude asks one permission at a time.

## Other limits

- Phone and web decisions both record as `web` for approvals. The broker does
  not know which device sent a `user` decision.
- A Lectern restart forgets any wait that was in progress, so the first decision
  after a restart may be missing. Nothing is recorded wrongly in its place.
- Task review records `accept` when a task in review is completed. Rejecting a
  task is not recorded as its own decision.
- Commit from a task or from a session (`commitTask`, `commitSession`) is not
  recorded yet. Only the Review panel's commit is.
