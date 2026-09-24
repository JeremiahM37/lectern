import assert from "node:assert/strict";
import { test } from "node:test";
import {
  PROJECT_PREFERENCE_KEY,
  RECENT_PROJECT_LIMIT,
  orderProjectsByRecency,
  readProjectPreference,
  rememberProjectSelection,
  rememberRecentProject,
} from "./project-preference";

class MemoryStorage implements Storage {
  private data = new Map<string, string>();
  get length() {
    return this.data.size;
  }
  clear() {
    this.data.clear();
  }
  getItem(key: string) {
    return this.data.has(key) ? this.data.get(key)! : null;
  }
  key(index: number) {
    return [...this.data.keys()][index] ?? null;
  }
  removeItem(key: string) {
    this.data.delete(key);
  }
  setItem(key: string, value: string) {
    this.data.set(key, String(value));
  }
  store(key: string, value: string) {
    this.data.set(key, value);
  }
}

class HostileStorage extends MemoryStorage {
  getItem(): string | null {
    throw new Error("storage disabled");
  }
  setItem() {
    throw new Error("storage full");
  }
}

test("a device that has never remembered anything asks the caller to default", () => {
  assert.deepEqual(readProjectPreference(new MemoryStorage()), {
    last: undefined,
    recent: [],
  });
});

test("corrupt storage falls back instead of breaking the sheet or picker", () => {
  const store = new MemoryStorage();
  for (const junk of [
    "{not json",
    "null",
    "[]",
    '"a string"',
    JSON.stringify({ recent_project_ids: { nope: true } }),
    JSON.stringify({ last_project_id: "twelve", recent_project_ids: [1, "2", -3, 0, null] }),
  ]) {
    store.setItem(PROJECT_PREFERENCE_KEY, junk);
    const preference = readProjectPreference(store);
    assert.ok(Array.isArray(preference.recent), junk);
    assert.ok(
      preference.recent.every((id) => Number.isSafeInteger(id) && id > 0),
      junk,
    );
  }
});

test("an unreadable last choice is unset, never mistaken for blank", () => {
  const store = new MemoryStorage();
  store.setItem(
    PROJECT_PREFERENCE_KEY,
    JSON.stringify({ last_project_id: "not-a-number", recent_project_ids: [] }),
  );
  assert.equal(readProjectPreference(store).last, undefined);
  store.setItem(
    PROJECT_PREFERENCE_KEY,
    JSON.stringify({ last_project_id: null, recent_project_ids: [] }),
  );
  assert.equal(readProjectPreference(store).last, null);
});

test("a blank choice is remembered, so it does not snap back to a project", () => {
  const store = new MemoryStorage();
  rememberProjectSelection(7, store);
  assert.equal(readProjectPreference(store).last, 7);
  rememberProjectSelection(null, store);
  assert.equal(readProjectPreference(store).last, null);
  // The recents are about projects that opened, so the blank run leaves them.
  assert.deepEqual(readProjectPreference(store).recent, []);
});

test("recent projects are most-recent first, deduped and bounded", () => {
  const store = new MemoryStorage();
  for (let id = 1; id <= RECENT_PROJECT_LIMIT + 3; id++) rememberRecentProject(id, store);
  const preference = readProjectPreference(store);
  assert.equal(preference.recent.length, RECENT_PROJECT_LIMIT);
  assert.deepEqual(preference.recent, [11, 10, 9, 8, 7, 6, 5, 4]);
  assert.equal(preference.last, 11);

  // Re-opening an older project moves it back to the front without growing.
  rememberRecentProject(6, store);
  assert.deepEqual(readProjectPreference(store).recent, [6, 11, 10, 9, 8, 7, 5, 4]);
});

test("a storage that refuses reads and writes costs the memory, not a crash", () => {
  const store = new HostileStorage();
  assert.deepEqual(readProjectPreference(store), { last: undefined, recent: [] });
  assert.doesNotThrow(() => rememberRecentProject(3, store));
  assert.doesNotThrow(() => rememberProjectSelection(null, store));
});

test("ordering puts remembered projects first and ignores deleted ones", () => {
  const projects = [
    { id: 1, name: "Alpha" },
    { id: 2, name: "Bravo" },
    { id: 3, name: "Charlie" },
  ];
  const ordered = orderProjectsByRecency(projects, [99, 3, 3, 1]);
  assert.deepEqual(
    ordered.recent.map((row) => row.name),
    ["Charlie", "Alpha"],
  );
  assert.deepEqual(
    ordered.rest.map((row) => row.name),
    ["Bravo"],
  );
});

test("nothing remembered leaves the alphabetical list untouched", () => {
  const projects = [
    { id: 1, name: "Alpha" },
    { id: 2, name: "Bravo" },
  ];
  const ordered = orderProjectsByRecency(projects, []);
  assert.deepEqual(ordered.recent, []);
  assert.deepEqual(ordered.rest, projects);
});
