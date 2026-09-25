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
