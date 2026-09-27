import { useEffect, useRef, useState } from "react";
import { createDeckApi, withToken } from "../api";
import { t, useLocale } from "../i18n";
import type { Approval, Event as AgentEvent, TaskView } from "../types";
type Api = ReturnType<typeof createDeckApi>;
function text(value: unknown) {
  return typeof value === "string"
    ? value
    : value === undefined
      ? ""
      : JSON.stringify(value);
}
function eventLine(event: AgentEvent) {
  const p = event.payload;
  return event.type === "text"
    ? text(p.text)
    : event.type === "tool_use"
      ? `▸ ${text(p.name)} ${text(p.input).slice(0, 160)}`
      : event.type === "tool_result"
        ? `↳ ${text(p.content).slice(0, 80)}`
        : event.type === "verify"
          ? `verify ${p.rc === 0 ? "PASS" : "FAIL"}`
          : event.type === "result"
            ? `✔ ${text(p.result)}`
            : event.type;
}
function Pane({
  task,
  api,
  onTask,
}: {
  task: TaskView;
  api: Api;
  onTask: (id: number) => void;
}) {
  const [events, setEvents] = useState<AgentEvent[]>([]),
    [error, setError] = useState(""),
    log = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const abort = new AbortController(),
      stream = new EventSource(withToken(`/api/tasks/${task.id}/stream`));
    const add = (rows: AgentEvent[]) =>
      setEvents((old) => {
        const seen = new Set<number>();
        return [...old, ...rows]
          .filter((row) => !seen.has(row.id) && !!seen.add(row.id))
          .sort((a, b) => a.id - b.id)
          .slice(-40);
      });
    void api
      .taskEvents(task.id, abort.signal)
      .then((rows) => {
        if (!abort.signal.aborted) add(rows.slice(-15));
      })
      .catch((error) => {
        if (!abort.signal.aborted) setError(String(error));
      });
    stream.addEventListener("agent_event", (event) => {
      try {
        add([JSON.parse((event as MessageEvent<string>).data) as AgentEvent]);
      } catch {
        setError(t("app.deck.badEvent"));
      }
    });
    return () => {
      abort.abort();
      stream.close();
    };
  }, [task.id, api]);
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [events]);
  return (
    <div className={`pane s-${task.status}`}>
      <button className="pane-head" onClick={() => onTask(task.id)}>
        <span className="statpill">{task.status}</span>
        <span className="pane-title">{task.title}</span>
        <span className="pane-sub">{task.target_name}</span>
      </button>
      <div className="pane-log" ref={log}>
        {error && <p role="status">{error}</p>}
        {events.map((event) => (
          <div className="pane-line" key={event.id}>
            {eventLine(event)}
          </div>
        ))}
      </div>
    </div>
  );
}
export function Deck({
  tasks,
  api,
  onTask,
}: {
  tasks: TaskView[];
  api: Api;
  onTask: (id: number) => void;
}) {
  useLocale();
  const active = tasks
    .filter((task) => ["running", "review", "queued"].includes(task.status))
    .slice(0, 16);
  return active.length ? (
    <div id="deck">
      {active.map((task) => (
        <Pane key={task.id} task={task} api={api} onTask={onTask} />
      ))}
    </div>
  ) : (
    <div className="hint">
      {t("app.deck.empty")}
      <br />
      {t("app.deck.emptyHint")}
    </div>
  );
}
export function Approvals({
  rows,
  api,
  onChanged,
  onNotice,
}: {
  rows: Approval[];
  api: Api;
  onChanged: () => void;
  onNotice: (message: string, error?: boolean) => void;
}) {
  useLocale();
  const [busy, setBusy] = useState<number[]>([]);
  async function decide(
    id: number,
    decision: "approved" | "denied",
    always = false,
  ) {
    let note = "";
    if (decision === "denied") {
      const response = prompt(
        t("app.approvals.reasonPrompt"),
        "not safe, find another way",
      );
      if (response === null) return;
      note = response;
    }
    setBusy((old) => [...old, id]);
    try {
      await api.decideApproval(id, decision, note, always);
      onNotice(
        decision === "approved"
          ? t("app.approvals.approved")
          : t("app.approvals.denied"),
      );
      onChanged();
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy((old) => old.filter((value) => value !== id));
    }
  }
  return rows.length ? (
    <div className="list">
      {rows.map((row) => (
        <div className="rowcard" key={row.id}>
          <h3>
            {row.tool_name} <span>{t("app.approvals.wantsToRun")}</span>
          </h3>
          <div className="sub">
            {t("app.approvals.task", { id: row.task_id ?? "", title: row.task_title ?? "" })}
          </div>
          <pre>{JSON.stringify(row.input, null, 2).slice(0, 1200)}</pre>
          <div className="btnrow">
            <button
              className="b ok grow"
              disabled={busy.includes(row.id)}
              onClick={() => void decide(row.id, "approved")}
            >
              {t("app.approvals.approve")}
            </button>
            <button
              className="b ok"
              title={t("app.approvals.alwaysHint")}
              disabled={busy.includes(row.id)}
              onClick={() => void decide(row.id, "approved", true)}
            >
              {t("app.approvals.always")}
            </button>
            <button
              className="b no grow"
              disabled={busy.includes(row.id)}
              onClick={() => void decide(row.id, "denied")}
            >
              {t("app.approvals.deny")}
            </button>
          </div>
        </div>
      ))}
    </div>
  ) : (
    <div className="hint">
      {t("app.approvals.empty")}
      <br />
      {t("app.approvals.emptyHint")}
    </div>
  );
}
