import test from "node:test";
import assert from "node:assert/strict";

// A browser-shaped window with an app bridge that records what it is asked.
const calls: string[] = [];
const store = new Map<string, string>();
const target = new EventTarget();
Object.assign(globalThis, {
  window: Object.assign(target, {
    LecternNative: { checkUpdate: () => calls.push("check"), installUpdate: () => calls.push("install") },
  }),
  localStorage: {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
  },
  CustomEvent: class<T> extends Event { detail: T; constructor(type: string, init: { detail: T }) { super(type); this.detail = init.detail; } },
});
const update = await import("./update");

test("the app checks for an update at most every few hours", () => {
  calls.length = 0;
  update.checkForUpdateNow(Date.now());
  update.checkForUpdateNow(Date.now() + 60_000);
  assert.deepEqual(calls, ["check"]);
  update.checkForUpdateNow(Date.now() + 7 * 60 * 60 * 1000);
  assert.deepEqual(calls, ["check", "check"]);
});

test("a dismissed version stays hidden until the next one, and Update asks the app", () => {
  update._setUpdateStatus({ state: "available", current: "2.8.0", latest: "2.9.0" });
  assert.equal(update.updateDismissed("2.9.0"), false);
  update.dismissUpdate("2.9.0");
  assert.equal(update.updateDismissed("2.9.0"), true);
  assert.equal(update.updateDismissed("2.10.0"), false);
  update.installUpdate();
  assert.equal(calls.at(-1), "install");
});
