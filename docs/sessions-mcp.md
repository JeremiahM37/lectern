# Handing a session off from a brainstorm

Claude Code and Codex sessions are good at a certain kind of conversation:
"let's figure out how this should work" with nothing being built yet. That
conversation has no worktree, no long-running agent, and (usually) no
Lectern session of its own — it is just chat. The three tools here exist so
that conversation can end with "start a Lectern session that builds this" or
"give my open session this file/context", without the operator having to
open Lectern, find the project, and retype what was just worked out.

All three are on the `lectern` MCP server (`internal/mcp/tools.go`), next to
`create_task`/`delegate_build` (queued, unattended work) and
`claim_work`/`active_work` (coordination). These are about the **interactive**
board — a session someone is going to watch and steer.

| Tool | Does |
|---|---|
| `list_sessions` | interactive sessions across every project: agent, status, project, whether it's waiting for input, sorted most recently active first |
| `start_session` | starts a NEW interactive session, optionally handing it files, freeform context, and Grimoire notes before its first message |
| `send_to_session` | sends a message (and optionally more files/context/notes) into an ALREADY-RUNNING session, found by id or by name |

## `start_session`

Give exactly one of `project` (a name, see `list_projects`), `workdir` (an
absolute path), or `scratch: true`. `prompt` is the build instruction.

With nothing else, this is just `POST /api/sessions` with `prime: prompt` —
the agent is primed the moment it comes up, same as launching from the UI.

With `context`, `files`, and/or `notes`, the sequence is different, because a
session cannot be told about an attachment until its workdir exists and the
agent is actually running:

1. create the session with **no** `prime`;
2. poll `GET /api/sessions/{id}` until it is `waiting` or `idle` (not
   `starting`, and not mid-worktree-setup) — up to ~180s, failing clearly if
   setup fails or the session dies first;
3. upload each local file, a generated `lectern-context.md` (the `context`
   text), and each Grimoire note, via `POST /api/sessions/{id}/attachments`;
4. send ONE message: `prompt`, then a "Context files:" list of the absolute
   paths the attachments endpoint staged them at.

When `context` is given, it is also saved as a Grimoire note by default
(`save_context_to_grimoire`, tagged `lectern,brief`, titled `<name or a
prompt summary> — brief`) so the brief the brainstorm produced outlives this
one session — the note's path comes back in the result. Grimoire being
unreachable never fails the tool: the session still starts and the message
still sends, with `grimoire_error` explaining why the note wasn't saved.

`files` are **local paths on the machine the MCP server itself runs on** —
they are read and uploaded, not shipped by reference. `notes` are Grimoire
vault paths (from `search_notes`/`list_notes` on the Grimoire MCP tools),
fetched and re-uploaded as attachments so the agent can read them the same
way as any other file, without needing its own Grimoire access.

## `send_to_session`

`session` is a session id, or its exact/unique name. A name that matches more
than one open session, or none, errors with the candidates — call
`list_sessions` and retry with the id. `message` is required unless
`files`/`notes`/`context` are given (a pure attachment drop is fine).
`interrupt: true` presses Escape first, for "stop what you're doing and look
at this" instead of queuing behind the current turn. Refused for a session
that has ended.

## Example

A brainstorm session (Claude Code or Codex, no Lectern session of its own)
that just designed a caching layer:

> **You:** OK I think we've got it — write that up and start a Lectern
> session in the `librarr` project to build it.
>
> **Agent:** *(calls `start_session` with `project: "librarr"`, `prompt:
> "Add the two-tier cache described in context.md"`, `context: "<the
> design we just worked out, as markdown>"`)*
>
> Started session #58 (`waiting`), context saved as a Grimoire note at
> `lectern/two-tier-cache-brief.md`. `lectern attach session 58` to watch it.

Later, in a different window, watching that session stall on an edge case:

> **You:** tell it about the retry logic we just found in the ticket
>
> **Agent:** *(calls `send_to_session` with `session: "58"`, `message: "the
> retry semantics are in this thread"`, `context: "<summary>"`)*
