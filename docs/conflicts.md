# Semantic conflicts

Two agents on one repository can each pass their own checks and still break
each other. One agent changes what `Parse` does, and the other adds a new
caller of `Parse` in a different file. File-level overlap warnings
("someone else touched this file") miss about half of these. Lectern predicts
the symbol-level ones from each session's live diff and lets a human check a
pair before merging.
Implementation: `internal/conflicts` (parsing and prediction, and the scanner),
with the API parts in `internal/api/conflicts_api.go`.

## What you see

- **A card chip** on a session that shares a semantic risk with another live
  session, naming that session. **Check together** runs the validation below.
- **A line in the agent's briefing** when a fresh briefing is sent:
  `Possible semantic conflict with Session #12: Session #12 changes Parse
  (internal/x/parse.go); Session #14's new code uses Parse. Coordinate or
  validate before merging.` It is advisory. The agent is not stopped.
- **`GET /api/conflicts?project_id=N`** lists every pair above "none", as
  `[{a:{kind,id,title}, b:{…}, level, reasons}]`. `kind` is `session` or
  `attempt`.

## How a risk is predicted

Each side's live diff (uncommitted and untracked work included, against the
merge-base) is reduced to:

- the **symbols it changes or adds**: Go functions, methods (qualified by
  receiver, as `DB.Close`) and types; Python `def` and `class`; TypeScript and
  JavaScript `function`, `class` and `export const`; Rust `fn`. A changed
  function body counts through git's hunk header, which names the enclosing
  function.
- the **identifiers its added lines use**, minus language keywords.

Levels:

- **semantic**: one side changes a symbol that the other side uses, or both
  change the same symbol. Works across files and within one file.
- **textual**: both sides touch the same file and no symbol links them.
- **none**: otherwise.

To keep noise down, a bare name must be at least four characters, and common
words such as `run`, `get`, `error` and `data` never link on their own. A
method-qualified name such as `Store.Get` always links. Scans are cached by
worktree fingerprint, so an idle project costs almost nothing to re-check.

It does **not** check the language. It works from text, so a name that
collides across unrelated code can produce a false positive, and an indirect
dependency (a call through an interface, a string-named reflection call) is
missed. Treat a prediction as a prompt to look, not a verdict.

## Check together

`POST /api/conflicts/validate` with `{"a":{"session_id":12},"b":{"attempt_id":7}}`
answers "would these two pass together?":

1. both diffs are read, and both sides must be worktrees of one repository on
   one target, against the same merge-base;
2. a scratch detached worktree is made at that merge-base, in a temporary
   directory on the target;
3. A's patch, then B's, is applied with `git apply --3way`;
4. if both apply, the project's check command (`verify_cmd`, or the
   auto-detected `.verify.yaml`) runs there;
5. the scratch worktree and directory are removed, even if the request is
   cancelled.

The response is `{applied, textual_conflict, check_status, command,
output_tail, reason}`, where `check_status` is `passed`, `failed`, `error` or
`skipped`. A patch that will not apply is a textual conflict, and the check is
skipped.

This needs a signed-in human, as other mutating endpoints do. It runs the
project's command on the target, so it is not something an agent calls.

## What it does NOT do

- It does not block, pause or reorder any agent.
- It does not speculatively execute, and it does not roll anything back.
- It does not write to the real worktrees. Validation only touches the scratch
  worktree.
- Two sides taken from different merge-bases are refused with a message, not
  merged. Bring one onto the other's base first.
- A session with changes in several repositories is refused. Validation
  applies one worktree.
- Nothing about a prediction is stored. A restart clears the cache.
