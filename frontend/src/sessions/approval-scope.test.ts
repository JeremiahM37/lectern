import assert from "node:assert/strict";
import { test } from "node:test";
import { sessionScopeLabel } from "./ApprovalCard";

test("a Bash approval names the command word it will allow", () => {
  assert.equal(sessionScopeLabel("Bash", { command: "git status" }), "Allow “git …” commands this session");
});

test("wrappers, paths and assignments get no session-wide option", () => {
  for (const command of ["sudo rm -rf x", "bash -c 'ls'", "env A=1 make", "./deploy.sh", "FOO=1 make", ""]) {
    assert.equal(sessionScopeLabel("Bash", { command }), null, command);
  }
});

test("other tools are scoped by name", () => {
  assert.equal(sessionScopeLabel("Edit", { file_path: "a.go" }), "Allow Edit this session");
});
