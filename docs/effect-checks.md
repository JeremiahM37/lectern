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

## Status

The package is not wired into hooks, checks or the UI yet. Nothing changes for
existing sessions.
