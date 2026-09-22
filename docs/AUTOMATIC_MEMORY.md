# Automatic project memory

## New projects get their own durable location

Creating, importing, or promoting a project allocates a random, persistent
`memory_topic` in Lectern's database. With automatic Grimoire memory enabled,
Lectern creates `memory/<memory_topic>.md` in the vault. Renaming the project
does not move its memory; similarly named projects cannot collide. The project
response exposes `memory_topic` and `memory_status` (`ready`, `unavailable`, or
`disabled`). A network failure does not discard the project or pretend that
setup succeeded: retry `POST /api/projects/{id}/memory` when Grimoire is back.
Retries never overwrite an existing note. Deleting a project retains its memory.

New projects read their own managed note by default. Explicit per-project paths
add other references alongside it. Existing projects keep their configured or
legacy paths; no bulk migration is performed. Manual/off policy performs no
provisioning or automatic reads. New launch/task prompts include a short memory
destination hint, and requested session handoffs write to that same topic with
topic-scoped reconciliation. Memory writes still require an explicit agent or
handoff action; ordinary prompts are not recorded automatically.

The default 2,400-byte retrieval ceiling does not include the short, once-per-
launch/task destination hint. Preview a project's brief to inspect context and
setup/lookup status before launching.

Lectern remains useful without Grimoire. When `LECTERN_GRIMOIRE_URL` is
configured, the optional provider can supply project knowledge automatically
without delegating that decision to the agent.

The default `LECTERN_GRIMOIRE_CONTEXT_MODE=project` narrows retrieval to
the assigned project's managed note. For older projects without a managed
topic it uses conventional paths: `memory/<slug>.md`,
`memory/<slug>/`, and `Agent Memory/project_<underscore_slug>.md`.
It does not search unrelated notes that merely mention the project.

Override mappings with `LECTERN_GRIMOIRE_CONTEXT_PROJECTS`, a JSON object
keyed by the exact registered project name:

```json
{
  "kestrel": {
    "mode": "scoped",
    "paths": ["memory/kestrel.md", "Projects/Kestrel/"],
    "max_bytes": 1800
  },
  "experiment": {"mode": "manual"}
}
```

Directories require a trailing `/`; other paths match exactly. A missing scoped
mapping never falls back to the whole vault. `all` permits the whole readable
corpus, while `manual` or `off` makes no automatic request. Per-project settings
override the global mode. Explicit MCP calls remain available separately.

## Delivery and cost

- Non-reviewer task dispatch uses the assigned project and actual task prompt.
- Interactive launches/resumes get a small project-start briefing even without
  `brief: true`. Existing repo-document and local-handoff briefings are separate.
- Messages sent through Lectern's send API request relevant project context;
  continuation/acknowledgement messages are skipped.
- Unassigned scratch sessions get no project memory.
- Each automatic response is bounded to 2,400 UTF-8 bytes by default (maximum
  8,000) and five items. These are byte bounds, not exact model token counts.
- Grimoire performs lexical retrieval without LLM or embedding calls. It returns
  current accepted facts plus note excerpts, excluding private/untrusted content.
  Scope filters never grant access beyond the caller's permissions.
- Delivered fingerprints are cached per interactive session for 30 minutes.
  Revised text has a new fingerprint. Failed sends are not marked delivered.
  Restarting Lectern resets this cache.
- Retrieval has a 1.5-second context deadline. Failure/older Grimoire versions
  return no recalled content; launches report unavailability briefly. There is
  no unscoped legacy fallback.
- No forced reflection turn or automatic transcript recording is introduced.
  Provisioning creates an empty note; requested handoffs use its stable topic.

Direct keyboard input in an attached terminal does not pass through Lectern's
send API, and Lectern cannot observe a host's context compaction. For per-prompt
coverage there, Grimoire ships a native Claude Code/Codex command hook. Install
it separately with the same explicit project scope; avoid overlapping delivery
paths if duplicate context is unwanted. Lectern does not silently modify host
hook configuration or send its Grimoire administrative credential to agents.

This integration is not a guarantee of semantic recall or agent compliance.
The conservative lexical pass can miss paraphrases. Explicit MCP lookup remains
appropriate when context is missing, stale, or insufficient. Operational facts
still require live verification.

## One key for a session, on both sides

Lectern knows a session by its row. The memory store used to know the same
session, at best, by its display name — which repeats and can be edited — for
the one write Lectern made itself at a handoff, and by nothing at all for what
the agent remembered on its own. So neither side could answer "what did this
session learn".

Every session now starts with its identity in its environment:

| Variable | Read by | Value |
|---|---|---|
| `LECTERN_SESSION_ID` | `lectern post`, `live`, `expose`, and the Lectern MCP tools | the session id |
| `GRIMOIRE_SESSION` | Grimoire's MCP server, which stamps it on every `remember` | `lectern-s<id>` |

Lectern's own handoff write uses the same `lectern-s<id>`, and
`GET /api/sessions/{id}/memory` reads back what the store recorded under it —
learned, changed (with what was replaced), retracted. **Sessions → Memory** on a
card shows it. The status is explicit, because an empty list has three causes:
`empty` (nothing written under this key), `unavailable` (the store did not
answer), `disabled` (no provider).

Two limits worth knowing. A session launched before this existed carries no
key, so its writes are under no session. And the variable has to reach the
memory server: Claude Code passes its environment to the MCP servers it starts,
but **Codex gives them a fixed short list**, so it must be told to forward
these. In `~/.codex/config.toml`:

```toml
[mcp_servers.grimoire]
env_vars = ["GRIMOIRE_SESSION"]

[mcp_servers.lectern]
env_vars = ["LECTERN_SESSION_ID", "LECTERN_BASE_URL", "LECTERN_API", "TMUX", "TMUX_PANE"]
```

Without the second block a Codex agent's Lectern tools cannot tell which
server launched them and fall back to a private local runtime.

An id taken from the environment is a hint: inherited from a session of some
other Lectern it names nothing here, and the post or view is simply left
unattributed. An id someone typed (`--session`, `session_id`) is a claim, and an
unknown one is an error.

## Where a project's memory actually is

`GET /api/projects/{id}/memory` reports the paths retrieval reads for a project,
how they were chosen, and whether anything is there:

- `managed` — the project has its own provisioned note (`memory/<topic>.md`). Exact.
- `configured` — the operator named the paths in `LECTERN_GRIMOIRE_CONTEXT_PROJECTS`.
- `guessed` — derived from the project's name: `memory/<slug>.md`, `memory/<slug>/`
  and `Agent Memory/project_<slug>.md`.

A guessed link whose paths are all missing is `unlinked`. Retrieval still runs,
finds nothing, and the project looks as though it simply has no memory — the
report is what makes that visible. File a note under one of the listed paths, or
name the real ones in the configuration, to link it.

