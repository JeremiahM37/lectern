import { test } from "node:test";
import assert from "node:assert/strict";
import {
  splitAgentMenu,
  fetchAgentMenu,
  saveAgentMenu,
  type AgentMenuApi,
} from "./menu";

function fakeApi(handlers: Record<string, unknown>): AgentMenuApi {
  return {
    async request<T>(path: string, opts?: { method?: string; body?: unknown }) {
      const key = `${opts?.method || "GET"} ${path}`;
      const handler = handlers[key];
      if (handler === undefined) throw new Error(`unexpected request ${key}`);
      if (handler instanceof Error) throw handler;
      return handler as T;
    },
  };
}

test("splitAgentMenu shows agents in the saved order, then everything else", () => {
  const all = [{ name: "aider" }, { name: "claude" }, { name: "codex" }, { name: "gemini" }];
  const { shown, more } = splitAgentMenu(all, ["codex", "claude"]);
  assert.deepEqual(shown.map((a) => a.name), ["codex", "claude"]);
  assert.deepEqual(more.map((a) => a.name), ["aider", "gemini"]);
});

test("splitAgentMenu drops a shown name that no longer exists in the registry", () => {
  const all = [{ name: "claude" }, { name: "codex" }];
  const { shown, more } = splitAgentMenu(all, ["removed-agent", "codex"]);
  assert.deepEqual(shown.map((a) => a.name), ["codex"]);
  assert.deepEqual(more.map((a) => a.name), ["claude"]);
});

test("splitAgentMenu treats an empty shown list as 'show everything' rather than hiding all agents", () => {
  const all = [{ name: "claude" }, { name: "codex" }, { name: "aider" }];
  const { shown, more } = splitAgentMenu(all, []);
  assert.deepEqual(shown.map((a) => a.name), ["claude", "codex", "aider"]);
  assert.deepEqual(more, []);
});

test("splitAgentMenu falls back to showing everything when every saved name is now unknown", () => {
  const all = [{ name: "claude" }, { name: "codex" }];
  const { shown, more } = splitAgentMenu(all, ["gone-1", "gone-2"]);
  assert.deepEqual(shown.map((a) => a.name), ["claude", "codex"]);
  assert.deepEqual(more, []);
});

test("fetchAgentMenu reads the saved agent list", async () => {
  const api = fakeApi({ "GET /agents/menu": { agents: ["codex", "claude"] } });
  assert.deepEqual(await fetchAgentMenu(api), ["codex", "claude"]);
});

test("fetchAgentMenu degrades to an empty list rather than throwing", async () => {
  const api = fakeApi({ "GET /agents/menu": new Error("boom") });
  assert.deepEqual(await fetchAgentMenu(api), []);
});

test("fetchAgentMenu tolerates a malformed response shape", async () => {
  const api = fakeApi({ "GET /agents/menu": {} });
  assert.deepEqual(await fetchAgentMenu(api), []);
});

test("saveAgentMenu round-trips the order it was given", async () => {
  let sentBody: unknown;
  const api: AgentMenuApi = {
    async request<T>(path: string, opts?: { method?: string; body?: unknown }) {
      assert.equal(path, "/agents/menu");
      assert.equal(opts?.method, "PUT");
      sentBody = opts?.body;
      return { agents: ["aider", "claude"] } as T;
    },
  };
  const got = await saveAgentMenu(api, ["aider", "claude"]);
  assert.deepEqual(got, ["aider", "claude"]);
  assert.deepEqual(sentBody, { agents: ["aider", "claude"] });
});
