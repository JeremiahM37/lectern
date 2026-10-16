import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { askUserView, isAskUser, PLAN_ANSWER, reviseAnswer } from "./ask-user";

test("an AskUser approval is recognised by its tool name", () => {
  assert.equal(isAskUser({ tool_name: "AskUser" }), true);
  assert.equal(isAskUser({ tool_name: "Bash" }), false);
});

test("a question shows its options and a free-text box when the agent allows one", () => {
  const view = askUserView({
    kind: "question",
    question: "Which database?",
    options: ["postgres", "sqlite", 3, "  "],
    body: "Both drivers exist.",
    allow_free_text: true,
  });
  assert.deepEqual(view, {
    kind: "question",
    question: "Which database?",
    options: ["postgres", "sqlite"],
    body: "Both drivers exist.",
    allowFreeText: true,
  });
});

test("a question with no options always gets a free-text box, even when the agent did not allow one", () => {
  assert.equal(askUserView({ kind: "question", question: "Why?", options: [], allow_free_text: false }).allowFreeText, true);
  assert.equal(askUserView({ kind: "question", question: "Pick", options: ["a"], allow_free_text: false }).allowFreeText, false);
});

test("anything that is not a plan review is a plain question", () => {
  assert.equal(askUserView({}).kind, "question");
  assert.equal(askUserView({ kind: "plan_review" }).kind, "plan_review");
});

test("Revise sends the feedback after the word, and a blank box sends the bare word", () => {
  assert.equal(reviseAnswer("  add retries  "), "Revise add retries");
  assert.equal(reviseAnswer("   "), "Revise");
});

test("the plan answers are the strings Scribe matches on", () => {
  const scribe = readFileSync(join(import.meta.dirname, "../../../internal/scribe/agent/interactive.go"), "utf8");
  for (const answer of [PLAN_ANSWER.execute, PLAN_ANSWER.clear, PLAN_ANSWER.revise, PLAN_ANSWER.cancel]) {
    assert.ok(scribe.includes(`"${answer}"`), `interactive.go does not define "${answer}"`);
  }
});

test("the approval card hands AskUser items to the question card, and keeps Y / A / N off them", () => {
  const card = readFileSync(join(import.meta.dirname, "ApprovalCard.tsx"), "utf8");
  assert.ok(card.includes("isAskUser(approval)"));
  assert.ok(card.includes("<AskUserCard"));
  assert.ok(/isAskUser\(approval\)\s*\|\|\s*event\.altKey/.test(card));
});
