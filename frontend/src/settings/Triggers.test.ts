import test from "node:test";
import assert from "node:assert/strict";
import { timeAgo } from "./Triggers";

test("timeAgo reads a source's last poll relative to now", () => {
  const now = Date.now() / 1000;
  assert.equal(timeAgo(undefined), "never");
  assert.equal(timeAgo(now - 5), "just now");
  assert.equal(timeAgo(now - 130), "2m ago");
  assert.equal(timeAgo(now - 3 * 3600), "3h ago");
  assert.equal(timeAgo(now - 2 * 86400), "2d ago");
});
