import assert from "node:assert/strict";
import { test } from "node:test";
import { handleBack, noteView, setViewNavigator } from "./back";

test("back walks the views visited and stops at the first", () => {
  const went: string[] = [];
  setViewNavigator((hash) => went.push(hash));
  (globalThis as { document?: unknown }).document = { querySelectorAll: () => [] };
  noteView("#board", 1000);
  noteView("#sessions", 1300); // where the app settled on start
  noteView("#board", 9000);
  noteView("#targets", 9500);
  assert.equal(handleBack(), true);
  assert.equal(handleBack(), true);
  assert.deepEqual(went, ["#board", "#sessions"]);
  // At the first view the app may close.
  assert.equal(handleBack(), false);
  // Like browser history: a view visited again is a new step.
  noteView("#media", 10000);
  noteView("#sessions", 11000);
  assert.equal(handleBack(), true);
  assert.deepEqual(went.slice(-1), ["#media"]);
});
