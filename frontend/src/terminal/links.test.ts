import assert from "node:assert/strict";
import { test } from "node:test";
import { hyperlinkTarget, linkAt } from "./links";

const W = "/home/me/repo";

test("a tap on an address opens it, without the sentence's punctuation", () => {
  const line = "Preview ready (see https://example.com/a?b=1).";
  const col = line.indexOf("example");
  assert.deepEqual(linkAt(line, col, W), { kind: "url", url: "https://example.com/a?b=1", text: "https://example.com/a?b=1" });
  assert.equal(linkAt(line, 2, W), undefined);
  assert.equal(linkAt("docs at https://en.wikipedia.org/wiki/Foo_(bar) ok", 20, W)?.text, "https://en.wikipedia.org/wiki/Foo_(bar)");
});

test("a compiler's path:line:col opens the file at that line", () => {
  const line = "src/app.ts:42:7 - error TS2322: Type 'x'";
  assert.deepEqual(linkAt(line, 3, W), { kind: "file", path: "src/app.ts", line: 42, column: 7, text: "src/app.ts:42:7" });
  assert.deepEqual(linkAt(`  at ${W}/internal/api/server.go:120`, 12, W), {
    kind: "file", path: "internal/api/server.go", line: 120, column: undefined, text: `${W}/internal/api/server.go:120`,
  });
  assert.equal(linkAt("M ./frontend/package.json", 5, W)?.kind, "file");
});

test("paths the viewer cannot open are not offered", () => {
  assert.equal(linkAt("/etc/passwd:1", 3, W), undefined);
  assert.equal(linkAt("~/secrets.txt", 3, W), undefined);
  assert.equal(linkAt("../outside/file.go", 5, W), undefined);
  // A URL's own path is not a file.
  assert.equal(linkAt("https://x.dev/a/b.js", 16, W)?.kind, "url");
});

test("OSC 8 hyperlinks: web addresses and workspace files", () => {
  assert.equal(hyperlinkTarget("https://ci.example/run/5", W)?.kind, "url");
  assert.deepEqual(hyperlinkTarget(`file://host${W}/src/main.go`, W), { kind: "file", path: "src/main.go", text: `file://host${W}/src/main.go` });
  assert.equal(hyperlinkTarget("file:///etc/hosts", W), undefined);
  assert.equal(hyperlinkTarget("javascript:alert(1)", W), undefined);
});
