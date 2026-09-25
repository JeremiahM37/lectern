import { useEffect, useRef, useState } from "react";
import type { JsonValue } from "../api";
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
      onNotice(helpful ? "Thank you — that tunes ranking." : "Noted as not relevant.");
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function challenge(item: MemoryItem) {
    if (!item.id) return;
    const reason = prompt("Why is this memory wrong or out of date?");
    if (reason == null) return;
    if (!reason.trim()) {
      onNotice("A challenge needs a reason — otherwise nobody can review it.", true);
      return;
    }
    try {
      await api.request(itemPath(item.id, "challenge"), { method: "POST", body: { reason: reason.trim() } });
      mark(item, "challenged");
      onNotice("Reported as wrong — the memory store will keep the claim and your objection side by side.");
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
  const state = useMemoryDeliveries(props.api, props.kind, props.id, props.onNotice);
  return <MemorySection state={state} />;
}

export function MemorySection({ state }: { state: MemoryDeliveriesState }) {
  return (
    <details
      className="conversation-memory"
      onToggle={(event) => {
        if (event.currentTarget.open && !state.deliveries && !state.busy) state.load();
      }}
    >
      <summary>
        Memory{state.deliveries?.length ? ` · ${state.deliveries.length}` : ""}
      </summary>
      <div className="memory-body">
        {state.busy && !state.deliveries && <p className="sub">Reading what was delivered…</p>}
        {state.error && (
          <p className="sub">
            What was delivered is unavailable, so this is not “nothing was”. {state.error}
          </p>
        )}
        {state.deliveries && state.deliveries.length === 0 && (
          <p className="sub">
            Nothing has been injected here yet. Project memory is added at launch and when a message
            retrieves something new.
          </p>
        )}
        {state.deliveries?.map((delivery) => (
          <section className="memory-delivery" key={delivery.id}>
            <header>
              <b>{formatAge(delivery.at) || "just now"}</b>
              <span className="memory-summary">{deliverySummary(delivery)}</span>
            </header>
            {delivery.items.length === 0 ? (
              <p className="sub">The store sent context without naming the records it came from.</p>
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
            Refresh
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
  return (
    <li data-verdict={verdict}>
      <div className="memory-item-head">
        <b>{itemLabel(item)}</b>
        {item.source && item.source !== itemLabel(item) && <code>{item.source}</code>}
      </div>
      {item.snippet && (
        <details className="memory-snippet">
          <summary>What it said</summary>
          <p>{item.snippet}</p>
        </details>
      )}
      {!item.id ? (
        <small>This store did not name the record, so it cannot be reviewed from here.</small>
      ) : verdict ? (
        <small className="memory-verdict" data-verdict={verdict}>
          {verdict === "helpful"
            ? "Marked helpful ✓"
            : verdict === "irrelevant"
              ? "Marked not relevant"
              : "Reported as wrong — sent for review"}
        </small>
      ) : (
        <div className="memory-actions">
          <button className="b" onClick={() => onReview(item, true)}>
            Helpful
          </button>
          <button className="b" onClick={() => onReview(item, false)}>
            Not relevant
          </button>
          <button className="b warn" onClick={() => onChallenge(item)}>
            This is wrong
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
  const names = delivery.items.map(itemLabel).join(" · ");
  return (
    <article className="ev e-memory">
      <div className="k">
        memory{attempt != null ? ` · A${attempt}` : ""} · {formatAge(delivery.at) || "just now"}
      </div>
      <pre className="body dim">
        {deliverySummary(delivery)}
        {names ? `\n${names}` : ""}
      </pre>
    </article>
  );
}
