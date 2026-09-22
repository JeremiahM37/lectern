# Terminal experience review

Reviewed upstream documentation on 2026-09-09. This is a feature/workflow review,
not a performance benchmark or a claim that every upstream feature was tested.
The bounded evidence ledger for the 2026-09-10 browser/TUI comparison is in
[comparison-2026-09-10.md](comparison-2026-09-10.md); it records tested cells and
keeps unverified parity and superiority claims open.

| Project | Where it sets a useful bar |
| --- | --- |
| [Agent Deck](https://github.com/asheshgoplani/agent-deck) | Live session list, project groups, fuzzy/status search, quick attachment, native conversation forks, worktrees, and a keyboard help system. It also has a web surface and agent configuration tools. |
| [Agent of Empires](https://github.com/agent-of-empires/agent-of-empires) | Persistent tmux sessions with TUI, browser and API access; worktrees, diff review, multi-repository workspaces and optional containers. |
| [Claude Squad](https://github.com/smtg-ai/claude-squad) | A focused terminal workflow for independent agent workspaces, previews, diff review and attachment. |
| [tmuxp](https://github.com/tmux-python/tmuxp) | Declarative, repeatable tmux window/pane layouts and startup commands. It is a workspace loader rather than an agent task/approval system. |
| [Zellij](https://github.com/zellij-org/zellij) | A general terminal multiplexer with strong keyboard UX, layouts, floating/stacked panes, collaboration, plugins and a browser client. |

None is established as better in every dimension. Our previous numbered terminal
menu was a clear usability gap relative to the agent dashboards. Our existing
remote-target, task/routine, takeover, approval and context-file workflows remain
useful, but breadth alone does not make a terminal interface pleasant.

## Implemented acceptance criteria

- A live dashboard, with stable selection while rows refresh or arrive.
- Search by name/project/target/agent/path; status filters and project/target groups.
- Readable status, preview and keyboard help without manually looking up IDs.
- One-key native attachment, proper terminal ownership while attached, and return
  to the same dashboard selection after detaching.
- Named project and target choices, multiline prompts, draft preservation after
  an error, context upload and common edit forms.
- Task dispatch into existing worktree infrastructure, captured diff review,
  routine actions/takeover and approval decisions from the terminal.
- Clear deletion semantics for adopted versus owned sessions; quitting a
  dashboard never ends its agents.
- Wide and narrow layouts, Unicode-safe sizing, real PTY tests and shell-mode
  restoration after exit. A plain menu remains for pipes and accessibility.

These changes target daily agent-management workflow quality. They do not imply
feature-for-feature replacement of every tool: tmuxp-compatible declarative layouts, and Zellij's pane/plugin system remain
separate capabilities. Existing tmux/Zellij workspaces can host the client;
Lectern continues using tmux for the underlying agent sessions.

## Full parity goal: evidence ledger

The active goal is terminal quality at least matching Agent Deck and Agent of
Empires, and web quality exceeding Agent of Empires. The acceptance list above
is an initial milestone, **not completion of that goal**. Compare the common
workflows on real Git/tmux and rendered desktop/mobile surfaces. A checked box
in this document is not a substitute for that evidence.

| Workflow | Current evidence and remaining work |
| --- | --- |
| Find, group, monitor, attach, detach, reconnect | Live dashboard + PTY tests; browser real tmux resize/reconnect/dual-client tests. Broader multi-session and saved-view UX comparison remains. |
| Review ongoing work | Live staged/working review implemented for TUI and web; real Git API tests cover renames, binary/untracked files, path boundaries and unchanged index; Playwright covers desktop/mobile, stale responses and retained attachment; actual SSH target proof passed. Full rollout verification is recorded in shared memory. |
| Branch a conversation / isolated parallel work | Native Claude/Codex workspace conversation picker, paginated reader, and exact-ID fork implemented in TUI/web. API, real tmux/PTY and mobile browser tests cover boundaries, unchanged original history and explicit confirmation. Installed Codex fork persisted a distinct ID; installed Claude loaded saved history with its native fork flag (no new turn). Fresh interactive worktree creation/removal now exists in both interfaces, with durable allocation, ownership checks, retained branches, local/SSH Git proof and mobile/desktop/PTy tests. Native forks now optionally allocate an isolated worktree in both interfaces. Real Git/tmux API, browser and PTY tests cover committed-base isolation, preserved parent changes/history, directory-aware continuation and cleanup protection. Installed Codex and Claude CLIs were verified with distinct child IDs and worktree directories; Claude persistence used a synthetic response-only turn with no tool calls. Current native identity is detected from verified Linux process evidence; unsupported/legacy cases retain manual selection. |
| Organize large fleets | Named group paths now persist across projects/targets and inherit on forks/handoffs. TUI and web have nested collapse/counts; terminal search reveals folded children and selection/collapse survive refresh. Web keeps per-tab display state. TUI grouping is remembered per section/server and folded named groups survive restarts, with real PTY restart coverage. Adopted sessions can restore their original record after stopping tracking, gated by a persistent tmux identity marker; real API, PTY and mobile/desktop tests cover retained metadata, concurrent requests and reused-name rejection. Unmarked live records capture identity when released; already-released records without identity still require explicit discovery. Archive now retains terminal snapshots and metadata, with explicit stop confirmation and unarchive without restart; exact native continuation is available separately. Named launch profiles and global conversation search are implemented; broader native identity and fleet-profile partitioning still need comparison. |
| Agent setup | Custom commands, named agent settings, and project MCP controls exist. Target-local skill discovery plus project attach/detach now support Claude and Codex through the API, TUI and web; real local/loopback-SSH lifecycle checks cover fresh launches, resumes, forks, worktrees and cleanup. Native repository skills remain preexisting and preserved. Broader upstream comparison and reusable setup hooks remain. |
| Workspace setup | Grouped interactive workspaces now support 1–8 repositories, per-repository bases, native forks, asynchronous creation, cancellation and interrupted-allocation recovery in TUI/web. Real Git/tmux/API/PTY and mobile/desktop tests cover ownership, dirty-file preservation and restart recovery. Adding repositories to an existing group, converting older single-repository allocations, and reusable setup hooks remain. The mobile release candidate passed the full 6/6 web verification; this comparison ledger remains separate from deployment state. |
| Sandbox choices | Existing Proxmox sandbox path is not equivalent to portable Docker/Podman sandboxing; portability gap remains. |
| Web everyday management | Global command search now reaches sessions, tasks, projects, settings and common actions from desktop/mobile; real browser/tmux tests cover attachment, retained terminal identity, keyboard selection, draft/focus restoration, and refresh failures. Mobile terminals now default to focused navigation with a one-button return, retained frames, visible file/tool controls, and Esc/Tab/arrows/Ctrl-C keys in focused, expanded and standalone phone terminals. Real tmux tests cover height gained, application cursor keys, offline recovery, rotation, and restored preferences. Internal terminal tabs, structured chat, PDFs, routines/takeover and approvals exist. Global search now includes native conversation content with context paging and validated forks. Benchmark the same create/find/attach/review/send/recover workflows against AoE desktop and phone; improve discoverability and consistency before claiming superiority. |
| Installation and keyboard UX | Linux and Windows SSH clients exist; installation portability, help consistency, terminal compatibility and first-run flows need further audit. |

Current upstream evidence: the Agent Deck README lists session forks, archive,
MCP/skills managers, global search and settings; the AoE README lists profiles,
repo hooks, multi-repo workspaces, portable containers and structured mobile
views. These remain part of the comparison, not exclusions added to declare the
current implementation sufficient.

## Local terminal layout preferences

The console remembers grouping separately for Sessions, Tasks and other sections,
and remembers folded named session groups. Preferences live under the operating
system user config directory at `lectern/console/<server-hash>.json` (on Linux,
`$XDG_CONFIG_HOME/lectern/console`, or `~/.config/lectern/console`). Each server
has a separate file. Remove its file while the dashboard is closed to reset the
layout. Queries, selected sessions, attention filters and credentials are not
saved. Invalid or newer-version files are preserved; the dashboard displays a
notice and remains usable without saving over them.

## Restore tracking

After stopping tracking, enable **Include ended and untracked sessions** in the
Sessions view and choose **Track again**. In the console press `z`, select the
record, and press Enter (or `m`) to choose **Track again**. For scripts use
`lectern api POST /sessions/ID/restore '{}'`.

Restoration retains the session ID, name, project, group, workdir and handoff
links. It does not start, restart or interrupt an agent. A random tmux session
marker captured during adoption (or while releasing an older live record) must still match. Reused names and sessions
already tracked through another record are rejected. Records already released without
identity capture cannot be restored automatically; use **Find running sessions**
(`f` in the console) to adopt explicitly. This operation restores monitoring of
a running process; recovery of a stopped native agent conversation is separate.

SSH time limits cover connection setup, handshake, channel opening and command
execution. A timed-out pooled connection is discarded, and later requests
reconnect. Other in-flight operations on that connection may need retrying; the
persistent tmux agents remain separate from these control connections. Real SSH
protocol tests cover stalled handshake/channel/command, a stalled cached
connection, reconnection and concurrent commands under the race detector.

Capturing identity while releasing an older live record is best effort and
limited to three seconds. An unreachable target still leaves tracking normally;
an already-released record never receives a guessed identity later.

Polling requires a complete framed response and a successful control command.
Blank panes remain live; capture errors and truncated replies preserve the last
known session state. Only explicit tmux absence marks a session dead. Pane text
is encoded so delimiter-shaped output cannot impersonate another session.
Real-command regression tests reproduce the previous false deaths and exercise
blank, long Unicode and missing panes. Adoption and Track again refresh only
the affected target; an integration test verifies they never contact an
unrelated target with a stalled SSH handshake.

## Continue a stopped conversation

In Sessions, include ended/untracked records, open **Saved conversations**, select
an exact history, and choose **Resume conversation**. The web interface opens the
new terminal inside Lectern. In the terminal dashboard use `z`, select the old
record, press `H`, choose the conversation and Resume action, then confirm.
Scripts can POST `/api/sessions/ID/resume` with `conversation_id` and optional
`name` (or `lectern api POST /sessions/ID/resume '{...}'`).

This continues the selected Claude/Codex history in its original workspace;
**Fork** creates an independent conversation. The old record is retained. The
original terminal must have stopped: merely stopping tracking does not suffice.
Failed or incomplete remote checks refuse the launch. The selected ID persists
on the new record, allowing later requests to check previous resumed terminals,
including released records, before launching again. Concurrent continuation
requests for the same target/agent/conversation are serialized by rejection.
There is no fallback to `--last`, `--continue`, or fresh history on an error.

Selection is explicit because a workspace can contain multiple conversations.
This does not automatically infer IDs for agents launched outside Lectern,
or detect a writer on an unrelated machine. Automatic authoritative identity
capture, profiles and broader comparison workflows remain.


## Archive and return later

Choose **Stop and archive** from a live session's actions. Confirmation explicitly
ends its terminal process. Captured output is saved before stopping it; tmux's
session-local identity is checked and absence is verified before the record moves
to Archive. Failed captures, changed identity and refused stops leave it visible.
Worktree files, native transcripts and handoff records remain in place.

The web **Show** selector switches between Active, Include ended/untracked and
Archived. In the terminal dashboard press `A` for Archive, `z` for ended records.
Archived records offer **Archived terminal output** and **Unarchive record**.
Unarchiving restores the ended record without starting a process. Use Saved
conversations afterward to continue an exact history when supported.

Already-stopped records use **Archive stopped record**. An untracked terminal
that is still running must be tracked again before Lectern can stop/archive it.
Archive is separate from Stop tracking, which continues to leave processes alone.
Snapshots contain up to 10,000 scrollback lines plus the visible screen, capped
at 2 MiB; if the terminal had already stopped, its last recorded preview is
clearly labeled. An existing archive snapshot survives unarchive/rearchive.

Scripts use POST `/api/sessions/ID/archive` with `{"stop":true}` for a live
terminal or `{"stop":false}` for a stopped record, DELETE the same route to
unarchive, GET `/api/sessions?archived=true` to list archived records, and GET
`/api/sessions/ID/archive/history` to read captured output. Ordinary session lists,
including `?all=true`, exclude archived records. This is an organizational filter,
not an access-control boundary.

Action menus now calculate their available space around the desktop sidebar,
header and mobile bottom navigation. The archive browser test reproduced a
visible-but-unclickable desktop action underneath the sidebar; the corrected
menu remains within the usable area on desktop and phone, including after resize.


## Configuration continuity

Native history now honors the project's environment overrides, including
`CLAUDE_CONFIG_DIR` and `CODEX_HOME`. New interactive launches retain their
resolved agent command, fixed arguments, declared environment and permission
mode privately in the database. Reading, forking and resuming that session use
those settings even if the agent or project settings later change. A new session
uses current settings: agent defaults, then project overrides, then explicit
launch overrides. Trust commands receive the same declared environment.

This records declared launch settings, not a copy of the target's ambient
environment or the contents of its configuration and credential files. Changes
inside those files still apply, and literal credentials explicitly set as
environment overrides remain the saved values. Existing/adopted records without
a snapshot use current agent and project settings; their original process
environment cannot be reconstructed. Invalid snapshots produce an error rather
than silently selecting another configuration.

The snapshot is excluded from session JSON and event payloads. SQLite database
and journal files are restricted to the service account. Regression tests use
real tmux processes and native Claude/Codex transcript files: conflicting project
and agent configuration directories, settings edits between fork and resume,
unchanged history files, private API responses, database migration and reopen.
Named launch profiles and profile selection are implemented; see `launch-profiles.md`.


## Verified terminal stops

End/Kill now confirms that the exact tmux session has disappeared before closing
its record. A refused command, timeout, incomplete remote check or no-op stop
returns an error and leaves the session available for retry. Session-local
identity guards against a replacement process with the same name; a learned
identity is retained even if the stop fails. An already-stopped record keeps its
original end time.

Real tmux tests cover failures, identity changes during the operation, neighboring
session names and repeated stops. Desktop and phone browser tests confirm that
a failed stop leaves the card visible and the terminal running, and that a later
successful retry closes it. Stop tracking remains non-destructive.

## Fork a conversation into an isolated worktree

Saved conversations → Fork offers a workspace choice in both interfaces. An
isolated fork retains the selected history and creates a Git worktree from the
chosen committed base, with a named or automatically generated branch. Parent
uncommitted changes remain in the parent working directory. The source
conversation and its launch configuration stay intact.

Codex receives an explicit directory override: changing only the outer shell's
directory opens a native picker that defaults to the parent directory. The
saved configuration uses a directory template so later resumes/forks use their
own recorded workspace. Unsupported native resume capability stays unsupported.
A resumed session also prevents removal of its still-active worktree. Once a
worktree is removed, a continuation fails clearly until its directory is restored.

Built-in trust preparation uses the selected CODEX_HOME or CLAUDE_CONFIG_DIR.
Known older built-in probe bodies in launch snapshots use the corrected helper;
custom trust commands remain unchanged. Codex configuration is read and appended
under a lock, preserving comments and existing project policy. Reading an existing
TOML file for pre-trust needs Python 3.11's tomllib; on older Python the helper
leaves it unchanged and the CLI can ask for trust normally.

The mobile confirmation presents one scrollable form rather than splitting its
fields across the transcript area. Session visibility filters also share the
dashboard's dark control style and a 44-pixel target.

## Identify the current native conversation

The saved-history picker now marks and defaults to a verified current conversation
in both interfaces. Detection checks the active tmux pane, its recorded identity,
process ancestry and process-start times. Claude runtime records provide its ID;
Codex provides an open transcript descriptor with CLI-origin metadata. A shell
merely opening a transcript does not qualify. Subagent transcripts, other native
profiles/workspaces, replaced terminals and multiple plausible conversations are
not selected automatically. No conversation ID is inferred from recency.

This currently covers Linux process information on local/SSH/WSL targets and does
not persist an inferred binding after the process stops. Unsupported/older runtime
formats and renamed Codex binaries retain manual choice. Older unmarked active
terminals use a stable pane/process observation without writing a marker or
claiming continuity with the original session.
A known but not-yet-persisted Claude conversation is distinguished from saved
history. Automatic native identity coverage therefore has explicit limits; global
conversation-content search remains separate work.

A real installed Codex fork exposed a history-reader bug during this check:
the copied parent header could overwrite the child's header before its first
message was read. The reader now treats the first native header as authoritative.
Regression coverage includes this nested-header structure and verifies the child
remains readable from its worktree without appearing as the parent conversation.


## Global saved-conversation search and forks

Lectern now searches native conversation content through private,
incremental indexes on local/SSH targets. Web and TUI expose progress, target/agent
filters, cancellation, retry/rebuild, exact match reading, context paging and
explicit whole-conversation forks. Forks offer captured launch settings and shared
or isolated workspaces, without needing a tracked record for the source history.
Real local/SSH, Git/tmux, browser/PTY and installed-native-CLI evidence is recorded
in `docs/native-search.md` and shared memory. This is implementation evidence;
search/fork rollout passed 177 end-to-end cases. Fair scheduling and named
launch-profile management in web/TUI subsequently passed 180 end-to-end cases
and were deployed in c5ac0e2. Agent setup, multi-repo/hooks, portable isolation
and comparative workflow testing remain part of the full goal.

## Recover a failed Git checkout hook

A failed Git post-checkout hook can leave a real worktree behind. Lectern now
records ownership only after verifying that new allocation's repository, path,
branch and revision. The session retains its failed state, setup error and base
commit. The error appears in web worktree details and the terminal preview.

Failed setup files remain in place; removal still refuses changed, untracked or
ignored files and retains the branch. Ended and archived sessions now expose
worktree removal in terminal actions as well as the web interface. Real Git,
desktop/phone browser and PTY tests cover the failed-hook recovery path. This
repairs existing Git-hook behavior; reusable Lectern setup-hook configuration
and multi-repository workspaces remain separate work.


## Browser entry workflow check — 2026-09-10

Rendered the locally built AoE revision `5687bbd` and Lectern `3021a70` at
390×900 and 1440×900 in isolated profiles. Neither entry page had JavaScript
errors or page-level horizontal overflow. AoE presents session creation and
repository cloning as its initial actions. Lectern initially shows its task
board. Lectern used its mock backend here; these are entry-page observations,
not evidence of complete workflow parity or performance superiority.

Lectern now remembers the last explicitly selected view on this browser and
origin. Opening the root URL returns there. Explicit task/session/tab links take
priority without replacing that preference. Terminal frames remain per browser
tab; opening a fresh tab with no retained frames falls back to Sessions. Invalid
saved views and malformed links fall back to the board. Desktop and phone E2E
coverage checks return visits, explicit links, new-tab fallback and bad input.
This change is separate from the staged workspace release.


A follow-up creation-form inspection at both viewport sizes found unassociated
labels in Lectern. Project, Name, Agent, Model, Start from and First message
now name their controls; related status/help text is associated with the relevant
fields, and the close button has an explicit accessible name. The real Git/tmux
phone and desktop launch-and-cleanup tests now select a project and enter a name
through those labels, including clicking the Name label to focus the input.


The shared details/form panel now exposes a named dialog, keeps Tab navigation
inside its visible enabled controls, and makes the background inert while open.
Closing restores focus to its opener (or the current navigation button if that
opener disappeared). Native profile/history dialogs opened above it retain their
own keyboard behavior. Rendering updates do not reset focus; a form's explicit
initial focus is preserved. Phone/desktop tests cover forward/backward wrapping,
nested profile editing with a retained draft, and Escape restoration; regression
flows cover task dispatch, approvals, routine editing, handoffs and task links.
These are browser keyboard/accessibility checks, not a manual screen-reader audit.


## Graceful restart with live browsers

Open board/task EventSource connections are indefinite. Waiting for them during
HTTP shutdown consumed the entire ten-second grace period whenever a browser
remained open. The service now drains those streams before waiting for ordinary
HTTP requests. New streams receive 503 while draining; repeated/concurrent drain
calls are safe. Other requests keep their normal lifetime. Terminal-manager
cleanup still closes attachment clients, leaving persistent tmux sessions intact.

A real browser/tmux regression test reproduces the old shutdown exceeding three
seconds, then verifies prompt shutdown, automatic browser reconnection after a
restart, retained output and the exact same tmux session ID. API integration
checks cover stream EOF, rejected reconnects while draining, continuing health
requests and concurrent drain calls under the race detector. This addresses a
shutdown delay; it does not shorten the grace period for ordinary requests.


The session launch action now stays at the bottom of the open form while its
settings scroll. Its opaque footer covers the sheet's bottom padding and keeps
phone safe-area space; fields remain reachable above it. Browser checks cover
390×844, 390×450 and 1440×900, expanded workspace options, the final message field,
and returning to the top without losing that message. Real Git/tmux phone and
desktop launch-and-cleanup flows also pass. Screenshot review caught and removed
a strip of scrolling content beneath the first footer layout.

### Target command inventory

Settings → Targets → **Agent commands**, or the terminal dashboard’s target
**Check agent commands** action, performs an on-demand lookup through the target’s
local/SSH executor. The same read-only endpoint is available with
`lectern api GET /targets/<id>/agents`.

This checks default command resolution, including configured builtin binary
paths; it does not run agents, version commands, trust hooks, authentication or
model requests. Commands with shell syntax and custom agent PATH values are
reported as unchecked. Project and launch-profile overrides may use a different
command or environment; the inventory is informational and never blocks launch.

### Reusable workspace creation commands

Projects now have a `setup_cmd`, editable in Settings → Projects and the terminal
project editor, or through the project create/PATCH API. It runs through Bash in
a newly allocated isolated checkout, with that repository project's environment,
before starting the interactive agent. Grouped workspaces run each repository's
command in its own checkout. The plan captures the command and environment before
checkout; a fresh grouped fork inherits its recorded repository setup. Setup does
not run when attaching, resuming or launching in an existing directory.

An overall 15-minute creation deadline applies when commands are configured.
Cancellation uses the existing target-side owner/lease protocol and retains files.
Nonzero exit stops launch and surfaces the error. The final 4 KiB of output is
kept with the allocation; environment values are omitted from workspace responses.
The browser's worktree details and terminal preview show setup results.

Recovery validates retained files and never reruns commands. If a grouped worker's
supervisor dies before saving its result, recovery reports setup as interrupted,
even if the command may have finished; filesystem recovery is not evidence that a
setup command succeeded. Synthetic real Git/process/tmux/API/PTY/browser tests
cover success, failure, cancellation, supervisor loss and drafts on failed saves.
This is the creation lifecycle step; reusable launch/destroy hooks and reading
repository-owned hook configuration remain comparative gaps.

## Evidence correction — 2026-09-10

The dated comparison ledger is the source of truth for the current bounded
comparison: [comparison-2026-09-10.md](comparison-2026-09-10.md). It supersedes
older wording in this review that described generic-agent proof or the full
comparison as pending without the current cell breakdown.

The Lectern `178bc00d406a893da8b7d4c76d5c5533021ea568` mobile candidate passed
the full web release verification (`6/6`). Current browser evidence covers
Lectern and AoE C1/C2/C5/C6 through actual UI-created sessions, with
request-boundary evidence for multiline values and a clear statement that the
deterministic fake agents did not make model/provider calls. C2 now includes
an active palette capture before selection, exactly one matching result, and
its session/project/target/repository context.

C3 is accepted for Lectern and upstream Agent Deck's actual ttyd evidence at
the paths recorded in the comparison ledger. The final AoE C3 worker receipt is
retained as `UNVERIFIED` because it did not expose every strict criterion. C4 is
complete for Lectern web, AoE web, and upstream Agent Deck's
CLI send-plus-capture workflow. Upstream `session send` returned a confirmation
warning; the captured terminal output still displayed the exact diff, so no
integrated upstream diff viewer is claimed.

The browser harness retained all failed and recovery attempts, including the
Lectern xterm-focus and long tmux-socket-path attempts and AoE's first-session
keyboard-focus and mobile-viewport attempts. These are disclosed in the ledger
and are not converted into product failure counts. The comparison remains
bounded and does not establish overall web superiority.
