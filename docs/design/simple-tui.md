# Simple terminal dashboard and attach bar

Status: design for branch `simple-tui`. It covers the terminal side of the
2026-09-28 usability audit (problems #12 and #13, and the terminal parts of
#5, #6 and #8). Inputs: the new-user audit, its `notes-tui.md` and the
TUI section of `research.md` (lazygit, k9s and btop conventions).

## Goal

A newcomer should be able to use the dashboard without opening help. Every
key that works where they are is shown on screen and named in plain words.
Power features stay, but move one level down: into a short context menu, a
searchable command palette and the `?` screen.

These stay exactly as they are: the live preview beside the list, click to
attach, right-click or `o` for a new window, `b` multi-select, double-click to
open paths, `Ctrl+] e` link labels, `Ctrl+\` to send a file, `Ctrl+] |`
splits, the Restore list and Undo.

## Rules

1. **The key bar.** One line at the bottom that is always visible. It shows
   only the keys that do something for the current pane and selection, most
   useful first, up to seven. `? keys` is pinned to the right and is never cut
   off.
2. **`?` opens the full key list for the current view.** It scrolls (↑↓,
   PgUp/PgDn) and filters as you type after `/`. Esc, `q` or `?` close it.
3. **Esc goes back one level and never quits.** `q` goes back in a sub-view
   and quits only from the top level. Ctrl+C quits from anywhere.
4. **Enter runs the highlighted item**, in menus, the palette and search
   results alike.
5. **Few shifted letters.** Every old shifted key still works for this release
   as a silent alias unless its key now means something else. The changed keys
   are listed below.
6. **The same status words as the web:** Working, Needs you, Idle, Ended.

## Status words

The web stream defined one vocabulary on the server (`internal/vocab`, sent
as `state`, `state_label` and `state_reason` on every session). The dashboard
shows those words, and applies the same rules itself when a server does not
send them yet.

| State | Word | When |
|---|---|---|
| `working` | **Working** | busy, starting, or its workspace is being set up |
| `needs_you` | **Needs you** | a pending approval for this session, or a permission prompt |
| `idle` | **Idle** | at its prompt or quiet; your turn |
| `ended` | **Ended** | ended, agent exited, setup failed, interrupted or archived |

A reason follows the word when there is one: "Ended · interrupted",
"Working · setting up". An agent that exited while its terminal stays open
reads **Stopped**. An approval the dashboard has seen more recently than
the row still reads "Needs you". A machine that is not answering keeps its
`unreachable ·` prefix.

## Panes

```
 1 Sessions   2 Approvals (1)   3 Projects   4 Tasks
```

- `1`–`4`, Tab / Shift+Tab and ←/→ switch panes.
- Routines and Machines (formerly Targets) are palette entries. `5` (Machines)
  and `6` (Approvals) still work as silent aliases.
- The Approvals tab shows a count whenever something is waiting. A one-line
  banner appears above every pane while an approval is pending:
  `⏸ 1 approval needs you — press 2`.

## Key map

### Sessions pane

| Key | Before | After |
|---|---|---|
| ↑↓ / j k, Home/End, PgUp/PgDn | move / scroll preview | same |
| Enter, click | attach | same (`a` still attaches on a session row) |
| o, right-click | new terminal window | same |
| b, Space | multi-select | same |
| n | new session (14-field form) | new session (one screen, four fields) |
| **x / d / Delete** | nothing: End was item ~20 of the `m` menu | **end session**, after a confirmation (adopted sessions: "stop tracking", which leaves them running) |
| **r** | refresh | **restore**. On an agent that exited, start it again. On an ended row, track it again. Otherwise open the Restore list. |
| **y** | nothing | allow once, when the selected session **needs you** |
| **a** | attach | allow for this session (asks to confirm) when the session **needs you**. Otherwise still attach. |
| / | filter | same, including the status prefixes |
| **m** | 21–33 item menu | context menu of at most 8 items for the selection; the last item opens the palette |
| **: / Ctrl+K** | nothing | command palette: every action, searchable by plain words |
| v | review changes | same |
| ? | help, any key closes it, cut off at 80×24 | scrollable, searchable key list |
| q | quit | quit (top level only) |
| Esc | clear search / leave preview | same; never quits |
| **Tab** | focus preview | next pane (`p` still focuses the preview) |
| Ctrl+R | nothing | refresh now (the list also refreshes every 3 s) |

### Approvals pane

| Key | Before | After |
|---|---|---|
| **y** | nothing (Approve was item 16 of 16, then y/n) | **allow once**, no second question |
| **a** | attach | **allow for this session**, confirm with y |
| **n** | "Approvals are created by agents" | **deny** |
| Enter | generic 16-item menu | attach to the session that is asking |
| m | generic menu | Allow once · Allow for this session · Deny · Deny with a reason… · Open session |

### Review (`v`)

| Key | Before | After |
|---|---|---|
| **q** | quits the whole dashboard | back (like Esc) |
| **c** | nothing | commit: message form, then `POST /sessions/{id}/git/commit` with stage-all. On `main`/`master` the form offers a new branch first (`new_branch`) and the branch itself second (`allow_base_branch`, confirmed). A server that still refuses is explained in plain words with the next step. |
| ←/→, [ ], s, Tab, r, Esc | files, scope, repository, refresh, back | same |

### Forms

| Key | Before | After |
|---|---|---|
| Enter | next field | next field; **on the last field it submits**. A one-field form submits on Enter. |
| Ctrl+S | submit | same |
| Tab / Shift+Tab, ←/→, Esc | fields, options, cancel | same |

### Other views

Help, menus, the palette, the Restore list, conversation search results and
forms all take Esc (and `q` where `q` is not typed text) as "back". Menus wrap
at the top and bottom.

### Keys that moved (silent aliases this release)

| Old key | Now | Old key still works? |
|---|---|---|
| `C` restore list | `r` | yes |
| `R` revive an exited agent | `r` on that session | yes |
| `r` refresh | Ctrl+R, palette "Refresh" | no: `r` is now restore |
| `U` undo last end | palette "Undo: reopen the last ended session" | yes |
| `F`, `H`, `O`, `P`, `Q`, `S`, `G`, `A` | palette | yes |
| `7` settings, `8` usage, `9` API | palette | yes |
| `2` Tasks, `3` Routines, `4` Projects | `4` Tasks, palette "Routines", `3` Projects | no: the numbers now mean the new panes |
| `5` Targets, `6` Approvals | palette "Machines", `2` | yes |
| `Tab` focus preview | `p` | no: Tab now switches panes |
| `e` rename | context menu "Rename" | yes |
| `h`, `u`, `s`, `f`, `g`, `w`, `z` | context menu or palette | yes |

## Screens

Sessions, 120×36, with one approval pending:

```
 ◆ Lectern                                                                          LIVE · 10:04:12
 1 Sessions   2 Approvals (1)   3 Projects   4 Tasks
 ⏸ 1 approval needs you — press 2, or y / a on the session
/ Search name, project, agent…
› approve-me                                   │  Needs you: Bash  echo hi > NOTES.md
  myapp · Needs you · claude                   │  y allow once · a allow for this session · 2 to deny
  myapp · main                                 │
  myapp · Idle · claude                        │  approve-me
                                               │  claude · Needs you · local
                                               │  …live pane…

 y allow once · a allow for session · Enter attach · x end · n new · : commands · q quit     ? keys
 Started "approve-me"
```

Sessions, 80×24, an idle session selected:

```
 ◆ Lectern                                            LIVE · 10:04:12
 ‹ 1 Sessions › 2 Approvals · 3 Projects · 4 Tasks
/ Search…
› myapp · main
  myapp · Idle · claude
…
 Enter attach · n new · x end · r restore · / search · q quit   ? keys
```

The context menu (`m`) on an idle session:

```
 myapp · main
 ─────────────────────────────
 › Attach                     Enter
   Open in a new window         o
   Send a message
   Send a file                  u
   Review changes               v
   Rename                       e
   End session                  x
   All commands…                :
```

The palette (`:` or Ctrl+K), after typing `end`:

```
 : end
 › End session "myapp · main"          x
   Undo: reopen the last ended session
   Send message
```

New session (one screen). Where defaults to the project that contains the
directory the dashboard was started in, or to that directory itself (only
when the server runs on this machine), or to the only project:

```
 New session
 › Where: myapp — /home/me/myapp
   Agent: claude
   First message (optional): —
   Approvals: Ask before running commands (recommended)
   More options…

 Enter next · ←/→ choose · Ctrl+S start · Esc cancel
```

- Agents come from `GET /targets/{id}/agents` on the chosen machine. Only
  agents that are found (or cannot be checked, such as custom commands) are
  offered. The default is the project's default agent if it is installed,
  otherwise the first installed agent in the shown-agents order. Codex is
  never picked just because it is first. When nothing is installed, the form
  says so instead of launching something that will fail.
- A name is optional; the server names the session as `lectern claude` does.
- "More options…" reveals the other fields: name, launch profile, model, a
  typed folder path, machine, a separate Git worktree, extra repositories,
  worktree base and branch, resume, project brief and group.
- A name is optional: the session is named after its project or folder.
- If no agent is installed on the machine, the form still opens (a launch
  profile may bring its own command) and says so; missing agents are labelled
  "not found on this machine".
- After the session is created it is selected and attached, like
  `lectern claude`.

Confirmation for End:

```
 End session "myapp · main"?
 The agent stops. r (Restore) brings it back with its conversation.
 y end · n / Esc keep it
```

## Native attach bar

Before (cut off at 80 columns, so the detach key is lost):

```
Ctrl+\ send file · Ctrl+] m controls · Ctrl+] | shell · Ctrl+] e links · Double-click open path · Ctrl-b d detach
```

After:

```
Ctrl+] menu · Ctrl+] d leave · Ctrl+\ send file · double-click opens paths
```

The leave key comes second, so a narrow terminal still shows it. Below 60
columns the bar shortens to `Ctrl+] menu · Ctrl+] d leave`.

`Ctrl+] d` detaches the private wrapper: the agent keeps running and you
return to where you started. It is Lectern's own key, so it also works inside
your own tmux, where `Ctrl-b d` would detach the wrong tmux. `Ctrl-b d` still
works as before.

Pressing Ctrl+] turns the bar into its key list until the next key:

```
Ctrl+] then:  m actions · d leave · u send file · | shell right · - shell below · e open a link · ? all keys
```

`Ctrl+] ?` (or `Ctrl+] Space`) opens a clickable menu with every attach key
and what it does, including "Lectern actions" (the `Ctrl+] m` popup). The
popup's own context menu shows the key next to each item, and an item's key
runs it.

While the session waits for an approval, the bar starts with
`⏸ Needs you · Ctrl+] y allow · Ctrl+] m more · Bash: rm -rf build`. The popup then leads
with Allow once (y), Allow for this session (a) and Deny, and closes back to
the agent after the answer, so answering while attached is `Ctrl+] m y`. The
note comes from a small status job that asks the server every 5 seconds; the
agent-written text in it is escaped so tmux cannot run it as a format.

After a CLI attachment ends, Lectern prints "Left the session; it keeps
running." instead of leaving tmux's `[exited]` as the last line.

## Tests

- Go unit tests for the key bar (per context and width), the palette ranking
  ("end" ranks End session first), `q`/Esc levels, the approval keys (y sends
  one request with no confirmation; a asks first; n denies), x, r, the new
  session defaults and the review commit.
- e2e in a real pty (`e2e/test_simple_tui.py`), using the fake agent's hook
  protocol: the audit's terminal walkthroughs (b) see agents, (c) approve,
  (d) end and restore, (f) review and commit, and the attach bar at 80×24.

## Round 2: attaching without tmux

The re-audit (N1) found that with the
PTY host as the default backend, a machine without tmux got none of the above:
no bar, no Ctrl+] menu, a wrong "Ctrl-b d still detaches" line that the agent
wiped at once, a Ctrl+\ that killed the agent, and a dead agent's pane that
showed its hook token.

Now `lectern attach` / `lectern claude` without a local tmux run Lectern's own
client (`cmd/lectern/native_bare.go`, `native_bare_screen.go`):

- The session is read into a terminal emulator (`charmbracelet/x/vt`, as the
  PTY host uses) and drawn above a key bar on the bottom row; only changed
  rows are redrawn, inside synchronized updates.
- The keys are the tmux path's: Ctrl+] then m, y, u, e, |, -, d, ?, Ctrl+];
  plus o and x for panes and [ for scrolling. Ctrl+\ always opens the file
  sender.
- "Needs you" is polled every 3 seconds and leads the bar with its keys.
- Double-click opens a path or link, right-click shows its menu, a drag
  copies (OSC 52), and the wheel scrolls back, unless the program asked for
  the mouse, which then gets it.
- Splits are panes in the client: side by side or stacked, up to four.
- The Ctrl+] m dashboard and the file sender run in the same process, in
  place of the session until they close; text they type (an uploaded path)
  goes straight to the agent.
- Ctrl+] y allows the waiting request once; the tmux path has it too.

The launch wrapper keeps secrets off command lines and off the screen: the
hook token and OTLP header go through a 0600 env file read and deleted by the
pane, and the shell's report of an agent killed by a signal is discarded.
