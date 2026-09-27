import { useCallback, useEffect, useState } from "react";
import { t, useLocale } from "../i18n";
import { Modal } from "../sessions/Modal";
import { errorText, type Notice, type TrackerApi } from "./api";
import { Avatar } from "./bits";
import { ago } from "./logic";
import type { ItemRef, QueueEntry, QueueResponse } from "./types";

function eta(seconds?: number): string {
  if (!seconds) return "";
  if (seconds < 90) return t("trackers.queue.eta.minute");
  if (seconds < 3600) return t("trackers.queue.eta.minutes", { n: Math.round(seconds / 60) });
  return t("trackers.queue.eta.hours", { n: Math.round(seconds / 3600) });
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
  useLocale();
  const [base, setBase] = useState("");
  const [draft, setDraft] = useState("");
  const [data, setData] = useState<QueueResponse>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [removing, setRemoving] = useState<QueueEntry>();
  const [busy, setBusy] = useState(false);
  const word = source === "gitlab" ? t("trackers.queue.word.train") : t("trackers.queue.word.queue");

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
      onNotice(t("trackers.queue.removed", { n: e.number, word }));
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
          {t("trackers.queue.targetBranch")}
          <input value={draft} onChange={(e) => setDraft(e.target.value)} spellCheck={false} autoCapitalize="off" placeholder={t("trackers.queue.defaultBranch")} />
        </label>
        <button className="b" type="submit" disabled={loading}>
          {loading ? t("trackers.loading") : t("trackers.queue.show")}
        </button>
      </form>
      {error && <p className="th-banner th-banner-err" role="alert">{error}</p>}
      {data && !data.supported && <p className="th-empty">{t("trackers.queue.unsupported", { word })}</p>}
      {data?.supported && data.entries.length === 0 && (
        <p className="th-empty">
          {t("trackers.queue.empty", { word })} <code className="th-branch">{data.base}</code>
          {t("trackers.queue.end")}
        </p>
      )}
      {data?.supported && data.entries.length > 0 && (
        <ol className="th-queue-list" aria-label={t("trackers.queue.listLabel", { word, base: String(data.base) })}>
          {data.entries.map((e) => (
            <li key={e.id}>
              <span className="th-queue-pos" aria-label={t("trackers.queue.position", { n: e.position })}>
                {e.position}
              </span>
              <span className="th-row-main">
                <button type="button" className="th-link-row th-queue-title" onClick={() => onOpen({ source, kind: "pr", id: String(e.number) })}>
                  <span className="th-num">#{e.number}</span> {e.title}
                </button>
                <span className="th-row-meta">
                  <span className={`th-badge th-queue-${e.status}`}>{e.status.replace(/_/g, " ")}</span>
                  {e.pipeline && <span className={`th-badge th-checks-${e.pipeline === "success" ? "pass" : e.pipeline === "failed" ? "fail" : "pending"}`}>{t("trackers.queue.pipeline", { status: e.pipeline })}</span>}
                  {e.eta_seconds ? <span>{t("trackers.queue.mergesIn", { eta: eta(e.eta_seconds) })}</span> : null}
                  {e.enqueued_at && <span>{t("trackers.queue.queued", { ago: ago(e.enqueued_at) })}</span>}
                  {e.author && <span>{t("trackers.hub.by", { name: e.author })}</span>}
                </span>
              </span>
              {e.author && <Avatar name={e.author} />}
              <button type="button" className="b no th-queue-remove" onClick={() => setRemoving(e)} aria-label={t("trackers.queue.removeLabel", { n: e.number, word })}>
                {t("trackers.queue.remove")}
              </button>
            </li>
          ))}
        </ol>
      )}
      {removing && (
        <Modal className="th-confirm" aria-labelledby="th-dequeue-title" onCancel={() => setRemoving(undefined)}>
          <h2 id="th-dequeue-title">{t("trackers.queue.confirmTitle", { n: removing.number, word })}</h2>
          <p>
            {t("trackers.queue.leaves", { title: removing.title, word })} <code className="th-branch">{data?.base}</code>
            {source === "gitlab" ? t("trackers.queue.leavesEndTrain") : t("trackers.queue.leavesEnd")}
          </p>
          <div className="btnrow">
            <button className="b no grow" id="th-dequeue-go" autoFocus disabled={busy} onClick={() => void remove(removing)}>
              {busy ? t("trackers.queue.removing") : t("trackers.queue.remove")}
            </button>
            <button className="b" onClick={() => setRemoving(undefined)}>
              {t("trackers.cancel")}
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}
