import { useEffect, useState } from "react";
import { t, useLocale } from "../i18n";
import { Modal } from "../sessions/Modal";
import { errorText, type Notice, type TrackerApi } from "./api";
import { itemMark } from "./logic";
import type { ItemRef } from "./types";

export interface StartedWork {
  kind: "session" | "task";
  session?: { id: number; name: string };
  task?: { id: number; title: string };
  branch?: string;
}

/** Start an agent on a tracker item: an interactive session in its own
 * worktree on a suggested branch, or a task on the board. The item's text is
 * read by the server, so only which item is meant travels from here. */
export function StartWork({
  api,
  projectId,
  item,
  title,
  branch: suggested,
  pr,
  onClose,
  onStarted,
  onNotice,
}: {
  api: TrackerApi;
  projectId: number;
  item: ItemRef;
  title: string;
  branch?: string;
  /** a pull request: always a session, on the PR's own branch */
  pr?: boolean;
  onClose(): void;
  onStarted(result: StartedWork): void;
  onNotice: Notice;
}) {
  useLocale();
  const [mode, setMode] = useState<"session" | "task">("session");
  const [agents, setAgents] = useState<string[]>([]);
  const [agent, setAgent] = useState("");
  const [branch, setBranch] = useState(suggested || "");
  const [base, setBase] = useState("");
  const [note, setNote] = useState("");
  const [dispatch, setDispatch] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api
      .request<{ name: string; kind?: string }[]>("/agents")
      .then((rows) => setAgents(rows.map((r) => r.name).filter((n) => n && n !== "shell")))
      .catch(() => setAgents([]));
  }, [api]);
  async function start() {
    setBusy(true);
    try {
      const body = {
        source: item.source,
        kind: item.kind,
        id: item.id,
        connection_id: item.connection_id ?? 0,
        mode: pr ? "session" : mode,
        agent,
        branch: pr ? "" : branch.trim(),
        base: base.trim(),
        note: note.trim(),
        dispatch,
      };
      const res = await api.request<StartedWork>(`/projects/${projectId}/work/start`, { method: "POST", body });
      onStarted(res);
    } catch (error) {
      onNotice(errorText(error), true);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal className="th-start" aria-labelledby="th-start-title" onCancel={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void start();
        }}
      >
        <header className="sheet-head">
          <h2 id="th-start-title">{t("trackers.start.title", { mark: itemMark(item) })}</h2>
          <button type="button" className="x" aria-label={t("trackers.close")} onClick={onClose}>
            ×
          </button>
        </header>
        <p className="th-muted th-start-title">{title}</p>
        {!pr && (
          <div className="th-seg" role="radiogroup" aria-label={t("trackers.start.startAs")}>
            <button type="button" role="radio" aria-checked={mode === "session"} className={mode === "session" ? "on" : ""} onClick={() => setMode("session")}>
              {t("trackers.start.session")}
              <small>{t("trackers.start.sessionHint")}</small>
            </button>
            <button type="button" role="radio" aria-checked={mode === "task"} className={mode === "task" ? "on" : ""} onClick={() => setMode("task")}>
              {t("trackers.start.task")}
              <small>{t("trackers.start.taskHint")}</small>
            </button>
          </div>
        )}
        <label>
          {t("trackers.start.agent")}
          <select value={agent} onChange={(e) => setAgent(e.target.value)}>
            <option value="">{t("trackers.start.projectDefault")}</option>
            {agents.map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </select>
        </label>
        {pr ? (
          <p className="th-muted">{t("trackers.start.prHint")}</p>
        ) : mode === "session" ? (
          <label>
            {t("trackers.start.branch")}
            <input id="th-start-branch" value={branch} onChange={(e) => setBranch(e.target.value)} spellCheck={false} autoCapitalize="off" />
          </label>
        ) : null}
        {!pr && (
          <label>
            {t("trackers.start.baseBranch")}
            <input value={base} onChange={(e) => setBase(e.target.value)} placeholder={t("trackers.start.basePlaceholder")} spellCheck={false} autoCapitalize="off" />
          </label>
        )}
        <label>
          {t("trackers.start.note")}
          <textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder={t("trackers.start.notePlaceholder")} />
        </label>
        {!pr && mode === "task" && (
          <label className="th-check">
            <input type="checkbox" checked={dispatch} onChange={(e) => setDispatch(e.target.checked)} /> {t("trackers.start.dispatchNow")}
          </label>
        )}
        <div className="btnrow">
          <button type="submit" className="b ok grow" id="th-start-go" disabled={busy}>
            {busy
              ? t("trackers.starting")
              : pr || mode === "session"
                ? t("trackers.startSession")
                : dispatch
                  ? t("trackers.start.createAndDispatch")
                  : t("trackers.start.createTask")}
          </button>
          <button type="button" className="b" onClick={onClose}>
            {t("trackers.cancel")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
