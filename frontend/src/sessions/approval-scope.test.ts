import assert from "node:assert/strict";
import { test } from "node:test";
import { decisionBody, sessionScope } from "./ApprovalCard";

test("a Bash approval names the command word a session rule would allow", () => {
  assert.deepEqual(sessionScope("Bash", { command: "git status" }), { kind: "command", word: "git" });
});

test("wrappers, paths and assignments are too broad to remember for a session", () => {
  for (const command of ["sudo rm -rf x", "bash -c 'ls'", "env A=1 make", "./deploy.sh", "FOO=1 make", ""]) {
    assert.equal(sessionScope("Bash", { command }).kind, "broad", command);
  }
});

test("other tools are scoped by name", () => {
  assert.deepEqual(sessionScope("Edit", { file_path: "a.go" }), { kind: "tool", word: "Edit" });
});

test("every surface sends the same decision", () => {
  assert.deepEqual(decisionBody("approved"), { decision: "approved" });
  assert.deepEqual(decisionBody("approved", { forSession: true }), { decision: "approved", for_session: true });
  assert.deepEqual(decisionBody("approved", { always: true }), { decision: "approved", always_allow: true });
  assert.deepEqual(decisionBody("denied", { note: "no" }), { decision: "denied", note: "no" });
});
