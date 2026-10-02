import assert from "node:assert/strict";
import { test } from "node:test";
import { flushPrefs, getPref, loadPrefs, prefsLocalOnly, resetPrefsForTest, setPref } from "./store";
import { ApiError } from "../api/client";

test("a change not yet delivered wins over the server's older copy", async () => {
  const server: Record<string, unknown> = { appearance: { theme: "dark" }, shortcuts: { a: ["Ctrl+J"] } };
  const calls: string[] = [];
  resetPrefsForTest({}, (async (path: string, options: { method?: string; body?: unknown } = {}) => {
    calls.push(`${options.method || "GET"} ${path}`);
    if (path === "/ui/prefs") return { prefs: server };
    return null;
  }) as never);
  setPref("appearance", { theme: "light" });
  await loadPrefs();
  assert.deepEqual(getPref("appearance", null), { theme: "light" });
  assert.deepEqual(getPref("shortcuts", null), { a: ["Ctrl+J"] });
  flushPrefs();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(calls.includes("PUT /ui/prefs/appearance"), calls.join(", "));
  // Once delivered, the next load takes the server's value again.
  server.appearance = { theme: "system" };
  await loadPrefs();
  assert.deepEqual(getPref("appearance", null), { theme: "system" });
});

test("a refused write keeps the value on this device and says so", async () => {
  resetPrefsForTest({}, (async () => {
    throw new ApiError(403, "changing preferences requires a signed-in human");
  }) as never);
  setPref("theme", "light");
  flushPrefs();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(prefsLocalOnly(), true);
  assert.equal(getPref("theme", ""), "light");
});

test("a change another document has not delivered yet survives this one's load", async () => {
  // A terminal frame added a quick command while this page's first load was
  // out; that frame's storage event has not arrived here yet.
  const storage = new Map<string, string>();
  (globalThis as { localStorage?: unknown }).localStorage = {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => void storage.set(key, value),
  };
  try {
    resetPrefsForTest({}, (async () => ({ prefs: { theme: "dark" } })) as never);
    storage.set("lec-ui-prefs-v1", JSON.stringify({ "quick-commands": [{ id: "q1", text: "echo hi" }] }));
    storage.set("lec-ui-prefs-unsent-v1", JSON.stringify({ "quick-commands": true }));
    await loadPrefs();
    assert.deepEqual(getPref("quick-commands", null), [{ id: "q1", text: "echo hi" }]);
    assert.equal(getPref("theme", ""), "dark");
    assert.match(storage.get("lec-ui-prefs-v1") || "", /echo hi/);
    assert.match(storage.get("lec-ui-prefs-unsent-v1") || "", /quick-commands/);
  } finally {
    delete (globalThis as { localStorage?: unknown }).localStorage;
    resetPrefsForTest();
  }
});
