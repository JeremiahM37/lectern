import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { Project, Target } from "../types";
import { AgentEditor, type AgentSpec } from "./AgentEditor";
import { Skills } from "./Skills";
import { Workflows } from "./Workflows";
import { Delegation } from "./Delegation";
import { Modal } from "../sessions/Modal";
import { AgentCommands } from "./AgentCommands";
export interface SettingsApi {
  request<T>(p: string, o?: { method?: string; body?: JsonValue }): Promise<T>;
}
type Agent = AgentSpec;
type Profile = {
  id: number;
  name: string;
  agent: string;
  command: string;
  model: string;
  env_json: string;
};
interface Stats {
  total_cost_usd: number;
  last_7d_usd: number;
  tasks_done: number;
  by_project?: { name: string; cost_usd: number }[];
}
interface Health {
  version: string;
  build?: { revision?: string; modified?: boolean };
}
export function syncRetained(
  draft: JsonValue,
  latest: JsonValue | undefined,
): JsonValue {
  if (
    latest &&
    typeof latest === "object" &&
    !Array.isArray(latest) &&
    Object.keys(latest).length === 1 &&
    "__lectern_retained" in latest
  )
    return structuredClone(latest);
  if (Array.isArray(draft) && Array.isArray(latest))
    return draft.map((v, i) => syncRetained(v, latest[i]));
  if (
    draft &&
    latest &&
    typeof draft === "object" &&
    typeof latest === "object" &&
    !Array.isArray(draft) &&
    !Array.isArray(latest)
  )
    return Object.fromEntries(
      Object.entries(draft).map(([k, v]) => [k, syncRetained(v, latest[k])]),
    );
  return draft;
}
export function Settings({
  api,
  onNotice,
  onEnablePush,
  initialSection = "machines",
  section,
  projectEdit,
  launchProfilesVersion = 0,
  onExternalActionConsumed = () => {},
  onMetadataRefresh,
  onOpenTerminal = () => {},
}: {
  api: SettingsApi;
  onNotice(t: string, e?: boolean): void;
  onEnablePush(): void;
  initialSection?: string;
  section?: { name: string; version: number };
  projectEdit?: { id: number; version: number };
  launchProfilesVersion?: number;
  onExternalActionConsumed?: (kind: "section" | "project" | "launchProfiles") => void;
  onMetadataRefresh?(): void;
  onOpenTerminal?(url: string, title: string): void;
}) {
  const [tab, setTab] = useState(initialSection),
    [targets, setTargets] = useState<Target[]>([]),
    [projects, setProjects] = useState<Project[]>([]),
    [settings, setSettings] = useState<Record<string, JsonValue>>({}),
    [stats, setStats] = useState<Stats>(),
    [agents, setAgents] = useState<Agent[]>([]),
    [profiles, setProfiles] = useState<Profile[]>([]);
  const [projectRoot, setProjectRoot] = useState("");
  async function load() {
    const [t, p, s, a, l, st] = await Promise.all([
      api.request<Target[]>("/targets"),
      api.request<Project[]>("/projects"),
      api.request<Record<string, JsonValue>>("/settings"),
      api.request<Agent[]>("/agents"),
      api.request<Profile[]>("/launch-profiles"),
      api.request<Stats>("/stats"),
    ]);
    setTargets(t);
    setProjects(p);
    setSettings(s);
    setAgents(a);
    setProfiles(l);
    setStats(st);
    onMetadataRefresh?.();
  }
  useEffect(() => {
    void load().catch((e) => onNotice(String(e), true));
  }, []);
  useEffect(() => {
    if (section?.version) { setTab(section.name); onExternalActionConsumed("section"); }
  }, [section?.version]);
  useEffect(() => {
    if (!projectEdit?.version) return;
    setTab("projects");
    requestAnimationFrame(() => document.getElementById(`project-${projectEdit.id}`)?.scrollIntoView({ behavior: "smooth", block: "start" }));
    onExternalActionConsumed("project");
  }, [projectEdit?.version, projects.length]);
  useEffect(() => {
    if (!launchProfilesVersion) return;
    setTab("agents");
    requestAnimationFrame(() => document.getElementById("launch-profiles")?.scrollIntoView({ behavior: "smooth", block: "start" }));
    onExternalActionConsumed("launchProfiles");
  }, [launchProfilesVersion]);
  return (
    <section className="settings-page">
      <header>
        <h2>Settings</h2>
        <p>Machines, projects and preferences in one place.</p>
      </header>
      <Delegation api={api} agents={agents} projects={projects} onNotice={onNotice} onChanged={load} />
      <nav role="tablist">
        {(
          [
            ["machines", "Targets"],
            ["projects", "Projects"],
            ["notifications", "Notifications"],
            ["about", "Usage & about"],
            ["agents", "Agents"],
          ] as const
        ).map(([k, v]) => (
          <button
            data-settings={k}
            role="tab"
            aria-selected={tab === k}
            onKeyDown={(e) => {
              if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
              const tabs = ["machines", "projects", "notifications", "about", "agents"];
              const next = tabs[(tabs.indexOf(k) + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length]!;
              setTab(next);
              requestAnimationFrame(() => document.querySelector<HTMLElement>(`[data-settings="${next}"]`)?.focus());
            }}
            onClick={() => setTab(k)}
            key={k}
          >
            {v}
          </button>
        ))}
      </nav>
      {tab === "machines" && (
        <Targets
          api={api}
          rows={targets}
          onChanged={load}
          onNotice={onNotice}
        />
      )}{" "}
      {tab === "projects" && (
        <Projects
          api={api}
          rows={projects}
          targets={targets}
          onChanged={load}
          onNotice={onNotice}
          editRequest={projectEdit}
          onOpenTerminal={onOpenTerminal}
          root={projectRoot}
          setRoot={setProjectRoot}
        />
      )}{" "}
      {tab === "notifications" && (
        <Notifications
          api={api}
          values={settings}
          onEnablePush={onEnablePush}
          onNotice={onNotice}
        />
      )}{" "}
      {tab === "about" && (
        <section>
          <h3>Spend</h3>
          {stats && (
            <p>
              ${Number(stats.total_cost_usd).toFixed(2)} all-time · $
              {Number(stats.last_7d_usd).toFixed(2)} last 7d ·{" "}
              {stats.tasks_done} tasks done
            </p>
          )}
          <Whoami api={api} />
          <Build api={api} />
        </section>
      )}{" "}
      {tab === "agents" && (
        <Agents
          api={api}
          agents={agents}
          profiles={profiles}
          onChanged={load}
          onNotice={onNotice}
        />
      )}
    </section>
  );
}
function Targets({
  api,
  rows,
  onChanged,
  onNotice,
}: {
  api: SettingsApi;
  rows: Target[];
  onChanged(): Promise<void>;
  onNotice(t: string, e?: boolean): void;
}) {
  const [commands,setCommands]=useState<Target>();
  const [checks, setChecks] = useState<Record<number, string>>({});
  return (
    <div className="settings-grid">
      {rows.map((t) => (
        <article className="rowcard" key={t.id}>
          <h3>{t.name}</h3>
          <p>
            {t.kind}
            {t.host && ` · ${t.user}@${t.host}`} · {t.max_concurrent} slots
            {t.sandbox ? " · sandbox" : ""}
          </p>
          <button
            onClick={() =>
              void api
                .request<Target>(`/targets/${t.id}/check`, { method: "POST" })
                .then((result) => {
                  let detail = result.info_json || result.status || "Probe complete";
                  try { detail = JSON.stringify(JSON.parse(detail)); } catch { /* show backend text */ }
                  setChecks((old) => ({ ...old, [t.id]: detail }));
                  return onChanged();
                })
                .catch((e) => onNotice(String(e), true))
            }
          >
            Probe
          </button>
          {checks[t.id] && <p className="sub" role="status">{checks[t.id]}</p>}
          <button
            onClick={() => setCommands(t)}
          >
            Agent commands
          </button>
        </article>
      ))}
      {commands&&<AgentCommands api={api} target={commands} onClose={()=>setCommands(undefined)}/>} 
    </div>
  );
}
function Projects({
  api,
  rows,
  targets,
  onChanged,
  onNotice,
  editRequest,
  onOpenTerminal,
  root,
  setRoot,
}: {
  api: SettingsApi;
  rows: Project[];
  targets: Target[];
  onChanged(): Promise<void>;
  onNotice(t: string, e?: boolean): void;
  editRequest?: { id: number; version: number };
  onOpenTerminal(url: string, title: string): void;
  root: string;
  setRoot(value: string): void;
}) {
  const [found, setFound] = useState<
      { name: string; path: string; registered: boolean }[]
    >([]),
    [usage, setUsage] = useState<
      Record<
        number,
        {
          tasks: number;
          open_tasks: number;
          sessions: number;
          last_active_at: number;
        }
      >
    >({}),
    [selecting, setSelecting] = useState(false),
    [selected, setSelected] = useState<number[]>([]),
    [query, setQuery] = useState(""),
    [editing, setEditing] = useState<Project>(),
    [scanned, setScanned] = useState(false);
  useEffect(() => {
    if (editRequest?.version) setEditing(rows.find((p) => p.id === editRequest.id));
  }, [editRequest?.version, rows]);
  useEffect(() => {
    void api
      .request<
        {
          project_id: number;
          tasks: number;
          open_tasks: number;
          sessions: number;
          last_active_at: number;
        }[]
      >("/projects/usage")
      .then((x) =>
        setUsage(Object.fromEntries(x.map((v) => [v.project_id, v]))),
      )
      .catch(() => {});
  }, [rows]);
  async function removeSelected() {
    if (!selected.length) return;
    const names = rows
      .filter((p) => selected.includes(p.id))
      .map((p) => p.name);
    if (
      !confirm(
        `Delete ${names.length} project${names.length === 1 ? "" : "s"}?\n\n${names.join("\n")}\n\nTasks, sessions and project settings are removed; code on disk is untouched.`,
      )
    )
      return;
    for (const id of selected)
      await api.request(`/projects/${id}?cascade=true`, { method: "DELETE" });
    setSelected([]);
    setSelecting(false);
    await onChanged();
  }
  return (
    <section>
      <div className="btnrow">
        <input id="pj-search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Filter projects…" />
        <button
          id="pj-select"
          onClick={() => {
            setSelecting(!selecting);
            setSelected([]);
          }}
        >
          {selecting ? "Cancel selection" : "Select…"}
        </button>
        {selecting && selected.length > 0 && (
          <button id="pj-del"
            onClick={() =>
              void removeSelected().catch((e) => onNotice(String(e), true))
            }
          >
            Delete {selected.length} selected
          </button>
        )}
      </div>
      <span id="pj-count">{rows.filter((p) => `${p.name} ${p.repo_path} ${p.target_name || ""}`.toLowerCase().includes(query.toLowerCase())).length} / {rows.length}</span>
      <div id="pj-list">
      {rows.filter((p) => `${p.name} ${p.repo_path} ${p.target_name || ""}`.toLowerCase().includes(query.toLowerCase())).map((p) => (
        <div key={p.id} id={`project-${p.id}`} className="pjrow" role="button" tabIndex={0} onClick={() => !selecting && setEditing(p)} onKeyDown={(e) => { if (!selecting && (e.key === "Enter" || e.key === " ")) setEditing(p); }}>
          {selecting && (
            <label>
              <input
                type="checkbox"
                checked={selected.includes(p.id)}
                onChange={(e) =>
                  setSelected((v) =>
                    e.target.checked
                      ? [...v, p.id]
                      : v.filter((x) => x !== p.id),
                  )
                }
              />{" "}
              Select {p.name}
            </label>
          )}
          <p>
            {usage[p.id]?.tasks || 0} tasks ({usage[p.id]?.open_tasks || 0}{" "}
            open) · {usage[p.id]?.sessions || 0} sessions
          </p>
          <div className="pjmain"><div className="pjname">{p.name}</div><div className="pjmeta">{p.target_name}</div><div className="pjpath">{p.repo_path}</div></div>
          <button aria-label={`Shell in ${p.name}`} onClick={(e) => { e.stopPropagation(); void api.request<{url:string}>(`/projects/${p.id}/terminal`, {method:"POST"}).then((r) => onOpenTerminal(r.url, p.name)).catch((error) => onNotice(String(error), true)); }}>⌨</button>
        </div>
      ))}
      </div>
      {editing && <Modal id="sheet" open className="sheet" aria-label={`Edit ${editing.name}`} onCancel={() => setEditing(undefined)}>
        <button className="x" onClick={() => setEditing(undefined)}>Close</button>
        <ProjectCard api={api} p={editing} onChanged={onChanged} onNotice={onNotice} />
      </Modal>}
      <article>
        <h3>Import projects</h3>
        <select aria-label="Import target" id="import-target">
          {targets.map((t) => (
            <option value={t.id} key={t.id}>
              {t.name}
            </option>
          ))}
        </select>
        <input
          id="imp-root"
          value={root}
          onChange={(e) => setRoot(e.target.value)}
          placeholder="/home/you/projects"
        />
        <button
          id="imp-scan"
          onClick={() =>
            void api
              .request<{ name: string; path: string; registered: boolean }[]>(
                `/projects/import/scan?root=${encodeURIComponent(root)}&target_id=${(document.querySelector("#import-target") as HTMLSelectElement)?.value}`,
              )
              .then((rows) => { setFound(rows); setScanned(true); })
          }
        >
          Scan
        </button>
        <div id="imp-out">{scanned && found.length === 0 ? "Nothing project-shaped found." : ""}</div>
        {found.map((f) => (
          <label key={f.path}>
            <input
              type="checkbox"
              disabled={f.registered}
              defaultChecked={!f.registered}
              value={f.path}
            />
            {f.name} · {f.path}
          </label>
        ))}
        {found.length > 0 && (
          <button
            onClick={() => {
              const paths = [
                ...document.querySelectorAll<HTMLInputElement>(
                  "input[type=checkbox]:checked",
                ),
              ].map((x) => x.value);
              void api
                .request("/projects/import", {
                  method: "POST",
                  body: {
                    target_id: Number(
                      (
                        document.querySelector(
                          "#import-target",
                        ) as HTMLSelectElement
                      ).value,
                    ),
                    paths,
                    verify: false,
                  },
                })
                .then(onChanged)
                .catch((e) => onNotice(String(e), true));
            }}
          >
            Import selected
          </button>
        )}
      </article>
    </section>
  );
}
function ProjectCard({
  api,
  p,
  onChanged,
  onNotice,
}: {
  api: SettingsApi;
  p: Project;
  onChanged(): Promise<void>;
  onNotice(t: string, e?: boolean): void;
}) {
  const [setup, setSetup] = useState(p.setup_cmd),
    [profile, setProfile] = useState(p.capability_profile),
    [perm, setPerm] = useState(p.default_permission_mode);
  const [mcp, setMcp] = useState("{}"),
    [mcpStatus, setMcpStatus] = useState("Loading…"),
    [revision, setRevision] = useState(""),
    [strict, setStrict] = useState(Boolean(p.strict_mcp)),
    [sources, setSources] = useState(() => {
      try {
        return (JSON.parse(p.skill_sources_json || "[]") as string[]).join(
          "\n",
        );
      } catch {
        return "";
      }
    }),
    [cap, setCap] = useState(""),
    [setupStatus, setSetupStatus] = useState("");
  useEffect(() => {
    void Promise.all([
      api.request<{
        mcp: Record<string, JsonValue>;
        revision: string;
        strict_mcp: boolean;
      }>(`/projects/${p.id}/mcp`),
      api.request<{
        profile: string;
        allow: string[];
        mcp_servers: string[];
        memory_dir: string;
      }>(`/projects/${p.id}/capability`),
    ])
      .then(([m, c]) => {
        setMcp(JSON.stringify(m.mcp, null, 2));
        setMcpStatus("MCP settings loaded");
        setRevision(m.revision);
        setStrict(m.strict_mcp);
        setCap(`${c.profile === "restricted" ? "⚠ " : ""}${c.profile} · MCP: ${c.mcp_servers.join(", ") || "none"} · memory: ${c.memory_dir ? "shared" : "none"} · ${c.allow.includes("Bash") ? "bash: unrestricted" : "bash: restricted"}`);
      })
      .catch(() => {});
  }, [p.id]);
  async function save() {
    await api.request(`/projects/${p.id}`, {
      method: "PATCH",
      body: {
        setup_cmd: setup,
        capability_profile: profile,
        default_permission_mode: perm,
      },
    });
    onNotice("Project saved");
    await onChanged();
  }
  async function saveSetup() {
    try {
      await api.request(`/projects/${p.id}`, { method: "PATCH", body: { setup_cmd: setup } });
      setSetupStatus("Saved setup command"); await onChanged();
    } catch (error) { setSetupStatus(error instanceof Error ? error.message : String(error)); }
  }
  async function saveMCP() {
    let next: Record<string, JsonValue>;
    try {
      next = JSON.parse(mcp) as Record<string, JsonValue>;
    } catch {
      return onNotice("MCP settings must be valid JSON", true);
    }
    try {
      const saved = await api.request<{ revision: string }>(
        `/projects/${p.id}/mcp`,
        { method: "PUT", body: { mcp: next, revision, strict_mcp: strict } },
      );
      setRevision(saved.revision);
      setMcpStatus("Saved MCP settings");
      onNotice("MCP settings saved");
    } catch (e) {
      const err = e as { status?: number; message?: string };
      if (err.status !== 409) { setMcpStatus(err.message || String(e)); return onNotice(err.message || String(e), true); }
      try {
        const latest = await api.request<{
          mcp: Record<string, JsonValue>;
          revision: string;
        }>(`/projects/${p.id}/mcp`);
        const merged = syncRetained(next, latest.mcp) as Record<
          string,
          JsonValue
        >;
        setMcp(JSON.stringify(merged, null, 2));
        setRevision(latest.revision);
        setMcpStatus("MCP settings changed elsewhere; your draft is preserved. Review it, then Save again.");
        onNotice(
          "MCP settings changed elsewhere; your draft is preserved. Review it, then Save again.",
          true,
        );
      } catch (refresh) {
        onNotice(
          `MCP settings changed elsewhere; the new revision could not be loaded: ${String(refresh)}`,
          true,
        );
      }
    }
  }
  function documentValue(): Record<string, JsonValue> {
    try { const value = JSON.parse(mcp) as unknown; return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, JsonValue> : {}; } catch { return {}; }
  }
  function changeMCP(index: number, field: "name" | "type" | "command" | "extra", value: string) {
    const entries = Object.entries(documentValue());
    const [oldName = "", raw = {}] = entries[index] || [];
    const spec = raw && typeof raw === "object" && !Array.isArray(raw) ? { ...raw } as Record<string, JsonValue> : {};
    let name = oldName;
    if (field === "name") name = value;
    else if (field === "type") {
      if (value === "http") { spec.url = typeof spec.url === "string" ? spec.url : String(spec.command || ""); delete spec.command; }
      else { spec.command = typeof spec.command === "string" ? spec.command : String(spec.url || ""); delete spec.url; }
    } else if (field === "command") { if (typeof spec.url === "string") spec.url = value; else spec.command = value; }
    else {
      try {
        const extra = JSON.parse(value) as Record<string, JsonValue>;
        const command = spec.command, url = spec.url;
        for (const key of Object.keys(spec)) delete spec[key];
        Object.assign(spec, extra);
        if (url !== undefined) spec.url = url; else if (command !== undefined) spec.command = command;
      } catch { return; }
    }
    entries[index] = [name, spec]; setMcp(JSON.stringify(Object.fromEntries(entries), null, 2));
  }
  return (
    <article className="rowcard project-mcp">
      <h3>{p.name}</h3>
      <p>
        {p.target_name} · {p.repo_path}
      </p>
      <label>
        Agent capability
        <select className="cap-sel" value={profile} onChange={(e) => {
          const value = e.target.value;
          setProfile(value);
          void api.request(`/projects/${p.id}`, { method: "PATCH", body: { capability_profile: value } }).then(async () => {
            const c = await api.request<{profile:string;allow:string[];mcp_servers:string[];memory_dir:string}>(`/projects/${p.id}/capability`);
            setCap(`${c.profile === "restricted" ? "⚠ " : ""}${c.profile} · MCP: ${c.mcp_servers.join(", ") || "none"} · memory: ${c.memory_dir ? "shared" : "none"} · ${c.allow.includes("Bash") ? "bash: unrestricted" : "bash: restricted"}`);
          }).catch((error) => onNotice(String(error), true));
        }}>
          <option value="restricted">restricted — only rules you set</option>
          <option value="parity">parity — same tools as your terminal</option>
        </select>
      </label>
      <label>
        Default permission mode
        <select value={perm} onChange={(e) => setPerm(e.target.value)}>
          <option value="">task default</option>
          <option>default</option>
          <option>acceptEdits</option>
          <option>plan</option>
          <option>bypassPermissions</option>
        </select>
      </label>
      <label>
        New worktree setup command
        <textarea aria-label="New worktree setup command" value={setup} onChange={(e) => setSetup(e.target.value)} />
      </label>
      <button onClick={() => void saveSetup()}>Save setup command</button>
      <p className="project-setup-status" role="status">{setupStatus}</p>
      <button
        onClick={() => void save().catch((e) => onNotice(String(e), true))}
      >
        Save project
      </button>
      <p className="cap-info">{cap}</p>
      <section className="project-mcp-editor">
        <h4>Project MCP servers</h4>
        <p className="project-mcp-status" role="status">{mcpStatus}</p>
        {Object.entries(documentValue()).map(([name, raw], index) => {
          const spec = raw && typeof raw === "object" && !Array.isArray(raw) ? raw as Record<string, JsonValue> : {};
          const http = typeof spec.url === "string";
          const extra = Object.fromEntries(Object.entries(spec).filter(([key]) => key !== "command" && key !== "url"));
          return <div className="mcp-row" key={`${index}-${name}`}>
            <input className="mcp-name" aria-label="Server name" value={name} onChange={(e) => changeMCP(index, "name", e.target.value)} />
            <select className="mcp-type" aria-label="Server type" value={http ? "http" : "stdio"} onChange={(e) => changeMCP(index, "type", e.target.value)}><option value="stdio">stdio</option><option value="http">http</option></select>
            <input className="mcp-command" aria-label={http ? "URL" : "Command"} value={String(http ? spec.url || "" : spec.command || "")} onChange={(e) => changeMCP(index, "command", e.target.value)} />
            <textarea className="mcp-extra" aria-label="Additional settings" value={JSON.stringify(extra, null, 2)} onChange={(e) => changeMCP(index, "extra", e.target.value)} />
            <button className="mcp-remove" onClick={() => { const entries=Object.entries(documentValue());entries.splice(index,1);setMcp(JSON.stringify(Object.fromEntries(entries),null,2)); }}>Remove</button>
          </div>;
        })}
        <button className="project-mcp-add" onClick={() => { const entries=Object.entries(documentValue());entries.push([`server_${entries.length+1}`, {command:""}]);setMcp(JSON.stringify(Object.fromEntries(entries),null,2)); }}>Add server</button>
      </section>
      <label>
        <input
          type="checkbox"
          checked={strict}
          onChange={(e) => setStrict(e.target.checked)}
        />{" "}
        Claude strict MCP replacement
      </label>
      <button className="project-mcp-save" onClick={() => void saveMCP()}>Save MCP settings</button>
      <Skills
        api={api}
        projectId={p.id}
        defaultAgent={p.default_agent}
        onNotice={onNotice}
        sourceText={sources}
        onSourceText={setSources}
      />
      <Workflows
        api={api}
        projectId={p.id}
        defaultAgent={p.default_agent}
        onNotice={onNotice}
      />
      <button
        onClick={() => {
          if (confirm(`Delete ${p.name}?`))
            void api
              .request(`/projects/${p.id}?cascade=true`, { method: "DELETE" })
              .then(onChanged);
        }}
      >
        Delete project
      </button>
    </article>
  );
}
function Notifications({
  api,
  values,
  onEnablePush,
  onNotice,
}: {
  api: SettingsApi;
  values: Record<string, JsonValue>;
  onEnablePush(): void;
  onNotice(t: string, e?: boolean): void;
}) {
  const [discord, setDiscord] = useState(String(values.discord_webhook ?? "")),
    [server, setServer] = useState(String(values.ntfy_server ?? "")),
    [topic, setTopic] = useState(String(values.ntfy_topic ?? ""));
  // Per-kind session-alert toggles (docs/agent-events.md section 3): stored
  // as "0"/"1" strings, default ON — missing or anything but "0" means the
  // alert is enabled (internal/alerts.Watcher.enabled).
  const alertKeys: [string, string][] = [
    ["alert_waiting_permission", "Needs permission"],
    ["alert_waiting_input", "Waiting for input"],
    ["alert_idle", "Finished"],
    ["alert_error", "Error"],
    ["alert_compacting", "Compacting"],
  ];
  const [alerts, setAlerts] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(alertKeys.map(([key]) => [key, String(values[key] ?? "") !== "0"])),
  );
  const [permissionMode, setPermissionMode] = useState(
    String(values.session_permission_mode ?? "") === "ask" ? "ask" : "bypass",
  );
  return (
    <article>
      <h3>Notifications</h3>
      <button onClick={onEnablePush}>Enable push on this device</button>
      <label>
        Discord webhook URL
        <input id="s-discord" value={discord} onChange={(e) => setDiscord(e.target.value)} />
      </label>
      <label>
        ntfy server
        <input id="s-ntfy-server" value={server} onChange={(e) => setServer(e.target.value)} />
      </label>
      <label>
        ntfy topic
        <input id="s-ntfy-topic" value={topic} onChange={(e) => setTopic(e.target.value)} />
      </label>
      <button
        id="s-save"
        onClick={() =>
          void api
            .request("/settings", {
              method: "PUT",
              body: {
                discord_webhook: discord.trim(),
                ntfy_server: server.trim(),
                ntfy_topic: topic.trim(),
              },
            })
            .then(() => onNotice("Sinks saved"))
        }
      >
        Save sinks
      </button>
      <button
        onClick={() =>
          void api
            .request("/settings/test-notification", { method: "POST" })
            .then(() => onNotice("Test sent"))
        }
      >
        Send test
      </button>

      <h4>Session alerts</h4>
      <p className="subhint">
        A push when a session needs permission, is waiting for you, finishes, hits an
        error, or auto-compacts its context. Suppressed for 30s after you type into that
        session's terminal.
      </p>
      {alertKeys.map(([key, label]) => (
        <label key={key}>
          <input
            type="checkbox"
            checked={alerts[key]}
            onChange={(e) => setAlerts((old) => ({ ...old, [key]: e.target.checked }))}
          />{" "}
          {label}
        </label>
      ))}
      <button
        id="s-save-alerts"
        onClick={() =>
          void api
            .request("/settings", {
              method: "PUT",
              body: Object.fromEntries(
                alertKeys.map(([key]) => [key, alerts[key] ? "1" : "0"]),
              ),
            })
            .then(() => onNotice("Alert settings saved"))
        }
      >
        Save alerts
      </button>

      <h4>New session permission mode</h4>
      <p className="subhint">
        The default for a new interactive session that does not say otherwise. "Ask"
        registers the PermissionRequest hook, so a tool call can be approved or denied
        from the phone; "Bypass" is today's default — the agent runs unattended.
      </p>
      <label>
        <select
          id="s-permission-mode"
          value={permissionMode}
          onChange={(e) => setPermissionMode(e.target.value)}
        >
          <option value="bypass">Bypass (no approval prompts)</option>
          <option value="ask">Ask (approve/deny from the phone)</option>
        </select>
      </label>
      <button
        id="s-save-permission-mode"
        onClick={() =>
          void api
            .request("/settings", {
              method: "PUT",
              body: { session_permission_mode: permissionMode },
            })
            .then(() => onNotice("Default permission mode saved"))
        }
      >
        Save default
      </button>
    </article>
  );
}
interface Whoami {
  mode: string;
  kind: string;
  login: string;
  node: string;
  human: boolean;
}
// Unobtrusive identity line: who this device is signed in as, and how. Most
// useful in tailscale mode, where nobody ever typed a credential in — this is
// the one place that confirms it actually resolved to the right person.
function Whoami({ api }: { api: SettingsApi }) {
  const [w, setW] = useState<Whoami>();
  useEffect(() => {
    void api.request<Whoami>("/whoami").then(setW);
  }, []);
  if (!w) return null;
  const identity =
    w.kind === "tailscale"
      ? `tailscale · ${w.login}${w.node ? ` (${w.node})` : ""}`
      : w.kind === "token"
        ? "access token"
        : "local (no login needed)";
  return (
    <article id="whoami">
      <h3>Signed in</h3>
      <p>
        {identity} · auth mode: {w.mode}
      </p>
    </article>
  );
}
function Build({ api }: { api: SettingsApi }) {
  const [h, setH] = useState<Health>();
  useEffect(() => {
    void api.request<Health>("/health").then(setH);
  }, []);
  return (
    <article id="running-build">
      <h3>Running build</h3>
      <p>
        {h
          ? `${h.version} · ${h.build?.revision?.slice(0, 12) || "revision unknown"} · ${h.build?.modified === true ? "local changes" : h.build?.modified === false ? "clean" : "build status unknown"}`
          : "Loading…"}
      </p>
    </article>
  );
}
function Agents({
  api,
  agents,
  profiles,
  onChanged,
  onNotice,
}: {
  api: SettingsApi;
  agents: Agent[];
  profiles: Profile[];
  onChanged(): Promise<void>;
  onNotice(t: string, e?: boolean): void;
}) {
  const [editing, setEditing] = useState<Agent | "new">(),
    [profile, setProfile] = useState<Profile | "new">();
  return (
    <section>
      <h3>Agent runners</h3>
      {agents.map((a) => (
        <article className="agent-card" key={a.name}>
          <b>
            {a.name}
            {a.builtin ? " · built in" : " · custom"}
          </b>
          <span>
            {a.command} · {a.model_flag || "no model flag"}
          </span>
          <button onClick={() => setEditing(a)}>Edit</button>
          {!a.builtin && (
            <button
              onClick={() =>
                void api
                  .request("/agents", {
                    method: "PUT",
                    body: agents.filter(
                      (x) => !x.builtin && x.name !== a.name,
                    ) as unknown as JsonValue,
                  })
                  .then(onChanged)
              }
            >
              Delete
            </button>
          )}
        </article>
      ))}
      <button onClick={() => setEditing("new")}>Add agent</button>
      <h3 id="launch-profiles">Launch profiles</h3>
      {profiles.map((p) => (
        <article key={p.id}>
          <b>{p.name}</b> · {p.agent} · {p.model}
          <button onClick={() => setProfile(p)}>Edit profile</button>
          <button
            onClick={() =>
              void api
                .request(`/launch-profiles/${p.id}`, { method: "DELETE" })
                .then(onChanged)
            }
          >
            Delete profile
          </button>
        </article>
      ))}
      <button onClick={() => setProfile("new")}>New profile</button>
      {editing && (
        <AgentEditor
          api={api}
          source={editing === "new" ? undefined : editing}
          all={agents}
          onClose={() => setEditing(undefined)}
          onSaved={onChanged}
          onNotice={onNotice}
        />
      )}
      {profile && (
        <ProfileEditor
          api={api}
          source={profile === "new" ? undefined : profile}
          agents={agents}
          onClose={() => setProfile(undefined)}
          onSaved={onChanged}
          onNotice={onNotice}
        />
      )}
    </section>
  );
}
function ProfileEditor({
  api,
  source,
  agents,
  onClose,
  onSaved,
  onNotice,
}: {
  api: SettingsApi;
  source?: Profile;
  agents: Agent[];
  onClose(): void;
  onSaved(): Promise<void>;
  onNotice(t: string, e?: boolean): void;
}) {
  const [name, setName] = useState(source?.name || ""),
    [agent, setAgent] = useState(source?.agent || agents[0]?.name || "claude"),
    [command, setCommand] = useState(source?.command || ""),
    [model, setModel] = useState(source?.model || ""),
    [environment, setEnvironment] = useState(() => {
      try {
        return JSON.stringify(JSON.parse(source?.env_json || "{}"), null, 2);
      } catch {
        return "{}";
      }
    }),
    [busy, setBusy] = useState(false);
  async function save() {
    let parsed: unknown;
    try {
      parsed = JSON.parse(environment);
    } catch {
      throw Error("Environment must be valid JSON.");
    }
    if (
      !parsed ||
      Array.isArray(parsed) ||
      typeof parsed !== "object" ||
      Object.values(parsed).some((v) => typeof v !== "string")
    )
      throw Error("Environment must be a JSON object with string values.");
    if (!name.trim()) throw Error("Name is required.");
    setBusy(true);
    try {
      await api.request(
        source ? `/launch-profiles/${source.id}` : "/launch-profiles",
        {
          method: source ? "PUT" : "POST",
          body: {
            name: name.trim(),
            agent,
            command: command.trim(),
            model: model.trim(),
            env_json: JSON.stringify(parsed),
          },
        },
      );
      await onSaved();
      onClose();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      className="launch-profiles"
      aria-label="Launch profiles"
      onCancel={(e) => {
        if (busy) e.preventDefault();
      }}
    >
      <h2>{source ? "Edit launch profile" : "New launch profile"}</h2>
      <button disabled={busy} onClick={onClose}>
        Close
      </button>
      <label>
        Name
        <input value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label>
        Agent
        <select value={agent} onChange={(e) => setAgent(e.target.value)}>
          {agents.map((a) => (
            <option key={a.name}>{a.name}</option>
          ))}
        </select>
      </label>
      <label>
        Command override
        <input value={command} onChange={(e) => setCommand(e.target.value)} />
      </label>
      <label>
        Default model
        <input value={model} onChange={(e) => setModel(e.target.value)} />
      </label>
      <label>
        Environment (JSON)
        <textarea
          value={environment}
          onChange={(e) => setEnvironment(e.target.value)}
        />
      </label>
      <button
        disabled={busy}
        onClick={() => void save().catch((e) => onNotice(String(e), true))}
      >
        Save profile
      </button>
    </Modal>
  );
}
