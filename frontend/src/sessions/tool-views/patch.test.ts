import assert from "node:assert/strict";
import { test } from "node:test";
import { parsePatchLines } from "../../review/diffLines";
import { buildMultiEditPatch, buildNewFilePatch, buildReplacePatch, parseCodexPatch } from "./patch";

test("buildReplacePatch keeps a small file's unchanged lines as context", () => {
  const old = ["one", "two", "three", "four", "five"].join("\n");
  const next = ["one", "two", "THREE", "four", "five"].join("\n");
  const patch = buildReplacePatch("f.txt", old, next);
  const lines = parsePatchLines(patch);
  assert.equal(lines.find((l) => l.kind === "del")!.text, "-three");
  assert.equal(lines.find((l) => l.kind === "add")!.text, "+THREE");
  assert.deepEqual(
    lines.filter((l) => l.kind === "ctx").map((l) => l.text),
    [" one", " two", " four", " five"],
  );
});

test("buildReplacePatch trims a long unchanged run down to a couple of lines of context", () => {
  const base = ["a", "b", "c", "d", "e", "f"];
  const old = [...base, "CHANGE", "g", "h", "i", "j", "k"].join("\n");
  const next = [...base, "CHANGED", "g", "h", "i", "j", "k"].join("\n");
  const patch = buildReplacePatch("f.txt", old, next);
  const lines = parsePatchLines(patch);
  const ctx = lines.filter((l) => l.kind === "ctx").map((l) => l.text);
  // Only the 2 lines nearest the change survive on each side; "a".."d" and
  // "h".."k" are trimmed out — a diff card is a diff, not the whole file.
  assert.deepEqual(ctx, [" e", " f", " g", " h"]);
  assert.ok(!patch.includes("\na\n") && !patch.includes(" a\n"));
});

test("buildNewFilePatch marks every line as added against /dev/null", () => {
  const patch = buildNewFilePatch("new.txt", "line one\nline two");
  assert.ok(patch.startsWith("--- /dev/null"));
  const lines = parsePatchLines(patch);
  assert.deepEqual(
    lines.filter((l) => l.kind === "add").map((l) => l.text),
    ["+line one", "+line two"],
  );
});

test("buildMultiEditPatch renders each edit as its own hunk under one file header", () => {
  const patch = buildMultiEditPatch("m.py", [
    { old_string: "a = 1", new_string: "a = 2" },
    { old_string: "b = 1", new_string: "b = 2" },
  ]);
  const lines = parsePatchLines(patch);
  const hunks = lines.filter((l) => l.kind === "hunk");
  assert.equal(hunks.length, 2);
  const removed = lines.filter((l) => l.kind === "del").map((l) => l.text);
  assert.deepEqual(removed, ["-a = 1", "-b = 1"]);
});

test("parseCodexPatch splits an apply_patch envelope by file, one file added and one updated", () => {
  const envelope = [
    "*** Begin Patch",
    "*** Add File: new.txt",
    "+hello",
    "*** Update File: existing.py",
    "@@",
    "-old line",
    "+new line",
    "*** End Patch",
  ].join("\n");
  const files = parseCodexPatch(envelope);
  assert.equal(files.length, 2);
  const added = files[0]!;
  const updated = files[1]!;
  assert.equal(added.path, "new.txt");
  assert.equal(added.action, "add");
  assert.equal(updated.path, "existing.py");
  assert.equal(updated.action, "update");
  const lines = parsePatchLines(updated.patch);
  assert.ok(lines.some((l) => l.kind === "del" && l.text === "-old line"));
  assert.ok(lines.some((l) => l.kind === "add" && l.text === "+new line"));
});

test("parseCodexPatch falls back to one unparsed block for text it doesn't recognize", () => {
  const files = parseCodexPatch("not a patch envelope at all");
  assert.equal(files.length, 1);
  assert.equal(files[0]!.patch, "not a patch envelope at all");
});
