import assert from "node:assert/strict";
import { test } from "node:test";
import { commentTarget, parsePatchLines, type DiffLine } from "./diffLines";

function at(lines: DiffLine[], i: number): DiffLine {
  const line = lines[i];
  assert.ok(line !== undefined, `no line at index ${i}`);
  return line;
}

const patch = [
  "diff --git a/app.py b/app.py",
  "index 83db48f..bf2f3f4 100644",
  "--- a/app.py",
  "+++ b/app.py",
  "@@ -1,4 +1,5 @@",
  " def main():",
  "-    print('hello')",
  "+    print('hello, lectern')",
  "+",
  " def health():",
  "     return True",
  "",
].join("\n");

test("parsePatchLines assigns real old/new line numbers per hunk", () => {
  const lines = parsePatchLines(patch);
  const kinds = lines.map((l) => l.kind);
  assert.deepEqual(kinds.slice(0, 4), ["meta", "meta", "meta", "meta"]);
  assert.equal(at(lines, 4).kind, "hunk");

  const ctx1 = at(lines, 5);
  assert.equal(ctx1.kind, "ctx");
  assert.equal(ctx1.oldLine, 1);
  assert.equal(ctx1.newLine, 1);

  const del = at(lines, 6);
  assert.equal(del.kind, "del");
  assert.equal(del.oldLine, 2);
  assert.equal(del.newLine, undefined);

  const add = at(lines, 7);
  assert.equal(add.kind, "add");
  assert.equal(add.newLine, 2);

  const addBlank = at(lines, 8);
  assert.equal(addBlank.kind, "add");
  assert.equal(addBlank.newLine, 3);

  const ctx2 = at(lines, 9);
  assert.equal(ctx2.kind, "ctx");
  assert.equal(ctx2.oldLine, 3);
  assert.equal(ctx2.newLine, 4);
});

test("commentTarget maps a removed line to the old side and an added/context line to the new side", () => {
  const lines = parsePatchLines(patch);
  const del = commentTarget(at(lines, 6));
  assert.deepEqual(del, { line: 2, side: "old", code: "    print('hello')" });

  const add = commentTarget(at(lines, 7));
  assert.deepEqual(add, {
    line: 2,
    side: "new",
    code: "    print('hello, lectern')",
  });

  const ctx = commentTarget(at(lines, 5));
  assert.deepEqual(ctx, { line: 1, side: "new", code: "def main():" });

  assert.equal(commentTarget(at(lines, 4)), undefined); // the @@ hunk header itself
  assert.equal(commentTarget(at(lines, 0)), undefined); // "diff --git" metadata
});

test("a second hunk resets line numbers from its own header, not the first hunk's tail", () => {
  const twoHunks = [
    "diff --git a/b.go b/b.go",
    "--- a/b.go",
    "+++ b/b.go",
    "@@ -1,2 +1,2 @@",
    " package b",
    "-var x = 1",
    "+var x = 2",
    "@@ -40,2 +40,3 @@",
    " func f() {",
    '+\tlog.Println("hi")',
  ].join("\n");
  const lines = parsePatchLines(twoHunks);
  const secondHunkCtx = at(lines, 8);
  assert.equal(secondHunkCtx.kind, "ctx");
  assert.equal(secondHunkCtx.oldLine, 40);
  const secondHunkAdd = at(lines, 9);
  assert.equal(secondHunkAdd.kind, "add");
  assert.equal(secondHunkAdd.newLine, 41);
});
