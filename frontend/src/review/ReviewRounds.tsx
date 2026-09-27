import type { CommentState, Placement } from "./diffModel";
import type { StoredComment } from "./types";
import { t, useLocale } from "../i18n";

const LABEL = (): Record<CommentState, string> => ({
  draft: t("review.state.draft"),
  open: t("review.rounds.open"),
  addressed: t("review.state.addressed"),
  resolved: t("review.state.resolved"),
});

/**
 * The resolve / re-review loop: every comment already sent to the agent,
 * grouped by round, showing whether the agent has since changed the code it
 * was about. Resolve the ones that are done; reopen the rest to send them
 * again in the next round.
 */
export function ReviewRounds({
  items,
  onResolve,
  onReopen,
}: {
  items: { c: StoredComment; placement?: Placement; state: CommentState }[];
  onResolve(id: number): void;
  onReopen(id: number): void;
}) {
  useLocale();
  if (!items.length) return null;
  const rounds = [...new Set(items.map((i) => i.c.round))].sort((a, b) => b - a);
  const addressed = items.filter((i) => i.state === "addressed");
  const open = items.filter((i) => i.state === "open");
  return (
    <details className="review-rounds" open={addressed.length + open.length > 0}>
      <summary>
        {t("review.rounds.summary", {
          addressed: addressed.length,
          open: open.length,
          resolved: items.filter((i) => i.state === "resolved").length,
        })}
      </summary>
      <div className="btnrow review-rounds-bulk">
        {addressed.length > 0 && (
          <button type="button" className="b ok" onClick={() => addressed.forEach((i) => onResolve(i.c.id))}>
            {t("review.rounds.resolveChanged", { n: addressed.length })}
          </button>
        )}
        {open.length > 0 && (
          <button type="button" className="b" onClick={() => open.forEach((i) => onReopen(i.c.id))}>
            {t("review.rounds.reopenUnchanged", { n: open.length })}
          </button>
        )}
      </div>
      {rounds.map((round) => (
        <section key={round} className="review-round">
          <h4>{t("review.rounds.round", { round })}</h4>
          <ul>
            {items
              .filter((i) => i.c.round === round)
              .map(({ c, placement, state }) => (
                <li key={c.id} className="review-round-item" data-state={state}>
                  <div className="review-tray-item-head">
                    <code>
                      {c.file}:{placement?.line ?? c.line}
                    </code>
                    <span className={`dl-note-state s-${state}`}>{LABEL()[state]}</span>
                  </div>
                  {c.code && <pre className="review-tray-code">{c.code}</pre>}
                  <p>{c.text}</p>
                  <div className="btnrow">
                    {state !== "resolved" && (
                      <button type="button" className="b ok" onClick={() => onResolve(c.id)}>
                        {t("review.action.resolve")}
                      </button>
                    )}
                    <button type="button" className="b" onClick={() => onReopen(c.id)}>
                      {t("review.action.reopen")}
                    </button>
                  </div>
                </li>
              ))}
          </ul>
        </section>
      ))}
    </details>
  );
}
