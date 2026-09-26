import test from "node:test";
import assert from "node:assert/strict";
import {
  claudeDesktopConfig,
  cursorDeepLink,
  vscodeDeepLink,
  fillOrigin,
  formatAgo,
  statusLine,
  LECTERN_API_PLACEHOLDER,
  type MCPClientInfo,
} from "./ConnectTools";

test("fillOrigin substitutes every occurrence of the placeholder", () => {
  const template = `claude mcp add --scope user lectern -e LECTERN_API=${LECTERN_API_PLACEHOLDER} -- lectern mcp`;
  assert.equal(
    fillOrigin(template, "https://aiserver.tail878d9e.ts.net"),
    "claude mcp add --scope user lectern -e LECTERN_API=https://aiserver.tail878d9e.ts.net -- lectern mcp",
  );
});

test("claudeDesktopConfig: local uses the absolute lectern path with no env", () => {
  const parsed = JSON.parse(claudeDesktopConfig("/opt/lectern/lectern", false, "https://example.invalid"));
  assert.deepEqual(parsed, {
    mcpServers: { lectern: { command: "/opt/lectern/lectern", args: ["mcp"] } },
  });
});

test("claudeDesktopConfig: remote uses the bare binary name and LECTERN_API env", () => {
  const parsed = JSON.parse(claudeDesktopConfig("/opt/lectern/lectern", true, "https://example.invalid"));
  assert.deepEqual(parsed, {
    mcpServers: {
      lectern: { command: "lectern", args: ["mcp"], env: { LECTERN_API: "https://example.invalid" } },
    },
  });
});

test("cursorDeepLink: local config is base64 of the plain command", () => {
  const link = cursorDeepLink("/opt/lectern/lectern", false, "https://example.invalid");
  assert.ok(link.startsWith("cursor://anysphere.cursor-deeplink/mcp/install?name=lectern&config="));
  const encoded = link.split("config=")[1]!;
  const decoded = JSON.parse(atob(encoded)) as unknown;
  assert.deepEqual(decoded, { command: "/opt/lectern/lectern", args: ["mcp"] });
});

test("cursorDeepLink: remote config carries LECTERN_API and no absolute path", () => {
  const link = cursorDeepLink("/opt/lectern/lectern", true, "https://example.invalid");
  const encoded = link.split("config=")[1]!;
  const decoded = JSON.parse(atob(encoded)) as unknown;
  assert.deepEqual(decoded, {
    command: "lectern",
    args: ["mcp"],
    env: { LECTERN_API: "https://example.invalid" },
  });
});

test("vscodeDeepLink: URL-encodes a JSON body naming the server", () => {
  const link = vscodeDeepLink("/opt/lectern/lectern", false, "https://example.invalid");
  assert.ok(link.startsWith("vscode:mcp/install?"));
  const decoded = JSON.parse(decodeURIComponent(link.slice("vscode:mcp/install?".length))) as unknown;
  assert.deepEqual(decoded, { name: "lectern", command: "/opt/lectern/lectern", args: ["mcp"] });
});

test("vscodeDeepLink: remote variant has no absolute path and carries env", () => {
  const link = vscodeDeepLink("/opt/lectern/lectern", true, "https://example.invalid");
  const decoded = JSON.parse(decodeURIComponent(link.slice("vscode:mcp/install?".length))) as unknown;
  assert.deepEqual(decoded, {
    name: "lectern",
    command: "lectern",
    args: ["mcp"],
    env: { LECTERN_API: "https://example.invalid" },
  });
});

test("formatAgo renders coarser units as the gap grows", () => {
  const now = 1_700_000_000_000;
  assert.equal(formatAgo(now / 1000 - 10, now), "just now");
  assert.equal(formatAgo(now / 1000 - 120, now), "2 min ago");
  assert.equal(formatAgo(now / 1000 - 3 * 3600, now), "3h ago");
  assert.equal(formatAgo(now / 1000 - 3 * 86400, now), "3d ago");
});

function baseClient(): MCPClientInfo {
  return {
    id: "claude-code",
    name: "Claude Code (CLI)",
    installed: false,
    detail: "Not connected yet",
    can_install: true,
  };
}

test("statusLine prefers a recorded connection over installed/detail", () => {
  const c = { ...baseClient(), last_seen: { name: "claude-code", version: "1.0", at: Date.now() / 1000 } };
  assert.match(statusLine(c), /^✓ Claude Code \(CLI\) connected · used /);
});

test("statusLine falls back to installed status, then to the CLI's own detail", () => {
  assert.equal(statusLine({ ...baseClient(), installed: true, detail: "Connected" }), "✓ Connected");
  assert.equal(statusLine({ ...baseClient(), installed: false, detail: "Not connected yet" }), "Not connected yet");
  assert.equal(
    statusLine({ ...baseClient(), installed: null, detail: "claude is not on this server's PATH" }),
    "claude is not on this server's PATH",
  );
});
