import assert from "node:assert/strict";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { FileLinksContext, Markdown, type FileLinks } from "./markdown";

const links = (have: string[]): FileLinks => ({ workdir: "/home/me/repo", exists: (path) => have.includes(path), open: () => {} });
const render = (text: string, value: FileLinks | null) =>
  renderToStaticMarkup(createElement(FileLinksContext.Provider, { value }, createElement(Markdown, { text })));

test("paths, file links and addresses in agent messages become links", () => {
  const html = render(
    "Wrote /home/admin/.formwork/report.pdf and src/app.ts:12; see [the plan](docs/plan.md), " +
      "[résumé](file:///home/admin/R%C3%A9sum%C3%A9.pdf), `internal/api/server.go` and https://example.com/x.",
    links(["src/app.ts", "internal/api/server.go"]),
  );
  for (const path of ["/home/admin/.formwork/report.pdf", "src/app.ts", "docs/plan.md", "/home/admin/Résumé.pdf", "internal/api/server.go"])
    assert.ok(html.includes(`data-path="${path}"`), `${path} is not a link: ${html}`);
  assert.match(html, /href="https:\/\/example\.com\/x"/);
  assert.ok(!html.includes('href="https://example.com/x."'), "a sentence's full stop is not part of the address");
});

test("a bare name is a link only if the workspace has it", () => {
  assert.ok(render("see README.md", links(["README.md"])).includes('data-path="README.md"'));
  assert.ok(!render("see notes.md", links([])).includes("data-path"));
});

test("without a workspace, and in code blocks, text stays text", () => {
  assert.ok(!render("Wrote /tmp/x.pdf", null).includes("<a"));
  assert.ok(!render("```\ncat /tmp/x.pdf\n```", links([])).includes("<a"));
  // Never a script link from agent text.
  assert.ok(!render("[x](javascript:alert(1))", links([])).includes('href="javascript'));
});
