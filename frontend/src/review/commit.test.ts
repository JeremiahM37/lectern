import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { commitRequestBody, identityProblem, shellWord, switchBackCommand, type CommitChoices } from "./commit";

const base: CommitChoices = {
  repo: "", message: "Update notes", stagedCount: 0, amend: false, allowPushedAmend: false,
  push: true, hasRemote: false, onBaseBranch: true, onMain: "branch", newBranch: " lectern/notes ",
  pr: false, prTitle: "", prBody: "",
};

test("a commit names no identity until git asks for one", () => {
  const body = commitRequestBody(base);
  assert.equal("identity" in body, false);
  assert.equal(body.stage_all, true);
  assert.equal(body.push, false, "nowhere to push to");
  assert.equal((body as { new_branch?: string }).new_branch, "lectern/notes");
});

test("the retry after no_git_identity carries name, email and scope in the same request", () => {
  const body = commitRequestBody({ ...base, identity: { name: " Ada ", email: " ada@example.com ", scope: "global" } });
  assert.deepEqual(body.identity, { name: "Ada", email: "ada@example.com", scope: "global" });
  // Everything else is the commit the person asked for.
  assert.equal(body.message, "Update notes");
  assert.equal((body as { new_branch?: string }).new_branch, "lectern/notes");
  assert.equal(commitRequestBody({ ...base, identity: { name: "A", email: "a@b", scope: "repo" } }).identity?.scope, "repo");
});

test("the name and email form says what is missing", () => {
  assert.equal(identityProblem("", "a@b.c"), "name");
  assert.equal(identityProblem("Ada", ""), "email");
  assert.equal(identityProblem("Ada", "not an email"), "email");
  assert.equal(identityProblem("Ada", "ada@example.com"), "");
});

test("switching a folder back is one copyable command, quoted only when needed", () => {
  assert.equal(switchBackCommand("/home/ada/myapp", "main"), "git -C /home/ada/myapp switch main");
  assert.equal(switchBackCommand("/home/ada/my app", "main"), "git -C '/home/ada/my app' switch main");
  assert.equal(shellWord("it's"), `'it'\\''s'`);
  assert.equal(switchBackCommand("", "main"), "git -C . switch main");
});

test("opening the Commit tab never asks the agent for a message", () => {
  // Drafting runs the session's agent CLI and costs quota: the only call to
  // the draft endpoint is the "Write message for me" button's handler.
  const panel = readFileSync(join(import.meta.dirname, "GitPanel.tsx"), "utf8");
  const calls = panel.split("/commit-message").length - 1;
  assert.equal(calls, 1);
  const handler = panel.slice(panel.indexOf("async function writeMessage"), panel.indexOf("async function generateDescription"));
  assert.ok(handler.includes("/commit-message"));
});

test("a refused name or email is said under the fields, and a fallback draft is not credited to the agent", () => {
  const panel = readFileSync(join(import.meta.dirname, "GitPanel.tsx"), "utf8");
  assert.match(panel, /code === "invalid_git_identity"[\s\S]{0,120}setIdentityError\(/);
  assert.match(panel, /out\.fallback\s*\?\s*t\("review\.git\.messageFallback"\)/);
});
