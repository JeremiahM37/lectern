import { useEffect, useMemo, useState } from "react";
import type { Project, TaskView } from "../types";
import type { BoardApi } from "./Board";
import { Modal } from "../sessions/Modal";
import "./board.css";

type AgentSpec = {
  name: string;
  builtin?: boolean;
  task?: unknown;
  model_flag?: string;
};
type Orchestration = {
  orchestrate_ready?: boolean;
  worker_problem?: string;
  settings?: { enabled?: boolean; lead_agent?: string; worker_agent?: string; worker_model?: string };
};
type Template = {
  name: string;
  title?: string;
  prompt?: string;
  permission_mode?: string;
  model?: string;
};
export function CreateTask({
  api,
  projects,
  onClose,
  onCreated,
  onChat,
  onNotice,
}: {
  api: BoardApi;
  projects: Project[];
  onClose(): void;
  onCreated(): void;
  onChat(t: TaskView): void;
  onNotice(t: string, e?: boolean): void;
}) {
  const [projectId, setProjectId] = useState(projects[0]?.id ?? 0),
    [title, setTitle] = useState(""),
    [prompt, setPrompt] = useState(""),
    [permission, setPermission] = useState("acceptEdits"),
    [agent, setAgent] = useState("claude"),
    [model, setModel] = useState(""),
    [modelB, setModelB] = useState(""),
    [priority, setPriority] = useState(2),
    [agents, setAgents] = useState<AgentSpec[]>([]),
    [templates, setTemplates] = useState<Template[]>([]),
    [cap, setCap] = useState(""),
    // Orchestrate: the same switch the quick bar has, with the rest of the
    // form choosing the lead instead of the worker.
    [orchestrate, setOrchestrate] = useState(false),
    [orchestration, setOrchestration] = useState<Orchestration>();
  const project = projects.find((p) => p.id === projectId);
  const eligible = useMemo(
    () => agents.filter((a) => a.builtin || a.task),
    [agents],
  );
  useEffect(() => {
    void Promise.all([
      api.request<AgentSpec[]>("/agents"),
      api.request<Template[]>("/templates"),
    ])
      .then(([a, t]) => {
        setAgents(a);
        setTemplates(t);
      })
      .catch(() => {});
    void api
      .request<Orchestration>("/delegation")
      .then((v) => setOrchestration(v && typeof v.orchestrate_ready === "boolean" ? v : { orchestrate_ready: false }))
      .catch(() => setOrchestration({ orchestrate_ready: false }));
  }, []);
  useEffect(() => {
    if (!project) return;
    setAgent(project.default_agent || "claude");
    setPermission(project.default_permission_mode || "acceptEdits");
    setCap("checking capability…");
    void api
      .request<{ profile: string; mcp_servers: string[]; memory_dir: string }>(
        `/projects/${project.id}/capability`,
      )
      .then((c) =>
        setCap(
          `${c.profile} · ${c.mcp_servers.length ? `MCP ${c.mcp_servers.join(", ")}` : "no MCP"} · ${c.memory_dir ? "shared memory" : "no memory"}`,
        ),
      )
      .catch(() => setCap(""));
  }, [projectId]);
  async function create(dispatch: boolean, chat = false) {
    if (!title.trim()) return onNotice("Title required", true);
    if (
      dispatch &&
      (model === "fable" || modelB === "fable") &&
      !confirm(
        "Dispatch on Fable 5? It's the most capable model and uses the most of your Claude Code plan. Continue?",
      )
    )
      return;
    try {
      const t = await api.createTask({
        project_id: projectId,
        title: title.trim(),
        prompt: prompt.trim(),
        priority,
        agent,
        model,
        permission_mode: permission,
        ...(orchestrate ? { orchestrate: true } : {}),
      });
      if (dispatch)
        await api.request(`/tasks/${t.id}/dispatch`, {
          method: "POST",
          body: modelB ? { model_b: modelB } : {},
        });
      onCreated();
      onClose();
      onNotice(dispatch ? (orchestrate ? "Orchestrating" : "Dispatched") : "Saved to backlog");
      if (chat) onChat(t);
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  return (
    <Modal id="sheet" open className="sheet" aria-label="New task" onCancel={onClose}>
      <header className="sheet-head">
        <h2>New task</h2>
        <button onClick={onClose}>✕</button>
      </header>
      <label>
        Template
        <select
          onChange={(e) => {
            const t = templates[Number(e.target.value)];
            if (t) {
              setTitle(t.title ?? title);
              setPrompt(t.prompt ?? prompt);
              setPermission(t.permission_mode ?? permission);
              setModel(t.model ?? model);
            }
          }}
        >
          <option value="">— none —</option>
          {templates.map((t, i) => (
            <option value={i} key={t.name}>
              {t.name}
            </option>
          ))}
        </select>
      </label>
      <label>
        Project
        <select
          id="f-project"
          value={projectId}
          onChange={(e) => setProjectId(Number(e.target.value))}
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name} — {p.target_name}
            </option>
          ))}
        </select>
      </label>
      <div id="f-cap-hint" className="subhint">{cap}<span className="cap-note">{cap.includes("restricted") ? " · denied with no prompt" : ""}</span></div>
      <div id="f-orchestrate" className={"orchestrate-row" + (orchestrate ? " on" : "") + (orchestration?.orchestrate_ready ? "" : " unavailable")}>
        <button
          type="button"
          role="switch"
          aria-checked={orchestrate}
          aria-label="Orchestrate"
          disabled={!orchestration?.orchestrate_ready}
          onClick={() => setOrchestrate(!orchestrate)}
        />
        <span onClick={() => orchestration?.orchestrate_ready && setOrchestrate(!orchestrate)}>
          <b>✦ Orchestrate</b>
          {orchestration?.orchestrate_ready ? (
            <small>
              A lead plans the prompt and writes the brief, <b>{orchestration.settings?.worker_agent}</b> builds it in its own worktree, the lead
              reviews, fixes and merges it into this task. Agent and model below choose the <b>lead</b> (Claude Code or Codex).
            </small>
          ) : (
            <small>
              Needs Delegated builds ON with a worker that answers — <a href="#targets">open Settings</a>.
            </small>
          )}
        </span>
      </div>
      <label>
        Title
        <input
          id="f-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Add /health endpoint"
        />
      </label>
      <label>
        {orchestrate ? "What should be built? The lead turns this into a plan and a brief." : "Prompt — what should the agent do?"}
        <textarea
          id="f-prompt"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          placeholder="Describe intent. Be specific about files, behavior, and how to verify."
        />
      </label>
      <label>
        Permissions
        <select
          id="f-perm"
          value={permission}
          onChange={(e) => setPermission(e.target.value)}
        >
          <option value="default" disabled={agent !== "claude"}>
            Gated — Claude Code only
          </option>
          <option value="acceptEdits">
            Accept edits — file changes auto-approved
          </option>
          <option value="plan">Plan only — no changes</option>
          <option value="bypassPermissions">
            Bypass — sandboxed targets only
          </option>
        </select>
      </label>
      <fieldset id="f-agent" data-value={agent}>
        <legend>Agent</legend>
        {(eligible.length
          ? eligible
          : [{ name: "claude" }, { name: "codex" }, { name: "gemini" }]
        ).map((a) => (
          <button
            type="button"
            className={agent === a.name ? "on" : ""}
            data-agent={a.name}
            key={a.name}
            disabled={orchestrate && a.name !== "claude" && a.name !== "codex"}
            title={orchestrate && a.name !== "claude" && a.name !== "codex" ? "Only Claude Code or Codex can lead an orchestrated task" : undefined}
            onClick={() => {
              setAgent(a.name);
              if (a.name !== "claude" && permission === "default")
                setPermission("acceptEdits");
            }}
          >
            {a.name === "claude" ? "Claude Code" : a.name === "codex" ? "Codex" : a.name}
          </button>
        ))}
      </fieldset>
      <label>
        Model
        <input
          id="f-model"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          placeholder="default"
        />
      </label>
      {agent === "claude" && (
        <label id="f-ab-row">
          A/B second attempt
          <select value={modelB} onChange={(e) => setModelB(e.target.value)}>
            <option value="">off</option>
            <option>fable</option>
            <option>opus</option>
            <option>sonnet</option>
            <option>haiku</option>
          </select>
        </label>
      )}
      <label>
        Priority
        <select
          value={priority}
          onChange={(e) => setPriority(Number(e.target.value))}
        >
          <option value={1}>low</option>
          <option value={2}>normal</option>
          <option value={3}>high</option>
        </select>
      </label>
      <div className="btnrow">
        <button id="f-save" onClick={() => void create(false)}>Save to backlog</button>
        <button id="f-go" onClick={() => void create(true)}>Dispatch to board</button>
        <button id="f-chat" className="ok" onClick={() => void create(true, true)}>
          Dispatch &amp; chat
        </button>
      </div>
    </Modal>
  );
}
