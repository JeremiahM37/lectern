import assert from "node:assert/strict";
import { test } from "node:test";
import { buildChatCards, type ConversationItem, type ToolCard } from "./chatCards";

function item(partial: Partial<ConversationItem>): ConversationItem {
  return { id: "0-0", role: "assistant", kind: "text", ...partial };
}

test("text and thinking items become cards in order", () => {
  const cards = buildChatCards([
    item({ id: "a", kind: "text", role: "user", text: "hi" }),
    item({ id: "b", kind: "thinking", text: "hmm" }),
    item({ id: "c", kind: "text", role: "assistant", text: "hello" }),
  ]);
  assert.deepEqual(
    cards.map((c) => c.kind),
    ["text", "thinking", "text"],
  );
});

test("a tool_use and its later tool_result merge into ONE card, not two", () => {
  const cards = buildChatCards([
    item({ id: "a", kind: "tool_use", tool_name: "Bash", tool_use_id: "t1", input: { command: "ls" } }),
    item({ id: "b", kind: "tool_result", tool_use_id: "t1", output: "file.txt", is_error: false }),
  ]);
  assert.equal(cards.length, 1);
  const card = cards[0] as ToolCard;
  assert.equal(card.kind, "tool");
  assert.equal(card.name, "Bash");
  assert.equal(card.status, "done");
  assert.equal(card.output, "file.txt");
  assert.equal(card.isError, false);
});

test("a tool_use with no result yet stays 'running'", () => {
  const cards = buildChatCards([
    item({ id: "a", kind: "tool_use", tool_name: "Read", tool_use_id: "t1", input: { file_path: "/x" } }),
  ]);
  const card = cards[0] as ToolCard;
  assert.equal(card.status, "running");
  assert.equal(card.output, undefined);
});

test("an orphan tool_result (its tool_use fell outside the page) still becomes its own card", () => {
  const cards = buildChatCards([item({ id: "b", kind: "tool_result", tool_use_id: "unseen", output: "3 passed" })]);
  assert.equal(cards.length, 1);
  const card = cards[0] as ToolCard;
  assert.equal(card.kind, "tool");
  assert.equal(card.status, "done");
  assert.equal(card.output, "3 passed");
});

test("empty text/thinking blocks are dropped, not rendered as blank cards", () => {
  const cards = buildChatCards([item({ id: "a", kind: "text", text: "" }), item({ id: "b", kind: "thinking", text: "" })]);
  assert.equal(cards.length, 0);
});

test("two independent tool calls in one turn stay two separate cards", () => {
  const cards = buildChatCards([
    item({ id: "a", kind: "tool_use", tool_name: "Read", tool_use_id: "t1", input: {} }),
    item({ id: "b", kind: "tool_use", tool_name: "Grep", tool_use_id: "t2", input: {} }),
    item({ id: "c", kind: "tool_result", tool_use_id: "t2", output: "match" }),
    item({ id: "d", kind: "tool_result", tool_use_id: "t1", output: "content" }),
  ]);
  assert.equal(cards.length, 2);
  const read = cards[0] as ToolCard;
  const grep = cards[1] as ToolCard;
  assert.equal(read.name, "Read");
  assert.equal(read.output, "content");
  assert.equal(grep.name, "Grep");
  assert.equal(grep.output, "match");
});
