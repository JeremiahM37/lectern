import { test } from "node:test";
import assert from "node:assert/strict";
import { capabilityChips, catalogMatches, groupCatalog } from "./agentCatalog";

const grok = { name: "grok", command: "grok", display_name: "Grok CLI", vendor: "xAI", group: "Popular" };
const devin = { name: "devin", command: "devin", display_name: "Devin CLI", vendor: "Cognition", group: "Vendor agents" };
const kiro = { name: "kiro", command: "kiro-cli", display_name: "Kiro CLI", vendor: "AWS", group: "Vendor agents" };
const entries = [grok, devin, kiro,
  { name: "pi", command: "pi", display_name: "Pi", vendor: "pi.dev", group: "Open source & community" },
  { name: "codex-acp", command: "codex-acp", display_name: "Codex (ACP)", vendor: "Zed", group: "ACP adapters" },
];

test("an empty search lists every group in server order", () => {
  const sections = groupCatalog(entries, "");
  assert.deepEqual(
    sections.map((s) => s.group),
    ["Popular", "Vendor agents", "Open source & community", "ACP adapters"],
  );
  assert.deepEqual(sections[1]?.items.map((e) => e.name), ["devin", "kiro"]);
});

test("search matches product, binary and vendor, all words required", () => {
  assert.equal(catalogMatches(kiro, "kiro-cli"), true);
  assert.equal(catalogMatches(kiro, "aws"), true);
  assert.equal(catalogMatches(devin, "cognition devin"), true);
  assert.equal(catalogMatches(devin, "cognition grok"), false);
  const sections = groupCatalog(entries, "xai");
  assert.deepEqual(sections.map((s) => s.items.map((e) => e.name)), [["grok"]]);
  assert.deepEqual(groupCatalog(entries, "nothing-like-this"), []);
});

test("capability chips keep a fixed order and carry the server's reason", () => {
  const chips = capabilityChips({
    ...grok,
    capabilities: {
      yolo: { available: true },
      resume: { available: false, reason: "Resume isn't available for x" },
      mcp: { available: true, reason: "over ACP only" },
    },
  });
  assert.deepEqual(chips.map((c) => [c.label, c.available]), [
    ["Resume", false],
    ["Auto-approve", true],
    ["MCP", true],
  ]);
  assert.equal(chips[0]?.title, "Resume isn't available for x");
  assert.equal(chips[2]?.title, "over ACP only");
});
