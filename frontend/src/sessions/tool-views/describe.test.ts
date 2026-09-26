import assert from "node:assert/strict";
import { test } from "node:test";
import { describeTool, formatMCPTitle } from "./describe";

test("Read/Edit/Write show the file path as the title, not the tool name", () => {
  assert.equal(describeTool("Read", { file_path: "/a/b.py" }).title, "/a/b.py");
  assert.equal(describeTool("Edit", { file_path: "/a/b.py" }).title, "/a/b.py");
  assert.equal(describeTool("Write", { file_path: "/a/b.py" }).title, "/a/b.py");
  assert.equal(describeTool("Read", {}).category, "read");
  assert.equal(describeTool("Edit", {}).category, "edit");
});

test("MultiEdit shows the edit count only when there is more than one", () => {
  const one = describeTool("MultiEdit", { file_path: "/x", edits: [{}] });
  assert.equal(one.subtitle, undefined);
  const many = describeTool("MultiEdit", { file_path: "/x", edits: [{}, {}, {}] });
  assert.equal(many.subtitle, "3 edits");
});

test("Bash shows the command as the subtitle and truncates a long one", () => {
  const short = describeTool("Bash", { command: "git status" });
  assert.equal(short.title, "Terminal");
  assert.equal(short.subtitle, "git status");
  const long = describeTool("Bash", { command: "x".repeat(200) });
  assert.ok(long.subtitle!.length <= 140);
  assert.ok(long.subtitle!.endsWith("…"));
});

test("exec_command (Codex) puts the command in the title, joining an argv array", () => {
  const summary = describeTool("exec_command", { command: ["ls", "-la"] });
  assert.equal(summary.title, "ls -la");
  assert.equal(summary.category, "terminal");
});

test("Grep/Glob surface the pattern", () => {
  assert.equal(describeTool("Grep", { pattern: "TODO" }).subtitle, "pattern: TODO");
  assert.equal(describeTool("Glob", { pattern: "**/*.go" }).title, "**/*.go");
});

test("WebFetch shows just the hostname; an invalid URL falls back to the raw string", () => {
  assert.equal(describeTool("WebFetch", { url: "https://example.com/a/b" }).title, "example.com");
  assert.equal(describeTool("WebFetch", { url: "not a url" }).title, "not a url");
});

test("TodoWrite counts items", () => {
  assert.equal(describeTool("TodoWrite", { todos: [{}, {}] }).subtitle, "2 items");
  assert.equal(describeTool("TodoWrite", { todos: [{}] }).subtitle, "1 item");
});

test("Task/Agent use the description when present", () => {
  assert.equal(describeTool("Task", { description: "Refactor the parser" }).title, "Refactor the parser");
  assert.equal(describeTool("Agent", {}).title, "Subagent task");
});

test("MCP tool names format as server · tool, never the raw mcp__ wire name", () => {
  assert.equal(formatMCPTitle("mcp__homelab__search_books_download"), "homelab · search_books_download");
  assert.equal(describeTool("mcp__grimoire__get_fact", {}).category, "mcp");
  assert.equal(describeTool("mcp__grimoire__get_fact", {}).title, "grimoire · get_fact");
});

test("an unknown tool falls back to its bare name and category 'other', never a blob", () => {
  const summary = describeTool("SomeFutureTool", { anything: "goes here" });
  assert.equal(summary.title, "SomeFutureTool");
  assert.equal(summary.category, "other");
  assert.equal(summary.subtitle, undefined);
});
