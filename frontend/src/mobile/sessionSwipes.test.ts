import assert from "node:assert/strict";
import { test } from "node:test";
import { sessionSwipes } from "./sessionSwipes";
import type { Approval, SessionView } from "../types";

function deps(log: string[], confirmed = true) {
  return {
    decide: async (id: number) => void log.push(`decide ${id}`),
    request: async (path: string, init: { method: string; body?: object }) => void log.push(`${init.method} ${path} ${JSON.stringify(init.body ?? null)}`),
    closed: (_: SessionView, text: string) => void log.push(`undo: ${text}`),
    refresh: async () => void log.push("refresh"),
    notice: (text: string) => void log.push(`notice: ${text}`),
    confirm: () => confirmed,
  };
}

const live = { id: 3, name: "api", status: "working", ended_at: null, archived_at: null } as unknown as SessionView;

test("right approves the waiting approval; left stops and archives after asking", async () => {
  const log: string[] = [];
  const pending = { id: 9, status: "pending", tool_name: "Bash" } as Approval;
  const { left, right } = sessionSwipes(live, pending, deps(log));
  await right!.run();
  await left!.run();
  assert.deepEqual(log, [
    "decide 9", "notice: Approved Bash for “api”.", "refresh",
    'POST /sessions/3/archive {"stop":true}', "undo: Stopped and archived “api”.", "refresh",
  ]);
});

test("nothing to approve means no right swipe; a refused confirm does nothing", async () => {
  const log: string[] = [];
  const { left, right } = sessionSwipes(live, undefined, deps(log, false));
  assert.equal(right, undefined);
  await left!.run();
  assert.deepEqual(log, []);
});

test("an ended record archives without stopping anything; an archived one has no swipe", async () => {
  const log: string[] = [];
  const ended = { ...live, ended_at: 5 } as SessionView;
  await sessionSwipes(ended, undefined, deps(log)).left!.run();
  assert.equal(log[0], 'POST /sessions/3/archive {"stop":false}');
  assert.equal(sessionSwipes({ ...ended, archived_at: 6 } as SessionView, undefined, deps(log)).left, undefined);
});
