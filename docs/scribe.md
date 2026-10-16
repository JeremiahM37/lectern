# Scribe

Scribe is Lectern's own coding agent. You run it as `lectern code` (the alias
`lectern scribe`). It works in three ways:

- **Terminal**: a full-screen interface in the folder you are in.
- **Headless**: one prompt from the command line or stdin, with text, JSON or
  JSONL output, for scripts and CI.
- **Inside a Lectern session**: Lectern starts it like any other agent. The
  dashboard, the phone and the approval queue then work with it the same way
  they work with Claude Code or Codex.

It is a separate program from the other agents, and it talks to the model
providers you configure directly. It does not need Claude Code, Codex or
Gemini installed.

## Quick start

```sh
export ANTHROPIC_API_KEY=...        # or any key from the table below
cd ~/code/my-project
lectern code                        # full-screen interface
lectern code "why does the build fail on CI?"   # the prompt is sent first
```

Without any key, Scribe can still run against a local Ollama server.

## `lectern code`

```text
lectern code [PROMPT] [flags]          full-screen interface; PROMPT is sent at once
lectern code -p PROMPT [flags]         one prompt, text answer on stdout
echo PROMPT | lectern code [flags]     prompt from stdin when stdin is not a terminal
lectern code --chat [flags]            line-based chat on stdin until EOF
lectern code --list-providers
lectern code --list-models [PROVIDER]
lectern code --set-key PROVIDER KEY
```

Flags for the interactive interface:

| Flag | Meaning |
|---|---|
| `--model PROVIDER/MODEL` | Model to use. Default: the config file, then the first provider with a key. |
| `--mode MODE` | `default`, `architect`, `socratic`, `challenge`, `plan` or `auto`. |
| `--cwd DIR` | Project folder. Default: the current folder. |
| `--resume ID`, `--resume` | Continue a saved session by id prefix; with no id, pick one. |
| `--continue`, `-c` | Continue the newest session in this folder. |
| `--demo` | Run with fake data, for trying the interface. |

Headless flags (used with `-p`, `--chat`, `--json` or `--events`):

| Flag | Meaning |
|---|---|
| `--json` | Print one JSON object for the run (chat: one per turn). |
| `--events` | Print JSONL events as they happen. |
| `--quiet`, `-q` | No progress on stderr. |
| `--max-steps N` | Stop after N model steps (exit 1). |
| `--timeout DURATION` | Abort after this long, e.g. `90s` or `10m` (exit 2). |
| `--system TEXT` | Extra instructions for this run only. |
| `--include FILE` | Prepend a file to the prompt. Repeatable. |
| `--no-repomap` | Skip the code map (also `SCRIBE_NO_REPOMAP=1`). |
| `--diff` | List the files this run edited (text mode). |
| `--save-session` | Keep the session so `--session ID` can continue it. |
| `--session ID`, `-s ID` | Continue a saved session (id prefix). |

Exit codes for headless runs: `0` done, `1` error or step limit, `2` timeout,
`130` interrupted.

## Keys

| Key | Action |
|---|---|
| `Shift+Tab` or `Ctrl+D` | Next mode |
| `Ctrl+L` | Model picker |
| `Ctrl+K` | Command palette |
| `Ctrl+N` | New session |
| `Ctrl+P` | Session picker |
| `Ctrl+G` | Git menu |
| `Ctrl+S` | Skills |
| `Ctrl+O` | Expand or collapse tool output |
| `Ctrl+T`, `Ctrl+W` | Open or close a tab; `Alt+1`…`Alt+5` switch tabs, `Alt+[` and `Alt+]` step through them |
| `Ctrl+B`, `Ctrl+F` | Step back and forward through checkpoints |
| `Ctrl+Z` | Undo the last turn and restore its files |
| `Esc` or `Ctrl+X` | Stop the running turn |
| `Alt+R` | Error log |

Type `/help` for the full list of keys and commands.

## Slash commands

Commands are grouped as the help screen groups them. Examples:

- **Session**: `/new`, `/sessions`, `/resume [id]`, `/fork`, `/rename [title]`,
  `/export [md|json]`, `/compact`, `/undo`, `/redo`, `/checkpoints`, `/plan`,
  `/auto`, `/default`, `/copy`.
- **Git**: `/git`, `/commit [message]`, `/diff [turn]`.
- **Models**: `/model [provider/model]`, `/models`, `/reasoning [off|low|medium|high]`,
  `/keys`.
- **Intelligence**: `/context`, `/cost`, `/memory`, `/skills`, `/tools`, `/mcp`,
  `/lsp`, `/map [rebuild]`.
- **Settings**: `/theme [name]`, `/verbose`, `/settings`.
- **Tabs**: `/tab [new|close|rename <name>|N]`, `/terminal [command]`, `/jobs`.
- **System**: `/init` (writes `SCRIBE.md` for the project), `/review` (reviews
  uncommitted changes), `/errors`, `/bug` (diagnostics for a bug report), `/quit`.

## Modes

- `default`: reads and searches freely. Edits, shell commands and other risky
  actions ask first.
- `auto`: tool calls are approved without asking.
- `plan`: read-only. Only read-only tools run; the model presents a plan for
  you to approve before anything is changed.
- `architect`, `socratic`, `challenge`: read-only as well. They ask for a
  design (architect), a set of options to choose from (socratic), or an
  objection review (challenge). They do not edit files.

Headless runs have no one to ask. In `default` mode they deny risky actions
unless the run is attached to a Lectern session (see below). Use `--mode auto`
when you want a script to edit files without a person.

## Configuration

Settings come from two JSON files. Project values override global values, one
field at a time.

- Global: `$SCRIBE_HOME/config.json`, or `$LECTERN_HOME/scribe/config.json`,
  or `~/.lectern/scribe/config.json`.
- Project: `<repo>/.scribe/config.json`.

Keys are never written to these files. See *Providers and keys*.

```json
{
  "model": "anthropic/claude-sonnet-5-5",
  "mode": "default",
  "reasoning": "medium",
  "compact_at": 0.8,
  "instructions": ["SCRIBE.md", "AGENTS.md", "CLAUDE.md"],
  "router": {
    "explore": "anthropic/claude-haiku-5-5",
    "code": "anthropic/claude-sonnet-5-5",
    "compact": "deepseek/deepseek-chat",
    "fallback": ["openai/gpt-5.4"],
    "max_agents": 3
  },
  "providers": [
    {
      "id": "local-vllm",
      "name": "vLLM on the LAN",
      "base_url": "http://gpu-box:8000/v1",
      "api_key_env": "VLLM_API_KEY",
      "models": ["qwen3-coder"]
    }
  ],
  "hooks": [
    {"event": "PreToolUse", "matcher": "^shell$", "command": "./scripts/check-command.sh", "timeout": 10}
  ],
  "mcp": [
    {"name": "docs", "command": "npx", "args": ["-y", "some-docs-mcp"]}
  ],
  "disabled_tools": []
}
```

Main fields:

| Field | Meaning |
|---|---|
| `model`, `mode`, `reasoning` | Defaults for new sessions. |
| `max_tokens` | Limit per response. |
| `router` | Models for subagent roles: `explore` (read-only), `code` (editing), `web_search`, `compact`, `title`; `fallback` is tried in order when the model keeps failing; `max_agents` is 2 to 8. |
| `providers` | Extra OpenAI-compatible endpoints: `id`, `name`, `base_url`, `api_key_env`, `models`, `headers`. |
| `hooks` | Shell commands run on agent events (`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `SessionStart`, `SessionEnd`, `PreCompact`, `Notification`). `matcher` is a tool-name regex for tool events. |
| `mcp` | MCP servers: stdio (`command`, `args`, `env`) or HTTP (`url`, `headers`). `disabled` keeps an entry without using it. |
| `instructions` | Files loaded into the system prompt. |
| `disabled_tools` | Tool names the model may not use. |
| `repo_map`, `memory`, `checkpoints`, `auto_compact` | Booleans, on by default. |
| `compact_at` | Fraction of the context window at which the conversation is shortened. Default `0.8`. |

## Providers and keys

Built-in providers, the environment variable each reads, and its default model:

| Provider | Environment variable | Default model |
|---|---|---|
| `anthropic` | `ANTHROPIC_API_KEY` | `claude-sonnet-5-5` |
| `openai` | `OPENAI_API_KEY` | `gpt-5.4` |
| `deepseek` | `DEEPSEEK_API_KEY` | `deepseek-chat` |
| `openrouter` | `OPENROUTER_API_KEY` | `anthropic/claude-sonnet-4.5` |
| `groq` | `GROQ_API_KEY` | `llama-3.3-70b-versatile` |
| `together` | `TOGETHER_API_KEY` | `meta-llama/Llama-3.3-70B-Instruct-Turbo` |
| `mistral` | `MISTRAL_API_KEY` | `mistral-large-latest` |
| `xai` | `XAI_API_KEY` | `grok-4` |
| `gemini` | `GEMINI_API_KEY` or `GOOGLE_GENERATIVE_AI_API_KEY` | `gemini-2.5-flash` |
| `fireworks` | `FIREWORKS_API_KEY` | `accounts/fireworks/models/llama-v3p3-70b-instruct` |
| `cerebras` | `CEREBRAS_API_KEY` | `llama-3.3-70b` |
| `nvidia` | `NVIDIA_API_KEY` | `meta/llama-3.3-70b-instruct` |
| `zai` | `ZAI_API_KEY` | `glm-4.6` |
| `moonshot` | `MOONSHOT_API_KEY` | `kimi-k2-0905-preview` |
| `minimax` | `MINIMAX_API_KEY` | `MiniMax-M2` |
| `lmstudio` | `LM_API_TOKEN` (optional) | local server |
| `llmgateway` | `LLM_GATEWAY_API_KEY` | `openai/gpt-4.1` |
| `ollama` | none; uses `OLLAMA_HOST` | local server |

Keys can come from the environment or from the key store:

```sh
lectern code --set-key anthropic sk-ant-...   # written to <scribe home>/keys.json
lectern code --list-providers                 # which providers have a key
lectern code --list-models anthropic          # models the provider reports
```

The key store is `keys.json` in Scribe's home directory. An environment
variable takes precedence over a stored key. Keys are kept out of the config
files.

## Headless and CI

Text answer:

```sh
lectern code -p "summarise the open TODOs in this repo" --quiet
```

One JSON object (the run's answer, steps, tokens, cost, tool calls and files
edited):

```sh
lectern code -p "add a unit test for parseDuration" --mode auto --json > run.json
jq -r '.output' run.json
jq -r '.filesEdited[]' run.json
```

JSONL events, one per line, as the run goes. Each line has a `type`:

```sh
lectern code -p "fix the failing test" --mode auto --events | tee run.jsonl
```

```jsonl
{"type":"start","model":"anthropic/claude-sonnet-5-5","mode":"auto","session":"…"}
{"type":"tool-call","tool":"read","toolCallId":"…","input":{"path":"parse_test.go"}}
{"type":"tool-result","tool":"read","summary":"…"}
{"type":"tool-call","tool":"edit_file","toolCallId":"…","input":{"path":"parse.go"}}
{"type":"text","content":"The test expected seconds, not milliseconds. Fixed."}
{"type":"done", …}
```

Other types: `ready` and `chat-done` (chat mode), `step` (token counts per
model step), `retry`, `warning`, `error`, and `session-saved` (with
`--save-session`). Read the field names from the output of a real run; the
JSON above is the shape, not a contract for every field.

Chat over stdin, one turn per line (a trailing `\` continues a line):

```sh
printf 'what does Makefile target lint run?\nnow run it\n' | lectern code --chat --mode auto --json
```

A CI job that wants to stop a runaway agent:

```sh
lectern code -p "$PROMPT" --mode auto --max-steps 40 --timeout 15m --json
```

## Inside a Lectern session

Lectern lists Scribe as a built-in agent named `scribe`. It appears in the
new-session pickers, in `lectern agent list` and in `lectern doctor`. It is
always installed, because it is the `lectern` binary itself. The launch command
is that binary with `code`, plus `--model` for the model, `--mode auto` when the
session runs without approval prompts, and `--continue` to resume the newest
Scribe session in the folder.

When Lectern starts a session, it gives the process two environment variables:
`LECTERN_HOOK_URL` (the session's hook endpoint) and `LECTERN_HOOK_TOKEN` (the
session's secret). Scribe checks for both at startup. If they are present, it
connects to Lectern (`internal/scribe/lecternbridge`):

- **Status**: the session is shown as working when a turn starts and idle when
  it ends. A failed turn is reported as an error. Tool calls appear in the
  session timeline, and each step updates the cost and the context gauge.
- **Approvals**: when Scribe needs approval for a risky action, it asks the
  local interface and Lectern at the same time. The first answer wins. A
  decision in the dashboard, on the phone or in the terminal ends the other
  request. A headless Scribe in a Lectern session waits for Lectern's answer
  rather than denying at once.
- **Outside Lectern**: nothing changes. Scribe does not contact any server
  unless you configured one.

Things that are not done yet:

- Resume uses `--continue`, which picks the newest Scribe session in the
  folder. Lectern does not record Scribe's own session id, so it cannot resume
  an exact conversation.
- Subagent activity is not sent to Lectern; only the main agent's turns and
  tool calls are.
- Questions the agent asks (`ask_user`) are answered only in the local
  interface.

## Files

- Source: `internal/scribe/` (`agent`, `app`, `config`, `headless`, `llm`,
  `lecternbridge`, `tools`, `tui`, and the rest); the CLI is `cmd/lectern/code.go`
  and `cmd/lectern/code_bridge.go`.
- Design notes: `internal/scribe/DESIGN.md`.
- Lectern's side of the hook contract: [Agent events](agent-events.md).
