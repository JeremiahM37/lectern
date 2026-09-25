import { useEffect, useMemo, useRef, useState } from "react";
import type { Claim, Project, TaskView } from "../types";
import type { JsonValue } from "../api";
import { CreateTask } from "./CreateTask";
import { Routines } from "./Routines";
import { TaskDetail } from "./TaskDetail";
import { ClaimsPanel } from "../claims/ClaimsPanel";
import { QuotaChip } from "../sessions/QuotaChip";
import { contextClass, formatCost, formatTokens, resultUsage } from "../sessions/usageFormat";
import "./board.css";

type QuickMode = "dispatch" | "orchestrate";
type OrchestrationView = {
  orchestrate_ready: boolean;
  settings?: { lead_agent?: string; worker_agent?: string };
};
function readQuickMode(): QuickMode {
  try {
    return localStorage.getItem("adk-quick-mode") === "orchestrate" ? "orchestrate" : "dispatch";
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
    [orchestration, setOrchestration] = useState<OrchestrationView>();
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
      onNotice("Orchestrate needs Delegated builds ON — open Settings", true);
      return;
    }
    try {
      const task = await api.createTask({
        project_id: project,
        title: text.slice(0, 70),
        prompt: text,
        ...(orchestrate ? { orchestrate: true } : {}),
      });
      await api.taskAction(task.id, "dispatch", {});
      setPrompt((current) => current === text ? "" : current);
      onNotice(`${orchestrate ? "Orchestrating" : "Dispatched"} — ${text.slice(0, 40)}`);
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
          `${data.status} → ${column}: not a thing. Drag to queued or done.`,
          true,
        );
      await refresh();
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  async function clear(column: "done" | "failed") {
    if (!confirm(`Clear every ${column} card?`)) return;
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
          {column}
          <span className="cnt">{rows.length}</span>
          {(column === "done" || column === "failed") && rows.length > 0 && (
            <button className="col-clear" onClick={() => void clear(column)}>
              clear
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
                aria-label={`Delete ${task.title}`}
                onClick={(e) => {
                  e.stopPropagation();
                  if (task.status === "done" || confirm(`Delete “${task.title}”?`))
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
                aria-label="Chat with this task"
                onClick={(e) => {
                  e.stopPropagation();
                  onChat(task);
                }}
              >
                Chat
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
                      {u.costUSD != null && <span className="chip cost">{formatCost(u.costUSD)}</span>}
                      {u.costUSD == null && u.outputTokens != null && (
                        <span className="chip">{formatTokens(u.outputTokens)} tok</span>
                      )}
                      {u.contextPct != null && (
                        <span
                          className={`ctxbar ctx-used ${contextClass(u.contextPct)}`}
                          title={`${formatTokens(u.contextTokens)} / ${formatTokens(u.contextSize)} tokens used`}
                        >
                          ctx <i><b style={{ width: `${u.contextPct}%` }} /></i> {u.contextPct}%
                        </span>
                      )}
                    </>
                  );
                })()}
                {typeof task.attempt?.verify?.cmd === "string" && <span className={`chip ${task.attempt.verify.rc === 0 ? "ds" : "bad"}`}>{task.attempt.verify.rc === 0 ? "✓ verified" : "✗ verify"}</span>}
                {task.priority >= 3 && (
                  <span className="chip warn">▲ high</span>
                )}
                {task.agent && task.agent !== "claude" && <span className="chip tgt">{task.agent}</span>}
                {task.labels?.includes("orchestrated") && <span className="chip orch">✦ orchestrated</span>}
                {task.attempts.length > 1 && (
                  <span className="chip info">⑂ ×{task.attempts.length}</span>
                )}
              </div>
            </article>
          ))}
          {rows.length === 0 && <div className="col-empty">Nothing here</div>}
          {rows.length > shown.length && (
            <button className="col-more" onClick={() => setShowDone(true)}>
              show {rows.length - shown.length} more
            </button>
          )}
        </div>
      </section>
    );
  };
  return (
    <section>
      <div className="page-heading">
        <h2>Task board</h2>
        <QuotaChip api={api} />
        <div className="btnrow">
          <button id="qb-routines" onClick={() => setSheet("routines")}>Routines</button>
          <button id="qb-claims" onClick={() => setSheet("claims")}>Claims</button>
          {/* A phone has the floating button under the thumb; one is enough. */}
          <button className="wide-only-control" onClick={() => setSheet("new")}>
            + New task
          </button>
        </div>
      </div>
      <div id="quickbar" className={mode === "orchestrate" ? "orchestrate" : ""}>
        <div id="qb-mode" role="radiogroup" aria-label="Quick bar mode">
          <button
            type="button"
            role="radio"
            aria-checked={mode === "dispatch"}
            className={mode === "dispatch" ? "on" : ""}
            onClick={() => setMode("dispatch")}
            title="One agent takes the task as written"
          >
            ⚡ Dispatch
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={mode === "orchestrate"}
            className={mode === "orchestrate" ? "on" : ""}
            onClick={() => setMode("orchestrate")}
            title="A lead plans it, the worker builds it, the lead reviews and merges"
          >
            ✦ Orchestrate
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
              ? "Describe the outcome, hit ⏎ — Lectern plans, builds and reviews it"
              : "Describe it, hit ⏎ — instant dispatch"
          }
        />
        <input
          id="qb-filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter…"
        />
      </div>
      {mode === "orchestrate" && orchestration && (
        <div id="qb-orch-hint" className={orchestration.orchestrate_ready ? "" : "off"}>
          {orchestration.orchestrate_ready ? (
            <>
              A lead ({orchestration.settings?.lead_agent || "the project's agent"}) writes the plan and brief, <b>{orchestration.settings?.worker_agent}</b> builds
              it in its own worktree, the lead reviews, fixes and merges it into this task. Want to pick the lead, model or permissions?{" "}
              <button type="button" className="linkish" onClick={() => setSheet("new")}>Open the full form</button>.
            </>
          ) : (
            <>
              Orchestrate needs <b>Delegated builds ON</b> with a worker that answers — <a href="#targets">open Settings</a>. Dispatch still works.
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
            {c}
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
          api={api}
          onClose={() => setSheet(undefined)}
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
