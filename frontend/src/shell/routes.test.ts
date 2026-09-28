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

test("the main navigation stays at three pages plus More with terminals open", () => {
  assert.equal(HOME, "sessions");
  assert.deepEqual(primaryViews(false), ["sessions", "approvals", "settings"]);
  assert.deepEqual(primaryViews(true), ["sessions", "approvals", "settings"]);
  const more = (open: boolean) => moreEntries(open).map((e) => ("view" in e ? e.view : e.section));
  assert.deepEqual(more(false), ["tasks", "terminals", "overview", "issues", "media", "evals", "machines", "plugins"]);
  assert.deepEqual(more(true), more(false));
});
