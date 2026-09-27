import assert from "node:assert/strict";
import { test } from "node:test";
import { dragAxis, pullOffset, PULL_TRIGGER, swipeCommits, swipeOffset } from "./gestures";

test("the pull follows at half speed and resists past the trigger", () => {
  assert.equal(pullOffset(-20), 0);
  assert.equal(pullOffset(80), 40);
  assert.equal(pullOffset(PULL_TRIGGER * 2), PULL_TRIGGER);
  assert.ok(pullOffset(400) < 200 && pullOffset(400) > PULL_TRIGGER);
});

test("a mostly vertical drag is a scroll, never a swipe", () => {
  assert.equal(dragAxis(4, 3), undefined);
  assert.equal(dragAxis(30, 5), "x");
  assert.equal(dragAxis(20, 18), "y");
  assert.equal(dragAxis(2, 40), "y");
});

test("a swipe acts past 30% of the card or on a flick", () => {
  assert.equal(swipeCommits(110, 360, 0), "right");
  assert.equal(swipeCommits(-120, 360, 0), "left");
  assert.equal(swipeCommits(60, 360, 0.2), undefined);
  assert.equal(swipeCommits(-50, 360, -1.2), "left");
  assert.equal(swipeCommits(30, 360, 3), undefined);
});

test("a card resists toward a side with no action", () => {
  assert.equal(swipeOffset(60, true, false, 360), 10);
  assert.equal(swipeOffset(-60, true, false, 360), -60);
  assert.ok(swipeOffset(-300, true, true, 360) > -300);
});
