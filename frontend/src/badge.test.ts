import assert from "node:assert/strict";
import { test } from "node:test";
import { applyBadge, computeBadgeCount, needsBadge, type BadgeNavigator } from "./badge";

test("computeBadgeCount sums pending approvals and waiting sessions", () => {
  assert.equal(computeBadgeCount({ approvals: 2, waitingSessions: 3 }), 5);
  assert.equal(computeBadgeCount({ approvals: 0, waitingSessions: 0 }), 0);
});

test("computeBadgeCount never goes negative on a bad input", () => {
  assert.equal(computeBadgeCount({ approvals: -1, waitingSessions: -5 }), 0);
});

test("needsBadge is true only for kinds a person must act on", () => {
  assert.equal(needsBadge("approval"), true);
  assert.equal(needsBadge("waiting_permission"), true);
  assert.equal(needsBadge("waiting_input"), true);
  assert.equal(needsBadge("idle"), false);
  assert.equal(needsBadge("error"), false);
  assert.equal(needsBadge("compacting"), false);
  assert.equal(needsBadge(undefined), false);
});

function fakeNavigator() {
  const calls: { method: string; count?: number }[] = [];
  const nav: BadgeNavigator = {
    setAppBadge: (count?: number) => {
      calls.push({ method: "set", count });
      return Promise.resolve();
    },
    clearAppBadge: () => {
      calls.push({ method: "clear" });
      return Promise.resolve();
    },
  };
  return { nav, calls };
}

test("applyBadge sets a positive count", () => {
  const { nav, calls } = fakeNavigator();
  applyBadge(nav, 4);
  assert.deepEqual(calls, [{ method: "set", count: 4 }]);
});

test("applyBadge clears at zero rather than setting setAppBadge(0)", () => {
  const { nav, calls } = fakeNavigator();
  applyBadge(nav, 0);
  assert.deepEqual(calls, [{ method: "clear" }]);
});

test("applyBadge is a no-op, not a throw, when Badging is unsupported", () => {
  assert.doesNotThrow(() => applyBadge({}, 3));
  assert.doesNotThrow(() => applyBadge({}, 0));
});

test("applyBadge swallows a rejected setAppBadge/clearAppBadge", () => {
  const nav: BadgeNavigator = {
    setAppBadge: () => Promise.reject(new Error("denied")),
    clearAppBadge: () => Promise.reject(new Error("denied")),
  };
  assert.doesNotThrow(() => applyBadge(nav, 1));
  assert.doesNotThrow(() => applyBadge(nav, 0));
});
