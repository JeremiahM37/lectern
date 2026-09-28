import assert from "node:assert/strict";
import { test } from "node:test";
import { shouldSend } from "./enter";

const key = (over: Partial<Parameters<typeof shouldSend>[0]> = {}) => ({ key: "Enter", shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, isComposing: false, ...over });

test("Enter sends and Shift+Enter adds a line, like every chat app", () => {
  assert.equal(shouldSend(key(), "auto", false), true);
  assert.equal(shouldSend(key({ shiftKey: true }), "auto", false), false);
  assert.equal(shouldSend(key({ key: "a" }), "auto", false), false);
  // An IME composing text keeps Enter for itself.
  assert.equal(shouldSend(key({ isComposing: true }), "auto", false), false);
});

test("touch-only devices add a line unless told otherwise; Ctrl/Cmd+Enter always sends", () => {
  assert.equal(shouldSend(key(), "auto", true), false);
  assert.equal(shouldSend(key(), "send", true), true);
  assert.equal(shouldSend(key(), "newline", false), false);
  assert.equal(shouldSend(key({ ctrlKey: true }), "newline", false), true);
  assert.equal(shouldSend(key({ metaKey: true }), "auto", true), true);
});
