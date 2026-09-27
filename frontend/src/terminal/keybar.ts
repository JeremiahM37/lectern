// The phone's key row, as the person arranged it: which keys, in what order,
// including Ctrl/Alt/Shift combinations and their quick commands. Kept free
// of the DOM so it is tested as plain functions; the row itself is
// Keybar.tsx. The arrangement is stored per device, like the type size; a
// quick command on the row is a reference to the one server-stored list
// (quick/commands.ts), so editing the command changes its key everywhere.
import { t } from "../i18n";
import { modified, type Mods } from "./keys";
import { commandBytes, type QuickCommand } from "../quick/commands";

// The keys a phone keyboard does not have, in the order a shell reaches for
// them. The row scrolls sideways, so it can be complete without being tall.
export const builtinKeys = {
  escape: "\x1b",
  tab: "\t",
  backtab: "\x1b[Z",
  left: "\x1b[D",
  up: "\x1b[A",
  down: "\x1b[B",
  right: "\x1b[C",
  interrupt: "\x03",
  slash: "/",
  dash: "-",
  pipe: "|",
  tilde: "~",
  home: "\x1b[H",
  end: "\x1b[F",
  pageup: "\x1b[5~",
  pagedown: "\x1b[6~",
  enter: "\r",
  backspace: "\x7f",
  delete: "\x1b[3~",
} as const;
export type BuiltinKey = keyof typeof builtinKeys;

const builtinKeyLabels: Record<BuiltinKey, string> = {
  escape: "Esc", tab: "Tab", backtab: "⇧Tab", left: "←", up: "↑", down: "↓", right: "→", interrupt: "^C",
  slash: "/", dash: "-", pipe: "|", tilde: "~", home: "Home", end: "End", pageup: "PgUp", pagedown: "PgDn",
  enter: "⏎", backspace: "⌫", delete: "Del",
};

/** [what the key shows, what a screen reader says] (keybar.key.* in en.ts). */
export function builtinLabel(id: BuiltinKey): [string, string] {
  return [builtinKeyLabels[id], t(`keybar.key.${id}`)];
}

export type KeyItem =
  | { t: "key"; id: BuiltinKey }
  | { t: "mod"; id: "ctrl" | "alt" }
  | { t: "combo"; key: string; ctrl?: boolean; alt?: boolean; shift?: boolean }
  | { t: "quick"; id: string }
  | { t: "find" }
  | { t: "snippets" };

// Esc and Tab lead; the sticky modifiers sit right after them, where a thumb
// looks for Ctrl. Saved replies close the row.
export const defaultKeybar: KeyItem[] = [
  ...(["escape", "tab"] as const).map((id) => ({ t: "key" as const, id })),
  { t: "mod", id: "ctrl" },
  ...(["left", "up", "down", "right", "interrupt", "backtab"] as const).map((id) => ({ t: "key" as const, id })),
  { t: "mod", id: "alt" },
  ...(["slash", "dash", "pipe", "tilde", "home", "end", "pageup", "pagedown"] as const).map((id) => ({ t: "key" as const, id })),
  { t: "snippets" },
  { t: "find" },
];

const named = new Set<string>(Object.keys(builtinKeys));
const isBuiltin = (id: unknown): id is BuiltinKey => typeof id === "string" && named.has(id);

/** A combo's key: one printable character, or a named key such as "left". */
export function validComboKey(key: string): boolean {
  return isBuiltin(key) || (key.length === 1 && key >= " " && key !== "\x7f");
}

export function sameItem(a: KeyItem, b: KeyItem): boolean {
  return itemId(a) === itemId(b);
}

/** A stable name for the item: data-terminal-key, and what dedupes the row. */
export function itemId(item: KeyItem): string {
  switch (item.t) {
    case "key":
    case "mod":
      return item.id;
    case "snippets":
      return "snippets";
    case "find":
      return "find";
    case "combo":
      return `${item.ctrl ? "C-" : ""}${item.alt ? "M-" : ""}${item.shift ? "S-" : ""}${item.key}`;
    case "quick":
      return `quick:${item.id}`;
  }
}

function keyLabel(key: string, ctrl?: boolean): string {
  if (isBuiltin(key)) return builtinKeyLabels[key];
  return ctrl ? key.toUpperCase() : key;
}

/** [what the button shows, what a screen reader says]. A quick command
 * needs its command; a key for one that no longer exists is not shown. */
export function itemLabel(item: KeyItem, quick?: QuickCommand): [string, string] {
  switch (item.t) {
    case "key":
      return builtinLabel(item.id);
    case "mod":
      return item.id === "ctrl" ? ["Ctrl", t("keybar.holdCtrl")] : ["Alt", t("keybar.holdAlt")];
    case "snippets":
      return ["⚡", t("keybar.quickCommands")];
    case "find":
      return ["⌕", t("terminal.searchThis")];
    case "combo": {
      const mods = [item.ctrl && "Ctrl", item.alt && "Alt", item.shift && "Shift"].filter(Boolean).join("-");
      const shown = `${item.ctrl ? "^" : ""}${item.alt ? "⌥" : ""}${item.shift ? "⇧" : ""}${keyLabel(item.key, item.ctrl)}`;
      const spoken = isBuiltin(item.key) ? builtinKeyLabels[item.key] : item.key;
      return [shown, t("keybar.sendCombo", { combo: `${mods}-${spoken}` })];
    }
    case "quick": {
      const text = quick ? quick.label || quick.text : "?";
      const shown = text.length > 12 ? text.slice(0, 11) + "…" : text;
      return [shown + (quick?.enter ? " ⏎" : ""), t("keybar.sendQuick", { text })];
    }
  }
}

/** The bytes one press sends. appCursor is xterm's application cursor mode,
 * in which an unmodified arrow is SS3 (ESC O A) rather than CSI. encode, when
 * given, is the extended encoding a program asked for (extended-keys.ts): a
 * Shift+Enter combo then really is Shift+Enter. */
export function itemBytes(item: KeyItem, appCursor = false, quick?: QuickCommand,
  encode?: (text: string, mods: Mods) => string | undefined): string {
  switch (item.t) {
    case "key": {
      const text: string = builtinKeys[item.id];
      const extended = encode?.(text, { ctrl: false, alt: false });
      if (extended !== undefined) return extended;
      return appCursor && /^\x1b\[[ABCD]$/.test(text) ? text.replace("[", "O") : text;
    }
    case "combo": {
      const base: string = isBuiltin(item.key) ? builtinKeys[item.key] : item.key;
      const mods = { ctrl: !!item.ctrl, alt: !!item.alt, shift: !!item.shift };
      return encode?.(base, mods) ?? modified(base, mods);
    }
    case "quick":
      return quick ? commandBytes(quick) : "";
    default:
      return "";
  }
}

const repeating = new Set<string>(["left", "up", "down", "right", "backspace", "delete", "pageup", "pagedown"]);

/** Whether holding the key repeats it, like a hardware keyboard's arrows. */
export function repeats(item: KeyItem): boolean {
  if (item.t === "key") return repeating.has(item.id);
  if (item.t === "combo") return repeating.has(item.key);
  return false;
}

/** Keys a person can add that are not on the row yet. */
export function missingBuiltins(row: KeyItem[]): KeyItem[] {
  const present = new Set(row.map(itemId));
  const all: KeyItem[] = [
    { t: "snippets" },
    { t: "find" },
    { t: "mod", id: "ctrl" },
    { t: "mod", id: "alt" },
    ...(Object.keys(builtinKeys) as BuiltinKey[]).map((id) => ({ t: "key" as const, id })),
  ];
  return all.filter((item) => !present.has(itemId(item)));
}

export function quickItem(command: QuickCommand): KeyItem {
  return { t: "quick", id: command.id };
}

export function move(row: KeyItem[], index: number, by: -1 | 1): KeyItem[] {
  const to = index + by;
  if (to < 0 || to >= row.length) return row;
  const next = row.slice();
  [next[index], next[to]] = [next[to]!, next[index]!];
  return next;
}

export function addItem(row: KeyItem[], item: KeyItem): KeyItem[] {
  return row.some((other) => sameItem(other, item)) || row.length >= MAX_ITEMS ? row : [...row, item];
}

const MAX_ITEMS = 60;
const KEY = "lec-terminal-keybar";

export function parseKeybar(raw: string | null): KeyItem[] {
  if (raw === null) return defaultKeybar;
  try {
    const value: unknown = JSON.parse(raw);
    if (!Array.isArray(value)) return defaultKeybar;
    const out: KeyItem[] = [];
    for (const row of value.slice(0, MAX_ITEMS)) {
      const item = cleanItem(row);
      if (item && !out.some((other) => sameItem(other, item))) out.push(item);
    }
    return out;
  } catch {
    return defaultKeybar;
  }
}

function cleanItem(row: unknown): KeyItem | undefined {
  if (!row || typeof row !== "object") return undefined;
  const r = row as Record<string, unknown>;
  if (r.t === "snippets") return { t: "snippets" };
  if (r.t === "find") return { t: "find" };
  if (r.t === "key" && isBuiltin(r.id)) return { t: "key", id: r.id };
  if (r.t === "mod" && (r.id === "ctrl" || r.id === "alt")) return { t: "mod", id: r.id };
  if (r.t === "combo" && typeof r.key === "string" && validComboKey(r.key) && (r.ctrl || r.alt || r.shift))
    return { t: "combo", key: r.key, ctrl: !!r.ctrl, alt: !!r.alt, shift: !!r.shift };
  if (r.t === "quick" && typeof r.id === "string" && r.id) return { t: "quick", id: r.id.slice(0, 40) };
  return undefined;
}

export function loadKeybar(): KeyItem[] {
  try {
    return parseKeybar(localStorage.getItem(KEY));
  } catch {
    return defaultKeybar;
  }
}

export function saveKeybar(row: KeyItem[] | null) {
  try {
    if (row === null) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, JSON.stringify(row));
  } catch {
    /* storage blocked: the row still works for this visit */
  }
}
