# Plugins

A plugin is how Lectern is extended. It is not a second mechanism beside the
agent catalog, project MCP servers, skills and workflows: it is the package
those already fit into. The bundled workflows (Spec Kit, Maestro, Delegated
build) and the agent catalog ship as plugins inside the binary, so every
Lectern exercises the plugin path with real content.

Code: `internal/plugins/` (manifest, hashing, install, consent, hooks),
`internal/api/plugins.go`, `frontend/src/settings/Plugins.tsx`,
`cmd/lectern/plugin.go`. Store: `plugins`, `plugin_sources`,
`plugin_hook_runs`, and the content store `lectern-plugins/` beside the
database.

## The package

A plugin is a directory, or a git repository, with a `lectern-plugin.yaml` at
its root:

```yaml
id: acme.release-notes          # [publisher.]name, lowercase; "lectern." is reserved
name: Release notes
version: 1.2.0
description: Drafts release notes when a task finishes.
author: Acme
homepage: https://example.com/release-notes
license: MIT
min_lectern: 2.3.0

capabilities:                   # what the plugin may do; shown before install
  host_exec: true               # runs commands on the Lectern server
  target_exec: false            # runs commands on your machines
  network: [api.example.com]    # hosts it says it contacts (declared, not enforced)
  secrets: [RELEASE_TOKEN]      # secret values it reads; you set them in Settings
  mcp_tools: false              # adds MCP tools agents can call
  agents: false                 # adds agent presets to the catalog
  notify: true                  # hook results may send you a notification

contributes:
  hooks:
    - event: task.finished
      run: host
      command: [python3, hooks/notes.py]
      timeout: 30
  quick_commands:
    - {id: notes, label: Notes, text: "write release notes", enter: true}
  palette_commands:
    - {id: docs, title: "Release notes: open docs", href: "https://example.com/release-notes"}
```

Everything else is declarative. Code runs only as a declared hook, a declared
MCP server, a command an agent or person runs from a skill, or a
[mod](mods.md): a JavaScript module that hooks the web app and the terminal
console from a sandbox of its own.

### Contributions

| Section | Becomes | Needs |
| --- | --- | --- |
| `agents` | entries in Settings → Agents → Add from catalog (the same fields a catalog preset has) | `agents` |
| `mcp_servers` | MCP servers added to every agent launch that supports MCP, next to the project's own | `mcp_tools`, and `target_exec` for a `command` server |
| `skills` | a skill a project can turn on per agent, like a workflow without commands | — |
| `workflows` | Settings → Projects → Project workflows | — |
| `hooks` | a command run on a lifecycle event (below) | `host_exec` or `target_exec` |
| `sandbox_providers` | a `lectern.sandbox.yaml` a sandbox machine can use as its script provider | `host_exec` |
| `quick_commands` | commands in the terminal key row and Snippets sheet | — |
| `themes` | presets in Settings → Appearance | — |
| `palette_commands` | entries in the command palette that open a Lectern view or a link | — |
| `mods` | JavaScript that hooks events and draws in the web app and `lectern console` ([mods.md](mods.md)) | `mods`, and `api` to call Lectern's API |

`lectern plugin validate` refuses a manifest whose contributions need a
capability it does not declare, so the capability list is never a summary
the author could get wrong: it is what Lectern enforces.

Skills and workflows point at a directory holding `SKILL.md`:

```yaml
  skills:
    - {id: review-checklist, path: skills/review-checklist, description: "…"}
  workflows:
    - id: triage
      name: Triage
      path: workflows/triage
      description: "…"
      commands: ["acme-triage start", "acme-triage close"]
```

MCP servers use the same shape as a project's MCP servers.
`${LECTERN_PLUGIN_ROOT}` is the plugin's directory on the machine the agent
runs on; Lectern copies the plugin there first, into an immutable
content-addressed path. `${secret:NAME}` is a declared secret's value.

```yaml
  mcp_servers:
    notes: {command: python3, args: ["${LECTERN_PLUGIN_ROOT}/mcp/server.py"], env: {TOKEN: "${secret:RELEASE_TOKEN}"}}
    docs:  {type: http, url: "https://mcp.example.com"}
```

A plugin MCP server reaches every agent that has an MCP translation
([context-parity.md](context-parity.md)). An agent without one runs without it
rather than being refused: the plugin is optional, the project's own servers
are not. When a project and a plugin name the same server, the project wins.

Themes set colour tokens for either mode. Text tokens are lifted just enough
to stay readable on every surface, as a custom accent is:

```yaml
  themes:
    - id: forest
      name: Forest
      accent: "#2f855a"
      dark:  {bg: "#0b1410", panel: "#122019"}
      light: {bg: "#f3f7f4"}
```

### Hooks

| Event | When |
| --- | --- |
| `session.start`, `session.end` | an interactive session appears, or its process ends |
| `task.dispatched`, `task.finished` | an attempt starts running; it reaches review, done or failed |
| `approval.requested`, `approval.decided` | an agent asks for permission; it is answered |
| `ci.failed` | a watched PR's checks fail |
| `limit.hit` | an agent reaches its provider's usage limit |

The event is JSON on stdin: `{"event", "plugin", "time", "project_id",
"data"}` where `data` is the task, session, approval, CI watch or limit hold.
A hook prints one JSON object as its result:

```json
{"ok": true, "message": "notes drafted", "notify": "Release notes ready for task 12"}
```

Output that is not JSON is recorded as the first line, with `ok` from the exit
status. `notify` is sent through your notification channels when the plugin
declares `notify`. Every run is recorded (plugin detail → Recent hook runs).

- `run: host` runs on the Lectern server, in the plugin's own read-only copy,
  with a clean environment: `PATH`, `HOME`, `LECTERN_PLUGIN_ROOT`,
  `LECTERN_EVENT` and the plugin's declared secrets. Nothing else of the
  server's environment is passed.
- `run: target` runs on the machine of the project the event belongs to, in its
  repository, with the plugin copied there. An event with no project has no
  machine, so the run is recorded as failed.
- `timeout` defaults to 30 seconds, at most 300. Hooks run one at a time per
  plugin and never block the event that fired them.

Hooks observe. None can approve or deny a permission request.

## Trust

A plugin can run commands on your server and your machines, so installing one
is a decision a person makes, the same way trusting a sandbox hooks file is.

- **Preview first.** Install fetches the plugin, validates it, and shows its
  capabilities and contributions before anything is saved. Nothing runs during
  a preview.
- **A person consents.** Install, enable, update, trust, scope changes and
  secrets need a signed-in person (Tailscale identity, access token or paired
  device), like deciding an approval. A process on the server, the MCP server
  and every agent are refused. In `LECTERN_AUTH=none` there is no identity to
  tell a person from an agent, so plugin changes are refused there unless
  `LECTERN_PLUGINS_ALLOW_UNAUTHENTICATED=1` is set; reading and using plugins
  already installed still works.
- **Pinned to content.** The consent names the exact content hash — sha256 over
  every file's path, mode and bytes — and, for git, the commit and tree hash.
  The files live in `lectern-plugins/store/<hash>/`, never edited in place.
  Lectern re-hashes them when it loads a plugin and before every hook run; a
  mismatch marks the plugin **modified**, and nothing from it is used until a
  person looks at it and presses **Trust** again.
- **Updates need consent.** An update is a new preview showing what changed.
  When the capabilities grow, the new ones are marked **new**, and the consent
  must still name the whole list the preview showed: a client cannot accept
  one list and install another.
- **Limits.** At most 2000 files and 50 MB. Symlinks, absolute paths and `..`
  are refused. Git URLs must be `https://`, `ssh://`, `git@host:path` or
  `file://`, so git's `ext::` transports cannot run. Plain `http://` is
  refused; only the test suite can allow it, to this machine.
- **Bundled plugins** are trusted as part of the binary. They can be disabled
  but not removed.

Existing configuration is not a plugin and needs no consent: custom agents
from Settings → Agents and a project's own MCP servers are listed on the
Plugins page as **local** contributions and keep working exactly as before.

### Sandbox providers

A sandbox machine uses a plugin's provider by setting its script provider's
hooks path to `plugin:<plugin id>/<provider id>`. The file is the consented
one, so it needs no separate Trust; turning the plugin off or changing its
files stops the provider.

### Scope

A plugin is enabled everywhere or for chosen projects. Project scope applies to
the contributions that belong to a project — MCP servers, skills, workflows,
hooks and quick commands. Agents, themes, palette commands and mods are global.

Turning a plugin off stops its MCP servers, hooks, quick commands, themes and
palette commands at once, and turns its skills and workflows off in every
project they were on; the confirmation lists them first. A skill file someone
changed is kept and reported, as Project workflows' own switch does. Turning
off the bundled agent catalog empties "Add from catalog" (agents already
added keep working), and the confirmation says so.

A session with no project (a scratch session) gets the MCP servers of plugins
enabled everywhere, never those of a plugin scoped to chosen projects.

## Installing

Settings → Plugins, or the CLI:

```bash
lectern plugin search [QUERY]                # every source's index
lectern plugin install ./my-plugin           # a directory
lectern plugin install https://github.com/acme/notes.git --ref v1.2.0
lectern plugin install acme.release-notes    # by id, from a source
lectern plugin list
lectern plugin update acme.release-notes [--accept]
lectern plugin enable|disable ID [--project ID ...]
lectern plugin trust ID                      # re-consent after a change
lectern plugin remove ID
lectern plugin secret ID NAME                # value on stdin; empty removes it
lectern plugin source add NAME URL [--ref REF] | source list | source remove NAME
```

`install` and `update` print the preview and ask before consenting; `--yes`
skips the question in a script. Both need a person, so an agent running the
CLI is refused.

A **source** (marketplace) is a git repository with a `lectern-plugins.yaml`:

```yaml
name: acme
plugins:
  - id: acme.release-notes
    description: Drafts release notes when a task finishes.
    source: https://github.com/acme/release-notes.git
    commit: 3f2a…                     # the exact commit to install
```

Lectern installs the listed commit, never a moving branch; an entry without a
full commit sha is refused. `path` names a plugin in a subdirectory, and
`source: ./dir` a plugin inside the source repository itself.

### Claude Code plugins

A directory or repository with `.claude-plugin/plugin.json` and no
`lectern-plugin.yaml` installs too. Lectern reads its `skills/` (as skills),
its `.mcp.json` or `mcpServers` (as MCP servers, `${CLAUDE_PLUGIN_ROOT}`
meaning the plugin root) and its identity, derives the capabilities, and shows
them like any other preview. Its skills and servers then reach every agent
that supports them, not only Claude Code. A Claude marketplace
(`.claude-plugin/marketplace.json`) works as a source: plugins in its own
repository (`"source": "./plugins/x"`) are pinned to the marketplace commit,
and GitHub or git entries are listed only when they name a `sha`.

Claude Code `commands/`, `agents/` (subagents) and `hooks/` are not imported:
they are Claude-specific, and a Claude hook runs inside the agent where
Lectern's consent cannot see it. The preview says what was skipped.

## Authoring

```bash
lectern plugin new acme.hello        # scaffold ./acme.hello
lectern plugin validate ./acme.hello # the checks install makes
lectern plugin install ./acme.hello
```

`examples/plugins/hello/` is a complete plugin: a skill, a quick command, a
theme, a palette command and a `task.finished` host hook that writes a line to
its log and returns a notification.

Tips:

- Declare the fewest capabilities that work. A reader sees the list first.
- Prefer a skill or an MCP server to a hook. Agents then choose when to use it,
  inside the approval gate.
- Keep hooks fast. They run in the background, one at a time, with a timeout.
- Bump `version` for a release. The content hash changes with any edit anyway.

## What is not a plugin (yet)

- **Triggers** (GitHub, Linear, Slack, Jira) and **tracker adapters** are Go
  code with credentials and polling state; a manifest cannot express one.
- The **memory provider** (Grimoire) is configured by environment.
- Plugin hooks cannot decide approvals. An approval policy contribution is a
  separate, riskier capability and was left out.
