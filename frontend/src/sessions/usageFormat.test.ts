import test from "node:test";
import assert from "node:assert/strict";
import {
  contextClass,
  clampPct,
  formatTokens,
  formatCost,
  formatLineDelta,
  formatCountdown,
  quotaClass,
  isStale,
  formatAge,
  resultUsage,
} from "./usageFormat";

test("contextClass thresholds: amber at 70, red at 85 (task contract)", () => {
  assert.equal(contextClass(0), "");
  assert.equal(contextClass(69), "");
  assert.equal(contextClass(70), "ctx-amber");
  assert.equal(contextClass(84), "ctx-amber");
  assert.equal(contextClass(85), "ctx-red");
  assert.equal(contextClass(100), "ctx-red");
});

test("quotaClass mirrors the same thresholds for the 5h/7d chip", () => {
  assert.equal(quotaClass(69), "");
  assert.equal(quotaClass(70), "quota-amber");
  assert.equal(quotaClass(85), "quota-red");
});

test("clampPct bounds a reading to 0-100", () => {
  assert.equal(clampPct(-5), 0);
  assert.equal(clampPct(150), 100);
  assert.equal(clampPct(42), 42);
});

test("formatTokens: bare below 1000, k-suffixed above, no trailing .0", () => {
  assert.equal(formatTokens(0), "0");
  assert.equal(formatTokens(999), "999");
  assert.equal(formatTokens(1000), "1k");
  assert.equal(formatTokens(1234), "1.2k");
  assert.equal(formatTokens(36451), "36.5k");
  assert.equal(formatTokens(null), "");
  assert.equal(formatTokens(undefined), "");
});

test("formatCost: 3 decimals under a cent, 2 decimals at or above", () => {
  assert.equal(formatCost(0.0034), "$0.003");
  assert.equal(formatCost(0.11431480000000001), "$0.11");
  assert.equal(formatCost(1.5), "$1.50");
  assert.equal(formatCost(0), "$0.00");
  assert.equal(formatCost(null), "");
});

test("formatLineDelta omits a zero side and uses a minus sign", () => {
  assert.equal(formatLineDelta(12, 0), "+12");
  assert.equal(formatLineDelta(0, 3), "−3");
  assert.equal(formatLineDelta(12, 3), "+12 −3");
  assert.equal(formatLineDelta(0, 0), "");
  assert.equal(formatLineDelta(null, null), "");
});

test("formatCountdown renders hours+minutes, minutes-only, or 'resets now'", () => {
  const now = 1_000_000;
  assert.equal(formatCountdown(now + 3 * 3600 + 12 * 60, now), "resets in 3h 12m");
  assert.equal(formatCountdown(now + 5 * 60, now), "resets in 5m");
  assert.equal(formatCountdown(now - 10, now), "resets now");
  assert.equal(formatCountdown(0, now), "");
  assert.equal(formatCountdown(null, now), "");
});

test("isStale: past the 30-minute default threshold only", () => {
  const now = 1_000_000;
  assert.equal(isStale(now - 1000, now), false);
  assert.equal(isStale(now - 1801, now), true);
  assert.equal(isStale(null, now), false);
});

test("resultUsage reads Claude's nested usage + modelUsage-derived context fields", () => {
  const u = resultUsage({
    cost_usd: 0.11,
    context_tokens: 36451,
    context_size: 1000000,
    output_tokens: 40,
    usage: { input_tokens: 2, output_tokens: 40 },
  });
  assert.equal(u.costUSD, 0.11);
  assert.equal(u.outputTokens, 40);
  assert.equal(u.contextTokens, 36451);
  assert.equal(u.contextSize, 1000000);
  assert.equal(u.contextPct, 4);
});

test("resultUsage reads codex's flatter input/output tokens with no cost", () => {
  const u = resultUsage({ cost_usd: null, input_tokens: 34710, output_tokens: 137, tokens: 137 });
  assert.equal(u.costUSD, undefined);
  assert.equal(u.inputTokens, 34710);
  assert.equal(u.outputTokens, 137);
  assert.equal(u.contextPct, undefined);
});

test("resultUsage tolerates an empty or missing result", () => {
  assert.deepEqual(resultUsage(undefined), {});
  const u = resultUsage({});
  assert.equal(u.costUSD, undefined);
  assert.equal(u.contextPct, undefined);
});

test("formatAge renders seconds/minutes/hours/days", () => {
  const now = 1_000_000;
  assert.equal(formatAge(now - 30, now), "30s ago");
  assert.equal(formatAge(now - 200, now), "3m ago");
  assert.equal(formatAge(now - 7200, now), "2h ago");
  assert.equal(formatAge(now - 172800, now), "2d ago");
  assert.equal(formatAge(null, now), "");
});
