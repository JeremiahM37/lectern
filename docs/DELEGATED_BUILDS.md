# Delegated builds

**Off by default.** When it is on, a lead session plans a substantial change
and reviews the result, and a cheaper worker agent does the implementation in
between, as a Lectern task in its own worktree. The lead never edits during
the build and never watches it: it writes a brief, waits once, reads the diff
and the worker's report once, then accepts or sends one round of findings back.

The workflow is adapted from
[astra-flash-orchestrator](https://github.com/ethanplusai/astra-flash-orchestrator)
(MIT; its skill, references and templates are vendored unmodified under
`internal/workflows/bundled/delegate/upstream/`). What Lectern changes is the
transport. There the worker is a native Codex subagent reached through a
router; here it is a **task**, which is what Lectern already knew how to run:

- the worker runs in a git worktree on the project's target, so the lead's own
  tree is untouched until it accepts;
- the diff is reviewable on its own (`task_diff`), with the worker's completion
  report next to it;
- a correction cycle (`request_changes`) resumes the same worker in the same
  tree;
- accepting brings the branch into the lead's checkout as uncommitted changes
  (`accept_build` with `mode: apply`, the default) or as a merge commit.

Why a task and not a native subagent: on a ChatGPT login, Codex refuses to run
a non-OpenAI model as a subagent at all, and the router that works around it
does so by signing Codex out, which loses the native model for the lead. A
Lectern task has neither problem, and it works the same for a Claude Code lead.

## Turning it on

Settings has one banner above the tabs, **Delegated builds**, with the state
in large type. It cannot be missed and it cannot be on by accident:

1. **Set up the worker.** Either pick any registry agent that has a task
   definition, or use the preset: *DeepSeek Flash* installs a `flash-builder`
   agent that runs Codex against DeepSeek's Responses API with the Flash model,
   configured with `-c` flags only, so your own `~/.codex/config.toml` is never
   touched. The API key is stored masked in the agent registry.
2. **Check the worker answers.** One small real request through the same
   command a task would run.
3. **Turn the switch on.** Enabling is refused until a runnable worker is
   chosen.
4. **Install the lead skill** (`lectern-delegate`) into the projects where a
   lead should know the workflow, for Claude Code or Codex, from the same card.
   It is the third bundled workflow, next to Spec Kit and Maestro.

API: `GET/PUT /api/delegation`, `POST /api/delegation/preset`,
`POST /api/delegation/check`.

## What the lead calls

On the `lectern` MCP server:

| Tool | Does |
|---|---|
| `delegate_build` | files the brief as a task for the worker, starts it, waits; returns the report, branch and diff stats, or `done:false` at its timeout |
| `wait_build` | one blocking wait on a task; not polling, nothing is reported in between |
| `task_diff` | the patch, per file |
| `request_changes` | one consolidated correction cycle; the same worker resumes |
| `accept_build` | marks the task done and integrates the branch into `workdir` (apply or merge) |

`delegate_build` is refused while the feature is off, with the reason.

Codex closes an MCP tool call after 60 s by default; give the `lectern`
server `tool_timeout_sec = 3600` so a wait can last a build. Claude Code reads
`MCP_TOOL_TIMEOUT` (milliseconds) from its environment.

## Worker agents

A worker is any registry agent with a `task` definition. Two fields were added
for wrappers of a known CLI:

- `task.output_mode: "codex"` or `"claude"` parses the wrapper's stream as
  that CLI's, so the timeline and the completion report read as they do for
  the built-in agent;
- `task.resume_args`, e.g. `["resume", "{id}"]`, lets a follow-up continue the
  worker's own conversation instead of starting a fresh process.

## Measured

See the README's *Delegated builds* section for the benchmark: three real
tasks in three repositories, graded by hidden acceptance tests and the
repositories' own suites, comparing the all-lead baseline, the upstream
workflow, and Lectern's.
