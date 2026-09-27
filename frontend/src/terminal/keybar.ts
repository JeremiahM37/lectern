// The phone's key row, as the person arranged it: which keys, in what order,
// including Ctrl/Alt/Shift combinations and their own saved replies (Quick
// Commands). Kept free of the DOM so it is tested as plain functions; the
// row itself is Keybar.tsx. Stored per device, like the type size.
import { modified } from "./keys";
import { snippetBytes, type Snippet } from "./snippets";

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

export const builtinLabels: Record<BuiltinKey, [string, string]> = {
  escape: ["Esc", "Send Escape"],
  tab: ["Tab", "Send Tab"],
  backtab: ["⇧Tab", "Send Shift-Tab"],
  left: ["←", "Send Left arrow"],
  up: ["↑", "Send Up arrow"],
  down: ["↓", "Send Down arrow"],
  right: ["→", "Send Right arrow"],
  interrupt: ["^C", "Send Ctrl-C"],
  slash: ["/", "Send slash"],
  dash: ["-", "Send dash"],
  pipe: ["|", "Send pipe"],
  tilde: ["~", "Send tilde"],
  home: ["Home", "Send Home"],
  end: ["End", "Send End"],
  pageup: ["PgUp", "Send Page Up"],
  pagedown: ["PgDn", "Send Page Down"],
  enter: ["⏎", "Send Enter"],
  backspace: ["⌫", "Send Backspace"],
  delete: ["Del", "Send Delete"],
};

export type KeyItem =
  | { t: "key"; id: BuiltinKey }
  | { t: "mod"; id: "ctrl" | "alt" }
  | { t: "combo"; key: string; ctrl?: boolean; alt?: boolean; shift?: boolean }
  | { t: "text"; text: string; enter: boolean }
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
    case "combo":
      return `${item.ctrl ? "C-" : ""}${item.alt ? "M-" : ""}${item.shift ? "S-" : ""}${item.key}`;
    case "text":
      return `text:${item.enter ? "1" : "0"}:${item.text}`;
  }
}

function keyLabel(key: string, ctrl?: boolean): string {
  if (isBuiltin(key)) return builtinLabels[key][0];
  return ctrl ? key.toUpperCase() : key;
}

/** [what the button shows, what a screen reader says]. */
export function itemLabel(item: KeyItem): [string, string] {
  switch (item.t) {
    case "key":
      return builtinLabels[item.id];
    case "mod":
      return item.id === "ctrl" ? ["Ctrl", "Hold Ctrl for the next key"] : ["Alt", "Hold Alt for the next key"];
    case "snippets":
      return ["⚡", "Snippets"];
    case "combo": {
      const mods = [item.ctrl && "Ctrl", item.alt && "Alt", item.shift && "Shift"].filter(Boolean).join("-");
      const shown = `${item.ctrl ? "^" : ""}${item.alt ? "⌥" : ""}${item.shift ? "⇧" : ""}${keyLabel(item.key, item.ctrl)}`;
      const spoken = isBuiltin(item.key) ? builtinLabels[item.key][1].replace(/^Send /, "") : item.key;
      return [shown, `Send ${mods}-${spoken}`];
    }
    case "text": {
      const shown = item.text.length > 12 ? item.text.slice(0, 11) + "…" : item.text;
      return [shown + (item.enter ? " ⏎" : ""), `Send ${item.text}${item.enter ? " and Enter" : ""}`];
    }
  }
}

/** The bytes one press sends. appCursor is xterm's application cursor mode,
 * in which an unmodified arrow is SS3 (ESC O A) rather than CSI. */
export function itemBytes(item: KeyItem, appCursor = false): string {
  switch (item.t) {
    case "key": {
      const text: string = builtinKeys[item.id];
      return appCursor && /^\x1b\[[ABCD]$/.test(text) ? text.replace("[", "O") : text;
    }
    case "combo": {
      const base: string = isBuiltin(item.key) ? builtinKeys[item.key] : item.key;
      return modified(base, { ctrl: !!item.ctrl, alt: !!item.alt, shift: !!item.shift });
    }
    case "text":
      return snippetBytes({ text: item.text, enter: item.enter });
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
    { t: "mod", id: "ctrl" },
    { t: "mod", id: "alt" },
    ...(Object.keys(builtinKeys) as BuiltinKey[]).map((id) => ({ t: "key" as const, id })),
  ];
  return all.filter((item) => !present.has(itemId(item)));
}

export function snippetItem(snippet: Snippet): KeyItem {
  return { t: "text", text: snippet.text, enter: snippet.enter };
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
  if (r.t === "key" && isBuiltin(r.id)) return { t: "key", id: r.id };
  if (r.t === "mod" && (r.id === "ctrl" || r.id === "alt")) return { t: "mod", id: r.id };
  if (r.t === "combo" && typeof r.key === "string" && validComboKey(r.key) && (r.ctrl || r.alt || r.shift))
    return { t: "combo", key: r.key, ctrl: !!r.ctrl, alt: !!r.alt, shift: !!r.shift };
  if (r.t === "text" && typeof r.text === "string" && r.text.length > 0)
    return { t: "text", text: r.text.slice(0, 2000), enter: r.enter !== false };
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
