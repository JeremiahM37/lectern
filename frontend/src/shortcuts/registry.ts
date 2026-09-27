// The one list of every keyboard-reachable action in the app. Components
// register handlers for the ids they own; Settings → Shortcuts lists, searches
// and remaps these; the command palette shows each action with its chord.
// A person's remaps are stored server-side (prefs key "shortcuts") as
// { actionId: [chord, ...] }, where an empty list unassigns the action.
import { browserReserved, isMac, normalizeChord, terminalSafe } from "./chords";

// global: anywhere in the app. workspace: the terminals/workspace view.
// terminal: inside a terminal frame (the handler lives in that frame).
export type ShortcutContext = "global" | "workspace" | "terminal";
export interface ShortcutDef {
  id: string;
  title: string;
  category: string;
  context: ShortcutContext;
  defaults: string[];
  keywords?: string;
}

const def = (id: string, title: string, category: string, context: ShortcutContext, defaults: string[] = [], keywords = ""): ShortcutDef => ({ id, title, category, context, defaults, keywords });
const digits = [1, 2, 3, 4, 5, 6, 7, 8, 9];

export const SHORTCUTS: ShortcutDef[] = [
  // General
  def("palette.open", "Open command palette", "General", "global", ["Mod+K", "Mod+Shift+P"], "search find anything"),
  def("search.saved", "Search saved conversations", "General", "global", ["Mod+Shift+S"], "history transcripts"),
  def("settings.open", "Open settings", "General", "global", ["Mod+,"], "preferences"),
  def("settings.shortcuts", "Keyboard shortcuts", "General", "global", ["Mod+/"], "keys bindings help"),
  def("settings.search", "Search settings", "General", "global", [], "preferences find"),
  def("theme.toggle", "Toggle light and dark theme", "Appearance", "global", ["Mod+Shift+L"], "dark light mode"),
  def("theme.system", "Use the system theme", "Appearance", "global"),
  def("theme.dark", "Use the dark theme", "Appearance", "global"),
  def("theme.light", "Use the light theme", "Appearance", "global"),
  def("zoom.in", "Zoom in", "Appearance", "global", [], "bigger scale"),
  def("zoom.out", "Zoom out", "Appearance", "global", [], "smaller scale"),
  def("zoom.reset", "Reset zoom", "Appearance", "global"),
  def("accent.next", "Next accent colour", "Appearance", "global", [], "color"),
  // Navigation
  def("nav.board", "Go to the task board", "Navigation", "global", ["Alt+Shift+1"]),
  def("nav.sessions", "Go to sessions", "Navigation", "global", ["Alt+Shift+2"]),
  def("nav.terminals", "Go to terminals", "Navigation", "global", ["Alt+Shift+3"], "workspace"),
  def("nav.media", "Go to media", "Navigation", "global", ["Alt+Shift+4"]),
  def("nav.deck", "Go to the deck", "Navigation", "global", ["Alt+Shift+5"], "overview"),
  def("nav.approvals", "Go to approvals", "Navigation", "global", ["Alt+Shift+6"]),
  def("nav.targets", "Go to settings", "Navigation", "global", ["Alt+Shift+7"]),
  def("nav.tasks", "Go to tasks", "Navigation", "global", ["Alt+Shift+8"], "issues pull requests"),
  def("nav.evals", "Open agent tests", "Navigation", "global", [], "evals"),
  def("settings.machines", "Settings: targets", "Settings", "global", [], "machines ssh"),
  def("settings.projects", "Settings: projects", "Settings", "global", [], "repositories"),
  def("settings.notifications", "Settings: notifications", "Settings", "global", [], "push alerts"),
  def("settings.devices", "Settings: devices", "Settings", "global", [], "pair phone"),
  def("settings.about", "Settings: usage and about", "Settings", "global", [], "version cost"),
  def("settings.budgets", "Settings: budgets", "Settings", "global", [], "spend limits"),
  def("settings.accounts", "Settings: accounts", "Settings", "global", [], "logins swap"),
  def("settings.agents", "Settings: agents", "Settings", "global", [], "runners models"),
  def("settings.plugins", "Settings: plugins", "Settings", "global", [], "extensions marketplace install"),
  def("settings.appearance", "Settings: appearance", "Settings", "global", [], "theme accent zoom language"),
  def("settings.workspace", "Settings: workspace and terminal", "Settings", "global", [], "layouts quick commands terminal theme"),
  // Actions
  def("session.new", "New session", "Actions", "global", [], "start agent launch"),
  def("session.discover", "Find running agents", "Actions", "global", [], "adopt restore"),
  def("task.new", "New task", "Actions", "global", [], "create plan"),
  def("routines.open", "Routines", "Actions", "global", [], "schedule"),
  def("profiles.manage", "Manage launch profiles", "Actions", "global", [], "accounts"),
  def("terminal.new", "New terminal", "Actions", "global", ["Alt+Shift+N"], "shell scratch"),
  // Workspace
  def("workspace.splitRight", "Split right", "Workspace", "workspace", ["Mod+Shift+\\"], "pane side by side"),
  def("workspace.splitDown", "Split down", "Workspace", "workspace", ["Mod+Alt+Shift+\\"], "pane below"),
  def("workspace.closePane", "Close the focused tab", "Workspace", "workspace", ["Alt+Shift+W"], "view"),
  def("workspace.reopenClosed", "Reopen the last closed tab", "Workspace", "workspace", ["Alt+Shift+T"], "undo close"),
  def("workspace.maximize", "Maximize or restore the focused pane", "Workspace", "workspace", ["Mod+Shift+Enter"], "zoom full"),
  def("workspace.focusLeft", "Focus the pane to the left", "Workspace", "workspace", ["Alt+Shift+Left"]),
  def("workspace.focusRight", "Focus the pane to the right", "Workspace", "workspace", ["Alt+Shift+Right"]),
  def("workspace.focusUp", "Focus the pane above", "Workspace", "workspace", ["Alt+Shift+Up"]),
  def("workspace.focusDown", "Focus the pane below", "Workspace", "workspace", ["Alt+Shift+Down"]),
  def("workspace.focusNextGroup", "Focus the next pane", "Workspace", "workspace", []),
  def("workspace.focusPrevGroup", "Focus the previous pane", "Workspace", "workspace", []),
  def("workspace.nextTab", "Next tab in this pane", "Workspace", "workspace", ["Ctrl+PageDown", "Alt+Shift+]"]),
  def("workspace.prevTab", "Previous tab in this pane", "Workspace", "workspace", ["Ctrl+PageUp", "Alt+Shift+["]),
  def("workspace.mruNext", "Switch to the most recent tab", "Workspace", "workspace", ["Ctrl+Tab", "Alt+`"], "recent mru switcher"),
  def("workspace.mruPrev", "Switch through recent tabs backwards", "Workspace", "workspace", ["Ctrl+Shift+Tab", "Alt+Shift+`"], "recent mru"),
  def("workspace.moveTabLeft", "Move tab left", "Workspace", "workspace", ["Ctrl+Shift+PageUp"], "reorder"),
  def("workspace.moveTabRight", "Move tab right", "Workspace", "workspace", ["Ctrl+Shift+PageDown"], "reorder"),
  def("workspace.moveToNextGroup", "Move tab to the next pane", "Workspace", "workspace", []),
  def("workspace.moveToPrevGroup", "Move tab to the previous pane", "Workspace", "workspace", []),
  def("workspace.equalize", "Make all panes the same size", "Workspace", "workspace", [], "reset sizes"),
  def("workspace.unsplit", "Join all panes into one", "Workspace", "workspace", [], "unsplit merge"),
  def("workspace.saveLayout", "Save the current layout", "Workspace", "workspace", [], "layouts named"),
  def("workspace.layouts", "Open saved layouts", "Workspace", "workspace", [], "restore named"),
  def("workspace.chatBeside", "Open this session's chat beside it", "Workspace", "workspace", [], "conversation"),
  def("workspace.diffBeside", "Open this session's changes beside it", "Workspace", "workspace", [], "diff review"),
  def("workspace.browserBeside", "Open this session's browser beside it", "Workspace", "workspace", [], "preview web design"),
  def("workspace.goToFile", "Go to file in the shown session", "Workspace", "workspace", ["Ctrl+P"], "quick open fuzzy file finder"),
  def("workspace.filesBeside", "Open this session's files beside it", "Workspace", "workspace", [], "explorer tree"),
  def("workspace.searchBeside", "Search this session's files beside it", "Workspace", "workspace", [], "grep ripgrep find in files"),
  ...digits.map((n) => def(`workspace.tab${n}`, `Go to tab ${n}`, "Workspace", "workspace", [`Alt+${n}`])),
  // Floating terminal
  def("floating.toggle", "Show or hide the floating terminal", "Floating terminal", "global", ["Ctrl+`"], "quake global drop-down"),
  def("floating.newTab", "New floating terminal tab", "Floating terminal", "global", ["Ctrl+Shift+`"]),
  def("floating.closeTab", "Close the floating terminal tab", "Floating terminal", "global", []),
  def("floating.nextTab", "Next floating terminal tab", "Floating terminal", "global", []),
  def("floating.prevTab", "Previous floating terminal tab", "Floating terminal", "global", []),
  def("floating.dock", "Move the floating tab into the workspace", "Floating terminal", "global", [], "dock"),
  def("floating.maximize", "Maximize the floating terminal", "Floating terminal", "global", []),
  // Terminal (inside a terminal frame)
  def("terminal.find", "Find in terminal", "Terminal", "terminal", ["Mod+F"], "search scrollback"),
  def("terminal.findNext", "Find next", "Terminal", "terminal", ["F3"]),
  def("terminal.findPrev", "Find previous", "Terminal", "terminal", ["Shift+F3"]),
  def("terminal.history", "Session history", "Terminal", "terminal", ["Mod+Shift+F"], "scrollback tmux"),
  def("terminal.copy", "Copy selection", "Terminal", "terminal", ["Mod+Shift+C"]),
  def("terminal.paste", "Paste", "Terminal", "terminal", ["Mod+Shift+V"]),
  def("terminal.selectAll", "Select all", "Terminal", "terminal", ["Mod+Shift+A"]),
  def("terminal.clear", "Clear the scrollback on this screen", "Terminal", "terminal", ["Mod+Shift+K"]),
  def("terminal.fontBigger", "Bigger terminal text", "Terminal", "terminal", ["Mod+Alt+="], "font size"),
  def("terminal.fontSmaller", "Smaller terminal text", "Terminal", "terminal", ["Mod+Alt+-"], "font size"),
  def("terminal.fontReset", "Reset terminal text size", "Terminal", "terminal", ["Mod+Alt+0"], "font size"),
  def("terminal.scrollTop", "Scroll to the top", "Terminal", "terminal", ["Mod+Shift+Home"]),
  def("terminal.scrollBottom", "Scroll to the bottom", "Terminal", "terminal", ["Mod+Shift+End"], "live"),
  def("terminal.pageUp", "Scroll up a page", "Terminal", "terminal", ["Shift+PageUp"]),
  def("terminal.pageDown", "Scroll down a page", "Terminal", "terminal", ["Shift+PageDown"]),
  def("terminal.pause", "Pause or resume the view", "Terminal", "terminal", [], "freeze select"),
  def("terminal.reconnect", "Reconnect the view", "Terminal", "terminal"),
  def("terminal.splitShell", "Show or hide the companion shell", "Terminal", "terminal"),
  def("terminal.files", "Workspace files", "Terminal", "terminal", ["Mod+Shift+E"], "browse explorer"),
  // Ctrl+P stays the shell's while the terminal itself has focus; there,
  // Cmd+P (Mac) or Ctrl+Alt+P opens Go to file.
  def("files.goToFile", "Go to file", "Files", "terminal", ["Mod+P", "Mod+Alt+P"], "quick open fuzzy file finder"),
  def("files.search", "Search in files", "Files", "terminal", ["Mod+Alt+F"], "grep ripgrep find in files"),
  def("terminal.attach", "Attach files", "Terminal", "terminal", [], "upload"),
  def("terminal.compose", "Write or paste text", "Terminal", "terminal", [], "compose prompt"),
  def("terminal.quickCommands", "Quick commands", "Terminal", "terminal", ["Mod+Shift+Space"], "snippets saved replies"),
  def("terminal.appearance", "Terminal appearance", "Terminal", "terminal", [], "theme font"),
  def("terminal.themeNext", "Next terminal theme", "Terminal", "terminal", [], "colours"),
  def("terminal.review", "Review changes", "Terminal", "terminal", [], "diff"),
  def("terminal.saved", "Saved conversations", "Terminal", "terminal", [], "history"),
  def("terminal.tools", "Terminal tools menu", "Terminal", "terminal", []),
  def("terminal.native", "Open in your own terminal", "Terminal", "terminal", [], "native desktop"),
  ...digits.map((n) => def(`terminal.quickCommand${n}`, `Send quick command ${n}`, "Terminal", "terminal", [], "snippet")),
];

export const SHORTCUT_IDS = new Set(SHORTCUTS.map((row) => row.id));
export const shortcutDef = (id: string) => SHORTCUTS.find((row) => row.id === id);

export type Overrides = Record<string, string[]>;

// Stored overrides are data from the server: drop unknown actions, junk chords
// and duplicates rather than trusting them.
export function cleanOverrides(value: unknown, mac = isMac()): Overrides {
  const out: Overrides = {};
  if (!value || typeof value !== "object" || Array.isArray(value)) return out;
  for (const [id, chords] of Object.entries(value)) {
    if (!SHORTCUT_IDS.has(id) || !Array.isArray(chords)) continue;
    const list: string[] = [];
    for (const chord of chords.slice(0, 4)) {
      const normal = typeof chord === "string" ? normalizeChord(chord, mac) : null;
      if (normal && !list.includes(normal)) list.push(normal);
    }
    out[id] = list;
  }
  return out;
}

// The chords each action answers to right now.
export function bindings(overrides: Overrides = {}, mac = isMac()): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const row of SHORTCUTS) {
    const chords = overrides[row.id] ?? row.defaults;
    out.set(row.id, chords.map((chord) => normalizeChord(chord, mac)).filter((chord): chord is string => !!chord));
  }
  return out;
}

// Two actions collide on a chord when both can receive it. A terminal action
// and an app action only meet when the chord is one a terminal forwards.
function overlap(a: ShortcutContext, b: ShortcutContext, chord: string) {
  if (a === b) return true;
  if (a === "terminal" || b === "terminal") return terminalSafe(chord);
  return true;
}

export interface Conflict {
  chord: string;
  ids: string[];
}

export function conflicts(current: Map<string, string[]>): Conflict[] {
  const byChord = new Map<string, string[]>();
  for (const [id, chords] of current) for (const chord of chords) byChord.set(chord, [...(byChord.get(chord) || []), id]);
  const out: Conflict[] = [];
  for (const [chord, ids] of byChord) {
    if (ids.length < 2) continue;
    const clashing = ids.filter((id) => ids.some((other) => other !== id && overlap(shortcutDef(id)!.context, shortcutDef(other)!.context, chord)));
    if (clashing.length > 1) out.push({ chord, ids: clashing });
  }
  return out;
}

// What else would answer if `chord` were given to `id` — the check the remap
// dialog runs before saving.
export function conflictsFor(id: string, chord: string, current: Map<string, string[]>): string[] {
  const context = shortcutDef(id)?.context;
  if (!context) return [];
  return [...current].filter(([other, chords]) => other !== id && chords.includes(chord) && overlap(context, shortcutDef(other)!.context, chord)).map(([other]) => other);
}

// Remaps `id` to exactly `chords`, returning the new override set. Setting an
// action back to its defaults removes the override instead of storing a copy.
export function remap(overrides: Overrides, id: string, chords: string[], mac = isMac()): Overrides {
  const row = shortcutDef(id);
  if (!row) return overrides;
  const normal = chords.map((chord) => normalizeChord(chord, mac)).filter((chord): chord is string => !!chord);
  const defaults = row.defaults.map((chord) => normalizeChord(chord, mac));
  const next = { ...overrides };
  if (normal.length === defaults.length && normal.every((chord, i) => chord === defaults[i])) delete next[id];
  else next[id] = normal;
  return next;
}

export function warningFor(chord: string, context: ShortcutContext): string {
  if (browserReserved(chord)) return "Browsers keep this chord for themselves in an ordinary tab; it works in the installed app.";
  if (context !== "terminal" && !terminalSafe(chord)) return "Terminals keep this chord for the program running in them, so it does not work while a terminal has focus.";
  return "";
}
