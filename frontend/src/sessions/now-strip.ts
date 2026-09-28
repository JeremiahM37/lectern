// The "Now" strip's pure logic: what state a session is in, at the glance a
// phone home screen affords — the PWA analogue of a Live Activity, since a
// browser tab cannot keep one on the lock screen itself. Kept separate from
// NowStrip.tsx so it is testable the same way push.ts/badge.ts are, with no
// DOM involved.
import type { SessionView } from "../types";
import { sessionState, STATE_RANK, type SessionState } from "./status";

export type NowState = SessionState;

export interface NowItem {
  id: number;
  name: string;
  state: NowState;
  reason: string;
}

// The strip uses the same four states as every session surface
// (sessions/status.ts): a glance and a card never disagree.
export function sessionNowState(session: SessionView): NowState {
  return sessionState(session).state;
}

// nowItems is what the strip shows: every non-archived session (archiving is
// the person's own "stop showing me this" signal, so it is the one thing
// respected here), ranked so the ones most likely to want a person sort
// first. Capped by the caller, not here — the cap is a rendering choice.
export function nowItems(rows: SessionView[]): NowItem[] {
  return rows
    .filter((session) => session.archived_at == null)
    .map((session) => {
      const info = sessionState(session);
      return { id: session.id, name: session.name, state: info.state, reason: info.reason };
    })
    .sort((a, b) => STATE_RANK[a.state] - STATE_RANK[b.state]);
}
