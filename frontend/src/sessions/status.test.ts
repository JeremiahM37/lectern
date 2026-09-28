import assert from "node:assert/strict";
import { test } from "node:test";
import { computeState, sessionState, stateText } from "./status";
import type { SessionView } from "../types";

const row = (over: Partial<SessionView>): SessionView => ({ id: 1, status: "running", ended_at: null, archived_at: null, ...over }) as SessionView;

test("an idle prompt is idle and a held approval needs you", () => {
  assert.deepEqual(computeState(row({ status: "waiting", agent_state: "waiting_input" })), { state: "idle", reason: "" });
  assert.deepEqual(computeState(row({ status: "running" }), true), { state: "needs_you", reason: "approval" });
  assert.deepEqual(computeState(row({ status: "waiting", agent_state: "waiting_permission" })), { state: "needs_you", reason: "permission_prompt" });
  assert.deepEqual(computeState(row({ status: "dead", ended_at: 5 }), true), { state: "ended", reason: "" });
  assert.deepEqual(computeState(row({ agent_exited_at: 3 })), { state: "ended", reason: "agent_exited" });
  assert.deepEqual(computeState(row({ setup_state: "creating" })), { state: "working", reason: "setting_up" });
});

test("the server's word wins, with this page's approvals applied on top", () => {
  const idle = row({ status: "waiting", state: "idle" });
  assert.equal(sessionState(idle).state, "idle");
  assert.equal(sessionState(idle, true).state, "needs_you");
  const held = row({ status: "running", state: "needs_you", state_reason: "approval" });
  assert.equal(sessionState(held, false).state, "working");
  assert.equal(sessionState(row({ state: "ended", status: "dead", ended_at: 1 }), true).state, "ended");
});

test("the words", () => {
  assert.equal(stateText({ state: "ended", reason: "agent_exited" }), "Ended · agent exited");
  assert.equal(stateText({ state: "needs_you", reason: "approval" }), "Needs you");
  assert.equal(stateText({ state: "working", reason: "" }), "Working");
});
