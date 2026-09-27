# Workspace, terminal, keyboard and appearance

The Terminals view is a workspace: every open pane — attached terminals,
a session's chat, a session's changes, and any pane type another feature
registers — sits in one tab strip and can be arranged in nested splits.
Preferences that belong to you rather than to a device (theme, shortcuts,
saved layouts, quick commands, terminal scheme) are stored on the server and
follow you to every device.

![Terminal, changes and chat in one layout](media/workspace/desktop-split-terminal-chat-diff.png)

## Layout

- **Split**: drag a tab onto the edge of a pane to put it beside or below
  that pane; drop it in the middle to join the pane's tabs. Splits nest to any
  depth. **◫ Split** (or Split right / Split down in the **⋯** menu) does the
  same without dragging, using the most recent tab not already on screen.
  **◫ Unsplit** joins everything back into one pane.
- **Resize** by dragging a boundary, or focus it and use the arrow keys.
  Double-click a boundary to even the sizes.
- **Maximize** a pane from its header (⤢) or by double-clicking the header.
- **Tabs**: drag a tab along the strip to reorder it, or Alt+Shift+←/→ on a
  focused tab. **Ctrl+Tab** (or **Alt+`**, since browsers keep Ctrl+Tab in an
  ordinary tab) opens the recent-tab switcher; release the modifier to go.
  Alt+Shift+T reopens the last closed tab.
- **Chat and changes beside a terminal**: **⋯ → Open chat beside / Open
  changes beside**. In a pane they are part of the layout, not a dialog over it.
- Moving a pane never reloads it: every pane is positioned over its slot
  rather than re-parented, so terminals keep their connection.
- **Phone**: the layout is kept but one pane shows at a time; the tab strip
  or a sideways swipe switches between them.

![Dropping a tab on a pane's lower edge](media/workspace/desktop-drag-to-split.png)
![Recent tabs](media/workspace/desktop-recent-tabs.png)

### Saved layouts

The current layout is saved on this device and restored on reload. A device
with no layout of its own opens the one you used last elsewhere.
**▦ Layouts** saves the current arrangement under a name, optionally only for
the current project, and restores it later; panes that are already open and
not in the layout stay open, and terminals of sessions that have ended are
left out. Saved layouts are listed, renamed and deleted in
**Settings → Workspace & terminal**.

![Saved layouts](media/workspace/desktop-saved-layouts.png)

### Adding a pane type

`frontend/src/workspace/registry.tsx` is the whole contract:

```tsx
registerPaneType({
  kind: "browser",
  label: "Browser",
  icon: "◎",
  check: (ref) => (/^\d+$/.test(ref.params.session || "") ? ref : null),
  render: ({ pane, services, close }) => <BrowserPane ... onClose={close} />,
});
services.openPane({ id: "browser:session:7", kind: "browser", title: "Browser", params: { session: "7" } }, { beside: activePaneId, edge: "right" });
```

`check` validates a restored or shared layout; a pane whose kind is not
registered is dropped from it. A component that is a `<dialog>` on its own can
read `PaneEmbedContext` and call `show()` instead of `showModal()`, as
`Modal` and the changes view do. When a `file` pane type is registered, the
command palette also searches the front session's files
(`workspace/files-provider.ts`).

## Floating terminal

**Ctrl+`** shows or hides a terminal over whatever view is open. It is not
tied to a session: its tabs are scratch shells, it keeps them across reloads,
and **⇲** moves the front one into the workspace. On a desk it can be moved,
resized and maximized; on a phone it is a bottom sheet.

![Floating terminal over the board](media/workspace/desktop-floating-terminal.png)
![Floating terminal on a phone](media/workspace/phone-floating-terminal.png)

## Terminal

- **Find** (Ctrl/⌘+F, or ⌕ on the phone key bar): match case, whole word and
  regular expression switches, a match count, Enter / Shift+Enter or F3 /
  Shift+F3 for next and previous. The switches are remembered. Ctrl+F is a
  shell key too; remap Find in Settings → Shortcuts to give it back.
- **Themes**: 27 built-in schemes, 10 of them light, plus import of Windows
  Terminal JSON, iTerm2 `.itermcolors`, kitty, Ghostty, Alacritty, foot and
  Xresources files. With no scheme chosen, the terminal follows the app theme.
  Font size stays per device; scheme and line spacing follow you.
- **OSC 52**: programs may set the clipboard (tmux's own copies, `tmux
  set-buffer -w`, and vim or remote shells when tmux passes them on with
  `set-clipboard on`). Reading the clipboard is never allowed. It can be
  switched off in Settings.
- **Kitty keyboard protocol**: not available. It needs xterm.js 6.1, which is
  still in beta; Lectern uses 5.5.

![Find with a regular expression](media/workspace/desktop-terminal-find.png)
![Find on a phone](media/workspace/phone-terminal-find.png)

## Quick commands

Saved text sent to a terminal with one tap: from **⚡** on the key bar, the
Snippets sheet (Ctrl/⌘+Shift+Space), or *Send quick command 1–9* once given
keys. Commands are either **everywhere** or for **one project**; a project's
own come first in its terminals. They are edited in the Snippets sheet and in
Settings → Workspace & terminal, and existing per-device snippets are carried
over the first time. Writing them needs a signed-in person: an agent cannot
plant text for you to send.

## Keyboard shortcuts

One registry (`frontend/src/shortcuts/registry.ts`) lists 114 actions across
general, appearance, navigation, settings, workspace, floating terminal and
terminal. **Settings → Shortcuts** searches them, records a new chord, removes
or resets one, and flags conflicts, and warns about chords a browser tab keeps
for itself. A terminal keeps chords a shell or TUI could use; app chords that
no program needs (⌘, Ctrl+Shift, Alt+Shift, F-keys, Ctrl+Tab, Ctrl/Alt+`)
still work while a terminal has focus.

![Recording a shortcut that is already taken](media/workspace/desktop-shortcuts-conflict.png)

## Command palette

Ctrl/⌘+K ranks sessions, tasks, projects, settings sections, individual
personal settings and every available action (with its chord); letters typed
out of order still find a title. Other features add results with
`registerPaletteProvider` (`shell/palette-providers.ts`).

![Palette](media/workspace/desktop-palette.png)

## Appearance

Settings → Appearance: **System / Dark / Light** for the whole app (dark
remains the default), an accent colour (presets or any colour; an illegible
one is darkened or lightened until it reads), UI zoom from 80% to 150%, and
language. Colours are CSS custom properties set from
`frontend/src/theme/app-theme.ts`; the theme test holds every text token to
WCAG AA (4.5:1) on every surface in both modes and with every accent.
Stylesheets that still name a dark colour directly get a light counterpart
from `frontend/scripts/light_theme.py`, which writes
`frontend/src/theme/light.generated.css`; re-run it after adding such a colour.

![Light theme](media/workspace/desktop-light-sessions.png)
![Light theme on a phone](media/workspace/phone-light-sessions.png)

## Settings search

The search box at the top of Settings finds individual controls in every
section, including each shortcut, opens the section and brings the control
into view. The command palette reaches the personal settings the same way.

![Settings search on a phone](media/workspace/phone-settings-search.png)

## Translation

Text goes through `t()` (`frontend/src/i18n`). English (`en.ts`) is the
complete catalog; a test fails if code asks for a key it does not have. The
pseudo-locale in Settings → Appearance → Language accents every translated
string, which shows at a glance what is not wired yet. The new workspace,
terminal, settings and shell chrome are wired; most older views are not yet,
and no other language ships.

## Storage and API

`GET /api/ui/prefs` returns the caller's own preferences;
`PUT`/`DELETE /api/ui/prefs/{key}` change one (JSON value, at most 256 KiB,
at most 200 keys). Rows are per signed-in login (`operator` when there is no
identity). Writes need a signed-in person; a caller that cannot write keeps
changes on its own device and Settings says so. Changes are cached locally,
survive a reload until the server confirms them, and other devices refetch
on the `ui_prefs` event.

## Tests

- Unit: `workspace/layout.test.ts` (tree operations, serialization and
  restore), `shortcuts/shortcuts.test.ts` (registry, conflicts, remap),
  `theme/theme.test.ts` (WCAG AA, theme import), `i18n`, `prefs`, `quick`.
- Go: `internal/api/ui_prefs_test.go`.
- End to end: `e2e/test_workspace_layout.py`,
  `e2e/test_workspace_terminal_features.py`, `e2e/test_personal_settings.py`.
- Screenshots: `e2e/doc_screenshots_workspace.py` (see its docstring).
