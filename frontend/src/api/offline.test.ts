import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError, createClient } from "./client";
import { cacheable, OfflineCache, staleAge, unreachable } from "./offline";

function memory() {
  const data = new Map<string, string>();
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
    data,
  };
}

test("only the home screens' lists are kept", () => {
  assert.equal(cacheable("/sessions?include_setup_failures=true"), true);
  assert.equal(cacheable("/approvals?status=pending"), true);
  assert.equal(cacheable("/approvals?status=approved"), false);
  assert.equal(cacheable("/tasks", "POST"), false);
  assert.equal(cacheable("/sessions/7/diff"), false);
  assert.equal(cacheable("/settings"), false);
});

test("an unreachable network falls back; a refusal does not", () => {
  assert.equal(unreachable(new TypeError("Failed to fetch")), true);
  assert.equal(unreachable(new ApiError(504, "Lectern relay: no answer")), true);
  assert.equal(unreachable(new ApiError(403, "forbidden")), false);
  assert.equal(unreachable(new ApiError(500, "boom")), false);
});

test("a reopened app shows what it last knew at once, then catches up", async () => {
  const storage = memory();
  let now = 1_000_000;
  const first = new OfflineCache(storage, () => now);
  const online = createClient({ token: () => "", offline: first, fetch: async () => Response.json([{ id: 1 }]) });
  assert.deepEqual(await online("/sessions"), [{ id: 1 }]);

  // Later, the phone has no network.
  now += 5 * 60_000;
  const second = new OfflineCache(storage, () => now, 20);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  let revalidated = 0;
  second.addEventListener("revalidated", () => revalidated++);
  const slow = createClient({ token: () => "", offline: second, fetch: async () => {
    await gate;
    return Response.json([{ id: 1 }, { id: 2 }]);
  } });
  // Answered from the cache once the network keeps it waiting.
  assert.deepEqual(await slow("/sessions"), [{ id: 1 }]);
  release();
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(revalidated, 1);
  // The live answer replaced the cached one.
  const third = new OfflineCache(storage, () => now);
  const fail = createClient({ token: () => "", offline: third, fetch: async () => { throw new TypeError("Failed to fetch"); } });
  assert.deepEqual(await fail("/sessions"), [{ id: 1 }, { id: 2 }]);
  await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(third.state, { stale: true, since: now });
});

test("after a live answer, a failure fails as before and raises the stale marker", async () => {
  const storage = memory();
  const cache = new OfflineCache(storage, () => 50);
  let up = true;
  let changes = 0;
  cache.addEventListener("change", () => changes++);
  const api = createClient({ token: () => "", offline: cache, fetch: async () => {
    if (!up) throw new TypeError("Failed to fetch");
    return Response.json({ ok: 1 });
  } });
  await api("/projects");
  up = false;
  // The screen keeps what it has; the request itself still fails.
  await assert.rejects(api("/projects"));
  assert.deepEqual(cache.state, { stale: true, since: 50 });
  up = true;
  assert.deepEqual(await api("/projects"), { ok: 1 });
  assert.deepEqual(cache.state, { stale: false });
  assert.equal(changes, 2);
});

test("a 401 forgets everything cached", async () => {
  const storage = memory();
  const cache = new OfflineCache(storage);
  let status = 200;
  const api = createClient({ token: () => "", offline: cache, fetch: async () => Response.json({ detail: "x" }, { status }) });
  await api("/tasks");
  assert.equal(storage.data.size, 1);
  status = 401;
  await assert.rejects(api("/settings"));
  assert.equal(storage.data.size, 0);
  assert.equal(cache.empty, true);
});

test("stale ages read naturally", () => {
  assert.equal(staleAge(0, 10_000), "just now");
  assert.equal(staleAge(0, 4 * 60_000), "4 min ago");
  assert.equal(staleAge(0, 3 * 3_600_000), "3 h ago");
  assert.equal(staleAge(0, 3 * 86_400_000), "3 days ago");
});

test("a reachable Lectern answers first even with a cache", async () => {
  const storage = memory();
  const first = new OfflineCache(storage);
  await createClient({ token: () => "", offline: first, fetch: async () => Response.json(["old"]) })("/tasks");
  const cache = new OfflineCache(storage);
  const api = createClient({ token: () => "", offline: cache, fetch: async () => Response.json(["new"]) });
  assert.deepEqual(await api("/tasks"), ["new"]);
  assert.deepEqual(cache.state, { stale: false });
});
