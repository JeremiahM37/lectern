# Claim board — who is doing what, in one repository

A2A and ACP handle an agent *talking* to another agent, but nobody handles
the much more common case: two agents (Claude Code, Codex, an ACP agent) —
or an agent and a human — working the **same repository** at the same time,
with no idea the other exists. Cross-agent awareness
(`docs/agent-events.md` "Cross-agent awareness") already makes peers
*visible*: a briefing on SessionStart, a warning when a file was recently
touched elsewhere. The Claim board goes one step further and gives agents a
way to actually **say what they're about to do**, so the coordination is
explicit instead of inferred from edit history after the fact.

It is entirely **advisory**. Nothing is ever blocked: a claim is a note left
for other agents and the operator, the same trust model as everything else
in `docs/agent-events.md` — Lectern narrates, it does not enforce.

Package: `internal/claims` (store/claims.go for persistence). Wired into the
hook response path in `internal/api/claims_hooks.go`, the REST surface in
`internal/api/claims.go`, the `claim_work`/`release_work`/`list_claims` MCP
tools in `internal/mcp/tools.go`, and a periodic sweep in
`internal/scheduler.Scheduler.Claims`.

## Model

```
claims {
  id, repo_key,
  scope_kind:  task | paths | topic,
  scope:       a task id, a JSON array of path globs, or a short topic string,
  holder,      holder_kind: session | attempt | human,
  session_id, attempt_id,   -- exactly one set for an agent-held claim, both
                             -- NULL for a human's own claim
  agent, intent,
  auto,        -- true for a claim internal/claims created on the agent's
               -- own behalf (first-edit paths claim, a task's own scope),
               -- not one an agent or human asked for by name
  ttl_seconds, created_at, expires_at, released_at
}
```

`repo_key` is the exact same opaque `"<target id>:<git common dir>"` string
cross-agent awareness already resolves (`internal/awareness.ResolveRepoKey`)
— a claim is scoped to a *repository*, not a worktree, so it is visible from
every checkout of it. A row is never deleted, only released (`released_at`
set), so the board keeps a short history rather than a claim just vanishing.

`scope_kind`:
- **`task`** — `scope` is a task id. Claimed automatically when a task
  dispatches (see Automatic claims below); an agent rarely claims this kind
  by hand.
- **`paths`** — `scope` is a JSON array of glob patterns, relative to the
  repository toplevel (the same `rel_path` space cross-agent awareness
  already computes edits in), e.g. `["frontend/src/sessions/**"]`. `*`
  matches within one path segment, `**` matches across `/` — a small
  regexp-backed matcher in `internal/claims.MatchGlob`, since Go's stdlib
  `path/filepath.Match` has no `**`.
- **`topic`** — `scope` is a short free-text description, e.g. `"rename
  button in session card"`, matched against other text by Jaccard
  word-overlap (`internal/claims.TopicScore`) — the same measure
  `internal/awareness.DuplicatePrompts` already uses for its own
  duplicate-session detection, reimplemented locally to keep the two
  features decoupled.

TTL defaults to **2 hours** (`internal/claims.DefaultTTL`), clamped to
5 minutes–24 hours. A claim held by a session is **renewed by activity**:
every PreToolUse/PostToolUse/UserPromptSubmit hook for that session pushes
every claim it holds back out to `now + its own ttl_seconds`
(`Tracker.RenewActivity`, called from `claimsAdditionalContext`) — so a
claim never lapses out from under an agent that is genuinely still working,
and naturally expires once it goes idle.

## Agents: claim and release

MCP tools (`internal/mcp/tools.go`), identified by `LECTERN_SESSION_ID` —
the same env every builtin session already carries, and the same resolution
`active_work` uses:

- **`claim_work {scope_kind, scope, paths?, intent?, ttl_minutes?}`** —
  `paths` is only for `scope_kind: "paths"`. The calling session's
  `repo_key` is resolved synchronously if it isn't cached yet
  (`internal/awareness.Tracker.ResolveNow` — safe here because this is an
  ordinary API call, not a hook response Claude/Codex is waiting seconds on;
  see that package's own doc comment for why the split matters).
- **`release_work {claim_id}` or `release_work {all: true}`** — release one
  claim, or every claim the calling session currently holds.
- **`list_claims {repo?}`** — active claims in the calling session's own
  repository, or a given `repo_key` (from `active_work`'s peer data), or the
  whole board with neither.

Call `active_work`/`list_claims` before starting non-trivial work, the same
discipline `active_work`'s own description already asks for — this is the
tool that lets you act on what you learn there instead of just reading it.

## Automatic claims

Two cases need no explicit `claim_work` call at all:

1. **A session's first tracked edit.** The first PostToolUse
   `Edit`/`Write`/`MultiEdit`/`NotebookEdit` a session makes gets it an
   implicit, lightweight `paths` claim on that file's directory
   (`internal/claims.AutoClaimGlob`: `<dir>/**`, or `*` for a toplevel file)
   — a soft "this session is active around here" signal, not a growing pile
   of claims for every directory it later touches. Setting:
   `claims_auto_paths` (default **on**).
2. **A task's own scope.** When a task attempt is created
   (`internal/api/tasks.go`'s `queueTask`), it automatically gets a `task`
   claim on its own task id, holder `attempt` — so a peer immediately sees
   "task #42 is already running" without the dispatching agent doing
   anything.

Both are marked `auto: true` so the UI can distinguish them from a claim an
agent or human asked for by name, without treating them as less real.

## Enforcement — always advisory, never blocking

a. **Briefing** (SessionStart/UserPromptSubmit `additionalContext`) lists
   every active claim in the repository, appended to cross-agent awareness's
   own peer briefing in the same hook response
   (`internal/api/claims_hooks.go`'s `claimsAdditionalContext`,
   `internal/claims.BriefingSection`). Setting: `claims_briefing` (default
   **on**).
b. **PreToolUse edit warning**: an `Edit`/`Write`/`MultiEdit`/`NotebookEdit`
   about to touch a file under someone ELSE's active `paths` claim gets an
   advisory naming it — *"session #143 (Codex) claimed
   `frontend/src/sessions/**` 12 min ago: 'rename button'. Coordinate with
   them before proceeding, or ask the operator."* Never a denial. Setting:
   `claims_edit_warning` (default **on**).
c. **Launch-time topic check.** `GET /api/claims/topic-overlap?project_id=&text=`
   scores a prospective prompt/title against every active `topic` claim in
   that project's repository; the New task dialog calls it (debounced) and
   shows a warning banner naming the overlapping claim(s) *before* dispatch
   — advisory, the button still works. The same claim also shows up in the
   next briefing (point a) once the session is actually running, so the
   warning is not a one-off the agent never sees again.

## Auto-release

A claim ends without anyone calling `release_work` in three cases, all
swept once per scheduler tick (`internal/claims.Tracker.Sweep`, wired
alongside the existing Budgets/Triggers ticks in
`internal/scheduler.Scheduler.Claims`):

- **TTL lapse** — no activity renewed it (see above).
- **Task attempt finished** — a `task`-scope claim's attempt is no longer
  `queued`/`running`.
- **Session ended** — `agent_state='ended'`, `status='dead'`, or
  `ended_at` set. A session's `SessionEnd` hook also releases its claims
  **immediately**, not waiting for the next sweep tick
  (`internal/api/hooks_agentevents.go`'s `hookSessionEvent`) — the sweep is
  the belt-and-braces catch for a tmux session killed outright, with no
  clean `SessionEnd`.

## Humans

A **Claims** button on the board (`frontend/src/claims/ClaimsPanel.tsx`)
opens the whole board: filter by project, release, extend (renews by its
own TTL, or a given `ttl_minutes`), and **claim for me** — a plain form
whose holder is the signed-in principal (`internal/auth`, the same identity
`GET /api/whoami` reports), for the case a human wants to flag "I'm working
on this" the same way an agent does.

Each session's own view (`frontend/src/claims/SessionClaims.tsx`, next to
the cross-agent-awareness overlap chip) shows every active claim in *that
session's* repository, with a Release button on the ones it holds itself.

An overlapping edit also pushes the existing alerts/`sinks.Notifier`
channel — the same web-push/Discord/ntfy sinks every other alert in
`docs/agent-events.md` section 3 uses — the first time a given (claim,
editor session) pair is seen, on a 10-minute cooldown
(`Tracker.ShouldNotifyOverlap`) so it does not re-fire on every keystroke.

## API

`GET/POST /api/claims`, `DELETE /api/claims/{id}`,
`POST /api/claims/{id}/extend`, `GET /api/claims/topic-overlap` — normal API
auth, appended to the router alongside the existing cross-agent-awareness
routes. `POST /api/claims` identifies the claim's repository and default
holder from exactly one of `session_id`, `attempt_id`, or `project_id` (a
human claim, holder = the signed-in principal or an explicit `holder`); a
bare `repo_key` is also accepted for advanced/test use. None of this affects
A2A's `ListTasks` — a claim is a Lectern-native concept, not part of that
protocol surface.

## Settings

`claims_briefing`, `claims_edit_warning`, `claims_auto_paths` — appended to
`internal/sinks.Keys` alongside the `awareness_*`/`alert_*` toggles, same
convention: default **on**, off only when explicitly set to `"0"`.

## Tests

- `internal/claims/claims_test.go` — DB-only: lifecycle (create/list/
  release), TTL default/clamping, expiry and activity-renewal via `Sweep`,
  auto-release on session end and on attempt finish, glob overlap
  (`MatchGlob`/`PathsOverlap`), topic overlap (`TopicScore`), briefing and
  edit-warning rendering.
- `internal/api/claims_test.go` — the real HTTP wire contract: creating a
  claim from a session/project, filtering, release/extend, the PreToolUse
  overlap warning (same-repo-different-worktree, self-exclusion), the
  SessionStart briefing, the settings toggle, automatic claims (first-edit
  paths claim stays singular, task auto-claim on dispatch, its release once
  the attempt finishes), and the topic-overlap endpoint.
- `internal/mcp/tools_test.go` — `claim_work`/`release_work`/`list_claims`
  against a real server (mock target), including `release_work {all:true}`
  and the "no `LECTERN_SESSION_ID`" refusal.
- `e2e/test_claims.py` — two real sessions, real hook tokens: one claims a
  path via the REST endpoint (the same endpoint `claim_work` calls), the
  other's real PreToolUse hook call gets the actual warning text, the
  Claims panel shows and releases it live with no reload, and a separate
  test covers the SessionStart topic-claim briefing.

## Not done

No dedicated frontend unit test for `ClaimsPanel`/`SessionClaims` (covered
by the e2e test, which exercises them against a real server, the same
precedent `AwarenessOverlapChip` set). No board-wide
`GET /api/awareness`-style dedicated overlap-chip endpoint for claims — the
session view's own claims fetch already covers it. The New Session dialog
does not yet run the topic-overlap check the New Task dialog does — a
follow-up once that dialog has an equivalent free-text field to check
against.
