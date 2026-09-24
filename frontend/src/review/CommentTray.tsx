import type { DraftComment } from "./types";

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
  const canSend = !busy && (comments.length > 0 || summary.trim() !== "");
  return (
    <div className="review-tray" id="review-tray">
      <details className="review-tray-list" open={comments.length > 0}>
        <summary>
          {comments.length === 0
            ? "No comments yet"
            : `${comments.length} comment${comments.length === 1 ? "" : "s"}`}
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
                  aria-label={`Remove comment on ${c.file}:${c.line}`}
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
      </details>
      <label className="f review-tray-summary">
        Overall summary (optional)
        <textarea
          className="f"
          rows={2}
          placeholder="Anything to say beyond the inline comments…"
          value={summary}
          onChange={(e) => onSummaryChange(e.target.value)}
        />
      </label>
      <button
        type="button"
        className="b ok review-tray-send"
        disabled={!canSend}
        onClick={onSend}
      >
        {busy ? "Sending…" : sendLabel}
      </button>
    </div>
  );
}
