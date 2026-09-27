// Keyboard chords as strings: "Ctrl+Shift+K", "Alt+`", "F3". Modifiers are
// always written in the order Ctrl, Alt, Shift, Meta, so two ways of pressing
// the same thing compare equal. "Mod" in a default means Meta (⌘) on a Mac and
// Ctrl everywhere else.

const MODIFIERS = ["Ctrl", "Alt", "Shift", "Meta"] as const;
type Modifier = (typeof MODIFIERS)[number];

const codeKeys: Record<string, string> = {
  Backquote: "`", Minus: "-", Equal: "=", BracketLeft: "[", BracketRight: "]", Backslash: "\\",
  Semicolon: ";", Quote: "'", Comma: ",", Period: ".", Slash: "/", Space: "Space",
  IntlBackslash: "\\",
};
const namedKeys: Record<string, string> = {
  ArrowUp: "Up", ArrowDown: "Down", ArrowLeft: "Left", ArrowRight: "Right", " ": "Space", Esc: "Escape",
};

export function isMac(platform = typeof navigator === "undefined" ? "" : navigator.platform || navigator.userAgent) {
  return /Mac|iPhone|iPad|iPod/i.test(platform);
}

export interface KeyLike {
  key: string;
  code?: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
}

// The key a chord names. Letters and digits come from the physical key (code)
// so Alt on a Mac, which rewrites event.key, still gives the same chord.
function keyName(event: KeyLike): string | null {
  if (["Control", "Alt", "Shift", "Meta", "AltGraph", "CapsLock", "OS", "Dead", "Unidentified", "Process"].includes(event.key)) return null;
  const code = event.code || "";
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1]!;
  const digit = /^(?:Digit|Numpad)(\d)$/.exec(code);
  if (digit) return digit[1]!;
  if (codeKeys[code]) return codeKeys[code]!;
  if (namedKeys[event.key]) return namedKeys[event.key]!;
  if (event.key.length === 1) return event.key.toUpperCase();
  return event.key;
}

export function eventChord(event: KeyLike): string | null {
  const key = keyName(event);
  if (!key) return null;
  const mods: Modifier[] = [];
  if (event.ctrlKey) mods.push("Ctrl");
  if (event.altKey) mods.push("Alt");
  if (event.shiftKey) mods.push("Shift");
  if (event.metaKey) mods.push("Meta");
  return [...mods, key].join("+");
}

// Canonical form of a written chord; null when it is not a chord at all.
export function normalizeChord(chord: string, mac = isMac()): string | null {
  const parts = chord.split("+").map((part) => part.trim());
  // "Ctrl++" names the plus key: the split leaves two empty parts at the end.
  if (chord.endsWith("++")) parts.splice(parts.length - 2, 2, "+");
  const mods = new Set<Modifier>();
  let key = "";
  for (const raw of parts) {
    if (!raw) return null;
    const lower = raw.toLowerCase();
    const mod =
      lower === "mod" ? (mac ? "Meta" : "Ctrl")
      : lower === "ctrl" || lower === "control" ? "Ctrl"
      : lower === "alt" || lower === "option" || lower === "opt" ? "Alt"
      : lower === "shift" ? "Shift"
      : lower === "meta" || lower === "cmd" || lower === "command" || lower === "super" || lower === "win" ? "Meta"
      : null;
    if (mod) {
      mods.add(mod);
      continue;
    }
    if (key) return null;
    key = raw.length === 1 ? raw.toUpperCase() : namedKeys[raw] || raw[0]!.toUpperCase() + raw.slice(1);
  }
  if (!key) return null;
  return [...MODIFIERS.filter((mod) => mods.has(mod)), key].join("+");
}

export function chordParts(chord: string) {
  const parts = chord.split("+");
  const key = chord.endsWith("++") ? "+" : parts.at(-1)!;
  return {
    ctrl: parts.includes("Ctrl"),
    alt: parts.includes("Alt"),
    shift: parts.includes("Shift"),
    meta: parts.includes("Meta"),
    key,
  };
}

// How a chord is shown: ⌘⌥⇧ on a Mac, words elsewhere.
export function displayChord(chord: string, mac = isMac()): string {
  const { ctrl, alt, shift, meta, key } = chordParts(chord);
  if (mac) return (ctrl ? "⌃" : "") + (alt ? "⌥" : "") + (shift ? "⇧" : "") + (meta ? "⌘" : "") + key;
  return [ctrl && "Ctrl", alt && "Alt", shift && "Shift", meta && "Win", key].filter(Boolean).join("+");
}

// Keys a terminal program might want are left to the terminal. A chord is
// forwarded out of a focused terminal only when no shell or TUI plausibly
// uses it: anything with ⌘/Win, Ctrl+Shift or Alt+Shift (except the three
// that type control characters on a US layout), function keys, and the tab
// and backquote chords editors use for switching.
export function terminalSafe(chord: string): boolean {
  const { ctrl, alt, shift, meta, key } = chordParts(chord);
  if (meta) return true;
  if (/^F\d{1,2}$/.test(key)) return true;
  if (ctrl && (key === "Tab" || key === "`" || key === "PageUp" || key === "PageDown")) return true;
  if (alt && !ctrl && key === "`") return true;
  if (shift && (ctrl || alt)) return !(ctrl && ["2", "6", "-"].includes(key));
  return false;
}

// Chords the browser keeps for itself in an ordinary tab; the page never sees
// them, so Settings warns rather than letting one silently do nothing.
const reserved = new Set(["Ctrl+T", "Ctrl+N", "Ctrl+W", "Ctrl+Shift+T", "Ctrl+Shift+N", "Ctrl+Shift+W", "Ctrl+Tab", "Ctrl+Shift+Tab", "Meta+T", "Meta+N", "Meta+W", "Meta+Q", "Alt+F4"]);
export function browserReserved(chord: string) {
  return reserved.has(chord);
}
