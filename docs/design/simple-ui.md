# Simple web UI

Status: built on branch `simple-web` (2026-09-28). Inputs: the new-user audit
(`/mnt/bulk/ux-audit/audit.md`, problems 4–11 and 15) and the research notes
next to it.

**Goal.** Someone who has just installed Lectern opens the web app or phone app
and is talking to an agent in under a minute, without reading docs. Every power
feature stays, one level down. Nothing is deleted.

## Vocabulary

One set of words on every surface (web, phone, terminal client, CLI, docs):

| Word | Means | Replaces |
|---|---|---|
| **Session** | an agent (or shell) running in a folder | — |
| **Project** | a folder Lectern knows about | "room" for a project folder |
| **Machine** | a computer that runs agents | Target |
| **Task** | a card on the task board, run in the background | Board card |
| **Tasks** (page) | the task board | Board |
| **Issues & PRs** | GitHub/GitLab/Jira/Linear items | Tasks (sidebar) |
| **Overview** | live view of running tasks | Deck |
| **Approval** | an action an agent is waiting for you to allow | — |
| **Add file** | attach a file to a chat message | Attach (in chat) |
| **Terminal** | open a session's terminal | Attach (on a card) |

API paths keep their names (`/api/targets`, `/api/tasks`); only labels change.

### Session status

Four states, the same words everywhere. The server computes them
(`internal/vocab`, field `state` / `state_label` on every session in the API)
so the web app, the phone and the terminal client cannot disagree.

| State | Label | When |
|---|---|---|
| `working` | Working | the agent is busy, starting, or its workspace is being set up |
| `needs_you` | Needs you | a pending approval for this session, or the agent is blocked on a permission prompt |
| `idle` | Idle | the agent is at its prompt, waiting for your next message |
| `ended` | Ended | the session ended, its agent exited, setup failed, a restart interrupted it, or it is archived |

"Needs you" is never shown for an idle prompt. A short reason can accompany a
state (`state_reason`: `starting`, `setting_up`, `setup_failed`,
`agent_exited`, `interrupted`, `archived`, `untracked`); the card shows it
after the state word, e.g. "Ended · agent exited".

## Information architecture

### Before

```
Desktop sidebar (9)                Phone bottom bar
┌───────────────┐                  ┌──────┬────────┬──────┬─────────┬──────┐
│ Board   ← home│                  │Board │Sessions│Tasks │Terminals│ More │
│ Sessions      │                  └──────┴────────┴──────┴─────────┴──────┘
│ Tasks  (=PRs) │                   More: Media, Deck, Approvals, Settings,
│ Terminals     │                         Agent tests
│ Media         │
│ Deck          │   Header: [Search]  ........  [+ New task]
│ Approvals     │   Home: Board on desktop, Sessions on phone,
│ Agent tests   │         then "whatever you visited last"
│ Settings      │
└───────────────┘
```

### After

```
Desktop sidebar                    Phone bottom bar
┌─────────────────┐                ┌────────┬──────────┬──────┬────────┬──────┐
│ Sessions  ← home│                │Sessions│Approvals²│Tasks │Settings│ More │
│ Approvals    ²  │                └────────┴──────────┴──────┴────────┴──────┘
│ Tasks           │                 (Terminals joins the bar while a terminal
│ (Terminals)     │                  tab is open)
│ ▸ More          │
│   Overview      │                 More: Overview, Issues & PRs, Terminals,
│   Issues & PRs  │                       Media, Agent tests, Machines, Plugins
│   Terminals     │
│   Media         │
│   Agent tests   │
│   Machines      │
│   Plugins       │
│ Settings        │
└─────────────────┘
```

- **Home is always Sessions**, on every device. A reload returns to Sessions
  unless the URL names a page.
- **Approvals** carries the count of pending approvals. The count is the only
  red/amber badge in the navigation.
- **Terminals** is in the main navigation only while at least one terminal tab
  is open; otherwise it is under More.
- **Machines** and **Plugins** in More open those Settings sections.

### Deep links

The hash names the page. Old hashes keep working and are rewritten:

| Old | New |
|---|---|
| `#board` | `#tasks` |
| `#tasks/<project>/…` (issues and PRs) | `#issues/<project>/…` |
| `#deck` | `#overview` |
| `#targets` | `#settings/machines` |
| `#settings/<section>`, `#task/<id>`, `#session/<id>[/terminal\|reply]`, `#approvals`, `#media[/<id>]`, `#terminals/…`, `#evals` | unchanged |

A bare old `#tasks` now opens the task board, which is what "Tasks" means now.

## First run and empty states

### Sessions, no sessions yet

```
┌──────────────────────────────────────────────────────────────┐
│  Start your first agent                                      │
│  Pick a folder and an agent. Lectern runs it and tells you   │
│  when it needs you.                                          │
│                                                              │
│  [ Start an agent ]     Try a demo agent (no install needed)│ ← demo only
│                                                                when no agent
│  ⚠ tmux is missing — sudo apt install tmux                   │ ← only failing
└──────────────────────────────────────────────────────────────┘   checks
```

The old checklist (machine / agent / tmux / git / python / project / first
session) is gone; a check appears only when it fails, and each carries its fix.
There is no "project" row: a project is created by starting in a folder.

### Start an agent (one sheet)

```
┌ Start an agent ────────────────────────────────── ✕ ┐
│ Folder                                               │
│ ┌──────────────────────────────────────────────────┐ │
│ │ ~/myapp                             [ Change… ]  │ │
│ └──────────────────────────────────────────────────┘ │
│  Recent: myapp · otherapp · ~/notes                  │
│                                                      │
│ Agent   ( claude ▾ )   only installed agents;        │
│                         "More agents…" for the rest  │
│                                                      │
│ [■] Ask before risky actions                         │
│     The agent asks you before running commands or    │
│     editing files. You can allow or deny from here   │
│     or your phone.                                   │
│                                                      │
│ ▸ More options  (name, model, worktree, profile, …)  │
│                                                      │
│            [ Start ]                                 │
└──────────────────────────────────────────────────────┘

Change… opens the folder picker:

┌ Choose a folder ─────────────────────────── ✕ ┐
│ Machine ( this computer ▾ )                   │
│ ~/code                              [ ↑ Up ]  │
│ ─────────────────────────────────────────────  │
│ 📁 myapp            git                        │
│ 📁 otherapp         git                        │
│ 📁 scratch                                     │
│ ─────────────────────────────────────────────  │
│ [ Use a new empty folder ]  [ Use this folder ]│
└───────────────────────────────────────────────┘
```

- The folder list comes from `GET /api/targets/{id}/folders?path=`, a
  read-only directory listing on that machine (names of subdirectories and
  whether each is a git repository).
- Starting in a folder that is not yet a project registers it
  (`POST /api/projects/import` with that path) and then starts the session in
  it. "Use a new empty folder" is today's blank room.
- The agent list shows agents whose command is found on that machine
  (`GET /api/targets/{id}/agents`); missing ones sit under "More agents…".
- **Ask before risky actions** is on by default for new installs: a fresh
  database starts with `session_permission_mode = ask`. Existing installs keep
  their setting. The toggle's value is sent with each start.
- **Try a demo agent** starts `demo`, a scripted stand-in agent that needs
  nothing installed, in a new empty folder. It answers messages and writes a
  file, so status, chat and the diff can be tried without a real agent.

## One approval component

```
┌ ⚠ Needs you · claude in myapp ─────────────────────────────┐
│ Run a command                                               │
│ $ rm -rf build/                                             │
│                                                             │
│ [ Allow once  Y ]  [ Allow for this session  A ]  [ Deny… N]│
└─────────────────────────────────────────────────────────────┘
Deny… opens a note field:  Why? [____________]  [Deny] [Cancel]
```

`ApprovalCard` is the only approval UI: session card, Approvals page, chat
sheet and the page a notification opens. Keys work while the card has focus
(the Approvals page focuses the first card): **Y** allow once, **A** allow for
this session, **N** deny with a note. A task's approval has no session, so its
second button is **Always allow in this project** (the old ∞ Always).

## Settings

```
Settings                                   [ Search all settings… ]
┌──────────┬────────────────────────────────────────────────────┐
│ Basics   │ Agents on this computer   ✓ claude  ○ codex  …     │
│──────────│ Ask before risky actions  [■]                      │
│ Advanced │ Phone                     [ Connect your phone ]   │
│ Machines │ Alerts on this device     [ Enable alerts ]        │
│ Projects │ Theme                     ( System ▾ )             │
│ Alerts   │ Language                  ( Browser default ▾ )    │
│ Phone &  │ Enter in chat             ( Sends the message ▾ )  │
│  devices │                                                    │
│ Agents   │                                                    │
│ …        │                                                    │
└──────────┴────────────────────────────────────────────────────┘
```

- **Basics** has seven controls. Everything else is in the Advanced sections,
  each one level deep; search covers every section.
- The section list is the first thing under the heading on every width; the
  AI-tool connectors and Delegated builds moved into their own
  **Connections** section instead of sitting above the tabs.
- The permission default moved out of Notifications into Basics.

## Commit when a session is on the default branch

```
Commit
This session works on main.
( ) Commit on a new branch  lectern/fix-login   ← default, one click
( ) Commit to main — I understand this changes main directly
[ Commit ]
```

- **Commit on a new branch** creates the branch (named from the session) from
  the current state and commits there (`new_branch` on
  `POST /api/sessions/{id}/git/commit`).
- **Commit to main** needs the confirmation tick (`allow_base_branch`) and a
  signed-in person.
- **Push** starts unticked when the repository has no remote, and says why.
- No control is disabled without saying why: a disabled button carries a
  visible reason next to it (and the same text as its tooltip).

## Connect your phone

```
┌ Connect your phone ────────────────────────────── ✕ ┐
│ Pick how your phone reaches this computer:           │
│ (•) Tailscale  https://box.tail1234.ts.net:8443      │
│     Works anywhere your phone is on your tailnet.    │
│ ( ) Same Wi-Fi  http://192.168.0.75:9110   ⚠          │
│     Only on this network, and not encrypted.         │
│ ( ) Relay  (not set up)                              │
│     Works anywhere, end-to-end encrypted; needs      │
│     `lectern relay` running somewhere both reach.    │
│                                                      │
│           [ Show QR code ]                           │
│   ┌──────┐  Scan with your phone's camera.           │
│   │ QR   │  Expires in 4:59.                         │
│   └──────┘                                           │
└──────────────────────────────────────────────────────┘
```

- `GET /api/phone/addresses` lists the addresses a phone could use, each with
  whether it can work and why not. It never offers a loopback address.
- When Lectern listens only on this computer (the local runtime), the LAN and
  tailnet options say so and name the one command that changes it.
- Show QR code turns device pairing on if needed and mints a code for the
  chosen address. The relay option uses the relay's own pairing.

## Chat box

Enter sends; Shift+Enter adds a new line. On a touch-only device, where there
is no Shift key, Enter adds a new line and the Send button sends. Basics has a
three-way setting: Automatic, Enter sends, Enter adds a new line.

## Kept as they were

The simple face of New session (now Start an agent), the diff reviewer, the
Restore panel, the Undo toast after End, one-click Allow on the card, the
Ctrl+K palette and the shortcut editor. Every feature that left the main
navigation is still reachable from More, from Settings search and from the
palette.

## Validation

`e2e/test_simple_walkthroughs.py` replays the audit's seven web tasks at 1440
and 390 px and asserts the number of clicks each takes, so the flows stay
short. The before/after table is in the branch report.
