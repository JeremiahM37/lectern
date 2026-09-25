// "Report keyboard layout" — a hidden Tools-menu diagnostic for the
// long-running "the keyboard still covers the prompt on my phone" issue
// (docs/testing/mobile-terminal.md, and the Sept 2026 investigation notes in
// agent memory) that every emulator/Playwright reproduction attempt has
// failed to reproduce on the owner's actual device. Rather than guess again,
// this captures the exact numbers a real device sees — visualViewport, the
// VirtualKeyboard API's rect if the browser overlays content, and the
// on-screen position of the pieces that matter (the key row, the Tools
// toggle, and the terminal's actual focus target) — into one blob the owner
// can paste back. Split into a pure builder/formatter (tested) and a capture
// step that touches the DOM (App.tsx, at the one call site), the same
// separation push.ts and badge.ts already use for their one live browser
// call site.

export interface RectSnapshot {
  top: number;
  left: number;
  width: number;
  height: number;
}

export interface KeyboardReportInput {
  userAgent: string;
  innerWidth: number;
  innerHeight: number;
  visualViewport: { width: number; height: number; offsetTop: number; offsetLeft: number; scale: number } | null;
  virtualKeyboard: { overlaysContent: boolean; rect: RectSnapshot } | null;
  /** Named element rects — whatever the caller could find on the page at capture time; a missing element is null, not omitted, so its absence is itself informative. */
  elements: Record<string, RectSnapshot | null>;
  /** The --lec-visible-height custom property this build's own viewport-fit logic is currently applying, if any (see terminal/viewport.ts). */
  fittedHeightVar: string | null;
  /** Epoch ms at capture time, threaded in rather than read from Date.now() so the stamping step stays pure and testable. */
  now: number;
}

export interface KeyboardReport extends KeyboardReportInput {
  timestamp: string;
}

// buildKeyboardReport stamps a capture with an ISO timestamp — the only
// non-trivial step in turning a snapshot into the report, and the one worth
// testing without a real Date.now().
export function buildKeyboardReport(input: KeyboardReportInput): KeyboardReport {
  return { ...input, timestamp: new Date(input.now).toISOString() };
}

// formatKeyboardReport is the copyable text a person pastes into a message —
// plain indented JSON, since the point is to hand the exact numbers back
// unmodified, not to make them pretty.
export function formatKeyboardReport(report: KeyboardReport): string {
  return JSON.stringify(report, null, 2);
}

// rectSnapshot rounds a DOMRect down to whole pixels — real device numbers
// carry enough sub-pixel noise that showing more precision would suggest a
// false confidence.
export function rectSnapshot(rect: {
  top: number;
  left: number;
  width: number;
  height: number;
}): RectSnapshot {
  return {
    top: Math.round(rect.top),
    left: Math.round(rect.left),
    width: Math.round(rect.width),
    height: Math.round(rect.height),
  };
}
