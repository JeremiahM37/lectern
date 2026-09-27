// Extended keyboard reporting for the web terminal: the kitty keyboard
// protocol (https://sw.kovidgoyal.net/kitty/keyboard-protocol/) and xterm's
// modifyOtherKeys. xterm.js 5.5 has neither, so a program could never tell
// Shift+Enter from Enter, Ctrl+I from Tab, or see Ctrl+Shift+letters.
//
// Two halves, both free of the DOM so they are tested as plain functions:
// KeyboardModes is the state programs set with escape sequences (engine.ts
// registers the parser hooks), and encodeKey turns one browser key event into
// the bytes a program asked for. Anything that returns null here is left to
// xterm.js, so with nothing requested the terminal behaves exactly as before.
//
// tmux, which every Lectern terminal runs in, asks for modifyOtherKeys mode 2
// (CSI > 4 ; 2 m) and forwards kitty-style CSI u to the programs that want it
// (docs/workspace.md). A program talking to this terminal directly may use
// either protocol.
//
// The kitty half is a port of kitty's own encoder (kitty/key_encoding.c) so
// that edge cases match the reference terminal, not a reading of the prose.

// kitty's key numbers for keys that are not text: its private-use range.
const F = {
  ESCAPE: 57344, ENTER: 57345, TAB: 57346, BACKSPACE: 57347, INSERT: 57348, DELETE: 57349,
  LEFT: 57350, RIGHT: 57351, UP: 57352, DOWN: 57353, PAGE_UP: 57354, PAGE_DOWN: 57355,
  HOME: 57356, END: 57357, CAPS_LOCK: 57358, SCROLL_LOCK: 57359, NUM_LOCK: 57360,
  PRINT_SCREEN: 57361, PAUSE: 57362, MENU: 57363, F1: 57364,
  KP_0: 57399, KP_DECIMAL: 57409, KP_DIVIDE: 57410, KP_MULTIPLY: 57411, KP_SUBTRACT: 57412,
  KP_ADD: 57413, KP_ENTER: 57414, KP_EQUAL: 57415, KP_SEPARATOR: 57416, KP_LEFT: 57417,
  KP_RIGHT: 57418, KP_UP: 57419, KP_DOWN: 57420, KP_PAGE_UP: 57421, KP_PAGE_DOWN: 57422,
  KP_HOME: 57423, KP_END: 57424, KP_INSERT: 57425, KP_DELETE: 57426, KP_BEGIN: 57427,
  LEFT_SHIFT: 57441, RIGHT_SHIFT: 57447, ISO_LEVEL3_SHIFT: 57453, ISO_LEVEL5_SHIFT: 57454,
} as const;
const FKEY_FIRST = F.ESCAPE, FKEY_LAST = F.ISO_LEVEL5_SHIFT;
const isModifierKey = (key: number) => key >= F.LEFT_SHIFT && key <= FKEY_LAST;

// Modifier bits, as the protocol numbers them (1 is added when encoded).
const SHIFT = 1, ALT = 2, CTRL = 4, SUPER = 8, CAPS_LOCK = 64, NUM_LOCK = 128;
const LOCK_MASK = CAPS_LOCK | NUM_LOCK;

export const Flag = { disambiguate: 1, events: 2, alternates: 4, allKeys: 8, text: 16 } as const;
const ALL_FLAGS = 31;
// Programs push and pop; a program that only pushes must not grow memory.
export const STACK_LIMIT = 16;

interface Screen { flags: number; stack: number[] }

// The state programs set: kitty flags per screen (main and alternate keep
// separate stacks, so an editor on the alternate screen never disturbs the
// shell's), and modifyOtherKeys, which xterm keeps once for the terminal.
export class KeyboardModes {
  private main: Screen = { flags: 0, stack: [] };
  private alt: Screen = { flags: 0, stack: [] };
  modifyOtherKeys = 0;
  private screen(alt: boolean) { return alt ? this.alt : this.main; }
  flags(alt: boolean) { return this.screen(alt).flags; }
  push(alt: boolean, flags: number) {
    const s = this.screen(alt);
    s.stack.push(s.flags);
    if (s.stack.length > STACK_LIMIT) s.stack.shift();
    s.flags = flags & ALL_FLAGS;
  }
  // A pop past the bottom of the stack resets every flag.
  pop(alt: boolean, count: number) {
    const s = this.screen(alt);
    for (let i = 0; i < Math.max(1, count); i++) s.flags = s.stack.pop() ?? 0;
  }
  set(alt: boolean, flags: number, mode: number) {
    const s = this.screen(alt);
    flags &= ALL_FLAGS;
    if (mode === 2) s.flags |= flags;
    else if (mode === 3) s.flags &= ~flags;
    else s.flags = flags;
  }
  // Entering the alternate screen starts it from nothing, as kitty does: the
  // program that left flags there has gone.
  clearScreen(alt: boolean) {
    const s = this.screen(alt);
    s.flags = 0;
    s.stack = [];
  }
  reset() {
    this.clearScreen(false);
    this.clearScreen(true);
    this.modifyOtherKeys = 0;
  }
  active(alt: boolean) { return this.flags(alt) !== 0 || this.modifyOtherKeys !== 0; }
}

/** What engine.ts reads off a KeyboardEvent; plain data so tests can build it. */
export interface KeyInput {
  type: "keydown" | "keyup";
  key: string;
  code: string;
  location?: number;
  repeat?: boolean;
  shiftKey: boolean;
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  capsLock?: boolean;
  numLock?: boolean;
  altGraph?: boolean;
}

export function keyInput(event: KeyboardEvent): KeyInput {
  const state = (name: string) => {
    try { return event.getModifierState(name); } catch { return false; }
  };
  return {
    type: event.type === "keyup" ? "keyup" : "keydown",
    key: event.key, code: event.code, location: event.location, repeat: event.repeat,
    shiftKey: event.shiftKey, altKey: event.altKey, ctrlKey: event.ctrlKey, metaKey: event.metaKey,
    capsLock: state("CapsLock"), numLock: state("NumLock"), altGraph: state("AltGraph"),
  };
}

// The US (PC-101) layout: the "base layout key" of the protocol, and how a
// shifted character is traced back to its key when the layout is unknown.
const US: Record<string, [string, string]> = {
  Backquote: ["`", "~"], Minus: ["-", "_"], Equal: ["=", "+"], BracketLeft: ["[", "{"],
  BracketRight: ["]", "}"], Backslash: ["\\", "|"], Semicolon: [";", ":"], Quote: ["'", "\""],
  Comma: [",", "<"], Period: [".", ">"], Slash: ["/", "?"], Space: [" ", " "],
  Digit1: ["1", "!"], Digit2: ["2", "@"], Digit3: ["3", "#"], Digit4: ["4", "$"], Digit5: ["5", "%"],
  Digit6: ["6", "^"], Digit7: ["7", "&"], Digit8: ["8", "*"], Digit9: ["9", "("], Digit0: ["0", ")"],
};
for (let c = 97; c <= 122; c++) {
  const ch = String.fromCharCode(c);
  US["Key" + ch.toUpperCase()] = [ch, ch.toUpperCase()];
}

const named: Record<string, number> = {
  Escape: F.ESCAPE, Enter: F.ENTER, Tab: F.TAB, Backspace: F.BACKSPACE, Insert: F.INSERT,
  Delete: F.DELETE, ArrowLeft: F.LEFT, ArrowRight: F.RIGHT, ArrowUp: F.UP, ArrowDown: F.DOWN,
  PageUp: F.PAGE_UP, PageDown: F.PAGE_DOWN, Home: F.HOME, End: F.END, CapsLock: F.CAPS_LOCK,
  ScrollLock: F.SCROLL_LOCK, NumLock: F.NUM_LOCK, PrintScreen: F.PRINT_SCREEN, Pause: F.PAUSE,
  ContextMenu: F.MENU, Clear: F.KP_BEGIN,
  MediaPlay: 57428, MediaPause: 57429, MediaPlayPause: 57430, MediaStop: 57432,
  MediaFastForward: 57433, MediaRewind: 57434, MediaTrackNext: 57435, MediaTrackPrevious: 57436,
  MediaRecord: 57437, AudioVolumeDown: 57438, AudioVolumeUp: 57439, AudioVolumeMute: 57440,
  AltGraph: F.ISO_LEVEL3_SHIFT,
};
// Left, right: KeyboardEvent.location 2 is the right-hand key.
const modifierKeys: Record<string, [number, number]> = {
  Shift: [57441, 57447], Control: [57442, 57448], Alt: [57443, 57449],
  Meta: [57444, 57450], OS: [57444, 57450], Super: [57444, 57450], Hyper: [57445, 57451],
};
const keypad: Record<string, number> = {
  NumpadDecimal: F.KP_DECIMAL, NumpadDivide: F.KP_DIVIDE, NumpadMultiply: F.KP_MULTIPLY,
  NumpadSubtract: F.KP_SUBTRACT, NumpadAdd: F.KP_ADD, NumpadEnter: F.KP_ENTER,
  NumpadEqual: F.KP_EQUAL, NumpadComma: F.KP_SEPARATOR,
};
// Keypad keys with Num Lock off report the key they act as.
const keypadNav: Record<string, number> = {
  ArrowLeft: F.KP_LEFT, ArrowRight: F.KP_RIGHT, ArrowUp: F.KP_UP, ArrowDown: F.KP_DOWN,
  PageUp: F.KP_PAGE_UP, PageDown: F.KP_PAGE_DOWN, Home: F.KP_HOME, End: F.KP_END,
  Insert: F.KP_INSERT, Delete: F.KP_DELETE, Clear: F.KP_BEGIN, Enter: F.KP_ENTER,
};

const single = (key: string) => [...key].length === 1;
const cp = (s: string) => s.codePointAt(0)!;
const printable = (key: string) => single(key) && cp(key) >= 32 && cp(key) !== 127 && !(cp(key) >= 128 && cp(key) < 160);

interface Event {
  key: number; shifted: number; alternate: number; text: string;
  mods: number; action: 0 | 1 | 2; // press, repeat, release
}

// Turns a browser event into kitty's view of it: key number, the text the
// key types (if any), modifiers. Returns undefined for anything that is not a
// key this protocol describes (IME, dead keys), which then stays legacy.
function describe(input: KeyInput): Event | undefined {
  const { key, code } = input;
  if (!key || key === "Dead" || key === "Unidentified" || key === "Process") return undefined;
  let mods = (input.shiftKey ? SHIFT : 0) | (input.altKey ? ALT : 0) | (input.ctrlKey ? CTRL : 0) |
    (input.metaKey ? SUPER : 0) | (input.capsLock ? CAPS_LOCK : 0) | (input.numLock ? NUM_LOCK : 0);
  const action = input.type === "keyup" ? 2 : input.repeat ? 1 : 0;
  const ev: Event = { key: 0, shifted: 0, alternate: 0, text: "", mods, action };
  const pair = modifierKeys[key];
  if (pair) {
    ev.key = pair[input.location === 2 ? 1 : 0]!;
    return ev;
  }
  if (code.startsWith("Numpad")) {
    const digit = /^Numpad(\d)$/.exec(code);
    if (printable(key)) {
      ev.key = digit ? F.KP_0 + Number(digit[1]) : keypad[code] ?? 0;
      if (!ev.key) return undefined;
      if (!(mods & (CTRL | ALT | SUPER)) && action !== 2) ev.text = key;
      return ev;
    }
    ev.key = keypadNav[key] ?? keypad[code] ?? 0;
    return ev.key ? ev : undefined;
  }
  const fkey = /^F(\d{1,2})$/.exec(key);
  if (fkey && Number(fkey[1]) >= 1 && Number(fkey[1]) <= 35) {
    ev.key = F.F1 + Number(fkey[1]) - 1;
    return ev;
  }
  if (named[key]) {
    ev.key = named[key]!;
    return ev;
  }
  if (!printable(key)) return undefined;
  const us = US[code];
  // AltGr (and the Option key on a Mac) is the layout's own: the character it
  // produced is the text, and the modifier was spent producing it.
  const composed = input.altGraph ||
    (input.altKey && !input.ctrlKey && !!us && key !== us[0] && key !== us[1] && key.toLowerCase() !== us[0]);
  if (composed) {
    mods &= ~(CTRL | ALT);
    ev.mods = mods;
  }
  const lower = key.toLowerCase();
  let base = key;
  if (lower !== key && single(lower)) base = lower;
  else if (input.shiftKey && us && us[1] === key) base = us[0];
  ev.key = cp(base);
  if (input.shiftKey && base !== key) ev.shifted = cp(key);
  if (us && us[0] !== base) ev.alternate = cp(us[0]);
  // A release types nothing, so it carries no text.
  if ((composed || !(mods & (CTRL | ALT | SUPER))) && action !== 2) ev.text = key;
  return ev;
}

// kitty's ctrl mapping for legacy text keys (key_encoding.c ctrled_key).
function ctrled(key: string): string {
  const map: Record<string, number> = {
    " ": 0, "/": 31, "2": 0, "3": 27, "4": 28, "5": 29, "6": 30, "7": 31, "8": 127, "?": 127,
    "@": 0, "[": 27, "\\": 28, "]": 29, "^": 30, "_": 31, "~": 30,
  };
  if (key >= "a" && key <= "z") return String.fromCharCode(key.charCodeAt(0) - 96);
  return key in map ? String.fromCharCode(map[key]!) : key;
}
const legacyAscii = (key: number) => key > 0 && key < 128 && /^[a-z0-9!@#$%^&*()`~\-_=+[\]{}\\|;:'",<.>/? ]$/.test(String.fromCharCode(key));

function printableLegacy(ev: Event): string {
  let mods = ev.mods & ~LOCK_MASK;
  if (!mods) return String.fromCodePoint(ev.key);
  let key = String.fromCodePoint(ev.key);
  if (mods & SHIFT && ev.shifted && ev.shifted !== ev.key && (!(mods & CTRL) || key < "a" || key > "z")) {
    key = String.fromCodePoint(ev.shifted);
    mods &= ~SHIFT;
  }
  const all = ev.mods & ~LOCK_MASK;
  if (all === SHIFT) return key;
  if (mods === ALT) return "\x1b" + key;
  if (mods === CTRL) return ctrled(key);
  if (mods === (CTRL | ALT)) return "\x1b" + ctrled(key);
  if (key === " ") {
    if (mods === (CTRL | SHIFT)) return ctrled(key);
    if (mods === (ALT | SHIFT)) return "\x1b" + key;
  }
  return "";
}

interface Options { flags: number; cursorKeys: boolean }

function serialize(fields: { key: number; shifted?: number; alternate?: number; mods: number; action: number; text?: string },
  flags: number, trailer: string): string {
  const alternates = !!(flags & Flag.alternates) && trailer === "u" &&
    ((fields.shifted! > 0 && !!(fields.mods & SHIFT)) || fields.alternate! > 0);
  const actions = !!(flags & Flag.events) && fields.action !== 0;
  const hasMods = fields.mods !== 0;
  const second = hasMods || actions;
  const text = !!(flags & Flag.text) && !!fields.text;
  let out = "\x1b[";
  if (fields.key !== 1 || alternates || second || text) out += fields.key;
  if (alternates) {
    out += ":";
    if (fields.mods & SHIFT && fields.shifted) out += fields.shifted;
    if (fields.alternate) out += ":" + fields.alternate;
  }
  if (second || text) {
    out += ";";
    if (second) out += fields.mods + 1;
    if (actions) out += ":" + (fields.action + 1);
  }
  if (text) out += ";" + [...fields.text!].map(cp).join(":");
  return out + trailer;
}

function functional(ev: Event, o: Options): string {
  const f = o.flags, disambiguate = !!(f & Flag.disambiguate), events = !!(f & Flag.events), all = !!(f & Flag.allKeys);
  const legacy = !events && !disambiguate && !all;
  const mods = ev.mods;
  let key = ev.key;
  if (o.cursorKeys && legacy && !mods) {
    const ss3: Record<number, string> = { [F.UP]: "A", [F.DOWN]: "B", [F.RIGHT]: "C", [F.LEFT]: "D", [F.KP_BEGIN]: "E", [F.END]: "F", [F.HOME]: "H" };
    if (ss3[key]) return "\x1bO" + ss3[key];
  }
  if (!mods) {
    if (!disambiguate && !all && key === F.ESCAPE) return "\x1b";
    if (legacy) {
      const ss3: Record<number, string> = { [F.F1]: "P", [F.F1 + 1]: "Q", [F.F1 + 2]: "R", [F.F1 + 3]: "S" };
      if (ss3[key]) return "\x1bO" + ss3[key];
    }
  } else if (legacy) {
    const alt = mods & ALT ? "\x1b" : "";
    if (key === F.ENTER) return alt + "\r";
    if (key === F.ESCAPE) return alt + "\x1b";
    if (key === F.BACKSPACE) return alt + (mods & CTRL ? "\x08" : "\x7f");
    if (key === F.TAB) return mods & SHIFT ? (mods & ALT ? "\x1b\x1b[Z" : "\x1b[Z") : alt + "\t";
  }
  // Enter, Tab and Backspace stay as typed even here, so `reset` can still be
  // typed after a program dies without restoring the mode.
  if (!(mods & ~LOCK_MASK) && !all) {
    if (key === F.ENTER) return ev.action === 2 ? "" : "\r";
    if (key === F.BACKSPACE) return ev.action === 2 ? "" : "\x7f";
    if (key === F.TAB) return ev.action === 2 ? "" : "\t";
  }
  let trailer = "u";
  const special: Record<number, [number, string]> = {
    [F.ESCAPE]: [27, "u"], [F.ENTER]: [13, "u"], [F.TAB]: [9, "u"], [F.BACKSPACE]: [127, "u"],
    [F.INSERT]: [2, "~"], [F.DELETE]: [3, "~"], [F.LEFT]: [1, "D"], [F.RIGHT]: [1, "C"],
    [F.UP]: [1, "A"], [F.DOWN]: [1, "B"], [F.PAGE_UP]: [5, "~"], [F.PAGE_DOWN]: [6, "~"],
    [F.HOME]: [1, "H"], [F.END]: [1, "F"], [F.F1]: [1, "P"], [F.F1 + 1]: [1, "Q"],
    [F.F1 + 2]: [13, "~"], [F.F1 + 3]: [1, "S"], [F.F1 + 4]: [15, "~"], [F.F1 + 5]: [17, "~"],
    [F.F1 + 6]: [18, "~"], [F.F1 + 7]: [19, "~"], [F.F1 + 8]: [20, "~"], [F.F1 + 9]: [21, "~"],
    [F.F1 + 10]: [23, "~"], [F.F1 + 11]: [24, "~"], [F.KP_BEGIN]: [1, "E"],
  };
  if (special[key]) [key, trailer] = special[key]!;
  else if (key === F.MENU && legacy) [key, trailer] = [29, "~"];
  return serialize({ key, mods, action: ev.action, text: ev.text }, f & ~Flag.alternates, trailer);
}

/**
 * The bytes for one key event under the kitty flags a program set. null means
 * "type it as usual" (text goes through xterm.js and the IME untouched); ""
 * means the event reports nothing.
 */
export function encodeKitty(input: KeyInput, o: Options): string | null {
  const f = o.flags;
  const ev = describe(input);
  if (!ev) return null;
  if (!(f & Flag.events) && ev.action === 2) return "";
  const all = !!(f & Flag.allKeys);
  if (!all && isModifierKey(ev.key)) return "";
  // The keypad reports itself only once disambiguation was asked for.
  if (!(f & Flag.disambiguate) && !all && ev.key >= F.KP_0 && ev.key <= F.KP_BEGIN) {
    const plain: Record<number, number> = {
      [F.KP_ENTER]: F.ENTER, [F.KP_HOME]: F.HOME, [F.KP_END]: F.END, [F.KP_INSERT]: F.INSERT,
      [F.KP_DELETE]: F.DELETE, [F.KP_PAGE_UP]: F.PAGE_UP, [F.KP_PAGE_DOWN]: F.PAGE_DOWN,
      [F.KP_UP]: F.UP, [F.KP_DOWN]: F.DOWN, [F.KP_LEFT]: F.LEFT, [F.KP_RIGHT]: F.RIGHT,
      [F.KP_DECIMAL]: 46, [F.KP_DIVIDE]: 47, [F.KP_MULTIPLY]: 42, [F.KP_SUBTRACT]: 45,
      [F.KP_ADD]: 43, [F.KP_EQUAL]: 61,
    };
    if (ev.key <= F.KP_0 + 9) ev.key = 48 + ev.key - F.KP_0;
    else if (plain[ev.key]) ev.key = plain[ev.key]!;
  }
  // Text is typed, not reported, unless every key is to be an escape code.
  if (!all && ev.text && ev.action !== 2) return null;
  if (ev.key >= FKEY_FIRST && ev.key <= FKEY_LAST) return functional(ev, o);
  const alternates = !!(f & Flag.alternates) && ((ev.shifted > 0 && !!(ev.mods & SHIFT)) || ev.alternate > 0);
  const actions = !!(f & Flag.events) && ev.action !== 0;
  const embed = !!(f & Flag.text) && !!ev.text;
  if (!actions && !alternates && !embed) {
    if (!ev.mods) return all ? serialize(ev, f, "u") : String.fromCodePoint(ev.key);
    if (!(f & Flag.disambiguate) && !all) {
      if (legacyAscii(ev.key) || (ev.shifted && legacyAscii(ev.shifted))) {
        const out = printableLegacy(ev);
        if (out) return out;
      }
      const m = ev.mods & ~LOCK_MASK;
      if ((m === CTRL || m === ALT || m === (CTRL | ALT)) && ev.alternate && !legacyAscii(ev.key) && legacyAscii(ev.alternate)) {
        const out = printableLegacy({ ...ev, key: ev.alternate, alternate: 0, shifted: 0 });
        if (out) return out;
      }
    }
  }
  return serialize(ev, f, "u");
}

/**
 * xterm's modifyOtherKeys: CSI 27 ; modifiers ; code ~ for modified keys that
 * the ordinary encoding loses. Level 1 reports only the ambiguous ones; level
 * 2 (what tmux asks for) reports every Ctrl, Alt or Shift+Enter-like chord.
 * null leaves the key to xterm.js, which already encodes arrows, function
 * keys and the rest.
 */
export function encodeModifyOtherKeys(input: KeyInput, level: number): string | null {
  if (input.type !== "keydown" || !level) return null;
  const { key } = input;
  let mods = (input.shiftKey ? SHIFT : 0) | (input.altKey ? ALT : 0) | (input.ctrlKey ? CTRL : 0) | (input.metaKey ? SUPER : 0);
  if (input.altGraph) mods &= ~(CTRL | ALT);
  if (!mods) return null;
  const report = (code: number) => `\x1b[27;${mods + 1};${code}~`;
  const control: Record<string, number> = { Enter: 13, Tab: 9, Backspace: 127, Escape: 27 };
  if (key in control) {
    if (key === "Tab" && mods === SHIFT) return null; // CSI Z, as xterm sends
    if (level < 2 && !(mods & (CTRL | SHIFT))) return null;
    return report(control[key]!);
  }
  if (!printable(key) || input.code.startsWith("Numpad")) return null;
  if (!(mods & (CTRL | ALT | SUPER))) return null; // Shift alone types the character
  const us = US[input.code];
  if (input.altKey && !input.ctrlKey && us && key !== us[0] && key !== us[1] && key.toLowerCase() !== us[0]) return null;
  // The character as typed, Shift applied: Ctrl+Shift+A is 65, as xterm and tmux have it.
  const ch = mods & SHIFT ? key : key.toLowerCase();
  if (level < 2) {
    const m = mods & ~SHIFT;
    const lower = key.toLowerCase();
    const hasControl = ctrled(lower) !== lower;
    if (m === ALT) return null; // ESC prefix, unambiguous
    if (m === CTRL && !(mods & SHIFT) && hasControl) return null;
  }
  return report(cp(ch));
}

// Bytes the key bar and a phone keyboard send, read back as the key that
// would have produced them, so a sticky modifier can be reported properly.
const legacyKeys: Record<string, Pick<KeyInput, "key" | "code"> & { shift?: boolean; ctrl?: boolean }> = {
  "\r": { key: "Enter", code: "Enter" }, "\t": { key: "Tab", code: "Tab" },
  "\x7f": { key: "Backspace", code: "Backspace" }, "\x1b": { key: "Escape", code: "Escape" },
  "\x1b[Z": { key: "Tab", code: "Tab", shift: true },
  "\x1b[A": { key: "ArrowUp", code: "ArrowUp" }, "\x1bOA": { key: "ArrowUp", code: "ArrowUp" },
  "\x1b[B": { key: "ArrowDown", code: "ArrowDown" }, "\x1bOB": { key: "ArrowDown", code: "ArrowDown" },
  "\x1b[C": { key: "ArrowRight", code: "ArrowRight" }, "\x1bOC": { key: "ArrowRight", code: "ArrowRight" },
  "\x1b[D": { key: "ArrowLeft", code: "ArrowLeft" }, "\x1bOD": { key: "ArrowLeft", code: "ArrowLeft" },
  "\x1b[H": { key: "Home", code: "Home" }, "\x1b[F": { key: "End", code: "End" },
  "\x1b[2~": { key: "Insert", code: "Insert" }, "\x1b[3~": { key: "Delete", code: "Delete" },
  "\x1b[5~": { key: "PageUp", code: "PageUp" }, "\x1b[6~": { key: "PageDown", code: "PageDown" },
  "\x03": { key: "c", code: "KeyC", ctrl: true },
};

/** A key press rebuilt from the bytes it would send legacy-style, plus sticky modifiers. */
export function keyFromBytes(text: string, mods: { ctrl?: boolean; alt?: boolean; shift?: boolean }): KeyInput | undefined {
  const known = legacyKeys[text];
  let key: string, code: string, shift = !!mods.shift, ctrl = !!mods.ctrl;
  if (known) {
    ({ key, code } = known);
    shift ||= !!known.shift;
    ctrl ||= !!known.ctrl;
  } else if (printable(text)) {
    key = text;
    const found = Object.entries(US).find(([, pair]) => pair[0] === text || pair[1] === text);
    code = found?.[0] ?? "";
    if (found && found[1][1] === text && found[1][0] !== text) shift = true;
    if (shift && found) key = found[1][1];
  } else return undefined;
  return { type: "keydown", key, code, shiftKey: shift, altKey: !!mods.alt, ctrlKey: ctrl, metaKey: false };
}

// Chords the browser keeps even from a terminal, as xterm.js leaves them to
// it: the Command key on a Mac (copy, paste, tabs), the developer tools, and
// the clipboard chords whose native paste event the terminal relies on
// (Ctrl+Shift+V, Ctrl+Shift+C, Shift+Insert, Ctrl+Insert). Programs never
// see these.
function browserKeeps(input: KeyInput) {
  if (input.metaKey && !modifierKeys[input.key]) return true;
  if (input.key === "Insert" && (input.shiftKey || input.ctrlKey) && !input.altKey) return true;
  return input.ctrlKey && input.shiftKey && !input.altKey &&
    ["KeyI", "KeyJ", "KeyC", "KeyV"].includes(input.code);
}

/** One entry point for engine.ts: the protocol in force decides. */
export function encodeKey(input: KeyInput, state: { flags: number; modifyOtherKeys: number; cursorKeys: boolean }): string | null {
  if (browserKeeps(input)) return null;
  if (state.flags) {
    const out = encodeKitty(input, { flags: state.flags, cursorKeys: state.cursorKeys });
    // Plain Enter, Tab and Backspace are what xterm.js sends anyway; leave
    // them on its own path, which also clears a selection and the like.
    return out === "\r" || out === "\t" || out === "\x7f" ? null : out;
  }
  if (state.modifyOtherKeys) return encodeModifyOtherKeys(input, state.modifyOtherKeys);
  return null;
}
