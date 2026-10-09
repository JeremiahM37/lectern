import assert from "node:assert/strict";
import { test } from "node:test";
import { canonicalHash, HOME, moreEntries, moreGroups, primaryViews, VIEWS } from "./routes";

test("old links land on the page that has the same meaning now", () => {
  assert.equal(canonicalHash("#board"), "#tasks");
  assert.equal(canonicalHash("#deck"), "#overview");
  assert.equal(canonicalHash("#targets"), "#settings/machines");
  assert.equal(canonicalHash("#tasks/3/github/pr/12"), "#issues/3/github/pr/12");
  assert.equal(canonicalHash("#tasks/3"), "#issues/3");
  // A bare #tasks is the task list now; everything else is untouched.
  assert.equal(canonicalHash("#tasks"), "#tasks");
  for (const hash of ["#sessions", "#approvals", "#task/7", "#session/4/reply", "#settings/plugins", "#media/2", "#terminals/session/1", "#issues/1", "#evals"])
    assert.equal(canonicalHash(hash), hash);
});

test("narrow navigation is three pages and More; Tasks joins once used", () => {
  assert.equal(HOME, "sessions");
  assert.deepEqual(primaryViews(false), ["sessions", "approvals", "settings"]);
  assert.deepEqual(primaryViews(true), ["sessions", "approvals", "tasks", "settings"]);
  const more = (used: boolean) => moreEntries(used).map((e) => ("view" in e ? e.view : e.section));
  assert.deepEqual(more(false), ["tasks", "terminals", "overview", "issues", "media", "evals", "machines", "plugins"]);
  assert.ok(!more(true).includes("tasks"));
  assert.ok(more(true).includes("terminals"));
});

test("a desktop sidebar lists every page and has no More", () => {
  for (const used of [false, true]) assert.deepEqual(primaryViews(used, true), [...VIEWS]);
});

test("More is grouped, and every entry appears once in the same order", () => {
  for (const hasTasks of [false, true]) {
    const key = (e: ReturnType<typeof moreEntries>[number]) => ("view" in e ? e.view : e.section);
    const flat = moreEntries(hasTasks).map(key);
    const grouped = moreGroups(hasTasks).flatMap((g) => g.entries.map(key));
    assert.deepEqual(grouped.sort(), [...flat].sort());
    assert.deepEqual(moreGroups(hasTasks).map((g) => g.id), ["running", "work", "setup"]);
  }
  assert.deepEqual(moreGroups(false)[0]!.entries.map((e) => ("view" in e ? e.view : e.section)), ["terminals", "overview"]);
});
