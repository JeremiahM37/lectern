import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { editorLink } from "./editor";
import { availableAgents, raceAgents } from "./race";

const ssh = { kind: "ssh", host: "10.0.0.5", user: "dev", port: 22, ssh_json: "{}" };

describe("editor links", () => {
  it("opens an SSH machine through the editor's remote support", () => {
    assert.equal(editorLink("vscode", ssh, "/srv/my app"), "vscode://vscode-remote/ssh-remote+dev%4010.0.0.5/srv/my%20app");
    assert.equal(editorLink("cursor", { ...ssh, ssh_json: '{"alias":"build-box"}' }, "/w"), "cursor://vscode-remote/ssh-remote+build-box/w");
    assert.equal(editorLink("zed", { ...ssh, port: 2222 }, "/w"), "zed://ssh/dev@10.0.0.5:2222/w");
  });
  it("uses a file link for this server and an editor host when set", () => {
    const local = { kind: "local", host: "", user: "", port: 0, ssh_json: "{}" };
    assert.equal(editorLink("vscode", local, "/home/a/x"), "vscode://file/home/a/x");
    assert.equal(editorLink("zed", local, "/x"), "zed://file/x");
    assert.equal(editorLink("vscode", { ...local, ssh_json: '{"editor_host":"aiserver"}' }, "/x"), "vscode://vscode-remote/ssh-remote+aiserver/x");
  });
  it("refuses what an editor cannot reach", () => {
    assert.equal(editorLink("vscode", { kind: "pct", host: "101", user: "", port: 0, ssh_json: "{}" }, "/x"), null);
    assert.equal(editorLink("vscode", ssh, "relative/path"), null);
  });
});

describe("race", () => {
  it("mixes the available agents, repeating when there are fewer", () => {
    assert.deepEqual(raceAgents(3, "claude", ["claude", "codex", "gemini"], "mixed"), ["claude", "codex", "gemini"]);
    assert.deepEqual(raceAgents(3, "codex", ["claude", "codex"], "mixed"), ["codex", "claude", "codex"]);
    assert.deepEqual(raceAgents(3, "claude", [], "mixed"), ["claude", "claude", "claude"]);
    assert.deepEqual(raceAgents(2, "gemini", ["claude"], "same"), ["gemini", "gemini"]);
    assert.equal(raceAgents(20, "claude", [], "same").length, 8);
  });
  it("reads a machine's probe", () => {
    assert.deepEqual(availableAgents('{"claude":"2.1","codex":null,"gemini":"0.9"}'), ["claude", "gemini"]);
    assert.deepEqual(availableAgents("not json"), []);
  });
});
