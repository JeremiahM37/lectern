import { useEffect, useRef, useState } from "react";
import { createDeckApi, withToken } from "../api";
import { t, useLocale } from "../i18n";
import type { Approval, Event as AgentEvent, TaskView } from "../types";
import { ApprovalCard, decisionBody, type ApprovalDecisionOptions } from "../sessions/ApprovalCard";
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
  onOpenSession,
  onOpenTask,
  onSettings,
}: {
  rows: Approval[];
  api: Api;
  onChanged: () => void;
  onNotice: (message: string, error?: boolean) => void;
  onOpenSession?: (id: number) => void;
  onOpenTask?: (id: number) => void;
  onSettings?: () => void;
}) {
  useLocale();
  // Whether agents are set to ask at all: an empty page on a bypass default
  // says so, instead of looking like nothing will ever arrive here.
  const [asks, setAsks] = useState<boolean>();
  useEffect(() => {
    void api
      .request<Record<string, string>>("/settings")
      .then((settings) => setAsks(settings.session_permission_mode === "ask"))
      .catch(() => setAsks(undefined));
  }, [api]);
  async function decide(row: Approval, decision: "approved" | "denied", opts?: ApprovalDecisionOptions) {
    try {
      await api.request(`/approvals/${row.id}/decision`, { method: "POST", body: decisionBody(decision, opts) });
      onNotice(decision === "approved" ? t("app.approvals.approved") : t("app.approvals.denied"));
      onChanged();
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  return rows.length ? (
    <section className="list approvals-page" id="approvals-page">
      <div className="page-heading">
        <h2>{t("nav.approvals")}</h2>
        <p className="subhint">{t("approval.pageHint")}</p>
      </div>
      {rows.map((row, index) => (
        <ApprovalCard
          key={row.id}
          approval={row}
          showContext
          autoFocus={index === 0}
          onDecide={(decision, opts) => decide(row, decision, opts)}
          onOpenSession={onOpenSession}
          onOpenTask={onOpenTask}
        />
      ))}
    </section>
  ) : (
    <div className="hint empty-state" id="approvals-empty">
      <strong>{t("app.approvals.empty")}</strong>
      <br />
      {t("app.approvals.emptyHint")}
      {asks === false && (
        <p className="subhint" id="approvals-ask-off">
          {t("approval.askOff")}{" "}
          {onSettings && (
            <button type="button" className="linkish" onClick={onSettings}>
              {t("approval.askOffLink")}
            </button>
          )}
        </p>
      )}
    </div>
  );
}
