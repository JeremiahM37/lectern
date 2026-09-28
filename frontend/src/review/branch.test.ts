import assert from "node:assert/strict";
import { test } from "node:test";
import { branchFor } from "./branch";

test("a new branch is named from the session", () => {
  assert.equal(branchFor("Fix the login page!"), "lectern/fix-the-login-page");
  assert.equal(branchFor("claude"), "lectern/claude");
  assert.equal(branchFor("  "), "lectern/changes");
  assert.equal(branchFor("Café résumé"), "lectern/cafe-resume");
  assert.ok(branchFor("x".repeat(90)).length <= "lectern/".length + 40);
});
