// One status vocabulary for every session surface — cards, lists, the Now
// strip, the phone and the palette (docs/design/simple-ui.md "Session
// status"). The server computes it (internal/vocab, `state` on every session);
// this module reads that, and computes the same answer itself only for a row
// from a server too old to send it (an offline copy, say). "Needs you" is
// reserved for a session that is blocked on a person.
import type { SessionView } from "../types";
import { t } from "../i18n";

export type SessionState = "working" | "needs_you" | "idle" | "ended";
export interface StateInfo {
  state: SessionState;
  reason: string;
}

const STATES: readonly string[] = ["working", "needs_you", "idle", "ended"];

/** Mirrors internal/vocab.SessionState. */
export function computeState(s: SessionView, pendingApproval = false): StateInfo {
  if (s.archived_at != null) return { state: "ended", reason: "archived" };
  if (s.setup_state === "failed") return { state: "ended", reason: "setup_failed" };
  if (s.ended_at != null && s.status !== "dead") return { state: "ended", reason: "untracked" };
  if (s.ended_at != null || s.status === "dead") return { state: "ended", reason: "" };
  if (s.status === "interrupted") return { state: "ended", reason: "interrupted" };
  if (s.agent_exited_at) return { state: "ended", reason: "agent_exited" };
  if (s.setup_state === "creating") return { state: "working", reason: "setting_up" };
  if (pendingApproval) return { state: "needs_you", reason: "approval" };
  if (s.agent_state === "waiting_permission") return { state: "needs_you", reason: "permission_prompt" };
  if (s.status === "starting") return { state: "working", reason: "starting" };
  if (s.status === "running") return { state: "working", reason: "" };
  return { state: "idle", reason: "" };
}

/**
 * The session's state. `pendingApproval` is what this page knows about the
 * approvals list, which can be a poll ahead of (or behind) the session row:
 * an approval seen here makes a live session "needs you" at once, and an
 * approval this page has just decided stops being one.
 */
export function sessionState(s: SessionView, pendingApproval?: boolean): StateInfo {
  if (!s.state || !STATES.includes(s.state)) return computeState(s, !!pendingApproval);
  const server: StateInfo = { state: s.state as SessionState, reason: s.state_reason || "" };
  if (pendingApproval === undefined || server.state === "ended") return server;
  if (pendingApproval && server.state !== "needs_you") return { state: "needs_you", reason: "approval" };
  if (!pendingApproval && server.state === "needs_you" && server.reason === "approval") return computeState({ ...s, state: undefined }, false);
  return server;
}

export function stateLabel(state: SessionState): string {
  return t(`status.${state}`);
}

export function reasonLabel(reason: string): string {
  return reason ? t(`status.reason.${reason}`, undefined, reason.replace(/_/g, " ")) : "";
}

/** "Ended · agent exited" — the words a card, a chip or a list row shows. */
export function stateText(info: StateInfo): string {
  const reason = info.reason && info.reason !== "approval" ? reasonLabel(info.reason) : "";
  return reason ? `${stateLabel(info.state)} · ${reason}` : stateLabel(info.state);
}

/** Sort order for lists: what wants a person first, what is over last. */
export const STATE_RANK: Record<SessionState, number> = { needs_you: 0, working: 1, idle: 2, ended: 3 };
