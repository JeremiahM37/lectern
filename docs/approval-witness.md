# Approvals bound to the state they were granted against

An approval is granted against what you saw: a commit, a diff, a branch. The
action it allows runs later. In between, another agent can edit the same
worktree, or the agent can commit more. Lectern re-checks the worktree at the
moment an approved action is about to run, and refuses it if the state has
moved.

Implementation: `internal/witness` (snapshot, drift, and which commands count),
`internal/broker` (record on request, re-check on approve), and the review
commit and push in `internal/api/review_git.go`.

## What it does

**When an effectful command is requested.** A Bash call that publishes or
mutates outside the worktree is recorded with the worktree's state: branch,
HEAD, upstream and a hash of the diff. Untracked files are included by content,
so editing a new file counts as a change. The commands are:

- `git push`, `git merge`, `git rebase`, `git commit`
- `gh pr merge`, `gh pr create`, `gh release`
- `npm publish`, `docker push`
- `systemctl start|stop|restart|reload`
- `kubectl apply|delete`, `terraform apply`
- any segment that contains the word `deploy`

Chains (`&&`, `;`, `|`), a leading `cd dir`, `sudo` and `VAR=value` prefixes are
handled. Read-only commands such as `git status` are never recorded.

**When you approve.** Lectern reads the state again. If it has moved, the
approval does not go through:

- the approval becomes `stale`, with a note such as
  `state changed since this was requested: HEAD moved 1a2b3c4 → 5d6e7f8; re-run the command to ask again`;
- the agent's blocked command is refused with that note, so it can re-run the
  command and ask again against the current state;
- you get a notification, "Approval out of date".

**When you deny.** A denial always stands, whatever the state.

**In the review.** The live diff now includes `head` and `diff_hash`. The
Commit and Force push actions send them back as `expected_head` and
`expected_diff_hash`. If the repository has moved since you loaded the diff,
the server answers `409 {"error":"stale","detail":"…"}` before it changes
anything, and the panel says **These changes moved since you loaded them**
with a button to reload. A request that does not send these fields is not
checked, so older clients behave as before.

## Turning it on

It is on for every Lectern install with the default build. It needs no setting.
It is only as strong as the state it can read, see below.

## What it does NOT do

- **It is a check, not a lock.** The gap between the re-check and the command
  actually running is short but not zero. Another process can still change the
  worktree in that gap.
- **It does not parse shell quoting.** `sh -c "git push"` is not recognised.
  The word `deploy` anywhere in a command counts, so `cat docs/deploy.md` is
  treated as effectful. An extra re-check is the cost, and a missed push is the
  thing being avoided.
- **Only the first repository of a grouped workspace is checked** for approvals.
  The review's Commit and Push are pinned to the repository you chose, and a
  multi-repository session without a chosen repository is not pinned at all.
- **A re-read that fails counts as stale.** If Lectern cannot read the worktree
  when you approve (for example its SSH target is down), the approval is refused
  and the message says so. Deny and re-run once the target is back.
- **A snapshot that fails at request time means no witness,** and the approval
  behaves as it did before this feature. This is deliberate: a broken probe must
  not stop an agent from asking.
- **Policy auto-approvals are not recorded.** No person looked at them, so there
  is nothing to re-check.
- **Sandbox attempts** read their worktree through the target's executor. If that
  path is not reachable from the target, there is no witness.
- **It does not change what you approved.** A command is not rewritten or
  narrowed; it is either allowed to run or refused.
