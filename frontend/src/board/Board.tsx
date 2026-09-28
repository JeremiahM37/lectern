import { LimitChip } from "../limits/LimitBanner";
import { useEffect, useMemo, useRef, useState } from "react";
import type { Claim, Project, TaskView } from "../types";
import type { JsonValue } from "../api";
import { CreateTask } from "./CreateTask";
import { Routines } from "./Routines";
import { TaskDetail } from "./TaskDetail";
import { ClaimsPanel } from "../claims/ClaimsPanel";
import { QuotaChip } from "../sessions/QuotaChip";
import { CIChip } from "../review/CIChip";
import { contextClass, formatResultCost, formatTokens, resultUsage } from "../sessions/usageFormat";
import { availableAgents, raceAgents } from "../remote/race";
import { t, useLocale } from "../i18n";
import "./board.css";

type QuickMode = "dispatch" | "orchestrate" | "race";
type OrchestrationView = {
  orchestrate_ready: boolean;
  settings?: { lead_agent?: string; worker_agent?: string };
};
function readQuickMode(): QuickMode {
  try {
    const saved = localStorage.getItem("adk-quick-mode");
    return saved === "orchestrate" || saved === "race" ? saved : "dispatch";
  } catch {
    return "dispatch";
  }
}

const columns = [
  "backlog",
  "queued",
  "running",
  "review",
  "done",
  "failed",
] as const;
type Column = (typeof columns)[number];
// A board status as people read it; anything unknown shows as the server sent it.
function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    backlog: t("board.status.backlog"),
    queued: t("board.status.queued"),
    running: t("board.status.running"),
    review: t("board.status.review"),
    done: t("board.status.done"),
    failed: t("board.status.failed"),
    cancelled: t("board.status.cancelled"),
  };
  return labels[status] ?? status;
}
export interface BoardApi {
  tasks: (signal?: AbortSignal) => Promise<TaskView[]>;
  task: (id: number, signal?: AbortSignal) => Promise<TaskView>;
  projects: (signal?: AbortSignal) => Promise<Project[]>;
  createTask: (body: {
    project_id: number;
    title: string;
    prompt: string;
    priority?: number;
    agent?: string;
    model?: string;
    permission_mode?: string;
    orchestrate?: boolean;
    budget_usd?: number;
  }) => Promise<TaskView>;
  taskAction: (
    id: number,
    action: "dispatch" | "complete",
    body?: Record<string, never>,
  ) => Promise<unknown>;
  request: <T>(
    path: string,
    options?: { method?: string; body?: JsonValue; signal?: AbortSignal },
  ) => Promise<T>;
  // Claim board (docs/claims.md) — see ClaimsPanel.
  claims: (
    filter?: { repo_key?: string; project_id?: number; session_id?: number; attempt_id?: number },
    signal?: AbortSignal,
  ) => Promise<Claim[]>;
  createClaim: (body: {
    repo_key?: string;
    project_id?: number;
    session_id?: number;
    attempt_id?: number;
    scope_kind: "task" | "paths" | "topic";
    scope?: string;
    paths?: string[];
    holder?: string;
    intent?: string;
    ttl_minutes?: number;
  }) => Promise<Claim>;
  releaseClaim: (id: number) => Promise<{ released: boolean }>;
  extendClaim: (id: number, ttlMinutes?: number) => Promise<Claim>;
}
export interface BoardProps {
  api: BoardApi;
  onOpenTask: (task: TaskView) => void;
  onChat: (task: TaskView) => void;
  onNotice: (message: string, error?: boolean) => void;
  onOpenSession?: (id: number) => void;
  onOpenTerminal?: (url: string, title: string) => void;
  refreshVersion?: number;
  openTaskId?: number;
  openTaskVersion?: number;
  newTaskVersion?: number;
  routinesVersion?: number;
  onExternalActionConsumed?: (kind: "task" | "new" | "routines") => void;
}

export function Board({
  api,
  onOpenTask,
  onChat,
  onNotice,
  onOpenSession = () => {},
  onOpenTerminal = () => {},
  refreshVersion = 0,
  openTaskId,
  openTaskVersion = 0,
  newTaskVersion = 0,
  routinesVersion = 0,
  onExternalActionConsumed = () => {},
}: BoardProps) {
  useLocale();
  const refreshSequence = useRef(0);
  const [tasks, setTasks] = useState<TaskView[]>([]),
    [projects, setProjects] = useState<Project[]>([]),
    [filter, setFilter] = useState(""),
    [project, setProject] = useState(0),
    [prompt, setPrompt] = useState(""),
    [mobile, setMobile] = useState<Column>("review"),
    [mobilePinned, setMobilePinned] = useState(false),
    [narrow, setNarrow] = useState(() => window.matchMedia("(max-width: 700px)").matches),
    [showDone, setShowDone] = useState(false),
    [sheet, setSheet] = useState<"new" | "routines" | "claims" | number>(),
    // The quick bar has two modes. Dispatch is the classic one-agent task;
    // Orchestrate hands the description to a lead that plans it, has the
    // delegated-build worker build it, reviews and integrates. The choice is
    // remembered per device, like the rest of the board's preferences.
    [mode, setMode] = useState<QuickMode>(() => readQuickMode()),
    [orchestration, setOrchestration] = useState<OrchestrationView>(),
    // Race N agents lands in the new task's Compare view.
    [compareTask, setCompareTask] = useState<number>();
  async function refresh(signal?: AbortSignal) {
    const sequence = ++refreshSequence.current;
    const [next, ps] = await Promise.all([api.tasks(signal), api.projects(signal)]);
    if (signal?.aborted || sequence !== refreshSequence.current) return;
    setTasks(next);
    setSheet((current) => typeof current === "number" && !next.some((task) => task.id === current) ? undefined : current);
    setProjects(ps);
    setProject((old) => old || ps[0]?.id || 0);
  }
  useEffect(() => {
    try {
      localStorage.setItem("adk-quick-mode", mode);
    } catch {
      /* private mode: the choice lasts the page */
    }
  }, [mode]);
  useEffect(() => {
    // Whether Orchestrate would actually run; the bar says so instead of
    // letting ⏎ fail with an API error.
    void api
      .request<OrchestrationView>("/delegation")
      .then((v) => setOrchestration(v && typeof v.orchestrate_ready === "boolean" ? v : { orchestrate_ready: false }))
      .catch(() => setOrchestration({ orchestrate_ready: false }));
  }, [refreshVersion]);
  useEffect(() => {
    const media = window.matchMedia("(max-width: 700px)");
    const update = () => setNarrow(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal).catch((e) => {
      if (!controller.signal.aborted) onNotice(String(e), true);
    });
    return () => controller.abort();
  }, [refreshVersion]);
  useEffect(() => {
    if (openTaskId) { setSheet(openTaskId); onExternalActionConsumed("task"); }
  }, [openTaskId, openTaskVersion]);
  useEffect(() => {
    if (newTaskVersion) { setSheet("new"); onExternalActionConsumed("new"); }
  }, [newTaskVersion]);
  useEffect(() => {
    if (routinesVersion) { setSheet("routines"); onExternalActionConsumed("routines"); }
  }, [routinesVersion]);
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return q
      ? tasks.filter((t) =>
          `${t.title} ${t.project_name} ${t.target_name}`
            .toLowerCase()
            .includes(q),
        )
      : tasks;
  }, [tasks, filter]);
  useEffect(() => {
    if (mobilePinned) return;
    const preferred: Column[] = ["review", "running", "queued", "backlog", "failed", "done"];
    const useful = preferred.find((column) => visible.some((task) => task.status === column));
    if (useful) setMobile(useful);
  }, [visible, mobilePinned]);
  async function dispatch() {
    const text = prompt.trim();
    if (!text || !project) return;
    const orchestrate = mode === "orchestrate";
    if (orchestrate && orchestration && !orchestration.orchestrate_ready) {
      onNotice(t("board.orchestrateNeedsDelegation"), true);
      return;
    }
    try {
      const task = await api.createTask({
        project_id: project,
        title: text.slice(0, 70),
        prompt: text,
        ...(orchestrate ? { orchestrate: true } : {}),
      });
      if (mode === "race") {
        // Three attempts of the same prompt: different agents where the
        // machine has them, each in its own worktree.
        const p = projects.find((x) => x.id === project);
        let available: string[] = [];
        try {
          const targets = await api.request<{ id: number; info_json: string }[]>("/targets");
          available = availableAgents(targets.find((t) => t.id === p?.target_id)?.info_json);
        } catch {
          /* an unknown machine races the project's own agent */
        }
        const agents = raceAgents(3, task.agent || "claude", available, "mixed");
        await api.request(`/tasks/${task.id}/dispatch`, {
          method: "POST",
          body: { variants: agents.map((agent) => ({ agent })) },
        });
        setPrompt((current) => current === text ? "" : current);
        onNotice(t("remote.race.racingAgents", { agents: agents.join(", "), text: text.slice(0, 40) }));
        await refresh();
        setCompareTask(task.id);
        setSheet(task.id);
        return;
      }
      await api.taskAction(task.id, "dispatch", {});
      setPrompt((current) => current === text ? "" : current);
      onNotice(orchestrate ? t("board.orchestrating", { text: text.slice(0, 40) }) : t("board.dispatched", { text: text.slice(0, 40) }));
      await refresh();
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  async function drop(event: React.DragEvent, column: Column) {
    event.preventDefault();
    const raw = event.dataTransfer.getData("text/lec-task");
    if (!raw) return;
    const data = JSON.parse(raw) as { id: number; status: string };
    try {
      if (
        column === "queued" &&
        ["backlog", "failed", "cancelled"].includes(data.status)
      )
        await api.taskAction(data.id, "dispatch", {});
      else if (column === "done" && data.status === "review")
        await api.taskAction(data.id, "complete", {});
      else
        return onNotice(
          t("board.badDrop", { from: statusLabel(data.status), to: statusLabel(column) }),
          true,
        );
      await refresh();
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  async function clear(column: "done" | "failed") {
    if (!confirm(t("board.clearConfirm", { status: statusLabel(column) }))) return;
    await api.request("/tasks/clear", {
      method: "POST",
      body: { statuses: [column] },
    });
    await refresh();
  }
  const render = (column: Column) => {
    const rows = visible.filter((t) => t.status === column),
      shown = column === "done" && !showDone ? rows.slice(0, 15) : rows;
    return (
      <section
        className={`col s-${column} ${mobile === column ? "mobile-on" : "mobile-off"}`}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => void drop(e, column)}
      >
        <header className="col-head">
          {statusLabel(column)}
          <span className="cnt">{rows.length}</span>
          {(column === "done" || column === "failed") && rows.length > 0 && (
            <button className="col-clear" onClick={() => void clear(column)}>
              {t("board.clear")}
            </button>
          )}
        </header>
        <div className="col-body">
          {shown.map((task) => (
            <article
              key={task.id}
              className={`card s-${task.status}`}
              draggable
              onDragStart={(e) =>
                e.dataTransfer.setData(
                  "text/lec-task",
                  JSON.stringify({ id: task.id, status: task.status }),
                )
              }
              onClick={() => {
                onOpenTask(task);
                setSheet(task.id);
              }}
            >
              <button
                className="card-x"
                aria-label={t("board.card.deleteLabel", { title: task.title })}
                onClick={(e) => {
                  e.stopPropagation();
                  if (task.status === "done" || confirm(t("board.card.deleteConfirm", { title: task.title })))
                    void api
                      .request(`/tasks/${task.id}`, { method: "DELETE" })
                      .then(() => refresh());
                }}
              >
                ✕
              </button>
              <div className="t">{task.title}</div>
              <button
                className="b task-chat"
                aria-label={t("board.card.chatLabel")}
                onClick={(e) => {
                  e.stopPropagation();
                  onChat(task);
                }}
              >
                {t("board.card.chat")}
              </button>
              <div className="meta">
                <span className="chip">{task.project_name}</span>
                <span className="chip tgt">{task.target_name}</span>
                {Array.isArray(task.attempt?.diff_stat) && task.attempt.diff_stat.length > 0 && (() => {
                  const stats = task.attempt!.diff_stat as Array<{ additions?: number; deletions?: number }>;
                  return <span className="chip ds">+{stats.reduce((n, row) => n + (row.additions || 0), 0)} <b>−{stats.reduce((n, row) => n + (row.deletions || 0), 0)}</b></span>;
                })()}
                {(() => {
                  const u = resultUsage(task.attempt?.result);
                  return (
                    <>
                      {u.costUSD != null && (
                        <span className="chip cost" title={u.costEstimated ? t("board.usage.costEstimated") : undefined}>
                          {formatResultCost(u)}
                        </span>
                      )}
                      {u.costUSD == null && u.outputTokens != null && (
                        <span className="chip">{t("board.usage.tokens", { tokens: formatTokens(u.outputTokens) })}</span>
                      )}
                      {u.contextPct != null && (
                        <span
                          className={`ctxbar ctx-used ${contextClass(u.contextPct)}`}
                          title={t("board.usage.contextUsed", { used: formatTokens(u.contextTokens), size: formatTokens(u.contextSize) })}
                        >
                          {t("board.usage.ctx")} <i><b style={{ width: `${u.contextPct}%` }} /></i> {u.contextPct}%
                        </span>
                      )}
                    </>
                  );
                })()}
                <CIChip ci={task.ci} />
                {typeof task.attempt?.verify?.cmd === "string" && <span className={`chip ${task.attempt.verify.rc === 0 ? "ds" : "bad"}`}>{task.attempt.verify.rc === 0 ? t("board.card.verified") : t("board.card.verifyFailed")}</span>}
                {task.priority >= 3 && (
                  <span className="chip warn">{t("board.card.highPriority")}</span>
                )}
                {task.agent && task.agent !== "claude" && <span className="chip tgt">{task.agent}</span>}
                {task.limit && <LimitChip hold={task.limit} />}
                {task.labels?.includes("orchestrated") && <span className="chip orch">{t("board.card.orchestrated")}</span>}
                {task.attempts.length > 1 && (
                  <span className="chip info">⑂ ×{task.attempts.length}</span>
                )}
              </div>
            </article>
          ))}
          {rows.length === 0 && <div className="col-empty">{t("board.empty")}</div>}
          {rows.length > shown.length && (
            <button className="col-more" onClick={() => setShowDone(true)}>
              {t("board.showMore", { n: rows.length - shown.length })}
            </button>
          )}
        </div>
      </section>
    );
  };
  return (
    <section>
      <div className="page-heading">
        <h2>{t("board.title")}</h2>
        <QuotaChip api={api} />
        <div className="btnrow">
          <button id="qb-routines" onClick={() => setSheet("routines")}>{t("board.routines")}</button>
          <button id="qb-claims" onClick={() => setSheet("claims")}>{t("board.claims")}</button>
          {/* A phone has the floating button under the thumb; one is enough. */}
          <button className="wide-only-control" onClick={() => setSheet("new")}>
            + {t("board.newTask")}
          </button>
        </div>
      </div>
      <div id="quickbar" className={mode === "orchestrate" ? "orchestrate" : ""}>
        <div id="qb-mode" role="radiogroup" aria-label={t("board.quick.modeLabel")}>
          <button
            type="button"
            role="radio"
            aria-checked={mode === "dispatch"}
            className={mode === "dispatch" ? "on" : ""}
            onClick={() => setMode("dispatch")}
            title={t("board.quick.dispatchTitle")}
          >
            {t("board.quick.dispatch")}
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={mode === "orchestrate"}
            className={mode === "orchestrate" ? "on" : ""}
            onClick={() => setMode("orchestrate")}
            title={t("board.quick.orchestrateTitle")}
          >
            {t("board.quick.orchestrate")}
          </button>
          <button
            type="button"
            role="radio"
            id="qb-race"
            aria-checked={mode === "race"}
            className={mode === "race" ? "on" : ""}
            onClick={() => setMode("race")}
            title={t("remote.race.quickTitle")}
          >
            {t("remote.race.quick")}
          </button>
        </div>
        <select
          id="qb-project"
          value={project}
          onChange={(e) => setProject(Number(e.target.value))}
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <input
          id="qb-input"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void dispatch();
          }}
          placeholder={
            mode === "orchestrate"
              ? t("board.quick.orchestratePlaceholder")
              : mode === "race"
                ? t("remote.race.quickPlaceholder")
                : t("board.quick.dispatchPlaceholder")
          }
        />
        <input
          id="qb-filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={t("board.quick.filter")}
        />
      </div>
      {mode === "orchestrate" && orchestration && (
        <div id="qb-orch-hint" className={orchestration.orchestrate_ready ? "" : "off"}>
          {orchestration.orchestrate_ready ? (
            <>
              {t("board.quick.hintLead", { lead: orchestration.settings?.lead_agent || t("board.quick.hintProjectAgent") })}<b>{orchestration.settings?.worker_agent}</b>{t("board.quick.hintWorker")}{" "}
              <button type="button" className="linkish" onClick={() => setSheet("new")}>{t("board.quick.openFullForm")}</button>.
            </>
          ) : (
            <>
              {t("board.quick.needsBefore")}<b>{t("board.quick.needsDelegated")}</b>{t("board.quick.needsMiddle")}<a href="#settings/connections">{t("board.quick.openSettings")}</a>{t("board.quick.needsAfter")}
            </>
          )}
        </div>
      )}
      <div className="colstrip">
        {columns.map((c) => (
          <button
            key={c}
            className={`colchip s-${c} ${mobile === c ? "on" : ""}`}
            onClick={() => { setMobile(c); setMobilePinned(true); }}
          >
            {statusLabel(c)}
            <b>{visible.filter((t) => t.status === c).length}</b>
          </button>
        ))}
      </div>
      <div id="board">
        {columns.filter((c) => !narrow || c === mobile).map((c) => <div key={c} className="board-column">{render(c)}</div>)}
      </div>
      {sheet === "new" && (
        <CreateTask
          api={api}
          projects={projects}
          onClose={() => setSheet(undefined)}
          onCreated={() => void refresh()}
          onChat={(task) => { setSheet(task.id); onChat(task); }}
          onCompare={(task) => { setCompareTask(task.id); setSheet(task.id); }}
          onNotice={onNotice}
        />
      )}
      {sheet === "routines" && (
        <Routines
          api={api}
          projects={projects}
          onClose={() => setSheet(undefined)}
          onTask={(t) => setSheet(t.id)}
          onChanged={() => void refresh()}
          onNotice={onNotice}
        />
      )}
      {sheet === "claims" && (
        <ClaimsPanel
          api={api}
          projects={projects}
          onClose={() => setSheet(undefined)}
          onNotice={onNotice}
        />
      )}
      {typeof sheet === "number" && (
        <TaskDetail
          taskId={sheet}
          key={sheet}
          initialCompare={compareTask === sheet}
          api={api}
          onClose={() => { setSheet(undefined); setCompareTask(undefined); }}
          onChat={onChat}
          onOpenSession={onOpenSession}
          onTerminal={onOpenTerminal}
          onChanged={() => void refresh()}
          onNotice={onNotice}
          refreshVersion={refreshVersion}
        />
      )}
    </section>
  );
}
