import { useState } from "react";
import type { DraftComment } from "./types";
import { t, useLocale } from "../i18n";

/**
 * The pending-comments tray: every drafted comment plus an optional overall
 * summary, and the one button that sends them. Docked at the bottom of the
 * review panel on desktop; becomes a fixed bottom sheet under 480px (see
 * .review-tray in web/static/style.css) so it's reachable with a thumb next
 * to the diff instead of scrolled away above it.
 */
export function CommentTray({
  comments,
  summary,
  onSummaryChange,
  onRemove,
  onSend,
  busy,
  sendLabel,
}: {
  comments: DraftComment[];
  summary: string;
  onSummaryChange(v: string): void;
  onRemove(key: string): void;
  onSend(): void;
  busy?: boolean;
  sendLabel: string;
}) {
  useLocale();
  const [writingSummary, setWritingSummary] = useState(false);
  // On a phone the tray is docked over the diff: start it folded to one
  // line (count + send), and let a tap open the list and the summary.
  const [openByDefault] = useState(
    () => !(typeof window !== "undefined" && window.matchMedia?.("(max-width: 480px)").matches),
  );
  const canSend = !busy && (comments.length > 0 || summary.trim() !== "");
  if (comments.length === 0 && !summary && !writingSummary) {
    // Nothing drafted: a one-line hint instead of a docked form, so on a
    // phone the diff keeps the screen.
    return (
      <div className="review-tray review-tray-empty" id="review-tray">
        <span className="sub">{t("review.tray.empty")}</span>
        <button type="button" className="b" onClick={() => setWritingSummary(true)}>
          {t("review.tray.writeSummary")}
        </button>
      </div>
    );
  }
  return (
    <div className="review-tray" id="review-tray">
      <details className="review-tray-list" open={openByDefault || writingSummary}>
        <summary>
          {comments.length === 0
            ? t("review.tray.noComments")
            : t("review.tray.comments", { count: comments.length })}
        </summary>
        <ul>
          {comments.map((c) => (
            <li key={c.key} className="review-tray-item">
              <div className="review-tray-item-head">
                <code>
                  {c.file}:{c.line}
                </code>
                <button
                  type="button"
                  className="b no review-tray-remove"
                  aria-label={t("review.tray.remove", { file: c.file, line: c.line })}
                  onClick={() => onRemove(c.key)}
                >
                  ✕
                </button>
              </div>
              {c.code && <pre className="review-tray-code">{c.code}</pre>}
              <p>{c.text}</p>
            </li>
          ))}
        </ul>
        <label className="f review-tray-summary">
          {t("review.tray.summaryLabel")}
          <textarea
            className="f"
            rows={2}
            placeholder={t("review.tray.summaryPlaceholder")}
            value={summary}
            onChange={(e) => onSummaryChange(e.target.value)}
          />
        </label>
      </details>
      <button
        type="button"
        className="b ok review-tray-send"
        disabled={!canSend}
        onClick={onSend}
      >
        {busy ? t("review.tray.sending") : sendLabel}
      </button>
    </div>
  );
}
