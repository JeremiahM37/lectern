import assert from "node:assert/strict";
import { test } from "node:test";
import { highlights, parseQuery, rank } from "./fuzzy";
import { naturalCompare, sortEntries } from "./natsort";
import { findLinks, hyperlinkTarget, linkAt } from "./links";
import { fileLink, lineHash, parseLineHash, readDeepLink } from "./deeplink";
import { detectDelimiter, parseDelimited } from "./csv";
import { parseNotebook } from "./notebook";
import { headings, splitFrontMatter, wikiLinks } from "./markdown-model";
import { languageFor, viewKind } from "./formats";

const repo = [
  "README.md",
  "go.mod",
  "cmd/lectern/main.go",
  "internal/api/server.go",
  "internal/api/server_test.go",
  "internal/api/terminal_workspace.go",
  "internal/domain/remain.go",
  "frontend/src/App.tsx",
  "frontend/src/apps/list.ts",
  "frontend/src/terminal/App.tsx",
  "frontend/src/files/Workbench.tsx",
  "docs/files.md",
  "scripts/revert/go.sum",
];

const top = (query: string, ignored: string[] = []) => rank(query, repo, ignored).map((r) => r.path);

test("Quick Open prefers the file name to scattered folder letters", () => {
  assert.equal(top("main")[0], "cmd/lectern/main.go");
  assert.equal(top("server")[0], "internal/api/server.go");
  assert.equal(top("srvgo")[0], "internal/api/server.go");
  assert.equal(top("wb")[0], "frontend/src/files/Workbench.tsx");
  assert.equal(top("termws")[0], "internal/api/terminal_workspace.go");
});

test("Quick Open ranks an exact name first and shorter paths on ties", () => {
  assert.equal(top("App.tsx")[0], "frontend/src/App.tsx");
  assert.deepEqual(top("readme"), ["README.md"]);
  assert.equal(top("go.mod")[0], "go.mod");
});

test("Quick Open narrows with space-separated terms and a folder", () => {
  assert.equal(top("terminal app")[0], "frontend/src/terminal/App.tsx");
  assert.equal(top("api test")[0], "internal/api/server_test.go");
  assert.deepEqual(top("zzz"), []);
});

test("gitignored files come after every tracked match, as their own section", () => {
  const results = rank("main", repo, ["node_modules/main.js", "dist/main.go"]);
  const firstIgnored = results.findIndex((r) => r.ignored);
  assert.ok(firstIgnored > 0);
  assert.ok(results.slice(firstIgnored).every((r) => r.ignored));
  assert.ok(results.slice(0, firstIgnored).every((r) => !r.ignored));
});

test("Quick Open reads a trailing line and column", () => {
  assert.deepEqual(parseQuery("server.go:42"), { text: "server.go", line: 42, column: undefined });
  assert.deepEqual(parseQuery("server.go:42:7"), { text: "server.go", line: 42, column: 7 });
  assert.deepEqual(parseQuery("server.go"), { text: "server.go" });
});

test("highlighting marks the matched characters of the name", () => {
  assert.deepEqual([...highlights("main", "cmd/lectern/main.go")].sort((a, b) => a - b), [12, 13, 14, 15]);
});

test("ranking 5,000 paths fits comfortably inside the 200 ms budget", () => {
  const paths: string[] = [];
  const words = ["api", "server", "client", "store", "model", "view", "util", "test", "handler", "config"];
  for (let i = 0; i < 5000; i++) paths.push(`pkg/${words[i % 10]}/${words[(i * 7) % 10]}_${i}.go`);
  rank("warm", paths);
  // CPU time, not wall time: the suite runs beside other suites on shared cores.
  let slowest = 0;
  for (const query of ["srv", "handlerconf", "store_49", "a", "util test"]) {
    const started = process.cpuUsage();
    rank(query, paths);
    const used = process.cpuUsage(started);
    slowest = Math.max(slowest, (used.user + used.system) / 1000);
  }
  assert.ok(slowest < 150, `slowest query took ${slowest.toFixed(1)} ms of CPU`);
});

test("explorer order is natural: folders first, file2 before file10", () => {
  assert.ok(naturalCompare("file2.txt", "file10.txt") < 0);
  assert.ok(naturalCompare("Zeta", "alpha") > 0);
  const sorted = sortEntries([
    { name: "b10.md", directory: false },
    { name: "b2.md", directory: false },
    { name: "src", directory: true },
    { name: "a.md", directory: false },
  ]).map((e) => e.name);
  assert.deepEqual(sorted, ["src", "a.md", "b2.md", "b10.md"]);
});

test("terminal output links files with line and column", () => {
  const work = "/home/me/repo";
  const cases: [string, Partial<ReturnType<typeof findLinks>[number]>][] = [
    ["src/app.ts:12:5: error TS2322", { path: "src/app.ts", line: 12, column: 5 }],
    ["./main.go:40 undefined: x", { path: "main.go", line: 40 }],
    ["at run (/home/me/repo/lib/x.js:3:9)", { path: "lib/x.js", line: 3, column: 9 }],
    ["Button.tsx(8,14): error", { path: "Button.tsx", line: 8, column: 14 }],
    ['  File "tools/run.py", line 9, in main', { path: "tools/run.py", line: 9 }],
    ["modified:   docs/files.md", { path: "docs/files.md" }],
    ["see Makefile:12", { path: "Makefile", line: 12 }],
  ];
  for (const [text, expected] of cases) {
    const link = findLinks(text, work).find((l) => l.path);
    assert.ok(link, text);
    for (const [key, value] of Object.entries(expected)) assert.equal(link[key as keyof typeof link], value, `${text}: ${key}`);
  }
});

test("terminal links skip outside paths, versions and prose, and find URLs", () => {
  const work = "/home/me/repo";
  assert.equal(findLinks("/etc/passwd:1", work).filter((l) => l.path).length, 0);
  assert.equal(findLinks("../../escape.txt:3", work).filter((l) => l.path).length, 0);
  assert.equal(findLinks("version 1.2.3 and/or 10.0.0.1", work).length, 0);
  const url = findLinks("open https://example.com/a?b=1. now", work);
  assert.deepEqual(url.map((l) => l.url), ["https://example.com/a?b=1"]);
  const line = "error in src/a.ts:3 and src/b.ts:4";
  const links = findLinks(line, work);
  assert.equal(linkAt(links, line.indexOf("b.ts"))?.path, "src/b.ts");
  assert.equal(linkAt(links, 0), undefined);
});

test("line deep links round-trip", () => {
  assert.deepEqual(parseLineHash("#L42"), { line: 42, column: undefined, endLine: undefined });
  assert.deepEqual(parseLineHash("#L10C3-L20"), { line: 10, column: 3, endLine: 20 });
  assert.equal(parseLineHash("#terminals"), undefined);
  assert.equal(lineHash({ line: 10, endLine: 20 }), "#L10-L20");
  const url = fileLink({ origin: "https://x", pathname: "/terminal/session/4" }, "src/a b.ts", { line: 7 });
  assert.equal(url, "https://x/terminal/session/4?open=src%2Fa%20b.ts#L7");
  assert.deepEqual(readDeepLink("?open=src%2Fa%20b.ts", "#L7"), { path: "src/a b.ts", target: { line: 7, column: undefined, endLine: undefined } });
});

test("CSV parsing keeps quoted delimiters, quotes and newlines", () => {
  const { rows } = parseDelimited('name,note\n"Smith, J","said ""hi""\nthen left"\nx,\n');
  assert.deepEqual(rows, [["name", "note"], ["Smith, J", 'said "hi"\nthen left'], ["x", ""]]);
  assert.equal(detectDelimiter("a.tsv", "a,b"), "\t");
  assert.equal(detectDelimiter("a.csv", "a;b;c\n1;2;3"), ";");
  const capped = parseDelimited("a\nb\nc\nd\n", ",", 2);
  assert.equal(capped.rows.length, 2);
  assert.equal(capped.truncated, true);
});

test("notebooks render markdown, code, outputs and errors", () => {
  const notebook = parseNotebook(
    JSON.stringify({
      metadata: { kernelspec: { language: "python" } },
      cells: [
        { cell_type: "markdown", source: ["# Title\n", "text"] },
        {
          cell_type: "code",
          execution_count: 3,
          source: "print(1)",
          outputs: [
            { output_type: "stream", name: "stdout", text: ["1\n"] },
            { output_type: "display_data", data: { "image/png": "iVBOR\nw0=", "text/plain": "<Figure>" } },
            { output_type: "error", ename: "E", evalue: "v", traceback: ["\u001b[31mTraceback\u001b[0m", "E: v"] },
          ],
        },
      ],
    }),
  );
  assert.equal(notebook.language, "python");
  assert.equal(notebook.cells[0]!.source, "# Title\ntext");
  const outputs = notebook.cells[1]!.outputs;
  assert.deepEqual(outputs[0], { kind: "text", text: "1\n", stream: "stdout" });
  assert.deepEqual(outputs[1], { kind: "image", mime: "image/png", data: "data:image/png;base64,iVBORw0=" });
  assert.deepEqual(outputs[2], { kind: "error", text: "Traceback\nE: v" });
  assert.throws(() => parseNotebook('{"nbformat":4}'));
});

test("front matter, headings and wiki links", () => {
  const doc = "---\ntitle: Files\ntags:\n  - a\n---\n# Intro\n```\n# not a heading\n```\nSetext\n---\n## Intro\n";
  const front = splitFrontMatter(doc);
  assert.deepEqual(front.fields, [["title", "Files"], ["tags", "- a"]]);
  assert.equal(front.lines, 5);
  assert.deepEqual(
    headings(front.body).map((h) => [h.level, h.text, h.slug, h.line]),
    [[1, "Intro", "intro", 1], [2, "Setext", "setext", 5], [2, "Intro", "intro-1", 7]],
  );
  assert.equal(wikiLinks("see [[Design Notes|the notes]]"), "see [the notes](lectern-wiki:Design%20Notes)");
});

test("file kinds and editor languages", () => {
  const text = new TextEncoder().encode("hello");
  assert.equal(viewKind("a/README.md", text), "markdown");
  assert.equal(viewKind("x.ipynb", text), "notebook");
  assert.equal(viewKind("data.tsv", text), "csv");
  assert.equal(viewKind("blob.dat", new Uint8Array([1, 0, 2])), "binary");
  assert.equal(viewKind("logo.svg", text), "svg");
  assert.equal(languageFor("src/App.tsx"), "typescript");
  assert.equal(languageFor("Dockerfile"), "dockerfile");
  assert.equal(languageFor("x.unknown"), "plaintext");
});

test("OSC 8 hyperlinks open workspace files and web addresses only", () => {
  const work = "/home/me/repo";
  assert.deepEqual(hyperlinkTarget("file://host/home/me/repo/src/a%20b.ts#L7C2", work), { start: 0, end: 0, text: "file://host/home/me/repo/src/a%20b.ts#L7C2", path: "src/a b.ts", line: 7, column: 2 });
  assert.equal(hyperlinkTarget("file:///home/me/repo/x.go:12:3", work)?.line, 12);
  assert.equal(hyperlinkTarget("file:///etc/passwd", work), undefined);
  assert.equal(hyperlinkTarget("file:///home/me/repo/../other/x", work), undefined);
  assert.equal(hyperlinkTarget("javascript:alert(1)", work), undefined);
  assert.equal(hyperlinkTarget("https://example.com", work)?.url, "https://example.com");
});
