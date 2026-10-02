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

test("a desktop sidebar is the same three items, with the rest under More", () => {
  assert.deepEqual(primaryViews(false, true), ["sessions", "approvals", "settings"]);
  assert.deepEqual(primaryViews(true, true), ["sessions", "approvals", "tasks", "settings"]);
  // An open terminal tab brings Terminals into the sidebar, not the phone bar.
  assert.deepEqual(primaryViews(false, true, true), ["sessions", "approvals", "terminals", "settings"]);
  assert.deepEqual(primaryViews(false, false, true), ["sessions", "approvals", "settings"]);
  const more = (used: boolean, open = false) => moreEntries(used, true, open).map((e) => ("view" in e ? e.view : e.section));
  // Machines and Plugins are Settings tabs; the sidebar does not repeat them.
  assert.deepEqual(more(false), ["tasks", "terminals", "overview", "issues", "media", "evals"]);
  assert.deepEqual(more(true, true), ["overview", "issues", "media", "evals"]);
  // Every page is reachable from the main items or More.
  for (const used of [false, true])
    for (const open of [false, true])
      assert.deepEqual([...primaryViews(used, true, open), ...more(used, open)].sort(), [...VIEWS].sort());
});
