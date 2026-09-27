import assert from "node:assert/strict";
import { test } from "node:test";
import { commandsFor } from "./commands";
import { resetPrefsForTest } from "../prefs/store";
import { pluginTheme, safeHref, setPluginContributionsForTest } from "../plugins/contributions";

test("plugin quick commands follow the person's own, and respect the plugin's scope", () => {
  resetPrefsForTest({ "quick-commands": [{ id: "g", text: "continue" }] }, async () => null as never);
  setPluginContributionsForTest({
    quick_commands: [
      { id: "acme.x/all", plugin: "acme.x", label: "All", text: "everywhere", enter: true, project_ids: [] },
      { id: "acme.x/four", plugin: "acme.x", label: "Four", text: "only four", enter: false, project_ids: [4] },
    ],
    themes: [],
    palette_commands: [],
  });
  const four = commandsFor(4).map((row) => [row.command.text, row.plugin || "", row.command.id]);
  assert.deepEqual(four, [["continue", "", "g"], ["everywhere", "acme.x", "plugin:acme.x/all"], ["only four", "acme.x", "plugin:acme.x/four"]]);
  assert.deepEqual(commandsFor(5).map((row) => row.command.text), ["continue", "everywhere"]);
  assert.equal(commandsFor(4).at(-1)!.command.enter, false);
  setPluginContributionsForTest({ quick_commands: [], themes: [], palette_commands: [] });
  assert.deepEqual(commandsFor(4).map((row) => row.command.text), ["continue"]);
});

test("a palette command opens only a Lectern view or an https page", () => {
  assert.equal(safeHref("#settings/plugins"), "#settings/plugins");
  assert.equal(safeHref("https://example.com/x"), "https://example.com/x");
  for (const bad of ["javascript:alert(1)", "http://example.com", "data:text/html,x", "#<img src=x>", "//evil.example"]) assert.equal(safeHref(bad), "", bad);
});

test("a plugin theme is found by its id, and an unknown one is ignored", () => {
  setPluginContributionsForTest({ quick_commands: [], themes: [{ id: "acme.x/forest", plugin: "acme.x", name: "Forest", accent: "#2f855a" }], palette_commands: [] });
  assert.equal(pluginTheme("acme.x/forest")?.name, "Forest");
  assert.equal(pluginTheme("acme.x/gone"), undefined);
  assert.equal(pluginTheme(""), undefined);
});
