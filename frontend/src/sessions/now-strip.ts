// The "Now" strip's pure logic: what state a session is in, at the glance a
// phone home screen affords — the PWA analogue of a Live Activity, since a
// browser tab cannot keep one on the lock screen itself. Kept separate from
// NowStrip.tsx so it is testable the same way push.ts/badge.ts are, with no
// DOM involved.
import type { SessionView } from "../types";

export type NowState = "working" | "waiting" | "done" | "error";

export interface NowItem {
  id: number;
  name: string;
  state: NowState;
}

// sessionNowState collapses the session/setup state machine SessionCard
// already reads (see its own `status` label map) down to the four states a
// glance actually needs. A setup failure is "error" even for a session whose
// last known `status` was something else; everything else follows `status`.
export function sessionNowState(session: SessionView): NowState {
  if (session.setup_state === "failed") return "error";
  if (session.status === "dead") return "done";
  if (session.status === "waiting") return "waiting";
  return "working"; // running, starting, idle
}

const RANK: Record<NowState, number> = { waiting: 0, error: 1, working: 2, done: 3 };

// nowItems is what the strip shows: every non-archived session (archiving is
// the person's own "stop showing me this" signal, so it is the one thing
// respected here), ranked so the ones most likely to want a person sort
// first. Capped by the caller, not here — the cap is a rendering choice.
export function nowItems(rows: SessionView[]): NowItem[] {
  return rows
    .filter((session) => session.archived_at == null)
    .map((session) => ({ id: session.id, name: session.name, state: sessionNowState(session) }))
    .sort((a, b) => RANK[a.state] - RANK[b.state]);
}
