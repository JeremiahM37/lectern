import { useLocale } from "../i18n";
import type { SessionView } from "../types";
import { sessionState, stateLabel, stateText } from "./status";

// The session status chip: the same four words on every card, list and
// phone row (sessions/status.ts). "Needs you" is the only loud one.
// On a session card the badge stays short: "Needs you" alone, so the title
// keeps the room. The reason ("· permission prompt") moves to the tooltip and
// stays in full on the strip and the Approvals page.
export function StatusBadge({ session, pendingApproval, className = "", brief = false }: { session: SessionView; pendingApproval?: boolean; className?: string; brief?: boolean }) {
  useLocale();
  const info = sessionState(session, pendingApproval);
  const full = stateText(info);
  const short = brief && info.state === "needs_you" ? stateLabel(info.state) : full;
  return (
    <span
      className={`sstate status-badge ${className}`.trim()}
      data-state={info.state}
      data-reason={info.reason || undefined}
      title={short !== full ? full : undefined}
      aria-label={short !== full ? full : undefined}
    >
      {short}
    </span>
  );
}
