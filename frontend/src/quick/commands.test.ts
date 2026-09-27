import assert from "node:assert/strict";
import { test } from "node:test";
import { cleanQuickCommands, commandBytes, commandsFor, readGlobal, writeScope } from "./commands";
import { flushPrefs, resetPrefsForTest } from "../prefs/store";

test("stored commands are validated and bounded", () => {
  const rows = cleanQuickCommands([{ text: "y" }, { text: "" }, null, { text: "npm test", enter: false, label: "Tests", id: "a" }, ...Array(80).fill({ text: "x" })]);
  assert.equal(rows.length, 60);
  assert.deepEqual(rows[1], { id: "a", label: "Tests", text: "npm test", enter: false });
  assert.equal(rows[0]!.enter, true);
  assert.deepEqual(cleanQuickCommands("junk"), []);
});

test("a project's commands come before everyone's, and scopes stay apart", () => {
  resetPrefsForTest({ "quick-commands": [{ id: "g", text: "continue" }], "quick-commands:project:4": [{ id: "p", text: "make test" }] }, async () => null as never);
  const four = commandsFor(4).map((row) => [row.scope.kind, row.command.text]);
  assert.deepEqual(four, [["project", "make test"], ["global", "continue"]]);
  assert.deepEqual(commandsFor(5).map((row) => row.command.text), ["continue"]);
  assert.deepEqual(commandsFor(null).map((row) => row.command.text), ["continue"]);
});

test("writes go to the scope's own key, and an emptied list stays empty", () => {
  const sent: [string, unknown][] = [];
  resetPrefsForTest({}, (async (path: string, options: { body?: unknown }) => { sent.push([path, options.body]); return null; }) as never);
  writeScope({ kind: "project", id: 9 }, [{ id: "a", label: "", text: "ls", enter: true }]);
  writeScope({ kind: "global" }, []);
  flushPrefs();
  assert.deepEqual(sent.map(([path]) => path).sort(), ["/ui/prefs/quick-commands", "/ui/prefs/quick-commands%3Aproject%3A9"]);
  assert.deepEqual(readGlobal(), []);
  assert.equal(commandBytes({ id: "", label: "", text: "y", enter: true }), "y\r");
});
