# Context parity

A dispatched agent is not the same as the agent in your terminal. It opens a
**fresh session** in a throwaway worktree, so it has no conversation history —
and on an `ssh`, `pct`, or `sandbox` target it also has none of the control
plane user's Claude config: no `CLAUDE.md`, no MCP servers, no memory.

Nothing announces that gap. The run just produces worse output.

These four settings close it. All are optional and default to off, so an
existing install behaves exactly as it did before.

| Setting | Where | Fixes |
|---|---|---|
| `context_paths` | target + project | agent doesn't know your conventions |
| `mcp` / `strict_mcp` | project | agent has fewer tools on remote targets |
| `permissions` | project | tools silently denied with no prompt |
| `memory_dir` | target | agent starts memory-blind every attempt |

## Staged context

Files listed in `context_paths` are read from the **control plane's** filesystem
at dispatch and copied into `<worktree>/.lectern/context/`, with a header
prepended to the prompt telling the agent to read them first. One place to
curate, identical result on every target kind. Globs are supported.

```bash
# every project on this target gets the house rules
curl -X PATCH .../api/targets/1 \
  -d '{"context_paths":["/home/you/CLAUDE.md","/home/you/docs/*.md"]}'

# plus something only this project needs
curl -X PATCH .../api/projects/3 \
  -d '{"context_paths":["/home/you/notes/schema.md"]}'
```

Target files are staged first, then the project's. Files are truncated at 256KB
and the bundle stops at 1MB — both cases are reported in the prompt and in
`.lectern/context/INDEX.md` rather than dropped silently. A path that doesn't
exist is reported too, so a typo shows up as a note instead of missing context.

On a `local` target you may not need this: Claude Code already discovers
`CLAUDE.md` by walking up from the worktree, which usually lands inside your home
directory. It is the remote targets that have nothing.

## MCP servers

The host user's MCP config doesn't exist on a remote target, so the same task
runs with strictly fewer tools there. Give a project its own:

```bash
curl -X PATCH .../api/projects/3 -d '{
  "mcp": {"myserver": {"command":"python3","args":["-m","myserver"]}},
  "strict_mcp": false}'
```

For interactive Claude and Codex sessions, the project declaration is applied
after the final worktree is selected, so fresh, resumed, and forked sessions
see the same servers. Claude receives a session-private
`.lectern/interactive/<session-id>/mcp.json` via
`--mcp-config`; `strict_mcp` additionally supplies `--strict-mcp-config`.
Codex receives additive `-c mcp_servers.<server>.<field>=<value>` overrides
before `exec`, while its normal `CODEX_HOME` remains intact (including auth,
history, instructions, and skills). A bare mapping is wrapped in `mcpServers`;
a full `{"mcpServers": {...}}` document is normalized once. Values are encoded
as argv/TOML rather than interpolated into logs or events. Unsupported agent
adapters do not claim MCP support. `strict_mcp` is rejected for Codex because
Codex's additive overrides cannot express replacement of its user config.

## Permissions

**This is the one that bites.** `claude -p` is headless: there is no prompt. A
tool the permission rules don't grant is simply denied, and the only trace is a
tool error the agent may or may not mention.

So `acceptEdits` — the default mode — lets an agent edit files but **not run
Bash**, unless you grant it:

```bash
curl -X PATCH .../api/projects/3 \
  -d '{"permissions":{"allow":["Bash(pytest*)","Bash(git status*)"],
                      "deny":["Bash(rm *)"]}}'
```

These are written to `.lectern/settings.json` for every mode. Accepted keys are
`allow`, `deny`, `ask`, `defaultMode`, `additionalDirectories`; anything else is
rejected with a 400 when you set it, rather than becoming a mystery denial later.

### Gated mode

`permission_mode: "default"` routes tool calls through the approval hook and onto
your phone. The matcher defaults to `*` — **every** tool. It used to be
`Bash|Write|Edit|MultiEdit|NotebookEdit`, which meant MCP tools, WebFetch, and
Task were never gated and therefore never *allowed* either. Narrow it per project
if you want fewer taps:

```bash
curl -X PATCH .../api/projects/3 -d '{"gate_matcher":"Bash|Write|Edit"}'
```

Anything the matcher excludes is denied, not allowed — narrow it deliberately.

## Shared memory (opt-in)

Claude Code keys its memory store by working directory, so a fresh worktree per
attempt starts memory-blind and everything an agent learns dies with the
worktree. Point a target's attempts at one shared store:

```bash
curl -X PATCH .../api/targets/1 \
  -d '{"memory_dir":"/home/you/.claude/projects/-home-you/memory"}'
```

At dispatch, lectern symlinks that attempt's session memory directory at your
store, and adds the store to `additionalDirectories` so the agent may actually
read it — the filesystem sandbox refuses paths outside the worktree, so without
that the store is linked and then unreadable.

Memory is keyed to the git **main worktree**, not to cwd. Sessions are keyed by
cwd, so the two diverge inside a worktree: a session running in
`repo/.lectern-worktrees/task9-a1` writes its transcript under that slug but
reads memory from `repo`'s. Verified by running the CLI inside a linked worktree
under `/tmp` and asking it for its own memory path. lectern resolves the main
worktree on the target (`git rev-parse --git-common-dir`) rather than guessing,
so worktrees, plain clones and sandbox checkouts all agree.

Because that path can be a repo you also use interactively, a **non-empty** real
directory at the link location is left alone and the attempt logs a warning
rather than replacing it.

> **Caveat.** There is no CLI flag for this, so it works by mirroring Claude
> Code's internal `~/.claude/projects/<slug>/memory` layout. That is not
> a public API and could change in a future release — which is why it is off
> unless you set it. A failed link logs a warning and never fails the run.
>
> If you'd rather not depend on internals, lectern's own project memory does a
> similar job through a supported path: agents call
> `python3 .lectern/lec.py add-note "..."` and the notes are prepended to every
> later prompt on that project.


## Capability profiles — the one setting that does all of it

Everything above is a dial. `capability_profile` is the preset, because the
defaults are not neutral: **headless `claude -p` has no prompt, so a tool nothing
granted is denied silently.** An agent dispatched with no rules cannot pipe a
shell command, cannot read a path outside its worktree, and cannot call a single
MCP tool — it just returns worse work and says nothing about why.

```bash
curl -X PATCH .../api/projects/3 -d '{"capability_profile":"parity"}'
```

| profile | what an agent gets |
|---|---|
| `restricted` (default) | only the rules you write yourself — unchanged behaviour |
| `parity` | the tools a terminal session has, the MCP servers this target can reach, and the shared memory store |

`parity` grants `Bash` bare rather than as prefix rules. A rule like
`Bash(git*)` makes Claude Code split compound commands and refuse the parts it
cannot match, so `ls | head` dies with *"This Bash command contains multiple
operations"* — which reads as the agent being broken rather than unpermitted.

Your explicit `permissions` still layer on top, and an explicit `deny` always
beats the profile, so a profile can never quietly re-grant something you refused.

### What parity does NOT do

It never copies your MCP server definitions to another machine. Those point at
local binaries and commonly carry live credentials in their `env` blocks, so on
`ssh`/`pct`/`sandbox` targets parity grants exactly the servers the project
ships in its own `mcp` config and nothing more. A local-target agent runs as the
control plane user and already inherits that user's servers — parity only gives
it permission to call them.

### Seeing what an agent actually has

```bash
curl .../api/projects/3/capability
```

Returns the resolved view — profile, reachable MCP servers, memory store,
granted tools — plus `notes` naming each gap it found, so a remote target with
no MCP says so instead of looking configured. The Targets tab renders the same
thing per project with a profile picker.
