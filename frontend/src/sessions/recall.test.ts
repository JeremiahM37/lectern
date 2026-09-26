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
