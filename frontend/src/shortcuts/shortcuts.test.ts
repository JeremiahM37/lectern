import assert from "node:assert/strict";
import { test } from "node:test";
import { displayChord, eventChord, normalizeChord, terminalSafe } from "./chords";
import { bindings, cleanOverrides, conflicts, conflictsFor, remap, SHORTCUTS, warningFor } from "./registry";

const key = (key: string, code: string, mods: Partial<Record<"ctrlKey" | "altKey" | "shiftKey" | "metaKey", boolean>> = {}) =>
  ({ key, code, ctrlKey: false, altKey: false, shiftKey: false, metaKey: false, ...mods });

test("the registry covers at least a hundred actions, each once", () => {
  assert.ok(SHORTCUTS.length >= 100, String(SHORTCUTS.length));
  assert.equal(new Set(SHORTCUTS.map((row) => row.id)).size, SHORTCUTS.length);
  for (const row of SHORTCUTS) for (const chord of row.defaults) assert.ok(normalizeChord(chord, false), `${row.id}: ${chord}`);
});

test("the shipped defaults do not collide, on a Mac or elsewhere", () => {
  assert.deepEqual(conflicts(bindings({}, false)), []);
  assert.deepEqual(conflicts(bindings({}, true)), []);
});

test("chords are canonical whatever order or spelling they were written in", () => {
  assert.equal(normalizeChord("shift+ctrl+k", false), "Ctrl+Shift+K");
  assert.equal(normalizeChord("Mod+K", true), "Meta+K");
  assert.equal(normalizeChord("Mod+K", false), "Ctrl+K");
  assert.equal(normalizeChord("cmd+option+ArrowLeft", true), "Alt+Meta+Left");
  assert.equal(normalizeChord("Ctrl+", false), null);
  assert.equal(normalizeChord("Ctrl+K+J", false), null);
});

test("a key press reads as the same chord the registry writes", () => {
  assert.equal(eventChord(key("k", "KeyK", { ctrlKey: true })), "Ctrl+K");
  // A Mac's Option rewrites event.key; the physical key still names the chord.
  assert.equal(eventChord(key("˚", "KeyK", { altKey: true, metaKey: true })), "Alt+Meta+K");
  assert.equal(eventChord(key("~", "Backquote", { altKey: true, shiftKey: true })), "Alt+Shift+`");
  assert.equal(eventChord(key("ArrowRight", "ArrowRight", { altKey: true, shiftKey: true })), "Alt+Shift+Right");
  assert.equal(eventChord(key("Control", "ControlLeft", { ctrlKey: true })), null);
  assert.equal(displayChord("Ctrl+Shift+K", false), "Ctrl+Shift+K");
  assert.equal(displayChord("Alt+Shift+Meta+K", true), "⌥⇧⌘K");
});

test("remapping onto a taken chord is reported as a conflict", () => {
  const current = bindings({}, false);
  assert.deepEqual(conflictsFor("theme.toggle", "Ctrl+K", current), ["palette.open"]);
  const overrides = remap({}, "theme.toggle", ["Ctrl+K"], false);
  assert.deepEqual(overrides, { "theme.toggle": ["Ctrl+K"] });
  const found = conflicts(bindings(overrides, false));
  assert.equal(found.length, 1);
  assert.equal(found[0]!.chord, "Ctrl+K");
  assert.deepEqual(found[0]!.ids.sort(), ["palette.open", "theme.toggle"]);
});

test("a terminal action and an app action only clash on chords a terminal forwards", () => {
  // Alt+1 stays inside a focused terminal, so a terminal action may reuse it.
  const current = bindings(remap({}, "terminal.quickCommand1", ["Alt+1"], false), false);
  assert.deepEqual(conflictsFor("terminal.quickCommand1", "Alt+1", current), []);
  assert.deepEqual(conflicts(current), []);
  // Ctrl+Shift+L is forwarded out of terminals, so it clashes with the theme toggle.
  assert.deepEqual(conflictsFor("terminal.quickCommand2", "Ctrl+Shift+L", current), ["theme.toggle"]);
});

test("remapping back to the defaults stores nothing, and unassigning stores an empty list", () => {
  const changed = remap({}, "palette.open", ["Ctrl+J"], false);
  assert.deepEqual(remap(changed, "palette.open", ["Mod+K", "Mod+Shift+P"], false), {});
  const off = remap({}, "palette.open", [], false);
  assert.deepEqual(off, { "palette.open": [] });
  assert.deepEqual(bindings(off, false).get("palette.open"), []);
});

test("stored overrides are cleaned: unknown ids and junk chords are dropped", () => {
  assert.deepEqual(cleanOverrides({ nope: ["Ctrl+K"], "palette.open": ["ctrl+j", "Ctrl+J", 7, "Ctrl+"] }, false), { "palette.open": ["Ctrl+J"] });
  assert.deepEqual(cleanOverrides("junk"), {});
  assert.deepEqual(cleanOverrides([["palette.open", ["Ctrl+J"]]]), {});
});

test("chords a shell needs stay in the terminal; the rest are forwarded", () => {
  for (const chord of ["Ctrl+K", "Ctrl+F", "Alt+B", "Ctrl+Shift+-", "Ctrl+Shift+2", "Escape", "Ctrl+\\"]) assert.equal(terminalSafe(chord), false, chord);
  for (const chord of ["Meta+K", "Ctrl+Shift+L", "Ctrl+Tab", "Ctrl+`", "Alt+`", "F3", "Alt+Shift+Left"]) assert.equal(terminalSafe(chord), true, chord);
  assert.match(warningFor("Ctrl+T", "global"), /Browsers/);
  assert.match(warningFor("Ctrl+K", "global"), /Terminals/);
  assert.equal(warningFor("Ctrl+Shift+L", "global"), "");
});
