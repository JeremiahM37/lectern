# Run a shared server

New to Lectern? Start with [Getting started](getting-started.md): install,
`lectern up`, and you are talking to an agent in a minute, with no server to
set up.

This page is for one Lectern server that several people or machines use, and
that drives agents on other machines over SSH.

## 1. Run the server

Install `lectern` as in [Getting started](getting-started.md) (or build it from
source: see [Local runtime](local.md#build-from-source)), then:

```bash
export LECTERN_AUTH_TOKEN="$(openssl rand -hex 32)"   # keep it in a private env file
LECTERN_HOST=0.0.0.0 lectern serve                     # http://<host>:9110
```

Without a sign-in, `lectern serve` listens on 127.0.0.1 only and refuses a
network address; with Tailscale running, Tailscale identity is used instead of
a token (see [Remote access](remote-access.md#where-lectern-listens)). The
database goes to `~/.local/state/lectern/server/lectern.db` unless
`LECTERN_DB` says otherwise.

Try it with fake agents first (no infrastructure needed):

```bash
LECTERN_MOCK=1 lectern serve
```

## 2. Register a target

A target is any machine that runs agents (the server's own machine is the
`local` target). It needs `git` and a coding-agent CLI
(`claude`), plus either the `lectern` binary (always true of the `local`
target) or `tmux` and `python3` (docs/ptyhost.md). It is reachable one of these
ways:

| kind | reaches | `host` field |
|------|---------|--------------|
| `local` | the control-plane host | — |
| `ssh` | anything with sshd (key auth) | hostname/IP |
| `pct` | a Proxmox LXC on this node (no SSH) | container vmid |
| `sandbox` | a **fresh ephemeral LXC per task** | template vmid |

Settings → Machines does the same in the browser. From a shell:

```bash
curl -H "Authorization: Bearer $LECTERN_AUTH_TOKEN" -X POST http://localhost:9110/api/targets \
  -d '{"name":"box1","kind":"ssh","host":"10.0.0.5","user":"dev","key_path":"~/.ssh/id_ed25519"}'
curl -H "Authorization: Bearer $LECTERN_AUTH_TOKEN" -X POST http://localhost:9110/api/targets/1/check     # probe toolchain + auth
```

## 3. Register a project (a git repo on a target)

```bash
curl -H "Authorization: Bearer $LECTERN_AUTH_TOKEN" -X POST http://localhost:9110/api/projects -d '{
  "name":"myrepo","target_id":1,"repo_path":"/home/dev/myrepo",
  "verify_cmd":"pytest -q"}'          # auto-run after each attempt, badges the card
```

## 4. Dispatch — or just open the board

Open `http://<host>:9110` (enter the token once, or pair your phone from Settings →
Connect your phone), type a task in the quick bar, hit enter.
Or via API:

```bash
curl -H "Authorization: Bearer $LECTERN_AUTH_TOKEN" -X POST http://localhost:9110/api/tasks -d '{
  "project_id":1,"title":"Add /health","prompt":"Add a health endpoint returning {ok:true}",
  "permission_mode":"acceptEdits"}' | jq .id
curl -H "Authorization: Bearer $LECTERN_AUTH_TOKEN" -X POST http://localhost:9110/api/tasks/1/dispatch -d '{}'
```

The agent works in an isolated git worktree; you watch the live timeline, review
the diff on your phone, and mark it done — or drag the card to the done column.

## Approvals from your phone

Use `"permission_mode":"default"` and the agent's risky tool calls block on a
push notification. Configure a sink in Settings: web-push, a Discord
webhook, or ntfy (whose notifications carry ✅/⛔ buttons — decide from the
lock screen).

## Local / alternative models

Point a project's `env` at any Anthropic-compatible endpoint:

```json
{"env": {"ANTHROPIC_BASE_URL": "http://ollama:11434",
         "ANTHROPIC_AUTH_TOKEN": "ollama"}}
```

Then dispatch with `"model": "<any served model>"`. (Agentic quality tracks the
model — small local models may respond conversationally instead of editing.)

## MCP

`lectern mcp` speaks MCP on stdio, so any MCP client can file and steer tasks.
It is a client of the HTTP API, so point it at a running control plane — local or
remote. Register with Claude Code:

```bash
claude mcp add lectern --env LECTERN_API=http://localhost:9110 -- /usr/local/bin/lectern mcp
# non-default host, or a token-protected instance:
#   claude mcp add lectern --env LECTERN_API=https://lectern.example.com --env LECTERN_AUTH_TOKEN=… -- /usr/local/bin/lectern mcp
```

## Deploy for real

`deploy/` has a `Dockerfile`, `docker-compose.yml`, and a `systemd` unit. Put it
behind a reverse proxy with auth; set `LECTERN_BASE_URL` to an address your
phone can reach (approval callbacks and ntfy buttons use it).
