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
