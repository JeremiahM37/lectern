import assert from "node:assert/strict";
import { test } from "node:test";
import { canonicalHash, HOME, moreEntries, primaryViews } from "./routes";

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

test("the main navigation is four pages, plus Terminals while one is open", () => {
  assert.equal(HOME, "sessions");
  assert.deepEqual(primaryViews(false), ["sessions", "approvals", "tasks", "settings"]);
  assert.deepEqual(primaryViews(true), ["sessions", "approvals", "tasks", "terminals", "settings"]);
  const more = (open: boolean) => moreEntries(open).map((e) => ("view" in e ? e.view : e.section));
  assert.deepEqual(more(false), ["overview", "issues", "terminals", "media", "evals", "machines", "plugins"]);
  assert.ok(!more(true).includes("terminals"));
});
