import assert from "node:assert/strict";
import { test } from "node:test";
import {
  actionURL,
  buildNotificationPlan,
  confirmationNotification,
  decisionForAction,
  decisionRequestInit,
  decisionURL,
} from "./sw-actions";

test("an approval push gets Approve/Deny action buttons and the approval id", () => {
  const plan = buildNotificationPlan({
    title: "Needs permission",
    body: "demo: Bash rm -rf /tmp",
    url: "/session/7",
    kind: "approval",
    approval_id: 42,
    session_id: 7,
  });
  assert.equal(plan.title, "Needs permission");
  assert.deepEqual(
    plan.options.actions.map((a) => a.action),
    ["approve", "deny"],
  );
  assert.equal(plan.options.data.approvalId, 42);
  assert.equal(plan.options.data.sessionId, 7);
  assert.equal(plan.options.data.url, "/session/7");
  // Approvals get their own, more insistent pattern.
  assert.deepEqual(plan.options.vibrate, [200, 80, 200, 80, 200]);
  assert.equal(plan.options.tag, "approval-42");
  assert.equal(plan.options.renotify, true);
});

test("a session waiting for permission or input offers terminal/reply, not Approve/Deny", () => {
  for (const kind of ["waiting_permission", "waiting_input"]) {
    const plan = buildNotificationPlan({ title: "Waiting", body: "…", kind, session_id: 9 });
    assert.deepEqual(
      plan.options.actions.map((a) => a.action),
      ["terminal", "reply"],
      kind,
    );
    assert.equal(plan.options.tag, "session-9", kind);
  }
});

test("a waiting push with no session id offers no actions at all", () => {
  const plan = buildNotificationPlan({ kind: "waiting_input" });
  assert.deepEqual(plan.options.actions, []);
});

test("a non-approval, non-waiting alert carries no action buttons", () => {
  const plan = buildNotificationPlan({ title: "Finished", body: "demo finished", url: "/session/7", kind: "idle", session_id: 7 });
  assert.deepEqual(plan.options.actions, []);
  assert.equal(plan.options.data.approvalId, undefined);
  assert.equal(plan.options.tag, "session-7");
});

test("two pushes about the same session share a tag so the second replaces the first", () => {
  const first = buildNotificationPlan({ kind: "waiting_permission", session_id: 3 });
  const second = buildNotificationPlan({ kind: "idle", session_id: 3 });
  assert.equal(first.options.tag, second.options.tag);
  assert.ok(first.options.renotify && second.options.renotify);
});

test("a broadcast with neither a session nor an approval groups by kind, not into one bucket", () => {
  const failed = buildNotificationPlan({ title: "Check failed", kind: "check" });
  const passed = buildNotificationPlan({ title: "Check passed", kind: "check" });
  const other = buildNotificationPlan({ title: "Something else", kind: "other" });
  assert.equal(failed.options.tag, passed.options.tag);
  assert.notEqual(failed.options.tag, other.options.tag);
});

test("a push with no data at all still renders a usable notification", () => {
  const plan = buildNotificationPlan({});
  assert.equal(plan.title, "lectern");
  assert.equal(plan.options.data.url, "/");
  assert.deepEqual(plan.options.actions, []);
  assert.equal(plan.options.tag, "kind-general");
});

test("actionURL builds a hash the router understands, ignoring the raw push path", () => {
  assert.equal(actionURL("terminal", 12), "/#session/12/terminal");
  assert.equal(actionURL("reply", 12), "/#session/12/reply");
});

test("decisionForAction maps the two action ids and nothing else", () => {
  assert.equal(decisionForAction("approve"), "approved");
  assert.equal(decisionForAction("deny"), "denied");
  assert.equal(decisionForAction(""), null, "a body tap (empty action) is not a decision");
  assert.equal(decisionForAction("open"), null);
});

test("decisionURL and decisionRequestInit build a same-origin, credential-free POST", () => {
  assert.equal(decisionURL(42), "/api/approvals/42/decision");
  const init = decisionRequestInit("approved");
  assert.equal(init.method, "POST");
  assert.equal(init.credentials, "same-origin");
  assert.equal(init.body, JSON.stringify({ decision: "approved" }));
  const headers = init.headers as Record<string, string>;
  assert.equal(headers["Content-Type"], "application/json");
  assert.equal("Authorization" in headers, false, "the phone is authenticated by identity, not a bearer token");
});

test("confirmationNotification reports success and failure distinctly", () => {
  const approved = confirmationNotification("approved", true);
  assert.equal(approved.title, "Approved");
  const deniedOk = confirmationNotification("denied", true);
  assert.equal(deniedOk.title, "Denied");
  const failed = confirmationNotification("approved", false);
  assert.match(failed.title.toLowerCase(), /could not approve/);
  assert.match(failed.body, /open lectern/i);
});
