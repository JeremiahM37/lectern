import assert from "node:assert/strict";
import { test } from "node:test";
import {
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
  });
  assert.equal(plan.title, "Needs permission");
  assert.deepEqual(
    plan.options.actions.map((a) => a.action),
    ["approve", "deny"],
  );
  assert.equal(plan.options.data.approvalId, 42);
  assert.equal(plan.options.data.url, "/session/7");
});

test("a non-approval alert carries no action buttons", () => {
  const plan = buildNotificationPlan({ title: "Finished", body: "demo finished", url: "/session/7", kind: "idle" });
  assert.deepEqual(plan.options.actions, []);
  assert.equal(plan.options.data.approvalId, undefined);
});

test("a push with no data at all still renders a usable notification", () => {
  const plan = buildNotificationPlan({});
  assert.equal(plan.title, "lectern");
  assert.equal(plan.options.data.url, "/");
  assert.deepEqual(plan.options.actions, []);
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
