# CI loop

When a project opts in, Lectern watches the checks on the pull requests it
opens. If CI fails, it sends the owning agent the failing job names and the end
of each failing log, and asks it to fix the problem. The fix is pushed to the
same PR: by the agent in a live session, and by Lectern itself for a task. It
keeps doing that until CI passes, the PR is merged or closed, or it reaches the
attempt cap.
Implementation: `internal/ciloop`, with the API parts in
`internal/api/ci_loop.go`.

## Turning it on

Open **Settings → Projects → your project** and enable **Fix CI failures
automatically**. **Fix attempts per PR** defaults to 3 and can be set from 1 to
10. The API fields are `ci_loop` and `ci_max_attempts` on `PATCH
/api/projects/{id}`.

A watch starts when Lectern opens the PR:

- **Commit → Push → PR** on a task or a session. If `gh pr create` reports that
  a PR already exists for the branch, Lectern watches that PR.
- A **GitHub trigger** postback, which opens a PR for a task created from an
  issue.

If the agent opened the PR itself, from inside its own terminal, Lectern
cannot know about it. To watch that PR, send
`POST /api/tasks/{id}/ci` or `POST /api/sessions/{id}/ci` with
`{"pr_url": "https://github.com/owner/repo/pull/12"}`. This request needs a
signed-in human, as approvals do. An explicit request works even when the
project has not opted in.

## What it does

The loop uses the target's own `gh` login through the same executor as the
Commit/PR button. It runs `gh pr view` to get the PR's state and head commit,
`gh pr checks` to get check results, and `gh run view --log-failed` to get
failing GitHub Actions jobs. It reads the plain `gh pr checks` output, so it
does not depend on `--json` and works with older `gh` versions.

Once no checks are pending and at least one has failed, Lectern sends one fix
request for that commit. Each request includes:

- every failing or cancelled check, with its link;
- up to four failing Actions job logs, with colour codes and timestamps
  removed. Each log is trimmed to its last 80 lines or 4 KB, and the whole
  request to about 14 KB;
- redacted credentials: GitHub/OpenAI/Anthropic/Slack/AWS-style tokens, JWTs,
  bearer headers, `user:pass@` URLs, private key blocks and `PASSWORD=`-style
  assignments. GitHub already masks the secrets it knows as `***`; this catches
  credentials it did not mask;
- the branch the fix goes to.

Where the request goes depends on who owns the PR. Any agent that can receive
a message works; nothing here is specific to Claude.

| Owner | Delivery |
|---|---|
| Session | Typed into the live session, like the phone keyboard's Send |
| Task taken over into a session | Typed into that session |
| Task | Queued as a task message. The scheduler resumes the agent in the same worktree as a follow-up once the task is idle, or after the running attempt finishes |

If a fix request cannot be delivered, the watch stops. Examples are an ended
session or a task whose worktree was cleaned up. The card shows the reason.

## Who commits the fix

A session's agent is asked to commit and push, as before. It can usually run
git; if it cannot, you can, from the session's Commit → Push button.

A task's follow-up attempt runs headless, usually in `acceptEdits` mode, where
every git command needs an approval nobody is there to give. So a task is told
only to fix the failures, and Lectern does the git part. When the attempt the
fix request went to finishes:

- If it left uncommitted changes, Lectern commits them as `Fix CI: <failing
  checks>` and pushes to the PR branch, using the same code as the Commit →
  Push button.
- If the agent committed but did not push, Lectern pushes.
- If nothing changed, or the attempt failed, or the commit or push fails, the
  watch stops and the card says why.

Lectern does this once per fix attempt, and never while another attempt on the
task is queued or running. A task prompt that says "do not commit or push" does
not stop it: the fix request says so explicitly, because the point of a CI fix
is to update the PR. Sandbox tasks are not committed this way.

## Polling, not webhooks

Lectern is normally reachable only on a private tailnet, so it polls. A watch
checks again after 30 seconds. It returns to 30 seconds whenever something
changes, such as a new commit or a new state. While nothing changes, the
interval doubles up to 10 minutes. When Lectern knows a push is coming, it
checks straight away and resets the interval: after the Commit → Push button
pushes, after it pushes a task's fix, when a task's fix attempt finishes, and
when a PR that is already watched is armed again. A push made from inside an
agent's own terminal is seen on the next poll, so it can take up to 10 minutes.
Polling happens only while checks are pending or failing. It stops on:

| State | Card | Phone push |
|---|---|---|
| passed | `CI passed` / `CI passed after N fixes` | only if at least one fix was requested |
| capped | `CI failing — gave up after 3/3` | yes |
| merged / closed | `PR merged` / `PR closed` | no |
| none | `No CI checks` (none reported within 15 minutes) | no |
| error | `CI watch stopped` (e.g. `gh` not signed in or not installed, or the PR is gone) | no |
| stalled | `CI watch stalled` (no progress for a day) | no |

While active, the card shows `CI running`, `CI running — attempt 1/3` or `CI
failing — attempt 2/3`. The chip links to the PR, and hovering it shows the
failing checks or the reason the watch stopped. Network errors are retried with
backoff. A missing `gh` login is not retried, because retrying cannot fix it.

## Limits

- Fix requests wait for every check to finish. One slow check therefore delays
  the report on an early failure.
- Logs are fetched only for GitHub Actions. Other CI systems still appear by
  name, with a link.
- Starting a new watch on a PR whose watch has finished resets its counters.
