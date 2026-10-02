import assert from "node:assert/strict";
import { test } from "node:test";
import { readerText } from "./reader-text";

test("trailing blank screen rows are dropped, so the last line stays in view", () => {
  assert.equal(readerText("> make a file\n⏺ done\n\n   \n\n\n"), "> make a file\n⏺ done");
  assert.equal(readerText("one\r\n\r\n  two  \n \n"), "one\n\n  two");
  assert.equal(readerText(""), "");
  assert.equal(readerText("\n\n"), "");
});
