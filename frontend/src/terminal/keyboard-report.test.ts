import assert from "node:assert/strict";
import { test } from "node:test";
import { buildKeyboardReport, formatKeyboardReport, rectSnapshot, type KeyboardReportInput } from "./keyboard-report";

function input(over: Partial<KeyboardReportInput> = {}): KeyboardReportInput {
  return {
    userAgent: "test-agent",
    innerWidth: 390,
    innerHeight: 844,
    visualViewport: null,
    virtualKeyboard: null,
    elements: {},
    fittedHeightVar: null,
    now: 0,
    ...over,
  };
}

test("buildKeyboardReport stamps an ISO timestamp from the given epoch", () => {
  const report = buildKeyboardReport(input({ now: 1_700_000_000_000 }));
  assert.equal(report.timestamp, new Date(1_700_000_000_000).toISOString());
});

test("buildKeyboardReport carries every input field through unchanged", () => {
  const raw = input({
    visualViewport: { width: 390, height: 470, offsetTop: 0, offsetLeft: 0, scale: 1 },
    elements: { keybar: { top: 780, left: 0, width: 390, height: 44 } },
    fittedHeightVar: "470px",
  });
  const report = buildKeyboardReport(raw);
  assert.equal(report.visualViewport?.height, 470);
  assert.deepEqual(report.elements.keybar, { top: 780, left: 0, width: 390, height: 44 });
  assert.equal(report.fittedHeightVar, "470px");
});

test("formatKeyboardReport is indented JSON with every field present", () => {
  const text = formatKeyboardReport(buildKeyboardReport(input({ now: 0 })));
  const parsed = JSON.parse(text);
  assert.equal(parsed.userAgent, "test-agent");
  assert.ok(text.includes("\n  "), "expected indentation, got: " + text.slice(0, 40));
});

test("rectSnapshot rounds to whole pixels", () => {
  assert.deepEqual(rectSnapshot({ top: 1.4, left: 2.6, width: 10.5, height: 9.49 }), {
    top: 1,
    left: 3,
    width: 11,
    height: 9,
  });
});
