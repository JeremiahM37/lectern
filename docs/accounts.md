# Accounts — several logins of one agent

An account is one signed-in login of an agent CLI on a machine. With more
than one, the **swap** usage-limit policy ([rate-limits.md](rate-limits.md))
moves an agent that hits its limit to the next free account and continues
the same conversation there. It is off by default.

Package: `internal/accounts` (CLI layouts, selection). Swap: `internal/limits/swap.go`
(the hold), `internal/sessions/account_swap.go` (the restart),
`internal/scheduler/limits.go` (tasks). Store: `agent_accounts`.

> Only add accounts that are your own. Whether you may use several accounts,
> and switch between them when one runs out, is up to your providers' terms;
> check them. Lectern does not work around any limit: each account keeps its
> own usage and resets.

## Adding one

Settings → **Accounts**, or:

```bash
lectern account add claude work              # on the default machine
lectern account add codex spare --machine box
lectern account login 3                      # sign it in, in a terminal
lectern account list
lectern account remove 3
```

Supported: Claude Code, Codex and Gemini CLI. Each account is its own config
directory on the machine, `~/.lectern/accounts/<agent>/<label>` (or `--dir`
to adopt one you already have), created with mode 0700, and passed to the CLI
as `CLAUDE_CONFIG_DIR`, `CODEX_HOME` or `GEMINI_CLI_HOME`. The first account
of a CLI also registers the login it already uses as **Default**, so the
rotation includes it.

**Sign in** opens a terminal running the CLI's own login (`claude auth login`,
`codex login`, `gemini`) with that directory set. Run the agent once there
afterwards so it can finish its first-run questions; an account in a new
directory also has none of your default login's settings, MCP servers or
trusted folders until you add them.

Lectern never reads what is inside an account directory. It checks only that
the credential file exists (`test -s`), to show "signed in" and to refuse to
swap to an account that is not. The directory path stays on the server: the
web app, relay clients and the chat connector see the label only, and
Lectern's log names an account by its id.
Removing an account forgets it; its directory and login stay on the machine.

## What the page shows

For each account: whether it is signed in, whether it is limited and until
when, and its usage as the CLI last reported it: Claude's statusline windows
and Codex's rollout windows, from the most recent session that ran under it.
Session cards show their account when a CLI has more than one on that machine.

## How a swap works

1. The next account in rotation order that is not limited is chosen. An
   account counts as limited until the reset Lectern last saw for it (5 hours
   when no reset was named), or while its reported usage is at 100%.
2. The conversation is copied into that account's directory, at the same
   place the CLI keeps it: Claude `projects/<folder>/<id>.jsonl` (and the
   `<id>/` folder beside it), Codex `sessions/YYYY/MM/DD/rollout-…-<id>.jsonl`.
   A copy, not a link; the newest copy moves each time.
3. The agent's terminal is stopped and relaunched in the same workspace, with
   the same tmux name, under the new account with `--resume <id>` / `resume <id>`.
4. Once it shows a prompt, the usage-limit nudge is typed and the resume is
   verified exactly like a wait-and-resume. The phone gets one push naming the
   account it moved to.

If no account is free, the policy's `then` applies. A task continuation is a
new attempt under the next account, resuming the attempt's conversation in the
same worktree.

## Checked against the real CLIs

`internal/accounts/real_cli_test.go` (opt in with `LECTERN_REAL_CLI_TEST=1`,
no login needed): Claude Code 2.1.283 and Codex 0.157.0 each write a
conversation into account A with an invalid key, Lectern's copy puts it in
account B, and resuming it by id under B loads it (the CLI appends to B's
copy), while an unknown id gets the CLI's own "No conversation found with
session ID" / "no rollout found for thread id". Both CLIs find a conversation
by id anywhere in their tree, whatever the current directory; Codex rebuilds
its thread cache from the rollout. What was not checked: a resume that
reached a real model under a second real login.

## Limits

- Claude Code and Codex conversations move. Gemini accounts can be added and
  signed in, and a limited Gemini task continues under the next account from
  its task prompt, but a Gemini session is not swapped: Lectern does not
  capture Gemini conversation ids, and Gemini files chats under a per-home
  project registry.
- A session needs its conversation id (captured from the running process, or
  the id it was resumed with). Sandboxed sessions and attempts are not swapped.
- The terminal is restarted: a web terminal open at that moment is
  disconnected (reopen it if it does not reconnect by itself), and scrollback
  from before the swap is gone. The conversation is not.
- Orca swaps Claude credentials inside one shared config directory instead.
  Lectern keeps logins in separate directories and moves the conversation,
  which leaves every login's files untouched.
