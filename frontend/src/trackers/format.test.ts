import assert from "node:assert/strict";
import { test } from "node:test";
import { applyFormat } from "./format";

test("wrapping formats wrap the selection and toggle back off", () => {
  const on = applyFormat("say hello now", 4, 9, "bold");
  assert.deepEqual(on, { text: "say **hello** now", start: 6, end: 11 });
  assert.deepEqual(applyFormat(on.text, on.start, on.end, "bold"), { text: "say hello now", start: 4, end: 9 });
  assert.equal(applyFormat("", 0, 0, "code").text, "`code`");
  assert.equal(applyFormat("x", 1, 1, "italic").text, "x*italic text*");
});

test("links select the URL to type over", () => {
  const e = applyFormat("see docs", 4, 8, "link");
  assert.equal(e.text, "see [docs](https://)");
  assert.equal(e.text.slice(e.start, e.end), "https://");
});

test("line formats apply to every selected line and toggle", () => {
  const text = "one\ntwo\nthree";
  const b = applyFormat(text, 1, 6, "bullet");
  assert.equal(b.text, "- one\n- two\nthree");
  assert.equal(applyFormat(b.text, b.start, b.end, "bullet").text, text);
  assert.equal(applyFormat(text, 0, text.length, "number").text, "1. one\n2. two\n3. three");
  assert.equal(applyFormat("title", 2, 2, "heading").text, "## title");
  assert.equal(applyFormat("a\nquoted", 3, 3, "quote").text, "a\n> quoted");
});

test("code blocks start on their own line", () => {
  assert.equal(applyFormat("run:make", 4, 8, "codeblock").text, "run:\n```\nmake\n```\n");
});
