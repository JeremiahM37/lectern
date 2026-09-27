import assert from "node:assert/strict";
import { test } from "node:test";
import { modified } from "./keys";
import {
  addItem,
  defaultKeybar,
  itemBytes,
  itemId,
  itemLabel,
  missingBuiltins,
  move,
  parseKeybar,
  repeats,
  type KeyItem,
} from "./keybar";

test("the default row is the one the phone always had", () => {
  assert.deepEqual(defaultKeybar.map(itemId), [
    "escape", "tab", "ctrl", "left", "up", "down", "right", "interrupt", "backtab", "alt",
    "slash", "dash", "pipe", "tilde", "home", "end", "pageup", "pagedown", "snippets",
  ]);
  assert.equal(parseKeybar(null), defaultKeybar);
  assert.equal(parseKeybar("not json"), defaultKeybar);
});

test("combos send what a hardware keyboard would", () => {
  const ctrlR: KeyItem = { t: "combo", key: "r", ctrl: true };
  assert.equal(itemBytes(ctrlR), "\x12");
  assert.deepEqual(itemLabel(ctrlR), ["^R", "Send Ctrl-r"]);
  assert.equal(itemBytes({ t: "combo", key: "b", alt: true }), "\x1bb");
  assert.equal(itemBytes({ t: "combo", key: "left", ctrl: true }), "\x1b[1;5D");
  assert.equal(itemBytes({ t: "combo", key: "left", shift: true }), "\x1b[1;2D");
  assert.equal(itemBytes({ t: "combo", key: "pageup", ctrl: true }), "\x1b[5;5~");
  assert.equal(itemBytes({ t: "combo", key: "tab", shift: true }), "\x1b[Z");
  assert.equal(itemLabel({ t: "combo", key: "left", ctrl: true, alt: true })[0], "^⌥←");
  assert.equal(modified("\x1b[3~", { ctrl: false, alt: true }), "\x1b[3;3~");
});

test("an unmodified arrow follows the application's cursor mode", () => {
  assert.equal(itemBytes({ t: "key", id: "up" }), "\x1b[A");
  assert.equal(itemBytes({ t: "key", id: "up" }, true), "\x1bOA");
  assert.equal(itemBytes({ t: "key", id: "escape" }, true), "\x1b");
});

test("a saved reply on the row sends its text and, if it says so, Enter", () => {
  assert.equal(itemBytes({ t: "text", text: "/compact", enter: true }), "/compact\r");
  assert.equal(itemBytes({ t: "text", text: "git status", enter: false }), "git status");
  assert.equal(itemLabel({ t: "text", text: "continue please now", enter: true })[0], "continue pl… ⏎");
});

test("arrows and deletion repeat when held; Esc and replies do not", () => {
  assert.equal(repeats({ t: "key", id: "left" }), true);
  assert.equal(repeats({ t: "key", id: "backspace" }), true);
  assert.equal(repeats({ t: "combo", key: "left", ctrl: true }), true);
  assert.equal(repeats({ t: "key", id: "escape" }), false);
  assert.equal(repeats({ t: "text", text: "y", enter: true }), false);
});

test("a stored row is cleaned: unknown keys, duplicates and empty combos are dropped", () => {
  const raw = JSON.stringify([
    { t: "key", id: "tab" }, { t: "key", id: "tab" }, { t: "key", id: "rm -rf" },
    { t: "combo", key: "r" }, { t: "combo", key: "r", ctrl: true }, { t: "combo", key: "left", alt: true },
    { t: "combo", key: "\x7f", ctrl: true }, { t: "text", text: "" }, { t: "text", text: "yes" }, { t: "mod", id: "meta" },
  ]);
  assert.deepEqual(parseKeybar(raw).map(itemId), ["tab", "C-r", "M-left", "text:1:yes"]);
  // An emptied row stays empty rather than growing the defaults back.
  assert.deepEqual(parseKeybar("[]"), []);
});

test("reordering, adding and what is left to add", () => {
  const row: KeyItem[] = [{ t: "key", id: "escape" }, { t: "key", id: "tab" }];
  assert.deepEqual(move(row, 1, -1).map(itemId), ["tab", "escape"]);
  assert.equal(move(row, 0, -1), row);
  assert.equal(addItem(row, { t: "key", id: "tab" }), row);
  assert.deepEqual(addItem(row, { t: "mod", id: "ctrl" }).map(itemId), ["escape", "tab", "ctrl"]);
  const left = missingBuiltins(row).map(itemId);
  assert.ok(left.includes("ctrl") && left.includes("enter") && !left.includes("tab"));
});
