import { useCallback, useEffect, useState } from "react";
import { Modal } from "../sessions/Modal";
import { errorText, type Notice, type TrackerApi } from "./api";
import { Avatar } from "./bits";
import { ago } from "./logic";
import type { ItemRef, QueueEntry, QueueResponse } from "./types";

function eta(seconds?: number): string {
  if (!seconds) return "";
  if (seconds < 90) return "about a minute";
  if (seconds < 3600) return `about ${Math.round(seconds / 60)} min`;
  return `about ${Math.round(seconds / 3600)} h`;
}

/** A base branch's merge queue (GitHub) or merge train (GitLab): position,
 * status, the host's time estimate or the train pipeline's state, and
 * removal behind a confirmation. */
export function MergeQueue({
  api,
  projectId,
  source,
  onOpen,
  onNotice,
}: {
  api: TrackerApi;
  projectId: number;
  source: "github" | "gitlab";
  onOpen(ref: ItemRef): void;
  onNotice: Notice;
}) {
  const [base, setBase] = useState("");
  const [draft, setDraft] = useState("");
  const [data, setData] = useState<QueueResponse>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [removing, setRemoving] = useState<QueueEntry>();
  const [busy, setBusy] = useState(false);
  const word = source === "gitlab" ? "merge train" : "merge queue";

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const res = await api.request<QueueResponse>(`/projects/${projectId}/forge/queue${base ? `?base=${encodeURIComponent(base)}` : ""}`);
      setData(res);
      if (!base && res.base) setDraft(res.base);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, [api, projectId, base]);
  useEffect(() => {
    void load();
  }, [load]);

  async function remove(e: QueueEntry) {
    setBusy(true);
    try {
      await api.request(`/projects/${projectId}/forge/queue/remove`, { method: "POST", body: { base: data?.base || base, id: e.id } });
      onNotice(`#${e.number} removed from the ${word}`);
      setRemoving(undefined);
      await load();
    } catch (err) {
      onNotice(errorText(err), true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="th-queue">
      <form
        className="th-queue-head"
        onSubmit={(e) => {
          e.preventDefault();
          setBase(draft.trim());
        }}
      >
        <label>
          Target branch
          <input value={draft} onChange={(e) => setDraft(e.target.value)} spellCheck={false} autoCapitalize="off" placeholder="default branch" />
        </label>
        <button className="b" type="submit" disabled={loading}>
          {loading ? "Loading…" : "Show"}
        </button>
      </form>
      {error && <p className="th-banner th-banner-err" role="alert">{error}</p>}
      {data && !data.supported && <p className="th-empty">This code host has no {word}.</p>}
      {data?.supported && data.entries.length === 0 && (
        <p className="th-empty">
          Nothing is waiting in the {word} for <code className="th-branch">{data.base}</code>.
        </p>
      )}
      {data?.supported && data.entries.length > 0 && (
        <ol className="th-queue-list" aria-label={`${word} for ${data.base}`}>
          {data.entries.map((e) => (
            <li key={e.id}>
              <span className="th-queue-pos" aria-label={`position ${e.position}`}>
                {e.position}
              </span>
              <span className="th-row-main">
                <button type="button" className="th-link-row th-queue-title" onClick={() => onOpen({ source, kind: "pr", id: String(e.number) })}>
                  <span className="th-num">#{e.number}</span> {e.title}
                </button>
                <span className="th-row-meta">
                  <span className={`th-badge th-queue-${e.status}`}>{e.status.replace(/_/g, " ")}</span>
                  {e.pipeline && <span className={`th-badge th-checks-${e.pipeline === "success" ? "pass" : e.pipeline === "failed" ? "fail" : "pending"}`}>pipeline {e.pipeline}</span>}
                  {e.eta_seconds ? <span>merges in {eta(e.eta_seconds)}</span> : null}
                  {e.enqueued_at && <span>queued {ago(e.enqueued_at)}</span>}
                  {e.author && <span>by {e.author}</span>}
                </span>
              </span>
              {e.author && <Avatar name={e.author} />}
              <button type="button" className="b no th-queue-remove" onClick={() => setRemoving(e)} aria-label={`Remove #${e.number} from the ${word}`}>
                Remove
              </button>
            </li>
          ))}
        </ol>
      )}
      {removing && (
        <Modal className="th-confirm" aria-labelledby="th-dequeue-title" onCancel={() => setRemoving(undefined)}>
          <h2 id="th-dequeue-title">Remove #{removing.number} from the {word}?</h2>
          <p>
            {removing.title} leaves the {word} for <code className="th-branch">{data?.base}</code>
            {source === "gitlab" ? " and its merge-when-ready is turned off" : ""}. It stays open.
          </p>
          <div className="btnrow">
            <button className="b no grow" id="th-dequeue-go" autoFocus disabled={busy} onClick={() => void remove(removing)}>
              {busy ? "Removing…" : "Remove"}
            </button>
            <button className="b" onClick={() => setRemoving(undefined)}>
              Cancel
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}
