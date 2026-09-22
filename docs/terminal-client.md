# Use Lectern from a terminal

Run `lectern` in an interactive terminal, or `lectern console`, for the live
dashboard. It opens on Sessions, groups by project, and refreshes automatically.
Use `lectern serve` to start the server explicitly. Existing systemd/container
launches with no arguments and no terminal still start the server.

| Keys | Action |
| --- | --- |
| ↑/↓ or j/k | Select a session, task, routine, project, target or approval |
| Enter | Attach; Ctrl-b then d returns to the same selection |
| S | Choose a machine and open a blank persistent shell |
| / | Fuzzy search names, projects, targets, agent names and paths |
| @ / ! / # / & at start of search | Waiting / running / idle / failed |
| 1–6, ←/→ | Switch sections |
| g / w | Group by project or target / show items needing attention |
| Tab / p, PgUp/PgDn | Focus and scroll the preview |
| n / e / m | Create / rename / all actions |
| P | Manage named launch profiles |
| Q | Manage agent runners (add custom CLIs) |
| h / v / u | Read retained history / review a task diff / upload context |
| f | Find running agents and add one to tracking by name |
| 7 / 8 / 9 | Settings / usage / full API |
| ? / q | Help / quit without stopping agents |

In **Projects** (key **4**), select a project and press **Enter** to open a
persistent shell in its repository on the project's machine. No agent is
launched. You can inspect files and run commands directly; **Ctrl-b**, then
**d** returns to the project list. A configured tmux `default-command` does
not replace this shell with an agent launcher.

For optional workflow packs, use **Projects → Actions → Workflows (Spec Kit /
Maestro)**. Choose the provider and pack, then Enable or Disable and save with
**Ctrl-s**. Each provider's availability is shown in the picker. Start a new
session to load changes; disabling preserves generated documents. In the plain
console, use the project's **workflows** action and select a pack to toggle it.

The wide layout shows a live preview beside the list. Narrow terminals keep one
focused pane visible; Tab switches between the list and preview. Forms use named
project/target choices, accept multiline prompts, and keep your draft after an
API error. Tab changes fields, arrows choose options, Ctrl-s submits, Esc cancels.
Task creation can dispatch into an isolated worktree; Tasks → Actions also offers
routine takeover, follow-up, diff review, completion and cancellation.

For a live unassigned native session, open Actions → Promote conversation. The
preview proves the exact agent, process, directory, terminal, and conversation
before offering compatible existing projects or a new project name. The final
confirmation keeps the same session, terminal, and native history. The same
flow is available from a terminal with `lectern promote SESSION-ID`.

`lectern console --plain` retains the line-oriented menu. Redirected input or
output selects it automatically, so scripts keep working. The JSON API commands
below are unchanged. The TUI polls the existing API; it does not run another
agent collector or maintain a second session database.

Use `lectern shell [MACHINE]` when you want to work directly in a machine's
shell. With no machine argument, an interactive client shows a searchable
picker; scripts should pass the target name or numeric ID. The shell is tracked
as a durable session and starts the target user's interactive shell in a fresh
Lectern scratch directory. It does not select a project, agent, model, launch
profile, memory, or worktree.

## Install on another Linux machine

Use an existing SSH alias for the Lectern server. Download
`/desktop/install-lectern-cli.sh` from your Lectern instance, then run:

```sh
bash install-lectern-cli.sh --server lectern --api https://YOUR_SERVER:8443
lectern
```

The installer copies the client from your server over SSH, checks the machine
architecture, preserves any previous client, and installs into `~/.local/bin`.
The installed launcher opens the console by default. Re-run it to update.
The server and client must use the same Linux architecture for this installer;
other architectures can build the Go binary from source.

For Windows, download `/desktop/install-lectern-cli.ps1` and run it with
`-Server YOUR_SSH_ALIAS` (and `-Api http://127.0.0.1:9110` when the server uses
a non-default API address). It installs an OpenSSH launcher on your user PATH;
management runs on the server with an explicit hosted API environment.
`lectern upload` stages local files over SCP.
The native Windows launcher requires OpenSSH, with your existing host/key setup.

For a terminal workspace that runs entirely on the current computer, use the
[standalone local installer](local.md) instead. It installs `lectern` (or
`lectern-local` when the remote client already owns that name) and does not
need a control-plane URL or SSH server; the remote installer above continues to
install the `lectern` client.

This client works in your existing terminal. The
separate desktop URI installers enable opening an attachment from the web UI.

## Scripting and context files

```sh
lectern api GET /launch-profiles
lectern agent list
lectern agent save @agents.json
lectern api POST /sessions '{"name":"Work","profile_id":7,"scratch":true}'
lectern api GET /sessions
lectern api POST /sessions '{"name":"Scratch","agent":"codex","scratch":true}'
lectern shell AIServer
lectern api POST /tasks/12/takeover '{}'
lectern api PATCH /routines/3 '{"enabled":false}'
lectern api POST /sessions/4/send '{"text":"Run the tests"}'
lectern api POST /sessions/4/setup/cancel '{}'  # request checkout cancellation; retain files
lectern api POST /sessions/4/worktree/recover '{}'  # validate interrupted allocation; keep files
lectern upload session 4 ./requirements.pdf
lectern files session 4
lectern download session 4 reports/result.txt ./result.txt
lectern post ./demo.mp4 --title "Checkout flow passing"
lectern attach session 4
lectern promote 4
```

`lectern agent save` accepts the runner fields shown in Settings → Agents:
`name`, required `command`, optional `args`, `model_flag`, provider endpoint
environment, `prompt_arg`, `resume_args`, `yolo_args`, and `env`. The command
starts the runner; provider URLs and models configure that runner through its
environment contract.

Uploads print the stored remote path. They do not submit a message: mention the
path in your prompt, or paste it in the attached terminal. Upload also accepts
`task`, `attempt`, and `project`. Files/download use `session`, `attempt`, or
`project`. Downloads preserve an existing destination file.

## Media: agents showing their work

An agent can put evidence in front of you instead of describing it. The
`post_media` MCP tool and `lectern post` both publish to the **Media** view: a
screen recording of the feature working, a screenshot, a generated report, a
log, or a link to the dev server the agent started.

```bash
lectern post ./demo.mp4 --title "Checkout flow passing" --note "Watch the total"
lectern post ./report.html --title "Test report"
lectern post http://127.0.0.1:5173 --title "Dev server"
lectern post ./trace.zip --title "Playwright trace" --session 4
```

Inside a Lectern session the post attaches itself to that session: the
poster reads the tmux session it is running in, so an agent never needs to know
its own id. `--session` (or the tool's `session_id`) is for scripts running
somewhere else. A post from outside any session still lands, unattributed.

Files are copied into Lectern's media store, so a recording outlives the
worktree that produced it. Video and audio play in place and seek, images and
PDFs render inline, HTML renders in a sandboxed frame that cannot act as the
Lectern origin, and text files preview on demand. A link posted as
`127.0.0.1` or `localhost` opens on the address you reached Lectern at, since
that is the machine the agent meant — the site has to listen on `0.0.0.0` for
that to work from another device.

`LECTERN_MEDIA_DIR` moves the store (default: `lectern-media` beside the
database) and `LECTERN_MEDIA_MAX_MB` caps one file (default 1024).

## Scratch workspaces and the sweep

Every blank shell and every session started without a project gets its own
directory under the target's scratch root (`~/lectern-scratch`, or
`LECTERN_SCRATCH_ROOT`). Once an hour the server sweeps them. A directory is
removed only when all of this is true: no session is running in it, no session
there belongs to a project, it holds no files and no commits, no conversation
was recorded there, and it has been idle for `LECTERN_SCRATCH_DAYS` (default 7;
`0` turns the sweep off). Each directory it takes is named in the server log.

A conversation counts as work even when the directory is empty: Claude's
history is looked up by directory, a session's recorded conversation identity is
checked, and Codex's dated history is searched for the path. That last search is
run only for directories that already pass every other test, and a search that
fails counts as a hit. Symlinks are never followed, so a scratch name that now
points at a promoted project is left alone.

Removal is a move into `.trash` inside the scratch root (or
`LECTERN_SCRATCH_TRASH`), purged after `LECTERN_SCRATCH_TRASH_DAYS` (default
14). Anything the sweep will not decide — work that no project claims — is listed
under **Sessions → Scratch workspaces**, where you keep it for good or discard it.

```bash
lectern api GET /scratch                       # what the sweep sees; changes nothing
lectern api POST /scratch/sweep '{"dry_run":true}'
lectern api POST /scratch/keep '{"target_id":1,"name":"shell-20260918-qfEK6Y"}'
lectern api POST /scratch/discard '{"target_id":1,"name":"codex-20260909-Y7AYgu"}'
```

## Live views: a machine's localhost, and a desktop on it

An agent's dev server, an admin console, a browser automation in headed mode:
they all live on the machine the agent runs on, bound to its `127.0.0.1` or
drawing to a display nobody can see. Two things in **Media → Live** bring them
to the device you are actually using.

**Expose a port.** A link posted as `http://127.0.0.1:5173` gets an *Expose*
button; so does any port you type, on any machine. Lectern opens a port of its
own and carries the TCP connection to that machine's loopback — over the SSH
connection it already holds, for a remote one. It is the port that is forwarded,
not a path, so absolute asset URLs, redirects and websockets all work and the
application never knows.

**A live desktop.** *＋ Live desktop* starts a private virtual display on the
machine, with noVNC in front of it, and shows it in the page. The card gives its
`DISPLAY`: anything started with that in its environment draws there, so
`DISPLAY=:90 your-tool --headed` is all it takes to watch a run. The address bar
opens a browser on that desktop, and that browser sees the machine's own
localhost — no forward needed. You watch by default; *Take control* passes your
mouse and keyboard through. An agent can ask for one with the `open_live_view`
tool, which returns the `DISPLAY` for it to use.

```bash
lectern expose 5173 --title "Dev server"            # this session's machine
lectern expose 8080 --machine lxc-104-work
lectern live http://127.0.0.1:18080 --title "Watching the replay"
lectern live list
lectern live stop 3
```

Live views are **off by default**: start the server with `LECTERN_LIVE=1` to
turn them on. They open listening ports of their own, and `open_live_view` lets
an agent start a desktop that can be driven from the network, which is not
something an upgrade should hand anyone. With it off, Media offers none of this
and the API and the tool say how to enable it.

Both make something loopback-only reachable by whoever can reach Lectern, so
even when enabled neither happens on its own: a posted localhost link is never exposed until you
press the button, every open view is listed with an *exposed* badge, and each
one closes when its session ends, after four hours (`ttl_minutes`, at most a
day), when you stop it, or when the server restarts. Forwards only ever reach
the target's `127.0.0.1`. A forwarded port cannot check a bearer token, so a
server with `LECTERN_AUTH_TOKEN` set refuses to open one unless
`LECTERN_LIVE_UNAUTHENTICATED=1`. `LECTERN_LIVE_PORTS` sets the range
Lectern listens on (default `19200-19299`).

A desktop needs `Xvfb`, `x11vnc`, `websockify` and `novnc` on the machine that
hosts it (`apt install xvfb x11vnc novnc websockify`); a machine without them
says which are missing. A browser is optional — a tool that brings its own, as
Playwright does, only needs the `DISPLAY`. Targets reached through a command
prefix or `pct` cannot forward, because their loopback is not the SSH host's.

## Project skills

Claude and Codex can discover skills on the selected target and attach them to a
project. Configure additional target-local source directories with the project
API; repository skills are discovered by walking up from the project's Git root:

```sh
lectern api PATCH /projects/7 '{"skill_sources":["/srv/agent-skills"]}'
lectern skill list 7 --agent codex
lectern skill attach 7 'configured:<source-hash>/lint' --agent codex
lectern skill attached 7 --agent codex
lectern skill detach 7 12
```

The web and terminal dashboards provide the same discovery and attach/detach
actions. Sources are read on the target and linked into `.claude/skills` or
`.agents/skills` in the project and any selected worktree; source files are not
copied. Detach removes only links proven to be Lectern-owned. Native
repository skills and changed or foreign destinations are preserved.

`api` writes JSON to stdout and failures to stderr with a nonzero exit code.
Bodies accept inline JSON, `@filename`, or `-` for stdin. Every web operation is
available through the same API; specialized menus cover frequent operations,
and Full API accepts the remainder without opening a browser.

| Resource | Operations |
| --- | --- |
| `/sessions` | GET list, POST create; GET/PATCH/DELETE `/{id}` |
| `/shells` | POST create a tracked blank shell on a target (`target_id` or `machine`) |
| `/sessions/discover`, `/sessions/adopt` | GET running agents, POST track |
| `/sessions/{id}/send`, `/handoff`, `/promote` | POST message/key, handoff, associate project (promotion requires the exact preview identity) |
| `/sessions/{id}/reader`, `/wraps` | GET conversation, handoff records |
| `/tasks` | GET list, POST create; GET/PATCH/DELETE `/{id}` |
| `/tasks/{id}/dispatch`, `/takeover`, `/followup`, `/complete`, `/cancel`, `/commit`, `/cleanup` | POST actions |
| `/tasks/{id}/messages`, `/events`, `/diff` | GET; messages also POST |
| `/tasks/clear` | POST completed-task cleanup |
| `/routines`, `/projects`, `/targets` | GET list, POST create; PATCH/DELETE `/{id}` |
| `/routines/{id}/run`, `/targets/{id}/check` | POST run/probe |
| `/projects/import/scan`, `/projects/import` | GET scan with target_id/root; POST import |
| `/projects/{id}/brief`, `/notes`, `/wraps`, `/capability` | GET project context; DELETE `/notes/{noteID}` |
| `/approvals`, `/approvals/{id}/decision` | GET queue, POST approved/denied decision |
| `/agents`, `/templates`, `/settings` | GET or PUT configuration |
| `/models`, `/stats`, `/health`, `/projects/usage` | GET available models, usage and health |
| `/settings/test-notification`, `/admin/janitor` | POST test or maintenance |

Configure `LECTERN_API` and `LECTERN_AUTH_TOKEN` for HTTP. Set
`LECTERN_ATTACH_HOST` to the server's SSH alias on a remote Linux client;
attachment is resolved on the server, where its tmux sessions and SSH targets
exist. The Linux installer sets the URL and alias in its launcher.

Inside an existing tmux workspace, native attachment opens a full-size popup
(tmux 3.2 or newer). The attachment owns its keyboard input; Ctrl-b d closes it
and returns to the same dashboard selection without detaching the outer workspace.

## Review live code changes

On a session or project, press `v` for live Git review. Left/right changes files,
`s` switches working-tree versus staged changes, PgUp/PgDn scrolls, `r` refreshes
the current file, and Esc returns. Tasks retain their captured diff on `v`; their
actions menu also offers **Review live changes** for an existing attempt.

The same review is available on the web through a session's **More → Review
changes** or an attached terminal's **Tools → Review changes**. It includes file
search, line numbers, mobile wrapping, and separate staged/working counts. It
reads a snapshot when opened/refreshed; the agent can continue editing. Large
patches are explicitly truncated at 512 KiB. Git and Python 3 run on the target,
including SSH targets; there is no local-checkout assumption and no staging or
checkout mutation.

## Saved native conversations and forks

The Sessions view also has **Recently closed**. It fetches the latest ten
server records each time it opens, so ended sessions remain available after the
live list is empty. The terminal dashboard opens the same list with `C` or
Actions → Recently closed; `Esc` or Backspace returns to the live list. A record
with an exact durable native binding offers **Resume** and continues that
conversation after refreshing the session list. Released adopted terminals
offer **Restore tracking**, which resumes monitoring the existing terminal
without launching another agent. Other records offer **Choose history**, which
opens the existing explicit native history picker.

On a Claude or Codex session, press `H` (or Actions → Saved conversations / fork)
to choose a saved conversation from that workspace on its target. Read it in the
preview, use `O` for the preceding page, or choose **Fork** in the picker. The
confirmation names the exact conversation ID. Choose **Use the same files** or
**New isolated Git worktree** in Workspace for forks. Isolation creates a branch
from the selected committed base (HEAD by default); uncommitted changes stay in
the parent workspace. A blank branch name gets a unique name. The original
conversation is unchanged, and forking submits no new prompt.

The web offers **Saved conversations** in the session's More menu and the
attached terminal's Tools menu. Choose a conversation explicitly; messages are
grouped by role, tool activity folds away, and **Load earlier messages** pages
back without replacing the messages already on screen. Codex's saved thread
names are used when present. Long messages and discovery limits are labelled.

The web fork confirmation offers the same Workspace, branch and base controls.
The confirmation uses the dialog space; Cancel returns to the transcript.
Scripts pass an optional worktree object to POST /sessions/ID/fork, for example
{"conversation_id":"UUID","worktree":{"branch":"experiment","base":"HEAD"}}.
Omitting worktree keeps the shared-files behavior. Resume always continues in
the recorded directory; it does not allocate another worktree. A removed or
unavailable directory produces an error until the workspace is restored.

History reads native JSONL stores using the target's `CODEX_HOME` or
`CLAUDE_CONFIG_DIR` (including configured agent environment overrides), and
checks the stored working directory. It never guesses that a directory's most
recent conversation belongs to the selected terminal. Other agents retain the
terminal history/reader. Native history is currently limited to these two agent
formats; private reasoning/system/developer records are omitted.

`GET /sessions/{id}/conversations` lists candidates;
`GET /sessions/{id}/conversations/{uuid}?before=BYTE_OFFSET` reads a page;
`POST /sessions/{id}/fork` accepts `conversation_id` and an optional `name`.
Custom Claude/Codex definitions can provide `fork_args` with an `{id}` placeholder.
Without it, the picker offers reading only. A fork is a conversation branch, not
a Git worktree; interactive worktree isolation is a separate remaining feature.

Optional installed-CLI checks (no new prompt/model turn):
`python3 tests/native_codex_fork.py` verifies a distinct persisted Codex ID,
copied fixture history, and unchanged original; `python3 tests/native_claude_fork.py`
verifies Claude loads fixture history with its native fork flag and leaves the
original unchanged. Claude need not write the new transcript before a new turn.
The regular suite tests target lookup, pagination, workspace boundaries and
actual tmux launch with scripted agents, without requiring either paid CLI.

## Interactive Git worktrees

In **New session**, select a project and enable **Isolate in a new Git worktree**.
The terminal dashboard's `n` form has the same choice and also accepts an
explicit repository directory. Choose a base branch/tag/commit (blank means
committed `HEAD`) and a new branch name, or let Lectern allocate a unique name.
The agent starts in a separate directory beside the repository. Uncommitted
source edits are not copied; this mode starts a fresh conversation.

Both interfaces show the branch, allocation state and working directory. On
the web, expand the worktree line to see its path/base. After ending a session,
use **Include ended and untracked sessions** (web) or `z` (TUI) to find it again.
**More/Actions → Remove worktree** removes the directory only after its sessions
and tmux terminals have left, and only when Git reports no changed, untracked
or ignored files. The branch and committed work remain. There is no force-delete
option. Cleanup checks the recorded repository, branch and ownership marker.

`POST /api/sessions` accepts `"worktree":{"base":"main","branch":"feature/example"}`;
it can use a project or an absolute `workdir` on a local/SSH target. Session
responses include `workspace`. `DELETE /api/sessions/{id}/worktree` performs
checked removal; ending/dismissing a session never removes its worktree.
Allocation is recorded before Git runs; failed allocations remain in ended
sessions with their planned path. A launch failure does not erase files.

This is separate from saved-conversation forking: native conversation forks
currently share their original directory. Interactive worktrees are not yet a
multi-repository workspace, and setup hooks/profile templates remain separate
work on the parity roadmap.

## Named session groups

Groups organize sessions across projects and targets. Press `G` or choose
**Actions → Move to group**; use a path such as `Work/Client`, or clear it to
ungroup. `g` cycles project, target, none and named-group ordering. Search also
matches group paths and worktree branches/directories. New-session forms accept
a group, and conversation forks and handoff successors inherit it.

On the web, **More → Move to group** offers existing names and keeps a failed
edit open for correction. **Group by** selects named group, project, target or
none. Named groups form collapsible nested sections with session/waiting counts.
The selected grouping and collapsed sections survive reloads in that browser
tab; searching opens matching sections. The API's `group_path` is shared across
clients, while these display preferences are local to the browser.

`PATCH /api/sessions/{id}` accepts `{"group_path":"Work/Client"}` or
`{"group_path":""}`. Names are normalized by trimming each level; empty levels,
control characters and more than eight levels are rejected. Moving a group
label does not move files, change the project, or restart the agent.

When a live Claude or Codex conversation can be verified, **Saved conversations**
marks it **Current terminal** and selects it initially. The web reader opens its
saved messages immediately; the terminal dashboard defaults its conversation
choice to that entry. Refresh preserves a different conversation you selected.

Identification is read-only and currently uses Linux process information, including
on SSH/WSL targets. Claude supplies a runtime record tied to its process start;
Codex must hold its transcript open in the native `codex` process. The active tmux
pane and its identity must remain stable. Ambiguous, unavailable or stale evidence
leaves manual selection available. A new Claude conversation can be identified
before it has saved any readable messages; it becomes readable after persistence.
Older active terminals are checked through their current pane without changing
their tracking metadata. Renamed Codex binaries may require manual selection.
