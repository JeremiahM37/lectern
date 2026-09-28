import { useLocale } from "../i18n";
import type { SessionView } from "../types";
import { sessionState, stateText } from "./status";

// The session status chip: the same four words on every card, list and
// phone row (sessions/status.ts). "Needs you" is the only loud one.
export function StatusBadge({ session, pendingApproval, className = "" }: { session: SessionView; pendingApproval?: boolean; className?: string }) {
  useLocale();
  const info = sessionState(session, pendingApproval);
  return (
    <span className={`sstate status-badge ${className}`.trim()} data-state={info.state} data-reason={info.reason || undefined}>
      {stateText(info)}
    </span>
  );
}
