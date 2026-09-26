# Using sessions on a phone

A fresh phone opens Sessions. An explicit URL or your remembered view still wins.
Blank shells with no project sit under their own **Scratch terminals** heading,
apart from **Sessions and projects**, and both lists keep the search, scope and
grouping controls. **Make a project** on a scratch card promotes it without
moving its files or restarting its terminal. Agent cards put **Chat** first.

**New session** opens with just **Project** and **Agent** and one **Start
session** button; the name, group, launch profile, model, worktree isolation,
start mode, YOLO and first message wait behind **Advanced options**. The chosen
project's folder and machine are spelled out beneath the picker, and the state
that changes what a launch does — permission mode above all — stays visible next
to Start. This browser remembers the project you picked last (a deliberate
**Blank room** included) and lists recently opened projects first, in New
session and in Terminal's new-terminal menu. A creation that fails is not
remembered as recent.

Two quick shells never read the same: each scratch card is titled with its own
scratch folder, shows that full path so it can be read or copied, and keeps
**✎ Rename** for a name you recognize. Renaming changes the card only — the
folder, its files and the terminal stay where they are — and the name survives a
reload. Cards nobody renamed are titled from their folder; stored rows are never
rewritten behind your back.
**Terminal** remains one tap away. Chat preserves
an unsent draft on the device, shows connection state, and offers working-file
changes. **Needs you** collects pending approvals, waiting sessions, setup/task
failures and work ready for review. A failed refresh is shown explicitly.

In Terminal, session names have their own full-width row. The switcher, new
terminal, search, menu and navigation controls use a separate row. Swipe sideways
on the terminal body to move between open terminals, including full-screen apps
that enable terminal mouse reporting. Vertical drags still scroll; long press
selects text and pinch changes text size. The terminal fits the visible viewport
when the phone keyboard opens, including browsers that overlay the layout.

**Switch agent or model** offers discovered models, saved provider profiles and
favorites. Switching saves a fresh handoff and starts the destination with that
context. It keeps the original session running. The progress display distinguishes
saving from starting, reports failures, and lets you retry. If the new session
already exists but its terminal cannot open, **Open new session** retries only the
attachment. **Go back** opens the predecessor; it does not restart it. This is a
context handoff between agents, not a claim that their native conversation formats
are interchangeable.

Favorites are saved per browser. A favorite for a deleted provider is marked
unavailable; editing a saved provider uses its current configuration. Keep API
keys in a protected server-side credential source or launcher, never in model
names, command arguments or browser storage. A launcher profile is specific to
the targets where its command and credentials are installed.

Native keyboard and gesture coverage, limitations, and the nightly audit are
in [Mobile terminal testing](testing/mobile-terminal.md). The Android fixture is
disposable and never types into a person's running agent.

## Chat cards and graduated approvals

Chat for a live session defaults to **structured cards**, not a dump of the
tmux pane. The server locates the session's own Claude JSONL or Codex rollout
file — the same identity-verified reader the saved-conversation picker
already uses, reused rather than reimplemented — and decodes it into typed
turns (assistant/user text as light markdown, a collapsed **Thinking** entry
for private reasoning, and one card per tool call) instead of the flattened
display string the plain-text history reader produces. The phone polls
`GET /sessions/{id}/conversation/live` and only asks for what was appended
since its last look; **Terminal text** is one tap away in the same controls
row, and is what a session falls back to automatically if its log can't be
found (a shell-tracked session, an agent without a reader, or a sandboxed
session) — nothing more to do, the toggle itself simply isn't offered.

Every tool call collapses to a title and, on a phone, one tap away: Read/
Edit/Write/MultiEdit show the file path and, for an edit, a real diff (the
same renderer the session review diff uses); Bash and Codex's `exec_command`
show the command and its output; Grep/Glob show the pattern; WebFetch/
WebSearch show the host or query; TodoWrite renders as a checklist; a
sub-agent `Task` shows its description; an MCP tool's `mcp__server__tool`
wire name becomes "server · tool"; Codex's `apply_patch` gets the same diff
treatment, split per file. A tool this registry doesn't specifically know
still gets a real card — name plus collapsed, pretty-printed arguments —
never a raw JSON blob.

**Approvals are graduated**, in the chat and in Needs you: **Allow once**
decides just this call; **Allow for this session** (session-scoped
approvals only — a task attempt's approval has no persistent session for
the rule to outlive) tells the broker to stop asking for this same tool, or
this exact Bash command's first token, for the rest of the session, entirely
in memory and forgotten when the session ends; **Deny with feedback** opens
a note that is sent back to the agent as the hook's denial message. The
approval's command or diff renders through the same tool-view registry as a
chat card, never `JSON.stringify(approval.input)`. Claude and Codex
interactive sessions share one PermissionRequest hook path end to end, so
"Allow for this session" behaves identically for both; it has nothing to do
with Codex's separate app-server driver, which only ever backs a scheduled
task attempt, never an interactive chat.

## Glanceable home, voice, badges and richer alerts

A **Now** strip sits at the top of Sessions: one chip per live session (working
/ waiting / done / error) and, when any exist, how many approvals are
waiting — a tap on either jumps straight there. It is the PWA analogue of a
native app's Live Activity, since a browser tab cannot keep one on the lock
screen. The app icon's own **badge** (where the platform supports it) mirrors
the same count — set from the page on every refresh, and bumped by the
service worker itself on a push, so it stays roughly right even between page
loads; it clears the moment nothing needs you.

Wherever there is a text box for a reply — the chat composer and the
Needs-you "deny with reason" field — a mic button appears if the browser
supports the Web Speech API and stays hidden if it does not. Interim words
show up as they are heard and are replaced, never duplicated, once
recognized; nothing is ever sent on your behalf; you still tap Send.

Notifications for the same session replace each other in the tray instead of
stacking (grouped by session, or by approval, or by kind for a broadcast like
a failed check). A session waiting on you gets **Open terminal** and
**Reply** actions alongside the usual tap-to-open — Reply opens the chat with
the composer already focused. iOS Safari not yet added to the Home Screen
gets explicit numbered steps in Settings → Notifications, not just the one
reason sentence every other unavailable case gets.

The terminal's Tools menu on a phone carries one more, deliberately
low-profile item: **Report keyboard layout**, which captures the exact
viewport/keyboard numbers this device is reporting — for pasting into a bug
report when the phone keyboard covers the prompt in a way no emulator has
reproduced (see the keyboard-coverage history in
[Mobile terminal testing](testing/mobile-terminal.md)).
