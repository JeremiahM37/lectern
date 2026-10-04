import { test } from "node:test";
import assert from "node:assert/strict";
import { paneSummary } from "./paneSummary";

test("drops Claude Code's prompt box and status line", () => {
  const tail = [
    "     (ctrl+b ctrl+b (twice) to run in background)",
    "· Ionizing… (10m 38s · ↓ 34.7k tokens)",
    "                                                  ✔ Update installed · Restart to update",
    "──────────────────────────────────────────",
    "❯ ",
    "──────────────────────────────────────────",
    "  Opus 5.5 (1M context) · 17% ctx · $184.21",
    "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
  ].join("\n");
  assert.equal(paneSummary(tail), "     (ctrl+b ctrl+b (twice) to run in background)\n· Ionizing… (10m 38s · ↓ 34.7k tokens)");
});

test("drops Codex's prompt and footer", () => {
  const tail = [
    "  • All CI jobs green before merge.",
    "  Worked for 6m 29s · 1:18 PM",
    "                                                  Tip: Press ctrl+r to search previously entered prompts.",
    "› Ask Codex to do anything",
    "  GPT-6-Astra medium · ~/agentdeck-scratch/shell-1 · Fix Librarr issue",
    "  ← for agents · ? for shortcuts",
  ].join("\n");
  assert.equal(paneSummary(tail), "• All CI jobs green before merge.\nWorked for 6m 29s · 1:18 PM");
});

test("keeps a plain shell untouched", () => {
  assert.equal(paneSummary("$ ls\nREADME.md\n$ "), "$ ls\nREADME.md\n$");
});
