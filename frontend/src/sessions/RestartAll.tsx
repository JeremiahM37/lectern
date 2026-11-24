import { useEffect, useState } from "react";
import type { SessionsApi } from "./Sessions";
import { Modal } from "./Modal";
import { t, useLocale } from "../i18n";

interface RestartRow {
  id: number;
  name: string;
  status: "restarting" | "restarted" | "pending" | "skipped" | "failed";
  reason?: string;
}

// "Restart all sessions" (docs/sessions-restart.md): the confirm dialog shows
// what will happen to each session before anything is stopped, then follows
// the run, since idle sessions restart one at a time.
export function RestartAllDialog({
  api,
  onClose,
  onNotice,
}: {
  api: Pick<SessionsApi, "request">;
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  useLocale();
  const [rows, setRows] = useState<RestartRow[]>();
  const [started, setStarted] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    void api
      .request<{ plan: RestartRow[] }>("/restart-all")
      .then((res) => setRows(res.plan))
      .catch((err) => setError(String(err)));
  }, []);
  // While the run goes on, follow it.
  useEffect(() => {
    if (!started) return;
    const timer = window.setInterval(() => {
      void api
        .request<{ last: RestartRow[] }>("/restart-all")
        .then((res) => setRows(res.last))
        .catch(() => {});
    }, 3000);
    return () => clearInterval(timer);
  }, [started]);
  const count = (...status: string[]) => (rows || []).filter((row) => status.includes(row.status)).length;
  async function start() {
    try {
      const res = await api.request<{ sessions: RestartRow[] }>("/restart-all", { method: "POST", body: {} });
      setRows(res.sessions);
      setStarted(true);
      onNotice(t("sessions.restartAll.started", { now: count("restarting"), later: count("pending") }));
    } catch (err) {
      setError(String(err));
    }
  }
  return (
    <Modal id="restart-all" className="sheet" aria-label={t("sessions.restartAll.title")} onCancel={onClose}>
      <div className="sheet-head">
        <h2>{t("sessions.restartAll.title")}</h2>
        <button className="x" aria-label={t("sessions.restartAll.close")} data-close onClick={onClose}>
          ✕
        </button>
      </div>
      <p className="subhint">{t("sessions.restartAll.hint")}</p>
      {!rows && !error && <p>…</p>}
      {rows && (
        <>
          <p id="restart-all-counts">
            {started
              ? t("sessions.restartAll.progress", { done: count("restarted"), now: count("restarting"), later: count("pending"), skipped: count("skipped", "failed") })
              : t("sessions.restartAll.counts", { now: count("restarting"), later: count("pending"), skipped: count("skipped") })}
          </p>
          <ul className="restart-all-list" id="restart-all-list">
            {rows.map((row) => (
              <li key={row.id} data-status={row.status}>
                {row.name || `#${row.id}`}: {t(`sessions.restartAll.status.${row.status}`)}
                {row.reason && row.status !== "pending" ? ` (${row.reason})` : ""}
              </li>
            ))}
          </ul>
        </>
      )}
      <p id="restart-all-error" role="status">
        {error}
      </p>
      {!started ? (
        <button className="b ok" id="restart-all-go" disabled={!rows || count("restarting", "pending") === 0} onClick={() => void start()}>
          {t("sessions.restartAll.confirm")}
        </button>
      ) : (
        <button className="b" id="restart-all-done" onClick={onClose}>
          {t("sessions.restartAll.close")}
        </button>
      )}
    </Modal>
  );
}
