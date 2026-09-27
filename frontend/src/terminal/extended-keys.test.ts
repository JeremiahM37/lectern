import assert from "node:assert/strict";
import { test } from "node:test";
import {
  encodeKey,
  encodeKitty,
  encodeModifyOtherKeys,
  Flag,
  KeyboardModes,
  keyFromBytes,
  STACK_LIMIT,
  type KeyInput,
} from "./extended-keys";

// A key event as Chromium reports it on a US layout.
type Mods = { shift?: boolean; alt?: boolean; ctrl?: boolean; meta?: boolean; caps?: boolean; num?: boolean; altGraph?: boolean };
function key(k: string, code: string, m: Mods = {}, extra: Partial<KeyInput> = {}): KeyInput {
  return {
    type: "keydown", key: k, code, shiftKey: !!m.shift, altKey: !!m.alt, ctrlKey: !!m.ctrl, metaKey: !!m.meta,
    capsLock: !!m.caps, numLock: !!m.num, altGraph: !!m.altGraph, ...extra,
  };
}
const up = (input: KeyInput): KeyInput => ({ ...input, type: "keyup" });
const rep = (input: KeyInput): KeyInput => ({ ...input, repeat: true });
const kitty = (input: KeyInput, flags: number, cursorKeys = false) => encodeKitty(input, { flags, cursorKeys });
const D = Flag.disambiguate, E = Flag.events, A = Flag.alternates, ALL = Flag.allKeys, T = Flag.text;

const enter = (m?: Mods) => key("Enter", "Enter", m);
const tab = (m?: Mods) => key("Tab", "Tab", m);
const bs = (m?: Mods) => key("Backspace", "Backspace", m);
const esc = (m?: Mods) => key("Escape", "Escape", m);
const letter = (ch: string, m: Mods = {}) =>
  key(m.shift || m.caps ? ch.toUpperCase() : ch, "Key" + ch.toUpperCase(), m);

test("the flag stack: push, pop, set, query value, per screen", () => {
  const modes = new KeyboardModes();
  assert.equal(modes.flags(false), 0);
  modes.push(false, 1);
  assert.equal(modes.flags(false), 1);
  modes.push(false, 1 | 2);
  assert.equal(modes.flags(false), 3);
  // The alternate screen has its own stack.
  assert.equal(modes.flags(true), 0);
  modes.push(true, 8);
  assert.equal(modes.flags(true), 8);
  assert.equal(modes.flags(false), 3);
  modes.pop(false, 1);
  assert.equal(modes.flags(false), 1);
  // Popping past the bottom resets every flag.
  modes.pop(false, 5);
  assert.equal(modes.flags(false), 0);
  // CSI = flags ; mode u: 1 replaces, 2 sets bits, 3 clears bits.
  modes.set(false, 5, 1);
  assert.equal(modes.flags(false), 5);
  modes.set(false, 2, 2);
  assert.equal(modes.flags(false), 7);
  modes.set(false, 4, 3);
  assert.equal(modes.flags(false), 3);
  modes.set(false, 255, 1);
  assert.equal(modes.flags(false), 31, "only the defined flags are kept");
  modes.clearScreen(true);
  assert.equal(modes.flags(true), 0);
  modes.modifyOtherKeys = 2;
  assert.ok(modes.active(true));
  modes.reset();
  assert.equal(modes.flags(false), 0);
  assert.equal(modes.modifyOtherKeys, 0);
  assert.ok(!modes.active(false));
});

test("a full stack evicts its oldest entry rather than growing", () => {
  const modes = new KeyboardModes();
  for (let i = 0; i < STACK_LIMIT + 5; i++) modes.push(false, (i % 31) + 1);
  for (let i = 0; i < STACK_LIMIT; i++) modes.pop(false, 1);
  // Everything after the evicted entries unwinds; one more pop is at the bottom.
  modes.pop(false, 1);
  assert.equal(modes.flags(false), 0);
});

test("no flags: nothing is intercepted, xterm.js keeps its legacy encoding", () => {
  const state = { flags: 0, modifyOtherKeys: 0, cursorKeys: false };
  for (const input of [enter({ shift: true }), tab({ ctrl: true }), letter("a", { ctrl: true, shift: true }), esc(), letter("a")])
    assert.equal(encodeKey(input, state), null);
});

test("disambiguate: Enter, Tab and Backspace with each modifier (spec C0 table, as CSI u)", () => {
  // Unmodified they stay as typed, so `reset` still works after a crash.
  assert.equal(kitty(enter(), D), "\r");
  assert.equal(kitty(tab(), D), "\t");
  assert.equal(kitty(bs(), D), "\x7f");
  const cases: [Mods, number][] = [
    [{ shift: true }, 2], [{ alt: true }, 3], [{ alt: true, shift: true }, 4], [{ ctrl: true }, 5],
    [{ ctrl: true, shift: true }, 6], [{ ctrl: true, alt: true }, 7], [{ ctrl: true, alt: true, shift: true }, 8],
  ];
  for (const [m, n] of cases) {
    assert.equal(kitty(enter(m), D), `\x1b[13;${n}u`, `Enter ${JSON.stringify(m)}`);
    assert.equal(kitty(tab(m), D), `\x1b[9;${n}u`, `Tab ${JSON.stringify(m)}`);
    assert.equal(kitty(bs(m), D), `\x1b[127;${n}u`, `Backspace ${JSON.stringify(m)}`);
    assert.equal(kitty(esc(m), D), `\x1b[27;${n}u`, `Escape ${JSON.stringify(m)}`);
  }
});

test("disambiguate: Escape is CSI 27 u, never a bare ESC", () => {
  assert.equal(kitty(esc(), D), "\x1b[27u");
});

test("disambiguate: text keys (spec example table)", () => {
  // Plain and shifted text is typed, through xterm.js and any IME.
  assert.equal(kitty(letter("i"), D), null);
  assert.equal(kitty(letter("i", { shift: true }), D), null);
  assert.equal(kitty(key("#", "Digit3", { shift: true }), D), null);
  // alt, ctrl, shift+alt, alt+ctrl, ctrl+shift: CSI u with the unshifted key.
  assert.equal(kitty(letter("i", { alt: true }), D), "\x1b[105;3u");
  assert.equal(kitty(letter("i", { ctrl: true }), D), "\x1b[105;5u");
  assert.equal(kitty(letter("i", { alt: true, shift: true }), D), "\x1b[105;4u");
  assert.equal(kitty(letter("i", { alt: true, ctrl: true }), D), "\x1b[105;7u");
  assert.equal(kitty(letter("i", { ctrl: true, shift: true }), D), "\x1b[105;6u");
  assert.equal(kitty(key("#", "Digit3", { ctrl: true, shift: true }), D), "\x1b[51;6u");
  assert.equal(kitty(key(":", "Semicolon", { ctrl: true, shift: true }), D), "\x1b[59;6u");
  assert.equal(kitty(key(" ", "Space", { ctrl: true }), D), "\x1b[32;5u");
  // Ctrl+I is not Tab, Ctrl+M is not Enter, Ctrl+[ is not Escape.
  assert.equal(kitty(letter("m", { ctrl: true }), D), "\x1b[109;5u");
  assert.equal(kitty(key("[", "BracketLeft", { ctrl: true }), D), "\x1b[91;5u");
  assert.equal(kitty(letter("c", { ctrl: true }), D), "\x1b[99;5u");
});

test("the Command key, clipboard and developer-tools chords stay the browser's", () => {
  const state = { flags: D, modifyOtherKeys: 2, cursorKeys: false };
  assert.equal(encodeKey(letter("v", { meta: true }), state), null);
  assert.equal(encodeKey(letter("i", { ctrl: true, shift: true }), state), null);
  assert.equal(encodeKey(letter("v", { ctrl: true, shift: true }), state), null, "native paste");
  assert.equal(encodeKey(letter("c", { ctrl: true, shift: true }), state), null, "native copy");
  assert.equal(encodeKey(key("Insert", "Insert", { shift: true }), state), null);
  assert.equal(encodeKey(key("Insert", "Insert", { ctrl: true }), state), null);
  assert.equal(encodeKey(letter("a", { ctrl: true, shift: true }), state), "\x1b[97;6u");
});

test("functional keys keep their CSI forms, with modifiers as parameters", () => {
  assert.equal(kitty(key("ArrowUp", "ArrowUp"), D), "\x1b[A");
  assert.equal(kitty(key("ArrowUp", "ArrowUp"), D, true), "\x1b[A", "cursor key mode is legacy-only");
  assert.equal(kitty(key("ArrowLeft", "ArrowLeft", { ctrl: true }), D), "\x1b[1;5D");
  assert.equal(kitty(key("Home", "Home", { shift: true }), D), "\x1b[1;2H");
  assert.equal(kitty(key("End", "End"), D), "\x1b[F");
  assert.equal(kitty(key("PageUp", "PageUp"), D), "\x1b[5~");
  assert.equal(kitty(key("Delete", "Delete", { alt: true }), D), "\x1b[3;3~");
  assert.equal(kitty(key("Insert", "Insert"), D), "\x1b[2~");
  assert.equal(kitty(key("F1", "F1"), D), "\x1b[P");
  assert.equal(kitty(key("F2", "F2", { ctrl: true }), D), "\x1b[1;5Q");
  assert.equal(kitty(key("F3", "F3"), D), "\x1b[13~");
  assert.equal(kitty(key("F4", "F4"), D), "\x1b[S");
  assert.equal(kitty(key("F5", "F5"), D), "\x1b[15~");
  assert.equal(kitty(key("F12", "F12", { shift: true }), D), "\x1b[24;2~");
  assert.equal(kitty(key("F13", "F13"), D), "\x1b[57376u");
  assert.equal(kitty(key("ContextMenu", "ContextMenu"), D), "\x1b[57363u");
  assert.equal(kitty(key("PrintScreen", "PrintScreen"), D), "\x1b[57361u");
  assert.equal(kitty(key("MediaPlayPause", "MediaPlayPause"), D), "\x1b[57430u");
});

test("legacy-only flags keep legacy functional encodings (report alternates alone)", () => {
  assert.equal(kitty(key("ArrowUp", "ArrowUp"), A, true), "\x1bOA");
  assert.equal(kitty(key("F1", "F1"), A), "\x1bOP");
  assert.equal(kitty(enter({ alt: true }), A), "\x1b\r");
  assert.equal(kitty(tab({ shift: true }), A), "\x1b[Z");
  assert.equal(kitty(bs({ ctrl: true }), A), "\x08");
  assert.equal(kitty(esc(), A), "\x1b");
  assert.equal(kitty(letter("a", { ctrl: true }), A), "\x01");
  assert.equal(kitty(letter("a", { alt: true }), A), "\x1ba");
  // Legacy has no encoding for this one: CSI u, as kitty does.
  assert.equal(kitty(letter("i", { ctrl: true, shift: true }), A), "\x1b[105:73;6u");
});

test("the keypad reports its own keys once disambiguation is on", () => {
  // With Num Lock on the digits are text.
  assert.equal(kitty(key("1", "Numpad1", { num: true }), D), null);
  assert.equal(kitty(key("End", "Numpad1"), D), "\x1b[57424u");
  assert.equal(kitty(key("Enter", "NumpadEnter"), D), "\x1b[57414u");
  assert.equal(kitty(key("Clear", "Numpad5"), D), "\x1b[E", "KP_BEGIN is CSI E");
  assert.equal(kitty(key("+", "NumpadAdd", { ctrl: true }), D), "\x1b[57413;5u");
  // ...and as the equivalent main keys without it.
  assert.equal(kitty(key("Enter", "NumpadEnter"), E), "\r");
  assert.equal(kitty(key("End", "Numpad1"), E), "\x1b[F");
  // Reporting all keys, a digit is the keypad key with its text.
  assert.equal(kitty(key("7", "Numpad7"), ALL | T), "\x1b[57406;;55u");
});

test("event types: repeat and release, never release for Enter/Tab/Backspace", () => {
  const f = D | E;
  assert.equal(kitty(rep(letter("a", { ctrl: true })), f), "\x1b[97;5:2u");
  assert.equal(kitty(up(letter("a", { ctrl: true })), f), "\x1b[97;5:3u");
  assert.equal(kitty(up(letter("a")), f), "\x1b[97;1:3u");
  assert.equal(kitty(rep(letter("a")), f), null, "a repeated text key is typed again");
  assert.equal(kitty(up(key("ArrowUp", "ArrowUp")), f), "\x1b[1;1:3A");
  assert.equal(kitty(rep(key("ArrowUp", "ArrowUp")), f), "\x1b[1;1:2A");
  assert.equal(kitty(up(esc()), f), "\x1b[27;1:3u");
  for (const k of [enter(), tab(), bs()]) {
    assert.equal(kitty(up(k), f), "");
    assert.equal(kitty(up(k), f | ALL), `\x1b[${k.key === "Enter" ? 13 : k.key === "Tab" ? 9 : 127};1:3u`);
  }
  assert.equal(kitty(up(enter({ shift: true })), f), "\x1b[13;2:3u");
  // Without the flag a release says nothing.
  assert.equal(kitty(up(letter("a", { ctrl: true })), D), "");
});

test("alternate keys: shifted key and base layout key", () => {
  const f = D | A;
  assert.equal(kitty(letter("a", { ctrl: true, shift: true }), f), "\x1b[97:65;6u");
  assert.equal(kitty(key("@", "Digit2", { ctrl: true, shift: true }), f), "\x1b[50:64;6u");
  assert.equal(kitty(letter("a", { ctrl: true }), f), "\x1b[97;5u", "no shift, no shifted key");
  // A Cyrillic layout: the key is с, its base layout key is c.
  assert.equal(kitty(key("с", "KeyC", { ctrl: true }), f), "\x1b[1089::99;5u");
  assert.equal(kitty(key("С", "KeyC", { ctrl: true, shift: true }), f), "\x1b[1089:1057:99;6u");
});

test("report all keys: text keys, Enter and the modifiers themselves become escapes", () => {
  assert.equal(kitty(letter("a"), ALL), "\x1b[97u");
  assert.equal(kitty(letter("a", { shift: true }), ALL), "\x1b[97;2u");
  assert.equal(kitty(enter(), ALL), "\x1b[13u");
  assert.equal(kitty(tab(), ALL), "\x1b[9u");
  assert.equal(kitty(bs(), ALL), "\x1b[127u");
  assert.equal(kitty(esc(), ALL), "\x1b[27u");
  assert.equal(kitty(key("Shift", "ShiftLeft", { shift: true }, { location: 1 }), ALL), "\x1b[57441;2u");
  assert.equal(kitty(key("Control", "ControlRight", { ctrl: true }, { location: 2 }), ALL), "\x1b[57448;5u");
  assert.equal(kitty(up(key("Shift", "ShiftLeft", {}, { location: 1 })), ALL | E), "\x1b[57441;1:3u");
  assert.equal(kitty(key("Alt", "AltLeft", { alt: true }, { location: 1 }), ALL), "\x1b[57443;3u");
  // Modifier keys alone are silent below this flag.
  assert.equal(kitty(key("Shift", "ShiftLeft", { shift: true }, { location: 1 }), D | E), "");
  // Lock keys are modifiers of every key reported this way.
  assert.equal(kitty(letter("a", { caps: true }), ALL), "\x1b[97;65u");
  assert.equal(kitty(enter({ num: true }), ALL), "\x1b[13;129u");
  // Below it, locks never turn typing or Enter into escapes.
  assert.equal(kitty(letter("a", { caps: true }), D), null);
  assert.equal(kitty(enter({ num: true }), D), "\r");
});

test("associated text (flag 16) with report all keys", () => {
  const f = ALL | T;
  assert.equal(kitty(letter("a"), f), "\x1b[97;;97u");
  assert.equal(kitty(letter("a", { shift: true }), f), "\x1b[97;2;65u");
  assert.equal(kitty(letter("a", { ctrl: true }), f), "\x1b[97;5u", "Ctrl+a has no text");
  // Option+a on a Mac types å: the modifier made the text and is not reported.
  assert.equal(kitty(key("å", "KeyA", { alt: true }), f), "\x1b[229;;229u");
  assert.equal(kitty(key("å", "KeyA", { alt: true }), D), null, "and below flag 8 it is simply typed");
  assert.equal(kitty(up(letter("a")), f | E), "\x1b[97;1:3u", "no text on release");
  // AltGr on Windows arrives as Ctrl+Alt; the character is still text.
  assert.equal(kitty(key("€", "KeyE", { ctrl: true, alt: true, altGraph: true }), D), null);
});

test("IME, dead keys and unidentified keys are never intercepted", () => {
  for (const k of ["Dead", "Process", "Unidentified", ""])
    assert.equal(kitty(key(k, "KeyA", { ctrl: true }), D | ALL), null);
});

test("modifyOtherKeys level 2, the form tmux asks for", () => {
  const mok = (input: KeyInput) => encodeModifyOtherKeys(input, 2);
  assert.equal(mok(enter({ shift: true })), "\x1b[27;2;13~");
  assert.equal(mok(enter({ ctrl: true })), "\x1b[27;5;13~");
  assert.equal(mok(enter({ alt: true })), "\x1b[27;3;13~");
  assert.equal(mok(enter()), null);
  assert.equal(mok(letter("a", { ctrl: true, shift: true })), "\x1b[27;6;65~");
  assert.equal(mok(letter("c", { ctrl: true })), "\x1b[27;5;99~");
  assert.equal(mok(letter("a", { alt: true })), "\x1b[27;3;97~");
  assert.equal(mok(letter("i", { ctrl: true })), "\x1b[27;5;105~");
  assert.equal(mok(tab({ ctrl: true })), "\x1b[27;5;9~");
  assert.equal(mok(tab({ shift: true })), null, "Shift+Tab stays CSI Z");
  assert.equal(mok(bs({ ctrl: true })), "\x1b[27;5;127~");
  assert.equal(mok(esc({ shift: true })), "\x1b[27;2;27~");
  assert.equal(mok(key(" ", "Space", { ctrl: true })), "\x1b[27;5;32~");
  assert.equal(mok(letter("a", { shift: true })), null, "Shift alone types the letter");
  assert.equal(mok(letter("a")), null);
  assert.equal(mok(key("ArrowUp", "ArrowUp", { ctrl: true })), null, "xterm.js already encodes it");
  assert.equal(mok(up(letter("a", { ctrl: true }))), null, "no releases");
  assert.equal(mok(key("å", "KeyA", { alt: true })), null, "a composed character is typed");
});

test("modifyOtherKeys level 1 reports only what legacy loses", () => {
  const mok = (input: KeyInput) => encodeModifyOtherKeys(input, 1);
  assert.equal(mok(enter({ shift: true })), "\x1b[27;2;13~");
  assert.equal(mok(enter({ ctrl: true })), "\x1b[27;5;13~");
  assert.equal(mok(enter({ alt: true })), null);
  assert.equal(mok(letter("c", { ctrl: true })), null);
  assert.equal(mok(letter("a", { alt: true })), null);
  assert.equal(mok(letter("a", { ctrl: true, shift: true })), "\x1b[27;6;65~");
  assert.equal(mok(key("1", "Digit1", { ctrl: true })), "\x1b[27;5;49~");
  assert.equal(mok(key(";", "Semicolon", { ctrl: true })), "\x1b[27;5;59~");
});

test("plain Enter, Tab and Backspace stay on xterm.js's own path", () => {
  const state = { flags: D, modifyOtherKeys: 0, cursorKeys: false };
  for (const k of [enter(), tab(), bs()]) assert.equal(encodeKey(k, state), null);
  assert.equal(encodeKey(enter({ shift: true }), state), "\x1b[13;2u");
});

test("kitty flags win over modifyOtherKeys when both are set", () => {
  assert.equal(encodeKey(enter({ shift: true }), { flags: D, modifyOtherKeys: 2, cursorKeys: false }), "\x1b[13;2u");
  assert.equal(encodeKey(enter({ shift: true }), { flags: 0, modifyOtherKeys: 2, cursorKeys: false }), "\x1b[27;2;13~");
});

test("the key bar's bytes are read back as keys for sticky modifiers", () => {
  const state = (flags: number, mok = 0) => ({ flags, modifyOtherKeys: mok, cursorKeys: false });
  const via = (text: string, mods: { ctrl?: boolean; alt?: boolean; shift?: boolean }, s: ReturnType<typeof state>) => {
    const input = keyFromBytes(text, mods);
    return input ? encodeKey(input, s) : undefined;
  };
  assert.equal(via("\r", { shift: true }, state(D)), "\x1b[13;2u");
  assert.equal(via("\r", { shift: true }, state(0, 2)), "\x1b[27;2;13~");
  assert.equal(via("a", { ctrl: true }, state(D)), "\x1b[97;5u");
  assert.equal(via("a", { ctrl: true }, state(0, 2)), "\x1b[27;5;97~");
  assert.equal(via("\x03", {}, state(D)), "\x1b[99;5u", "the ^C key is Ctrl+C");
  assert.equal(via("\x1b", {}, state(D)), "\x1b[27u");
  assert.equal(via("\x1b[Z", {}, state(D)), "\x1b[9;2u", "the backtab key is Shift+Tab");
  assert.equal(via("\x1b[D", { ctrl: true }, state(D)), "\x1b[1;5D");
  assert.equal(via("\x1bOA", {}, state(D)), "\x1b[A");
  assert.equal(via("!", { ctrl: true }, state(D)), "\x1b[49;6u");
  assert.equal(via("a", {}, state(D)), null, "plain text is typed");
  assert.equal(keyFromBytes("hello", { ctrl: true }), undefined, "a word is not a key");
});
