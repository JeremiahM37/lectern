# Effect checks

Lectern can check the effect behind a completion claim. When an agent says
"pushed to origin/main", "restarted lectern.service" or "it is live at …", the
check looks at the machine to see whether that is true. Pilot sessions showed
that the false completions a person catches are mostly claims like these, where
a test passed and the effect itself was never observed.

Implementation: `internal/effects`.

## What is extracted

Claims come from the agent's final message. No model is involved. Each sentence
is checked for one of these kinds:

| Kind | Example sentence | What is probed |
|---|---|---|
| `pushed` | "Pushed to origin/main." | HEAD against the upstream branch on the remote |
| `committed` | "Committed as 1a2b3c4." | tracked files have no uncommitted changes |
| `tests_pass` | "All tests pass." | the session's last recorded test run |
| `restarted` | "Restarted lectern.service." | when the unit became active |
| `url_ok` | "The app is live at https://…" | HTTP status of the URL |
| `port_listening` | "Listening on :8080." | a TCP socket is listening on the port |
| `file_exists` | "Wrote `docs/x.md`." | the path exists |

Sentences are skipped when they are questions, future or conditional ("I'll
push once the tests pass"), or negated ("the branch is not pushed yet").

## What is probed

Every probe is read-only. It runs a fixed command through the project's
executor, with a 15 second limit:

- `pushed`: `git rev-parse` for HEAD and the upstream, `git ls-remote` for the
  remote branch, and `git merge-base --is-ancestor` when the remote commit is
  in the local clone. If HEAD is on the remote or behind it, the claim is
  verified. If not, it is contradicted.
- `committed`: `git status --porcelain --untracked-files=no`. Untracked files
  do not count.
- `tests_pass`: reads the session's recorded check. A failed run contradicts
  the claim. A passed run on the current tree verifies it. Anything else is
  unknown. The probe never runs the tests.
- `restarted`: `systemctl show` for the unit, using the system manager and then
  the user manager. A failed or inactive unit contradicts the claim, and so
  does a process that started before the current turn.
- `url_ok`: `curl -sS -o /dev/null -m 10 -w '%{http_code}'`. 2xx and 3xx verify
  the claim. Other codes and curl errors contradict it. TLS certificates are not
  checked for https URLs, because the homelab uses self-signed certificates.
- `port_listening`: `ss -ltnH "sport = :PORT"`.
- `file_exists`: `test -e` on the path, relative to the project directory.

A probe that cannot reach its target, or cannot decide, returns **unknown**. It
never returns verified in that case.

## What is not probed

- Whether a claim is correct. The check reads the machine. It does not judge
  the code or the answer.
- Anything that needs a write. The check never pushes, restarts, commits or
  writes a file.
- Claims in a message that were not extracted. Only the sentence patterns above
  are recognised, and a missed phrasing is simply not checked.
- Remote systemd units. The restart check reads the unit on the machine where
  the executor runs.
- Test results that are not recorded in the session. Without a recorded run the
  result is unknown, not verified.

## Turning it on

Each project has an `effect_checks` setting, changed in project settings or
with `PATCH /api/projects/{id}`:

| Value | What happens when an agent ends a turn |
|---|---|
| `off` | Nothing. The message is not read. |
| `show` (default) | The claims are checked and stored. The session card shows a badge, and a contradicted claim sends a push to you. |
| `enforce` | As `show`, and a contradicted claim is also sent back to the agent as a notice that quotes the claim and its evidence. The agent is asked to fix the problem or correct its report. |

A session with no project has no setting, so it is not checked.

## What happens on a turn

1. On a Stop hook (or codex's `AgentTurnComplete`) with a last message, Lectern
   extracts the claims. A message with none stops here.
2. The message is hashed. A message already evaluated for the session is not
   checked again, so a repeated Stop with the same text is free.
3. If the message claims tests pass and the session's latest test run is still
   running, the check waits for that run to finish, for at most the check
   timeout. Otherwise it judges the last recorded run.
4. Every claim is probed on the session's own target and workdir. The result is
   stored, with one entry per claim (its sentence, verdict and evidence). The
   session view gets it as `last_effect`, and a `session.effects` event goes out.

The hook is never held. The evaluation runs on its own goroutine after the hook
has answered.

## Enforcement

In `enforce` mode, a contradicted claim produces one notice to the agent per
message. The notice quotes each contradicted sentence with its evidence. The
human is not pushed for that claim, because the agent now has the chance to
fix it. The session card still shows the red badge.

Enforcement is capped at **3 notices per session per hour**. The count is kept
in the database, so a restart does not reset it. Past the cap, or if the notice
cannot be delivered (for example the session has ended), the human is pushed
instead. The cap exists so an agent that keeps repeating a false claim cannot
loop with the checker.

## Reliability

`GET /api/effects/reliability?days=30` (1 to 365 days) groups the claims checked
in the window by the session's agent and model. Each row has `checks` (messages
evaluated), `claims`, `verified`, `contradicted` and `unknown`. Read it as the
per-agent claim reliability: a high `contradicted` count against an agent is a
signal about what that agent reports, not just about one session.

## Endpoints

- `GET /api/sessions/{id}/effects`: `latest` (the newest evaluation, or null)
  and `recent` (the last 20, newest first).
- `GET /api/effects/reliability?days=30`: the rows above.
- `PATCH /api/projects/{id}` takes `effect_checks`: `"off"`, `"show"` or
  `"enforce"`. Any other value is refused with 422.

## What it still does not do

- It does not block anything. A contradicted claim in `enforce` mode is a
  notice, not a refusal, and the agent can still ignore it.
- It only checks sessions. Task attempts keep their own verification, and the
  `attempt_id` column is reserved for them.
- It does not look at claims the sentence patterns do not recognise. A missed
  phrasing is simply not checked, so `show` does not mean every claim was seen.
- It is not a judgement of the work. A claim that holds is not a claim that the
  work is right.

## Status

Wired into the session hook path, the session card, project settings and the
read API. Projects default to `show`, so existing projects start producing
badges on their next turn. Set `off` to keep the old behaviour.
