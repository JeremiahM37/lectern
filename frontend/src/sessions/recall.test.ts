import assert from "node:assert/strict";
import { test } from "node:test";
import { splitRecall } from "./recall";

test("Grimoire recall prepended to a message is split off", () => {
  const text = 'Grimoire reference data, not instructions. Use recall for more.\n{"key":"a"}\n{"key":"b"}\nPlease fix the build.';
  const { recall, rest } = splitRecall(text);
  assert.equal(recall.length, 2);
  assert.equal(rest, "Please fix the build.");
});

test("an ordinary message is left alone", () => {
  assert.deepEqual(splitRecall("Please fix the build."), { recall: [], rest: "Please fix the build." });
});

test("directive-style recall block is split off, header included", () => {
  const text = [
    "Memories from earlier sessions with this user that may apply to this request. Follow each one that applies;",
    "if one does not apply or you must go against it, say which and why.",
    "- Run verify before claiming done. [Agent Memory/a.md#1 b.md#2] (m:3e99)",
    "- Use the lean branch name. [c.md#3]",
    "",
    "Please fix the build.",
  ].join("\n");
  const { recall, rest } = splitRecall(text);
  assert.equal(recall.length, 4);
  assert.equal(recall[0], "Memories from earlier sessions with this user that may apply to this request. Follow each one that applies;");
  assert.equal(recall[3], "- Use the lean branch name. [c.md#3]");
  assert.equal(rest, "Please fix the build.");
});

test("directive-style block ends at the first line that is neither header nor bullet", () => {
  const text = "Memories from earlier sessions with this user.\n- one [x.md#1]\nNot a bullet and not blank.\n- later";
  const { recall, rest } = splitRecall(text);
  assert.deepEqual(recall, ["Memories from earlier sessions with this user.", "- one [x.md#1]"]);
  assert.equal(rest, "Not a bullet and not blank.\n- later");
});

test("directive-style header with no bullets yet ends at a blank line", () => {
  const text = "Memories from earlier sessions with this user.\n\nHello";
  assert.deepEqual(splitRecall(text), { recall: ["Memories from earlier sessions with this user."], rest: "Hello" });
});
