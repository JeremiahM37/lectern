# Scribe — Lectern's own coding agent

`lectern code` (alias `lectern scribe`) starts Scribe: a coding agent that
runs in the terminal (full-screen TUI), headless (CI / scripts), or inside a
Lectern session (so the dashboard, phone and approvals work with it like any
other agent). It is a clean-room re-implementation of the feature set of
Empryo / SoulForge. The behavioural specs live in
`/mnt/bulk/empryo-ref/spec/0[1-6]-*.md`. **Never copy code from
`/mnt/bulk/empryo-ref/Empryo` (BSL-licensed); Lectern is MIT.** Read the spec,
then write original Go.

## Packages (all under `internal/scribe/`)

| Package | Owns | Contract |
|---|---|---|
| `llm` | provider-neutral messages, tool specs, streaming events, error classes | `llm/types.go` |
| `llm/anthropic`, `llm/openai` | provider adapters (`llm.Provider`) | SSE over `net/http`, no SDKs |
| `llm/registry` | built-in provider table, key store, model lists, pricing, `Resolve("provider/model")` | |
| `tools` | `Tool` interface, `Registry`, `Env`, `Result`, `Host` | `tools/tool.go` |
| `tools/fs` | read, write_file, edit_file, multi_edit, undo_edit, list_dir, glob, grep | |
| `tools/exec` | shell (foreground + background jobs), project (detect + run test/lint/typecheck/build/format), git | |
| `tools/web` | web_search (SearXNG or DuckDuckGo HTML), fetch_page | |
| `repomap` | code map: scan, symbols, references, PageRank, render, search, impact | |
| `lsp` | generic LSP client over stdio + `navigate`/`diagnostics` tools | |
| `symedit` | symbol-addressed editing (`ast_edit`), rename_symbol | |
| `session` | session store, checkpoints (per-turn file snapshots, undo/redo), prompt history | |
| `memory` | per-project + global memory (SQLite FTS5), recall, `memory` tool | |
| `agent` | the loop, modes, injects, steering, compaction, retry/fallback, cost, subagents (`dispatch`), plan/ask_user/task_list tools | `agent/events.go` |
| `config` | config load/merge/save | `config/config.go` |
| `hooks`, `skills`, `mcp` | user hooks, skills, MCP client | |
| `headless` | `--headless` runner: text / `--json` / `--events` JSONL, exit codes | |
| `tui` | bubbletea full-screen app | spec 01 + 02 |

Dependency direction: `llm` and `tools` depend on nothing in scribe except
`llm`. Feature packages depend on `llm`, `tools`, `config`. `agent` depends on
feature packages. `tui` and `headless` depend on `agent`. Nothing imports
`tui`.

## Conventions

- Go 1.25, module `github.com/JeremiahM37/lectern/v2`. Toolchain is
  `/usr/local/go/bin/go` (add it to PATH). Stdlib first; already-vendored deps
  are fine (`bubbletea`, `bubbles`, `lipgloss`, `x/ansi`, `modernc.org/sqlite`,
  `yaml.v3`, `uuid`). Ask the orchestrator before adding a dependency.
- Must build for linux, darwin and windows with `CGO_ENABLED=0`.
- Every package ships real tests (`go test ./internal/scribe/...`). Prefer
  tests against real files, real git, real processes, `httptest` servers that
  speak the real wire format. Mocks hide the real bugs.
- Paths shown to the model and the user are relative to the workdir.
- Tools never panic; errors become `tools.Result{IsError:true}` with a message
  that tells the model what to do next.
- User-visible strings are plain and specific. No forge/soul flavour text:
  Scribe has its own voice (calm, short). Do not reuse Empryo's names
  (Forge, Spark, Ember, Soul Map, SoulForge); ours are Scribe, explore agent,
  code agent, code map.
- Run `gofmt -l` and `go vet` on your package before reporting.
- Never run `pkill`/`killall` by name, never kill tmux servers or sessions you
  did not create, never restart `lectern.service`.
