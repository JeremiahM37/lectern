import assert from "node:assert/strict";
import { test } from "node:test";
import { nowItems, sessionNowState } from "./now-strip";
import type { SessionView } from "../types";

function session(over: Partial<SessionView>): SessionView {
  return {
    setup_cancel_requested: false,
    setup_state: undefined,
    setup_error: "",
    archived_at: null,
    group_path: "",
    id: 1,
    project_id: null,
    target_id: 1,
    name: "demo",
    agent: "claude",
    model: "",
    workdir: "/",
    tmux_session: "",
    status: "running",
    origin: "tracked",
    pane_tail: "",
    context_pct: null,
    last_activity_at: null,
    created_at: 0,
    updated_at: 0,
    ended_at: null,
    launch_profile: undefined,
    can_restore: true,
    idle_seconds: 0,
    uptime_seconds: 0,
    handoff_in_flight: false,
    wraps: 0,
    ...over,
  } as SessionView;
}

test("the strip uses the four shared states", () => {
  assert.equal(sessionNowState(session({ setup_state: "failed", status: "starting" })), "ended");
  assert.equal(sessionNowState(session({ status: "dead" })), "ended");
  // An agent at its prompt is idle, not "needs you".
  assert.equal(sessionNowState(session({ status: "waiting" })), "idle");
  assert.equal(sessionNowState(session({ status: "running" })), "working");
  assert.equal(sessionNowState(session({ status: "starting" })), "working");
  assert.equal(sessionNowState(session({ status: "waiting", agent_exited_at: 1 })), "ended");
  assert.equal(sessionNowState(session({ status: "running", state: "needs_you", state_reason: "approval" })), "needs_you");
});

test("nowItems excludes archived sessions", () => {
  const rows = [session({ id: 1, archived_at: 12345 }), session({ id: 2, archived_at: null })];
  assert.deepEqual(
    nowItems(rows).map((row) => row.id),
    [2],
  );
});

test("nowItems ranks needs you first, then working, idle and ended", () => {
  const rows = [
    session({ id: 1, status: "dead", name: "done" }),
    session({ id: 2, status: "running", name: "working" }),
    session({ id: 3, status: "waiting", name: "idle" }),
    session({ id: 4, status: "running", state: "needs_you", state_reason: "approval", name: "blocked" }),
  ];
  assert.deepEqual(
    nowItems(rows).map((row) => row.state),
    ["needs_you", "working", "idle", "ended"],
  );
});
