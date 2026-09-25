# Memory you can see

Lectern has injected project memory into sessions and task attempts since
[automatic memory](AUTOMATIC_MEMORY.md) landed — but it hid the injection. An
operator could not ask "what was this agent told?" or "that memory is wrong,
correct it", and the evidence for both lived where nobody could read it: in the
retrieval that ran at launch and in the model's prompt.

Recording is now automatic. Correcting is deliberate, and takes a person.

## What is recorded

One row in the `memory_deliveries` table per delivery (`internal/store/schema.go`):

| column | meaning |
|---|---|
| `id` | the row |
| `session_id` | an interactive session, or NULL |
| `task_id` / `attempt_id` | a dispatched task attempt, or NULL |
| `at` | when the injection happened, unix seconds |
| `mode` | what the store was allowed to search (`scoped`, `all`, `off`, or whatever the store reports) |
| `bytes` | length of the injected context block — the same byte bound retrieval applied, not a token count |
| `items_json` | the delivered records, as the provider described them: `id`, source path, title, and a snippet |

`items_json` is kept verbatim rather than normalised, because what a store calls
a "memory" is the store's business and Lectern only has to show it back. The
snippet is bounded at 300 characters, clipped on a UTF-8 rune boundary: the log
answers "was this the right memory", not "what did it say" — the record's own
source is a click away. A delivery the store described only by fingerprint (its
keys) still records an item per key, with the path half of `path#n` kept as the
source.

Four rules keep the log honest:

- **Only a delivery that carried something is recorded.** A lookup that matched
  nothing injected nothing, and logging it would make the section read as a
  stream of things the agent never saw.
- **Exactly one of `session_id` or `task_id`** identifies the run. The two
  delivery paths share the table and neither knows the other's id.
- **They are deliberately not foreign keys.** A delivery records something that
  *happened*; deleting a session, task or attempt must not erase the record of
  the memory that ran there.
- **A malformed `items_json` degrades, it does not fail.** It is parsed at the
  API boundary into an item list, so a bad blob costs the item descriptions and
  nothing else.

## Where a delivery comes from

`internal/sessions` records the interactive paths, `internal/scheduler` the
dispatched one:

- a launch whose opening message is passed as an argument;
- a launch that has to wait for the pane to settle (`primeWhenReady`) — recorded
  when the message actually lands, not when the launch races the pane;
- a message sent through the send API that retrieves new context (continuation
  and acknowledgement messages retrieve nothing and are not recorded);
- a non-reviewer task dispatch, before the recalled block is prepended to the
  attempt's prompt.

Each write publishes `memory.delivered` on `board` and on `session:<id>` or
`task:<id>`, so a view can update without polling.

## Seeing it

- `GET /api/sessions/{id}/memory` answers both directions of a session's
  memory: `deliveries` (what Lectern handed this session) and `changes` (what
  the session's agent then wrote back). One URL, because they are one question.
- `GET /api/tasks/{id}/memory` lists every attempt's deliveries, newest first.
- `?limit=` raises the default of 50 (maximum 200).

In the control panel the same list appears as a **Memory** section in the
session conversation and in the task detail, and each delivery is also an entry
on the task timeline — where it happened, before the attempt's first prompt.
Items expand to show the snippet.

## Correcting it

Two endpoints forward the operator's verdict to the memory store:

- `POST /api/memory/items/{id}/feedback` with `{"helpful": bool, "note": "…"}`
  → Grimoire `POST /api/memory/feedback`. "Helpful" and "Not relevant" are
  ranking feedback; **not relevant is not wrong**.
- `POST /api/memory/items/{id}/challenge` with `{"reason": "…"}`
  → Grimoire `POST /api/memory/challenge`. This marks a record as possibly
  wrong or superseded for review. Lectern never deletes the record: the claim
  and the objection are the store's to reconcile, side by side.

**Both require a human principal** — the same `CanDecide` rule approvals use
(`internal/auth`). An agent may read its memory and may not retune its own
recall, so a request from a local process or a tagged node gets `403` with a
message saying a signed-in person is needed. The frontend asks for the reason
for a challenge and refuses to send an empty one; the API rejects a blank
reason too, because a challenge without a reason is unreviewable.

Failures are surfaced with the store's own words rather than a bare status:

- `501` — the configured provider has no review surface (`memory.Reviewer`,
  implemented by the Grimoire client).
- `422` — `helpful` missing, or `reason` blank.
- `502` — the store refused; the response body is passed through as
  `grimoire feedback: …`.

Grimoire's answers are handled defensively — unknown fields are ignored and a
non-2xx body is reported verbatim — because Lectern does not own that API and
cannot see its source.

## What this does not do

- **A verdict does not change the delivery log.** The log records what happened;
  the store owns what it means.
- **Lectern does not re-rank or re-decide.** Feedback tunes the store's
  retrieval, not Lectern's. Nothing in Lectern short-circuits a challenged item:
  the store is expected to act on it (the challenge is precisely the message
  that lets it).
- **Deliveries are not replayed.** The 30-minute per-session delivered-
  fingerprint cache is unchanged, so a record delivered once is not re-sent
  within that window regardless of its verdict.
- **Nothing is deleted, and nothing is refused.** `internal/memory`'s delivery
  path has no new failure mode: a store that cannot record or review still
  injects context.

## Where this lives

| Piece | File |
|---|---|
| delivery log table | `internal/store/schema.go`, `internal/store/memory_deliveries.go` |
| item shape, parsing, snippet bound | `internal/memory/delivery.go` |
| feedback / challenge client | `internal/memory/feedback.go` |
| context mode and items from the store | `internal/memory/automatic.go` |
| session-side recording | `internal/sessions/memory_context.go`, `internal/sessions/manager.go` |
| task-side recording | `internal/scheduler/memory_delivery.go` |
| API | `internal/api/memory_deliveries.go`, `internal/api/session_memory.go`, `internal/api/server.go` |
| UI | `frontend/src/sessions/MemoryDeliveries.tsx`, `frontend/src/sessions/memoryDelivery.ts` |
