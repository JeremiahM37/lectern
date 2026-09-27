import { useEffect, useRef, useState } from "react";
import type { JsonValue } from "../api";
import { t, useLocale } from "../i18n";
import { formatAge } from "./usageFormat";
import {
  deliverySummary,
  itemLabel,
  itemPath,
  type MemoryDelivery,
  type MemoryItem,
} from "./memoryDelivery";

// The two API surfaces that render this (the sessions client and the board's)
// both expose request<T>. Naming only that keeps the component usable from
// either without importing a client type it does not need.
export interface MemoryApi {
  request<T>(
    path: string,
    options?: { method?: string; body?: JsonValue; signal?: AbortSignal },
  ): Promise<T>;
}

type Verdict = "helpful" | "irrelevant" | "challenged";

export interface MemoryDeliveriesState {
  deliveries?: MemoryDelivery[];
  busy: boolean;
  error: string;
  verdicts: Record<string, Verdict>;
  load(): void;
  review(item: MemoryItem, helpful: boolean): Promise<void>;
  challenge(item: MemoryItem): Promise<void>;
}

// Memory you can see (docs/memory-visibility.md). What lectern handed this
// session or task attempt was recorded but never shown; this reads it back,
// and carries the operator's verdict to the store. "Helpful" and "Not relevant"
// are ranking feedback; "This is wrong" asks the store to review the record as
// possibly superseded, which is why it needs a reason and why the API requires
// a human for both.
export function useMemoryDeliveries(
  api: MemoryApi,
  kind: "session" | "task",
  id: number,
  onNotice: (text: string, error?: boolean) => void,
  loadOnMount = false,
): MemoryDeliveriesState {
  const [deliveries, setDeliveries] = useState<MemoryDelivery[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [verdicts, setVerdicts] = useState<Record<string, Verdict>>({});
  const loadedFor = useRef("");
  // A slow answer for the run we just left must not land under the new heading.
  const subject = `${kind}:${id}`;
  const currentSubject = useRef(subject);

  function load() {
    const forSubject = subject;
    setBusy(true);
    const path = kind === "session" ? `/sessions/${id}/memory` : `/tasks/${id}/memory`;
    api
      .request<{ deliveries?: MemoryDelivery[] }>(path)
      .then((body) => {
        if (currentSubject.current !== forSubject) return;
        setDeliveries(Array.isArray(body.deliveries) ? body.deliveries : []);
        setError("");
      })
      .catch((e) => {
        if (currentSubject.current !== forSubject) return;
        setDeliveries(undefined);
        setError(String(e));
      })
      .finally(() => {
        if (currentSubject.current === forSubject) setBusy(false);
      });
  }
  // A different run's memory must never be shown under this heading: the panel
  // is reused when the board switches tasks.
  useEffect(() => {
    currentSubject.current = subject;
    loadedFor.current = "";
    setDeliveries(undefined);
    setError("");
    setVerdicts({});
  }, [subject]);
  useEffect(() => {
    if (!loadOnMount || loadedFor.current === subject) return;
    loadedFor.current = subject;
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadOnMount, subject]);

  // A verdict is final for the life of the panel: re-answering the same item
  // would only add noise to the store's own history.
  function mark(item: MemoryItem, verdict: Verdict) {
    if (!item.id) return;
    setVerdicts((old) => ({ ...old, [item.id as string]: verdict }));
  }

  async function review(item: MemoryItem, helpful: boolean) {
    if (!item.id) return;
    try {
      await api.request(itemPath(item.id, "feedback"), { method: "POST", body: { helpful } });
      mark(item, helpful ? "helpful" : "irrelevant");
      onNotice(helpful ? t("sessions.memoryDeliveries.thanksHelpful") : t("sessions.memoryDeliveries.notedIrrelevant"));
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function challenge(item: MemoryItem) {
    if (!item.id) return;
    const reason = prompt(t("sessions.memoryDeliveries.challengePrompt"));
    if (reason == null) return;
    if (!reason.trim()) {
      onNotice(t("sessions.memoryDeliveries.challengeNeedsReason"), true);
      return;
    }
    try {
      await api.request(itemPath(item.id, "challenge"), { method: "POST", body: { reason: reason.trim() } });
      mark(item, "challenged");
      onNotice(t("sessions.memoryDeliveries.challengeSent"));
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  return { deliveries, busy, error, verdicts, load, review, challenge };
}

// MemoryDeliveries is the section itself: a collapsible list that reads on
// first open, like the session's Changed files.
export function MemoryDeliveries(props: {
  api: MemoryApi;
  kind: "session" | "task";
  id: number;
  onNotice(text: string, error?: boolean): void;
}) {
  useLocale();
  const state = useMemoryDeliveries(props.api, props.kind, props.id, props.onNotice);
  return <MemorySection state={state} />;
}

export function MemorySection({ state }: { state: MemoryDeliveriesState }) {
  useLocale();
  return (
    <details
      className="conversation-memory"
      onToggle={(event) => {
        if (event.currentTarget.open && !state.deliveries && !state.busy) state.load();
      }}
    >
      <summary>
        {t("sessions.memoryDeliveries.summary")}{state.deliveries?.length ? ` · ${state.deliveries.length}` : ""}
      </summary>
      <div className="memory-body">
        {state.busy && !state.deliveries && <p className="sub">{t("sessions.memoryDeliveries.reading")}</p>}
        {state.error && (
          <p className="sub">
            {t("sessions.memoryDeliveries.unavailable")} {state.error}
          </p>
        )}
        {state.deliveries && state.deliveries.length === 0 && (
          <p className="sub">
            {t("sessions.memoryDeliveries.empty")}
          </p>
        )}
        {state.deliveries?.map((delivery) => (
          <section className="memory-delivery" key={delivery.id}>
            <header>
              <b>{formatAge(delivery.at) || t("sessions.memoryDeliveries.justNow")}</b>
              <span className="memory-summary">{deliverySummary(delivery)}</span>
            </header>
            {delivery.items.length === 0 ? (
              <p className="sub">{t("sessions.memoryDeliveries.unnamedRecords")}</p>
            ) : (
              <ul className="memory-items">
                {delivery.items.map((item, index) => (
                  <MemoryItemRow
                    key={item.id || `${delivery.id}-${index}`}
                    item={item}
                    verdict={item.id ? state.verdicts[item.id] : undefined}
                    onReview={state.review}
                    onChallenge={state.challenge}
                  />
                ))}
              </ul>
            )}
          </section>
        ))}
        {/* Offered whenever an answer was attempted — including a failed one,
            or a store that was down when the panel was first opened. */}
        {(state.deliveries || state.error) && (
          <button className="b" disabled={state.busy} onClick={state.load}>
            {t("sessions.memory.refresh")}
          </button>
        )}
      </div>
    </details>
  );
}

function MemoryItemRow({
  item,
  verdict,
  onReview,
  onChallenge,
}: {
  item: MemoryItem;
  verdict?: Verdict;
  onReview(item: MemoryItem, helpful: boolean): void;
  onChallenge(item: MemoryItem): void;
}) {
  useLocale();
  return (
    <li data-verdict={verdict}>
      <div className="memory-item-head">
        <b>{itemLabel(item)}</b>
        {item.source && item.source !== itemLabel(item) && <code>{item.source}</code>}
      </div>
      {item.snippet && (
        <details className="memory-snippet">
          <summary>{t("sessions.memoryDeliveries.whatItSaid")}</summary>
          <p>{item.snippet}</p>
        </details>
      )}
      {!item.id ? (
        <small>{t("sessions.memoryDeliveries.cannotReview")}</small>
      ) : verdict ? (
        <small className="memory-verdict" data-verdict={verdict}>
          {verdict === "helpful"
            ? t("sessions.memoryDeliveries.markedHelpful")
            : verdict === "irrelevant"
              ? t("sessions.memoryDeliveries.markedIrrelevant")
              : t("sessions.memoryDeliveries.markedWrong")}
        </small>
      ) : (
        <div className="memory-actions">
          <button className="b" onClick={() => onReview(item, true)}>
            {t("sessions.memoryDeliveries.helpful")}
          </button>
          <button className="b" onClick={() => onReview(item, false)}>
            {t("sessions.memoryDeliveries.notRelevant")}
          </button>
          <button className="b warn" onClick={() => onChallenge(item)}>
            {t("sessions.memoryDeliveries.thisIsWrong")}
          </button>
        </div>
      )}
    </li>
  );
}

// MemoryTimelineEntry puts a delivery on the task timeline, where it belongs
// chronologically: memory is injected before the attempt's first prompt, so it
// reads as the run's opening condition rather than a footnote.
export function MemoryTimelineEntry({
  delivery,
  attempt,
}: {
  delivery: MemoryDelivery;
  /** The attempt number, when the task has more than one — otherwise the entry
   *  would claim a re-dispatched attempt's memory was this attempt's. */
  attempt?: number;
}) {
  useLocale();
  const names = delivery.items.map(itemLabel).join(" · ");
  return (
    <article className="ev e-memory">
      <div className="k">
        {t("sessions.memoryDeliveries.timelineKind")}{attempt != null ? " · " + t("sessions.memoryDeliveries.attempt", { n: attempt }) : ""} · {formatAge(delivery.at) || t("sessions.memoryDeliveries.justNow")}
      </div>
      <pre className="body dim">
        {deliverySummary(delivery)}
        {names ? `\n${names}` : ""}
      </pre>
    </article>
  );
}
