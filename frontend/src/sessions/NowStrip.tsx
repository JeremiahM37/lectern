import { useMemo } from "react";
import { t, useLocale } from "../i18n";
import type { SessionView } from "../types";
import { nowItems, type NowState } from "./now-strip";
import "./now-strip.css";

// The PWA analogue of a Live Activity: a phone cannot keep a native strip on
// the lock screen for a web app, but the home view can still lead with the
// same one-line-per-session glance — every live session's state and, if any
// approval is waiting, how many — with one tap into whichever one matters.
// Deliberately independent of the search box below it (Sessions.tsx's own
// `shown` list): a person typing to find one older session should not watch
// their glanceable strip shrink to match.

const LABEL = (): Record<NowState, string> => ({
  working: t("sessions.now.working"),
  waiting: t("sessions.now.waiting"),
  exited: t("sessions.now.exited"),
  done: t("sessions.now.done"),
  error: t("sessions.now.error"),
});
const DOT: Record<NowState, string> = {
  working: "●",
  waiting: "◆",
  exited: "↻",
  done: "○",
  error: "✕",
};
const CAP = 10;

interface Props {
  rows: SessionView[];
  approvalsCount: number;
  onShowSession(session: SessionView): void;
  onShowApprovals(): void;
}

export function NowStrip({ rows, approvalsCount, onShowSession, onShowApprovals }: Props) {
  useLocale();
  const labels = LABEL();
  const items = useMemo(() => nowItems(rows).slice(0, CAP), [rows]);
  if (!items.length && approvalsCount <= 0) return null;
  return (
    <section className="now-strip" id="now-strip" aria-label={t("sessions.now.label")}>
      <div className="now-strip-row">
        {approvalsCount > 0 && (
          <button
            type="button"
            className="now-chip now-approvals"
            id="now-approvals"
            onClick={onShowApprovals}
          >
            <span className="now-dot" aria-hidden="true">
              !
            </span>
            {t("sessions.now.toApprove", { count: approvalsCount })}
          </button>
        )}
        {items.map((item) => {
          const session = rows.find((row) => row.id === item.id);
          return (
            <button
              type="button"
              key={item.id}
              className="now-chip"
              data-state={item.state}
              data-session-id={item.id}
              aria-label={t("sessions.now.chipLabel", { name: item.name, state: labels[item.state] })}
              onClick={() => session && onShowSession(session)}
            >
              <span className="now-dot" aria-hidden="true">
                {DOT[item.state]}
              </span>
              <span className="now-name">{item.name}</span>
              <span className="now-state">{labels[item.state]}</span>
            </button>
          );
        })}
      </div>
    </section>
  );
}
