import { useState } from "react";
import { t, useLocale } from "../i18n";
import type { SessionView } from "../types";

// AwarenessOverlapChip is the "⚠ overlaps #N" chip (docs/agent-events.md
// "Cross-agent awareness" point 6): shown when session.awareness_overlap is
// set — another LIVE session touched at least one of the SAME files as this
// one within the last 30 minutes. The data rides free on the ordinary
// session row (internal/api/sessions.go's computeAwarenessOverlap), so this
// component needs no fetch of its own; tapping it only expands what is
// already in hand.
export function AwarenessOverlapChip({ session: s }: { session: SessionView }) {
  useLocale();
  const [expanded, setExpanded] = useState(false);
  const overlap = s.awareness_overlap;
  if (!overlap) return null;
  return (
    <span className="awareness-overlap">
      <button
        type="button"
        className="chip warn awareness-overlap-chip"
        aria-expanded={expanded}
        title={t("sessions.overlap.title", { id: overlap.session_id, name: overlap.name })}
        onClick={() => setExpanded((open) => !open)}
      >
        {t("sessions.overlap.chip", { id: overlap.session_id })}
      </button>
      {expanded && (
        <div className="awareness-overlap-detail">
          <div>
            {t("sessions.overlap.detailBefore")}<strong>{overlap.name}</strong>{t("sessions.overlap.detailAfter", { id: overlap.session_id })}
          </div>
          <ul>
            {overlap.files.map((f) => (
              <li key={f}>
                <code>{f}</code>
              </li>
            ))}
          </ul>
        </div>
      )}
    </span>
  );
}
