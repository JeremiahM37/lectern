import assert from "node:assert/strict";
import { test } from "node:test";
import { canonicalHash, HOME, moreEntries, primaryViews, VIEWS } from "./routes";

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

test("a desktop sidebar shows every page directly", () => {
  assert.deepEqual(primaryViews(false, true), [...VIEWS]);
  assert.deepEqual(primaryViews(true, true), [...VIEWS]);
});
