import assert from "node:assert/strict";
import { test } from "node:test";
import {
  anchorFor,
  attributionCounts,
  authorOf,
  buildTree,
  commentState,
  fingerprint,
  isImagePath,
  parseFile,
  placeComment,
  splitRows,
} from "./diffModel";
import {
  conflictCount,
  hasMarkers,
  parseConflicts,
  resolveConflicts,
} from "./conflicts";

const patch = [
  "diff --git a/app.py b/app.py",
  "index 83db48f..bf2f3f4 100644",
  "--- a/app.py",
  "+++ b/app.py",
  "@@ -1,5 +1,5 @@",
  " def main():",
  "-    print('hello')",
  "-    return 1",
  "+    print('hello, lectern')",
  " ",
  " def health():",
  "@@ -20,3 +20,4 @@ def other():",
  "     a = 1",
  "+    b = 2",
  "     return a",
  "",
].join("\n");

test("parseFile splits a patch into indexed hunks and drops the trailing newline", () => {
  const f = parseFile(patch);
  assert.equal(f.hunks.length, 2);
  assert.equal(f.meta.length, 4);
  assert.deepEqual(
    f.hunks.map((h) => h.index),
    [0, 1],
  );
  assert.equal(f.hunks[0]!.lines.length, 6);
  assert.equal(f.hunks[1]!.lines.at(-1)!.text, "     return a");
  assert.equal(f.binary, false);
  const bin = parseFile("diff --git a/x.png b/x.png\nBinary files a/x.png and b/x.png differ\n");
  assert.equal(bin.binary, true);
  assert.equal(bin.hunks.length, 0);
});

test("splitRows pairs a deletion run with the additions that replace it", () => {
  const rows = splitRows(parseFile(patch).hunks[0]!.lines);
  // ctx, (del,add), (del,-), ctx, ctx
  assert.equal(rows.length, 5);
  assert.equal(rows[0]!.left!.kind, "ctx");
  assert.equal(rows[1]!.left!.text, "-    print('hello')");
  assert.equal(rows[1]!.right!.text, "+    print('hello, lectern')");
  assert.equal(rows[2]!.left!.text, "-    return 1");
  assert.equal(rows[2]!.right, undefined);
  // both sides of a context row are the same line, with both numbers
  assert.equal(rows[3]!.left, rows[3]!.right);
  const addOnly = splitRows(parseFile(patch).hunks[1]!.lines);
  assert.equal(addOnly[1]!.left, undefined);
  assert.equal(addOnly[1]!.right!.newLine, 21);
});

test("buildTree nests paths, puts folders first and collapses single-child folders", () => {
  const tree = buildTree(["src/review/a.ts", "src/review/b.ts", "README.md", "docs/x/y/z.md"]);
  assert.deepEqual(
    tree.map((n) => [n.name, n.file]),
    [
      ["docs/x/y", false],
      ["src/review", false],
      ["README.md", true],
    ],
  );
  assert.deepEqual(
    tree[1]!.children.map((c) => c.path),
    ["src/review/a.ts", "src/review/b.ts"],
  );
  assert.equal(tree[0]!.children[0]!.path, "docs/x/y/z.md");
});

test("fingerprint changes with content", () => {
  assert.equal(fingerprint(patch), fingerprint(patch));
  assert.notEqual(fingerprint(patch), fingerprint(patch.replace("b = 2", "b = 3")));
  assert.ok(isImagePath("assets/Logo.PNG"));
  assert.ok(!isImagePath("logo.png.txt"));
});

test("a comment follows its line when the agent inserts code above it", () => {
  const before = parseFile(patch);
  const anchor = anchorFor(before, "new", 21)!;
  assert.equal(anchor.code, "    b = 2");
  assert.equal(anchor.context_before, "    a = 1");
  assert.equal(anchor.context_after, "    return a");
  assert.deepEqual(placeComment(before, anchor), { line: 21, exact: true });

  // The agent adds three lines before the second hunk's code: same code,
  // same context, new line numbers.
  const shifted = parseFile(
    patch.replace("@@ -20,3 +20,4 @@", "@@ -20,3 +23,4 @@"),
  );
  assert.deepEqual(placeComment(shifted, anchor), { line: 24, exact: true });
  assert.equal(commentState("sent", placeComment(shifted, anchor)), "open");
});

test("a sent comment reads as addressed when the agent changes or removes its line", () => {
  const anchor = anchorFor(parseFile(patch), "new", 21)!;
  const changedContext = parseFile(patch.replace("     return a", "     return a + b"));
  const p = placeComment(changedContext, anchor);
  assert.deepEqual(p, { line: 21, exact: false });
  assert.equal(commentState("sent", p), "addressed");

  const removed = parseFile(patch.replace("+    b = 2\n", ""));
  assert.equal(placeComment(removed, anchor), undefined);
  assert.equal(commentState("sent", undefined), "addressed");
  assert.equal(commentState("resolved", undefined), "resolved");
  assert.equal(commentState("draft", undefined), "draft");
});

test("with the same code twice, context picks the right one", () => {
  const twice = [
    "@@ -1,7 +1,7 @@",
    " alpha",
    "+x = 1",
    " beta",
    " gamma",
    "+x = 1",
    " delta",
    "",
  ].join("\n");
  const f = parseFile(twice);
  const second = anchorFor(f, "new", 5)!;
  assert.equal(second.context_before, "beta\ngamma");
  assert.deepEqual(placeComment(f, second), { line: 5, exact: true });
});

test("authorOf reads attribution ranges", () => {
  const attr = { agent: [[3, 5]] as [number, number][], human: [[9, 9]] as [number, number][] };
  assert.equal(authorOf(attr, 4), "agent");
  assert.equal(authorOf(attr, 9), "human");
  assert.equal(authorOf(attr, 6), undefined);
  assert.equal(authorOf(undefined, 1), undefined);
  assert.deepEqual(attributionCounts(attr), { agent: 3, human: 1 });
});

const conflict = [
  "one",
  "<<<<<<< main",
  "TWO-main",
  "||||||| base",
  "two",
  "=======",
  "TWO-feat",
  ">>>>>>> feat",
  "three",
  "<<<<<<< HEAD",
  "x",
  "=======",
  "y",
  ">>>>>>> feat",
  "",
].join("\n");

test("parseConflicts reads diff3 and two-way regions", () => {
  const s = parseConflicts(conflict);
  assert.equal(conflictCount(s), 2);
  const first = s[1]!;
  assert.equal(first.kind, "conflict");
  if (first.kind !== "conflict") return;
  assert.deepEqual(first.ours, ["TWO-main"]);
  assert.deepEqual(first.base, ["two"]);
  assert.deepEqual(first.theirs, ["TWO-feat"]);
  assert.equal(first.oursLabel, "main");
  assert.equal(first.theirsLabel, "feat");
  const second = s[3]!;
  assert.equal(second.kind === "conflict" && second.base, undefined);
});

test("resolveConflicts applies ours, theirs, both and manual choices", () => {
  const s = parseConflicts(conflict);
  assert.equal(resolveConflicts(s, ["ours", "theirs"]), "one\nTWO-main\nthree\ny\n");
  assert.equal(resolveConflicts(s, ["both", "both-theirs-first"]), "one\nTWO-main\nTWO-feat\nthree\ny\nx\n");
  assert.equal(resolveConflicts(s, ["base", { custom: "hand\nmade" }]), "one\ntwo\nthree\nhand\nmade\n");
  const partial = resolveConflicts(s, ["theirs"]);
  assert.ok(hasMarkers(partial), "an unresolved region keeps its markers");
  assert.ok(!hasMarkers(resolveConflicts(s, ["theirs", "ours"])));
  // A file with no conflicts round-trips unchanged.
  assert.equal(resolveConflicts(parseConflicts("a\nb\n"), []), "a\nb\n");
});
