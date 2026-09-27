import { useEffect, useState } from "react";
import type { Project, TaskView } from "../types";
import type { BoardApi } from "./Board";
import { Modal } from "../sessions/Modal";
import { t, useLocale } from "../i18n";
import "./board.css";

interface Routine {
  id: number;
  name: string;
  prompt: string;
  project_ids: number[];
  schedule: string;
  permission_mode: string;
  agent: string;
  model: string;
  enabled: boolean;
  next_run_at?: number;
}
interface RunOutput {
  tasks: unknown[];
  failed: unknown[];
}
export function Routines({
  api,
  projects,
  onClose,
  onTask,
  onChanged,
  onNotice,
}: {
  api: BoardApi;
  projects: Project[];
  onClose(): void;
  onTask(t: TaskView): void;
  onChanged(): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [rows, setRows] = useState<Routine[]>([]),
    [runs, setRuns] = useState<TaskView[]>([]),
    [editing, setEditing] = useState<number>();
  const [name, setName] = useState(""),
    [prompt, setPrompt] = useState(""),
    [ids, setIds] = useState<number[]>([]),
    [schedule, setSchedule] = useState(""),
    [permission, setPermission] = useState("acceptEdits"),
    [agent, setAgent] = useState(""),
    [model, setModel] = useState("");
  async function load() {
    const [r, all] = await Promise.all([
      api.request<Routine[]>("/routines"),
      api.tasks(),
    ]);
    setRows(r);
    setRuns(
      all.filter(
        (x) =>
          x.created_by?.startsWith("routine:") &&
          (["queued", "running", "review"].includes(x.status) ||
            Boolean(x.takeover)),
      ),
    );
  }
  useEffect(() => {
    void load().catch((e) => onNotice(String(e), true));
  }, []);
  function reset() {
    setEditing(undefined);
    setName("");
    setPrompt("");
    setIds([]);
    setSchedule("");
    setPermission("acceptEdits");
    setAgent("");
    setModel("");
  }
  function edit(r: Routine) {
    setEditing(r.id);
    setName(r.name);
    setPrompt(r.prompt);
    setIds(r.project_ids);
    setSchedule(r.schedule);
    setPermission(r.permission_mode || "acceptEdits");
    setAgent(r.agent || "");
    setModel(r.model || "");
  }
  async function save() {
    if (!ids.length) {
      onNotice(t("board.routines.pickProject"), true);
      return;
    }
    const body = {
      name: name.trim(),
      prompt: prompt.trim(),
      project_ids: ids,
      schedule: schedule.trim(),
      permission_mode: permission,
      agent,
      model: model.trim(),
    };
    await api.request(editing ? `/routines/${editing}` : "/routines", {
      method: editing ? "PATCH" : "POST",
      body,
    });
    onNotice(editing ? t("board.routines.updated") : t("board.routines.saved"));
    reset();
    await load();
  }
  async function run(r: Routine) {
    const x = await api.request<RunOutput>(`/routines/${r.id}/run`, {
      method: "POST",
    });
    onNotice(
      t("board.routines.started", { count: x.tasks.length }) + (x.failed.length ? t("board.routines.couldNotRun", { n: x.failed.length }) : ""),
    );
    onChanged();
    await load();
  }
  return (
    <Modal id="sheet" open className="sheet routines" aria-label={t("board.routines")} onCancel={onClose}>
      <header className="sheet-head">
        <h2>{t("board.routines")}</h2>
        <button onClick={onClose}>✕</button>
      </header>
      <p>
        {t("board.routines.intro")}
      </p>
      {runs.length > 0 && (
        <section>
          <h3>{t("board.routines.startedRuns")}</h3>
          {runs.map((task) => (
            <button key={task.id} onClick={() => onTask(task)}>
              {task.title} · {task.project_name} ·{" "}
              {task.takeover?.status === "ready" ? t("board.routines.interactive") : t(`board.status.${task.status}`, undefined, task.status)}
            </button>
          ))}
        </section>
      )}
      {rows.length === 0 && (
        <p>
          {t("board.routines.empty")}
        </p>
      )}
      <div id="rt-list">{rows.map((r) => (
        <article className="rowcard" key={r.id}>
          <h3>
            {r.name}
            {!r.enabled && t("board.routines.off")}
          </h3>
          <p>
            {r.project_ids
              .map((id) => projects.find((p) => p.id === id)?.name)
              .filter(Boolean)
              .join(", ") || t("board.routines.noProjects")}
          </p>
          <p>
            {r.agent || t("board.routines.projectDefault")}
            {r.model && ` · ${r.model}`} · {r.schedule || t("board.routines.manualOnly")}
            {r.schedule && t("board.routines.next", { when: r.next_run_at ? new Date(r.next_run_at * 1000).toLocaleString() : t("board.routines.pending") })}
          </p>
          <div className="btnrow">
            <button
              onClick={() =>
                void run(r).catch((e) => onNotice(String(e), true))
              }
            >
              {t("board.routines.runNow")}
            </button>
            {r.schedule && (
              <button
                onClick={() =>
                  void api
                    .request(`/routines/${r.id}`, {
                      method: "PATCH",
                      body: { enabled: !r.enabled },
                    })
                    .then(load)
                }
              >
                {r.enabled ? t("board.routines.pause") : t("board.routines.resume")}
              </button>
            )}
            <button onClick={() => edit(r)}>{t("board.routines.edit")}</button>
            <button
              onClick={() => {
                if (
                  confirm(
                    t("board.routines.deleteConfirm", { name: r.name }),
                  )
                ) {
                  void api
                    .request(`/routines/${r.id}`, { method: "DELETE" })
                    .then(load);
                }
              }}
            >
              {t("board.routines.delete")}
            </button>
          </div>
        </article>
      ))}</div>
      <details open={editing != null}>
        <summary id="rt-legend">{editing ? t("board.routines.editing", { name }) : t("board.routines.new")}</summary>
        <label>
          {t("board.routines.name")}
          <input id="rt-name" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          {t("board.routines.projects")}
          <select
            id="rt-projects"
            multiple
            value={ids.map(String)}
            onChange={(e) =>
              setIds([...e.target.selectedOptions].map((o) => Number(o.value)))
            }
          >
            {projects.map((p) => (
              <option value={p.id} key={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("board.routines.prompt")}
          <textarea
            id="rt-prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
        </label>
        <label>
          {t("board.routines.schedule")}
          <input
            id="rt-schedule"
            value={schedule}
            onChange={(e) => setSchedule(e.target.value)}
            placeholder="daily at 09:00"
          />
        </label>
        <label>
          {t("board.routines.agent")}
          <input
            value={agent}
            onChange={(e) => setAgent(e.target.value)}
            placeholder={t("board.routines.projectDefault")}
          />
        </label>
        <label>
          {t("board.routines.model")}
          <input value={model} onChange={(e) => setModel(e.target.value)} />
        </label>
        <label>
          {t("board.routines.permissionMode")}
          <select
            value={permission}
            onChange={(e) => setPermission(e.target.value)}
          >
            <option>acceptEdits</option>
            <option>bypassPermissions</option>
            <option>plan</option>
            <option>default</option>
          </select>
        </label>
        <button
          id="rt-save"
          onClick={() => void save().catch((e) => onNotice(String(e), true))}
        >
          {editing ? t("board.routines.saveChanges") : t("board.routines.save")}
        </button>
        {editing && <button onClick={reset}>{t("board.routines.cancel")}</button>}
      </details>
    </Modal>
  );
}
