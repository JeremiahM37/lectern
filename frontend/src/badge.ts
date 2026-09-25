// The app icon's badge count (navigator.setAppBadge): what number to show,
// and when to clear it. Kept pure/testable like push.ts — the two live call
// sites (the open page after an SSE-driven refresh, and the service worker
// on a push event) each hand this a plain count and let it do the
// try/catch dance, since Badging is unsupported on most desktop browsers and
// must never throw into a render path or a push handler.

export interface BadgeCounts {
  approvals: number;
  waitingSessions: number;
}

// A pending approval and a session waiting for input are both things a
// person glancing at the home screen would want counted. They are not
// deduplicated against each other — a session with its own pending approval
// is double-counted here — because NeedsYou already does that finer
// bookkeeping for its own list; the badge only needs a glanceable total.
export function computeBadgeCount({ approvals, waitingSessions }: BadgeCounts): number {
  return Math.max(0, approvals) + Math.max(0, waitingSessions);
}

/** The subset of navigator (or self, inside a service worker) this needs — real in either context, fakeable in a test. */
export interface BadgeNavigator {
  setAppBadge?(count?: number): Promise<void>;
  clearAppBadge?(): Promise<void>;
}

// applyBadge sets or clears the badge, swallowing every rejection an
// unsupported or permission-denied browser produces. A badge is a nicety —
// never worth a toast, and never worth throwing out of a push handler.
export function applyBadge(nav: BadgeNavigator, count: number): void {
  try {
    if (count > 0) void nav.setAppBadge?.(count)?.catch(() => {});
    else void nav.clearAppBadge?.()?.catch(() => {});
  } catch {
    /* some implementations throw synchronously rather than rejecting */
  }
}

// needsBadge says whether a push's kind represents something that should
// bump the badge from the service worker, which — unlike the open page —
// cannot see the live approval/session list and so can only increment by
// one per push. "approval" and both "waiting_*" kinds mean a person must
// act; idle, error and compacting are informational only.
const ACTIONABLE_KINDS = new Set(["approval", "waiting_permission", "waiting_input"]);
export function needsBadge(kind?: string): boolean {
  return !!kind && ACTIONABLE_KINDS.has(kind);
}
