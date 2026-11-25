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

interface UpdateView {
  current: string;
  latest?: string;
  available: boolean;
  can_update: boolean;
  reason?: string;
  phase?: string;
  target?: string;
  error?: string;
  sessions_restart?: string;
}

type UpdatePhase = "idle" | "installing" | "restarting" | "sessions" | "failed";

const bare = (v: string) => v.replace(/^v/, "");

// "Restart all sessions" (docs/sessions-restart.md): the confirm dialog shows
// what will happen to each session before anything is stopped, then follows
// the run, since idle sessions restart one at a time. When a newer Lectern
// is out, the same dialog updates the server first: it installs the release,
// restarts in place, and the new server restarts every session.
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
  const [update, setUpdate] = useState<UpdateView>();
  const [phase, setPhase] = useState<UpdatePhase>("idle");
  const [target, setTarget] = useState("");
  const [updateError, setUpdateError] = useState("");
  useEffect(() => {
    void api
      .request<UpdateView>("/update")
      .then(setUpdate)
      .catch(() => {});
  }, []);
  // While the server restarts on the new release, wait for it to answer.
  useEffect(() => {
    if (phase !== "restarting") return;
    const timer = window.setInterval(() => {
      void api
        .request<UpdateView>("/update")
        .then((res) => {
          if (bare(res.current) === bare(target)) {
            setUpdate(res);
            setPhase("sessions");
            setStarted(true);
          }
        })
        .catch(() => {});
    }, 2000);
    return () => clearInterval(timer);
  }, [phase, target]);
  async function runUpdate() {
    setPhase("installing");
    setUpdateError("");
    setTarget(update?.latest || "");
    try {
      const res = await api.request<{ to: string }>("/update", { method: "POST", body: { restart_sessions: true } });
      setTarget(res.to);
      setPhase("restarting");
    } catch (err) {
      setUpdateError(String(err));
      setPhase("failed");
    }
  }
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
      <section id="update-lectern" data-phase={phase}>
        {!update && <p className="subhint">{t("sessions.update.checking")}</p>}
        {update && phase === "idle" && !update.available && <p id="update-status">{t("sessions.update.current", { current: update.current })}</p>}
        {update && phase === "idle" && update.available && (
          <>
            <p id="update-status">{t("sessions.update.available", { latest: update.latest || "", current: update.current })}</p>
            {update.can_update ? (
              <>
                <p className="subhint">{t("sessions.update.hint")}</p>
                <button className="b ok" id="update-go" onClick={() => void runUpdate()}>
                  {t("sessions.update.go")}
                </button>
              </>
            ) : (
              <p className="subhint" id="update-blocked">
                {t("sessions.update.cannot", { reason: update.reason || "" })}
              </p>
            )}
          </>
        )}
        {phase === "installing" && <p id="update-status" role="status">{t("sessions.update.installing", { target: target || update?.latest || "" })}</p>}
        {phase === "restarting" && <p id="update-status" role="status">{t("sessions.update.restarting", { target })}</p>}
        {phase === "sessions" && <p id="update-status" role="status">{t("sessions.update.done", { current: update?.current || target })}</p>}
        {phase === "failed" && (
          <p id="update-status" role="alert">
            {t("sessions.update.failed", { error: updateError })}
          </p>
        )}
      </section>
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
        <button
          className={update?.available && update.can_update ? "b" : "b ok"}
          id="restart-all-go"
          disabled={!rows || count("restarting", "pending") === 0 || phase === "installing" || phase === "restarting"}
          onClick={() => void start()}
        >
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
