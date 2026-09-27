import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { hyperlinkTarget, linkAt, linkAtRows, linksOnRow, type TerminalLink } from "./links";

const W = "/home/me/repo";

interface Vector {
  name: string;
  workdir: string;
  rows: string[];
  wrapped?: boolean[];
  width?: number;
  row: number;
  col: number;
  expect: null | { kind: string; path?: string; url?: string; external?: boolean; verify?: boolean; line?: number; column?: number; spans?: number[][] };
}

// The same vectors drive the native client's Go port (internal/filelinks).
const vectors: { cases: Vector[]; hyperlinks: { name: string; workdir: string; uri: string; expect: Vector["expect"] }[] } = JSON.parse(
  readFileSync(join(import.meta.dirname, "..", "..", "..", "internal", "filelinks", "testdata", "vectors.json"), "utf8"),
);

function shape(link: TerminalLink | undefined) {
  if (!link) return null;
  const spans = (link.spans || []).map((span) => [span.row, span.start, span.end]);
  if (link.kind === "url") return { kind: "url", url: link.url, spans };
  return {
    kind: "file",
    path: link.path,
    external: !!link.external,
    verify: !!link.verify,
    line: link.line,
    column: link.column,
    spans,
  };
}

for (const vector of vectors.cases)
  test("shared vector: " + vector.name, () => {
    const rows = vector.rows.map((text, i) => ({ text, wrapped: vector.wrapped?.[i] || false }));
    const want = vector.expect && (vector.expect.kind === "url"
      ? { kind: "url", url: vector.expect.url, spans: vector.expect.spans }
      : { kind: "file", path: vector.expect.path, external: !!vector.expect.external, verify: !!vector.expect.verify, line: vector.expect.line, column: vector.expect.column, spans: vector.expect.spans });
    const got = shape(linkAtRows(rows, vector.row, vector.col, vector.workdir, vector.width || 0));
    // Vectors without spans only pin down what the link is.
    if (want && !vector.expect!.spans && got) want.spans = got.spans;
    assert.deepEqual(got, want);
  });

for (const vector of vectors.hyperlinks)
  test("shared OSC 8 vector: " + vector.name, () => {
    const link = hyperlinkTarget(vector.uri, vector.workdir);
    const got = shape(link);
    const want = vector.expect && (vector.expect.kind === "url"
      ? { kind: "url", url: vector.expect.url, spans: [] }
      : { kind: "file", path: vector.expect.path, external: !!vector.expect.external, verify: !!vector.expect.verify, line: vector.expect.line, column: vector.expect.column, spans: [] });
    assert.deepEqual(got, want);
  });

test("a joined link is found from every row it covers", () => {
  const rows = [{ text: "  • (/home/a/" }, { text: "    b/" }, { text: "    c.pdf)" }];
  for (const row of [0, 1, 2]) assert.equal((linksOnRow(rows, row, "/w")[0] as { path: string }).path, "/home/a/b/c.pdf");
});

test("single-line linkAt keeps working for callers without rows", () => {
  const line = "src/app.ts:42:7 - error";
  assert.equal(linkAt(line, 3, W)?.kind, "file");
  assert.equal(linkAt("https://x.dev/a/b.js", 16, W)?.kind, "url");
  assert.equal(linkAt(line, 20, W), undefined);
});

test("OSC 8 file links outside the workspace open read-only", () => {
  assert.deepEqual(hyperlinkTarget("file:///etc/hosts", W), { kind: "file", path: "/etc/hosts", external: true, line: undefined, column: undefined, text: "file:///etc/hosts" });
  assert.equal((hyperlinkTarget(`file://${W}/a%20b.md`, W) as { path: string }).path, "a b.md");
  assert.equal(hyperlinkTarget("javascript:alert(1)", W), undefined);
});
