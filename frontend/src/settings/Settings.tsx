import { AppHosts, VoiceSettings } from "./PhonePanels";
import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { IsolationConfig, Project, Target } from "../types";
import { AgentEditor, type AgentSpec } from "./AgentEditor";
import { fetchAgentMenu, saveAgentMenu } from "../agents/menu";
import { Skills } from "./Skills";
import { Workflows } from "./Workflows";
import { Triggers } from "./Triggers";
import { TrackerSettings } from "../trackers/TrackerSettings";
import { Delegation } from "./Delegation";
import { ConnectTools } from "./ConnectTools";
import { Devices } from "./Devices";
import { Modal } from "../sessions/Modal";
import { AgentCommands } from "./AgentCommands";
import { UsagePanel } from "./UsagePanel";
import { OutcomesPanel } from "./OutcomesPanel";
import { BudgetsPanel } from "./BudgetsPanel";
import { ModelPrices } from "./ModelPrices";
import { LimitPolicyEditor } from "./LimitPolicy";
import { AccountsPanel } from "./Accounts";
import { LaunchProfiles } from "./LaunchProfiles";
import { instructionsHelp } from "./launchProfileForm";
import { shortEndpoint, type PushSubscriptionInfo } from "../push";
import { AppearancePanel, ShortcutsPanel, WorkspacePanel } from "./Personal";
import { SettingsSearch } from "./SettingsSearch";
import { focusSetting, SECTIONS } from "./search-index";
import { t, useLocale } from "../i18n";
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
  description?: string;
  instructions?: string;
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
  pushAvailable = true,
  pushUnavailableReason,
  pushUnavailableReasonKind,
  pushEndpoint,
  onUnsubscribePush,
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
  // Whether this browser can even do push, and why not when it can't — see
  // frontend/src/push.ts. Defaults to available so callers that do not pass
  // it (the standalone settings-harness fixture used by e2e tests) keep
  // showing the control rather than an unexplained "unavailable" state.
  pushAvailable?: boolean;
  pushUnavailableReason?: string;
  // "ios-not-installed" gets step-by-step Home Screen instructions instead
  // of just the one-sentence reason — see push.ts's PushUnavailableReason.
  pushUnavailableReasonKind?: "insecure" | "ios-not-installed" | "unsupported";
  // This device's current subscription endpoint: undefined while unknown,
  // null once known to have none, or the endpoint string once subscribed.
  pushEndpoint?: string | null;
  onUnsubscribePush?(endpoint: string): void;
  initialSection?: string;
  section?: { name: string; version: number; focus?: string };
  projectEdit?: { id: number; version: number };
  launchProfilesVersion?: number;
  onExternalActionConsumed?: (kind: "section" | "project" | "launchProfiles") => void;
  onMetadataRefresh?(): void;
  onOpenTerminal?(url: string, title: string): void;
}) {
  useLocale();
  const [searchFocus, setSearchFocus] = useState(0),
    [focused, setFocused] = useState<string>();
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
    if (!section?.version) return;
    setTab(section.name);
    // A palette or search hit names the control to bring into view.
    if (section.focus === "search") setSearchFocus(Date.now());
    else if (section.focus) {
      setFocused(section.focus);
      requestAnimationFrame(() => focusSetting({ id: section.focus! }));
    }
    onExternalActionConsumed("section");
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
        <h2>{t("settings.title")}</h2>
        <p>{t("settings.subtitle")}</p>
      </header>
      <SettingsSearch focusVersion={searchFocus} onPick={(entry) => {
        setTab(entry.section);
        setFocused(entry.id);
        requestAnimationFrame(() => focusSetting(entry));
      }} />
      <ConnectTools api={api} onNotice={onNotice} />
      <Delegation api={api} agents={agents} projects={projects} onNotice={onNotice} onChanged={load} />
      <nav role="tablist">
        {SECTIONS.map(([k, key]) => [k, t(key)] as const).map(([k, v]) => (
          <button
            data-settings={k}
            role="tab"
            aria-selected={tab === k}
            onKeyDown={(e) => {
              if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
              const tabs = SECTIONS.map(([key]) => key);
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
          pushAvailable={pushAvailable}
          pushUnavailableReason={pushUnavailableReason}
          pushUnavailableReasonKind={pushUnavailableReasonKind}
          pushEndpoint={pushEndpoint}
          onUnsubscribePush={onUnsubscribePush}
          onNotice={onNotice}
        />
      )}{" "}
      {tab === "notifications" && <VoiceSettings />}
      {tab === "devices" && <AppHosts />}
      {tab === "devices" && <Devices api={api} onNotice={onNotice} />}{" "}
      {tab === "about" && (
        <section>
          <h3 data-setting="about.spend">{t("settings.about.spend")}</h3>
          {stats && (
            <p>
              {t("settings.about.spendLine", { total: Number(stats.total_cost_usd).toFixed(2), week: Number(stats.last_7d_usd).toFixed(2), tasks: stats.tasks_done })}
            </p>
          )}
          <UsagePanel api={api} />
          <h3 data-setting="about.outcomes">{t("settings.about.outcomes")}</h3>
          <OutcomesPanel api={api} />
          <Whoami api={api} />
          <Build api={api} />
        </section>
      )}{" "}
      {tab === "budgets" && (
        <>
          <BudgetsPanel api={api} onNotice={onNotice} />
          <ModelPrices api={api} onNotice={onNotice} />
          <LimitPolicyEditor api={api} onNotice={onNotice} />
        </>
      )}{" "}
      {tab === "accounts" && (
        <AccountsPanel api={api} targets={targets} onNotice={onNotice} onOpenTerminal={onOpenTerminal} />
      )}{" "}
      {tab === "appearance" && <AppearancePanel />}
      {tab === "workspace" && <WorkspacePanel projects={projects} />}
      {tab === "shortcuts" && <ShortcutsPanel focus={focused} />}
      {tab === "agents" && (
        <Agents
          api={api}
          agents={agents}
          profiles={profiles}
          targets={targets}
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
  useLocale();
  const [adding, setAdding] = useState(false);
  const [commands,setCommands]=useState<Target>();
  const [checks, setChecks] = useState<Record<number, string>>({});
  return (
    <div className="settings-grid">
      <button type="button" onClick={() => setAdding(true)}>{t("settings.targets.addMachine")}</button>
      {adding && <TargetEditor api={api} onClose={() => setAdding(false)} onChanged={onChanged} />}
      {rows.map((target) => (
        <article className="rowcard" key={target.id}>
          <h3>{target.name}</h3>
          <p>
            {target.kind}
            {target.host && ` · ${target.user}@${target.host}`} · {t("settings.targets.slots", { n: target.max_concurrent })}
            {target.sandbox ? ` · ${t("settings.targets.sandbox")}` : ""}
          </p>
          <button
            onClick={() =>
              void api
                .request<Target>(`/targets/${target.id}/check`, { method: "POST" })
                .then((result) => {
                  let detail = result.info_json || result.status || t("settings.targets.probeComplete");
                  try { detail = JSON.stringify(JSON.parse(detail)); } catch { /* show backend text */ }
                  setChecks((old) => ({ ...old, [target.id]: detail }));
                  return onChanged();
                })
                .catch((e) => onNotice(String(e), true))
            }
          >
            {t("settings.targets.probe")}
          </button>
          {checks[target.id] && <p className="sub" role="status">{checks[target.id]}</p>}
          <button
            onClick={() => setCommands(target)}
          >
            {t("settings.targets.agentCommands")}
          </button>
        </article>
      ))}
      {commands&&<AgentCommands api={api} target={commands} onClose={()=>setCommands(undefined)}/>} 
    </div>
  );
}
function TargetEditor({ api, onClose, onChanged }: {
  api: SettingsApi; onClose(): void; onChanged(): Promise<void>;
}) {
  useLocale();
  const [kind, setKind] = useState("ssh");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <Modal className="sheet machine-sheet" aria-label={t("settings.targets.addMachine")} onCancel={onClose}>
    <div className="sheet-head"><h2>{t("settings.targets.addMachine")}</h2>
    <button type="button" onClick={onClose} aria-label={t("settings.common.close")} data-close>×</button></div>
    <form onSubmit={async (event) => {
      event.preventDefault();
      const data = new FormData(event.currentTarget);
      setBusy(true); setError("");
      try {
        await api.request("/targets", { method: "POST", body: {
          name: String(data.get("name") || "").trim(), kind,
          ...(kind === "ssh" ? {
            host: String(data.get("host") || "").trim(),
            user: String(data.get("user") || "").trim(),
            port: Number(data.get("port") || 22),
            key_path: String(data.get("key_path") || "").trim(),
          } : {}),
        }});
        await onChanged(); onClose();
      } catch (e) { setError(String(e)); } finally { setBusy(false); }
    }}>
      <label data-setting="machines.name">{t("settings.targets.name")}<input name="name" required maxLength={100} /></label>
      <label>{t("settings.targets.connection")}<select value={kind} onChange={(e) => setKind(e.target.value)}>
        <option value="ssh">{t("settings.targets.remote")}</option>
        <option value="local">{t("settings.targets.local")}</option>
      </select></label>
      {kind === "ssh" ? <>
        <label data-setting="machines.host">{t("settings.targets.host")}<input name="host" required placeholder="server.example" /></label>
        <label data-setting="machines.user">{t("settings.targets.user")}<input name="user" required autoComplete="username" /></label>
        <label data-setting="machines.port">{t("settings.targets.port")}<input name="port" type="number" min="1" max="65535" defaultValue="22" required /></label>
        <label data-setting="machines.key">{t("settings.targets.key")}<input name="key_path" placeholder="~/.ssh/id_ed25519" /></label>
        <p className="sub">{t("settings.targets.sshHint")}</p>
      </> : <p className="sub">{t("settings.targets.localHint")}</p>}
      {error && <p role="alert">{error}</p>}
      <button type="submit" disabled={busy}>{busy ? t("settings.targets.adding") : t("settings.targets.save")}</button>
    </form>
  </Modal>;
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
  useLocale();
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
        t("settings.projects.deleteConfirm", { count: names.length, names: names.join("\n") }),
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
        <input id="pj-search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("settings.projects.filter")} />
        <button
          id="pj-select"
          onClick={() => {
            setSelecting(!selecting);
            setSelected([]);
          }}
        >
          {selecting ? t("settings.projects.cancelSelection") : t("settings.projects.select")}
        </button>
        {selecting && selected.length > 0 && (
          <button id="pj-del"
            onClick={() =>
              void removeSelected().catch((e) => onNotice(String(e), true))
            }
          >
            {t("settings.projects.deleteSelected", { count: selected.length })}
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
              {t("settings.projects.selectOne", { name: p.name })}
            </label>
          )}
          <p>
            {t("settings.projects.usage", { tasks: usage[p.id]?.tasks || 0, open: usage[p.id]?.open_tasks || 0, sessions: usage[p.id]?.sessions || 0 })}
          </p>
          <div className="pjmain"><div className="pjname">{p.name}</div><div className="pjmeta">{p.target_name}</div><div className="pjpath">{p.repo_path}</div></div>
          <button aria-label={t("settings.projects.shellIn", { name: p.name })} onClick={(e) => { e.stopPropagation(); void api.request<{url:string;notice?:string}>(`/projects/${p.id}/terminal`, {method:"POST"}).then((r) => { onOpenTerminal(r.url, p.name); if (r.notice) onNotice(r.notice); }).catch((error) => onNotice(String(error), true)); }}>⌨</button>
        </div>
      ))}
      </div>
      {editing && <Modal id="sheet" open className="sheet" aria-label={t("settings.projects.editName", { name: editing.name })} onCancel={() => setEditing(undefined)}>
        <button className="x" onClick={() => setEditing(undefined)}>{t("settings.common.close")}</button>
        <ProjectCard api={api} p={editing} onChanged={onChanged} onNotice={onNotice} />
      </Modal>}
      <article>
        <h3 data-setting="projects.import">{t("settings.projects.import")}</h3>
        <select aria-label={t("settings.projects.importTarget")} id="import-target">
          {targets.map((target) => (
            <option value={target.id} key={target.id}>
              {target.name}
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
          {t("settings.projects.scan")}
        </button>
        <div id="imp-out">{scanned && found.length === 0 ? t("settings.projects.nothingFound") : ""}</div>
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
            {t("settings.projects.importSelected")}
          </button>
        )}
      </article>
    </section>
  );
}
// The one-line capability summary shown under a project card.
function capabilityText(c: { profile: string; allow: string[]; mcp_servers: string[]; memory_dir: string }): string {
  return t("settings.projects.capability", {
    warn: c.profile === "restricted" ? "⚠ " : "",
    profile: c.profile,
    servers: c.mcp_servers.join(", ") || t("settings.projects.none"),
    memory: c.memory_dir ? t("settings.projects.memoryShared") : t("settings.projects.none"),
    bash: c.allow.includes("Bash") ? t("settings.projects.bashUnrestricted") : t("settings.projects.bashRestricted"),
  });
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
  useLocale();
  const [setup, setSetup] = useState(p.setup_cmd),
    [profile, setProfile] = useState(p.capability_profile),
    [perm, setPerm] = useState(p.default_permission_mode);
  // Isolation default (internal/isolation, docs/isolation.md): this
  // project's sandbox tier for a new session/task that doesn't say
  // otherwise. default_isolation_json is a raw internal/isolation.Config
  // JSON string, same pattern as env_json elsewhere in Project.
  const initialIsolation = (() => {
    try {
      return JSON.parse(p.default_isolation_json || "{}") as IsolationConfig;
    } catch {
      return {} as IsolationConfig;
    }
  })();
  const [isolationMode, setIsolationMode] = useState<"" | "bwrap" | "docker">(
      initialIsolation.mode || "",
    ),
    [isolationNetwork, setIsolationNetwork] = useState<"allow" | "deny">(
      initialIsolation.network === "deny" ? "deny" : "allow",
    );
  const [mcp, setMcp] = useState("{}"),
    [mcpStatus, setMcpStatus] = useState(() => t("settings.common.loading")),
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
    [setupStatus, setSetupStatus] = useState(""),
    [checkCmd, setCheckCmd] = useState(p.verify_cmd),
    [checkStatus, setCheckStatus] = useState(""),
    [ciLoop, setCiLoop] = useState(Boolean(p.ci_loop)),
    [ciMax, setCiMax] = useState(p.ci_max_attempts || 3),
    [computerUse, setComputerUse] = useState(Boolean(p.computer_use)),
    [autoDetect, setAutoDetect] = useState<{ command: string; source: string }>();
  function loadCheckCommand() {
    api
      .request<{ command: string; source: string }>(`/projects/${p.id}/check-command`)
      .then(setAutoDetect)
      .catch(() => {});
  }
  useEffect(() => {
    loadCheckCommand();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.id]);
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
        setMcpStatus(t("settings.projects.mcpLoaded"));
        setRevision(m.revision);
        setStrict(m.strict_mcp);
        setCap(capabilityText(c));
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
        isolation:
          isolationMode === ""
            ? { mode: "" }
            : { mode: isolationMode, network: isolationNetwork },
      },
    });
    onNotice(t("settings.projects.saved"));
    await onChanged();
  }
  // The CI loop settings save as they change, like the capability picker.
  function saveCI(body: { ci_loop?: boolean; ci_max_attempts?: number }) {
    void api
      .request(`/projects/${p.id}`, { method: "PATCH", body })
      .then(() => onNotice(t("settings.projects.ciSaved")))
      .catch((error) => onNotice(String(error), true));
  }
  async function saveSetup() {
    try {
      await api.request(`/projects/${p.id}`, { method: "PATCH", body: { setup_cmd: setup } });
      setSetupStatus(t("settings.projects.setupSaved")); await onChanged();
    } catch (error) { setSetupStatus(error instanceof Error ? error.message : String(error)); }
  }
  async function saveCheckCmd() {
    try {
      await api.request(`/projects/${p.id}`, { method: "PATCH", body: { verify_cmd: checkCmd } });
      setCheckStatus(t("settings.projects.checkSaved"));
      loadCheckCommand();
      await onChanged();
    } catch (error) { setCheckStatus(error instanceof Error ? error.message : String(error)); }
  }
  async function saveMCP() {
    let next: Record<string, JsonValue>;
    try {
      next = JSON.parse(mcp) as Record<string, JsonValue>;
    } catch {
      return onNotice(t("settings.projects.mcpInvalid"), true);
    }
    try {
      const saved = await api.request<{ revision: string }>(
        `/projects/${p.id}/mcp`,
        { method: "PUT", body: { mcp: next, revision, strict_mcp: strict } },
      );
      setRevision(saved.revision);
      setMcpStatus(t("settings.projects.mcpSavedStatus"));
      onNotice(t("settings.projects.mcpSaved"));
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
        setMcpStatus(t("settings.projects.mcpConflict"));
        onNotice(
          t("settings.projects.mcpConflict"),
          true,
        );
      } catch (refresh) {
        onNotice(
          t("settings.projects.mcpConflictLoad", { error: String(refresh) }),
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
        {t("settings.projects.capabilityLabel")}
        <select className="cap-sel" value={profile} onChange={(e) => {
          const value = e.target.value;
          setProfile(value);
          void api.request(`/projects/${p.id}`, { method: "PATCH", body: { capability_profile: value } }).then(async () => {
            const c = await api.request<{profile:string;allow:string[];mcp_servers:string[];memory_dir:string}>(`/projects/${p.id}/capability`);
            setCap(capabilityText(c));
          }).catch((error) => onNotice(String(error), true));
        }}>
          <option value="restricted">{t("settings.projects.capRestricted")}</option>
          <option value="parity">{t("settings.projects.capParity")}</option>
        </select>
      </label>
      <label>
        {t("settings.projects.permission")}
        <select value={perm} onChange={(e) => setPerm(e.target.value)}>
          <option value="">{t("settings.projects.taskDefault")}</option>
          <option>default</option>
          <option>acceptEdits</option>
          <option>plan</option>
          <option>bypassPermissions</option>
        </select>
      </label>
      <label>
        {t("settings.projects.isolation")}
        <select
          value={isolationMode}
          onChange={(e) =>
            setIsolationMode(e.target.value as typeof isolationMode)
          }
        >
          <option value="">{t("settings.projects.isolationNone")}</option>
          <option value="bwrap">{t("settings.projects.isolationBwrap")}</option>
          <option value="docker">{t("settings.projects.isolationDocker")}</option>
        </select>
      </label>
      {isolationMode !== "" && (
        <label>
          {t("settings.projects.isolationNetwork")}
          <select
            value={isolationNetwork}
            onChange={(e) =>
              setIsolationNetwork(e.target.value as typeof isolationNetwork)
            }
          >
            <option value="allow">{t("settings.projects.networkAllow")}</option>
            <option value="deny">{t("settings.projects.networkDeny")}</option>
          </select>
        </label>
      )}
      <label>
        {t("settings.projects.check")}
        <input
          aria-label={t("settings.projects.check")}
          value={checkCmd}
          placeholder={
            autoDetect?.source === "auto"
              ? t("settings.projects.autoDetected", { command: autoDetect.command })
              : t("settings.projects.checkExample")
          }
          onChange={(e) => setCheckCmd(e.target.value)}
        />
      </label>
      <button onClick={() => void saveCheckCmd()}>{t("settings.projects.saveCheck")}</button>
      <p className="project-check-status" role="status">
        {checkStatus ||
          (checkCmd
            ? ""
            : autoDetect?.source === "auto"
              ? t("settings.projects.checkAuto", { command: autoDetect.command })
              : t("settings.projects.checkNone"))}
      </p>
      <label>
        <input
          type="checkbox"
          aria-label={t("settings.projects.ciFix")}
          checked={ciLoop}
          onChange={(e) => {
            setCiLoop(e.target.checked);
            saveCI({ ci_loop: e.target.checked });
          }}
        />{" "}
        {t("settings.projects.ciFixHint")}
      </label>
      {ciLoop && (
        <label>
          {t("settings.projects.ciAttempts")}
          <input
            type="number"
            aria-label={t("settings.projects.ciAttempts")}
            min={1}
            max={10}
            value={ciMax}
            onChange={(e) => setCiMax(Math.min(10, Math.max(1, Number(e.target.value) || 1)))}
            onBlur={() => saveCI({ ci_max_attempts: ciMax })}
          />
        </label>
      )}
      <label>
        <input
          type="checkbox"
          aria-label={t("settings.projects.computerUse")}
          checked={computerUse}
          onChange={(e) => {
            const on = e.target.checked;
            void api
              .request(`/projects/${p.id}`, { method: "PATCH", body: { computer_use: on } })
              .then(() => {
                setComputerUse(on);
                onNotice(on ? t("settings.projects.computerUseOn") : t("settings.projects.computerUseOff"));
              })
              .catch((error) => onNotice(String(error), true));
          }}
        />{" "}
        {t("settings.projects.computerUseHint")}
      </label>
      <label>
        {t("settings.projects.setup")}
        <textarea aria-label={t("settings.projects.setup")} value={setup} onChange={(e) => setSetup(e.target.value)} />
      </label>
      <button onClick={() => void saveSetup()}>{t("settings.projects.saveSetup")}</button>
      <p className="project-setup-status" role="status">{setupStatus}</p>
      <button
        onClick={() => void save().catch((e) => onNotice(String(e), true))}
      >
        {t("settings.projects.save")}
      </button>
      <p className="cap-info">{cap}</p>
      <section className="project-mcp-editor">
        <h4 data-setting="projects.mcp">{t("settings.projects.mcpServers")}</h4>
        <p className="project-mcp-status" role="status">{mcpStatus}</p>
        {Object.entries(documentValue()).map(([name, raw], index) => {
          const spec = raw && typeof raw === "object" && !Array.isArray(raw) ? raw as Record<string, JsonValue> : {};
          const http = typeof spec.url === "string";
          const extra = Object.fromEntries(Object.entries(spec).filter(([key]) => key !== "command" && key !== "url"));
          return <div className="mcp-row" key={`${index}-${name}`}>
            <input className="mcp-name" aria-label={t("settings.projects.serverName")} value={name} onChange={(e) => changeMCP(index, "name", e.target.value)} />
            <select className="mcp-type" aria-label={t("settings.projects.serverType")} value={http ? "http" : "stdio"} onChange={(e) => changeMCP(index, "type", e.target.value)}><option value="stdio">stdio</option><option value="http">http</option></select>
            <input className="mcp-command" aria-label={http ? t("settings.projects.serverUrl") : t("settings.projects.serverCommand")} value={String(http ? spec.url || "" : spec.command || "")} onChange={(e) => changeMCP(index, "command", e.target.value)} />
            <textarea className="mcp-extra" aria-label={t("settings.projects.serverExtra")} value={JSON.stringify(extra, null, 2)} onChange={(e) => changeMCP(index, "extra", e.target.value)} />
            <button className="mcp-remove" onClick={() => { const entries=Object.entries(documentValue());entries.splice(index,1);setMcp(JSON.stringify(Object.fromEntries(entries),null,2)); }}>{t("settings.common.remove")}</button>
          </div>;
        })}
        <button className="project-mcp-add" onClick={() => { const entries=Object.entries(documentValue());entries.push([`server_${entries.length+1}`, {command:""}]);setMcp(JSON.stringify(Object.fromEntries(entries),null,2)); }}>{t("settings.projects.addServer")}</button>
      </section>
      <label>
        <input
          type="checkbox"
          checked={strict}
          onChange={(e) => setStrict(e.target.checked)}
        />{" "}
        {t("settings.projects.strictMcp")}
      </label>
      <button className="project-mcp-save" onClick={() => void saveMCP()}>{t("settings.projects.saveMcp")}</button>
      <LimitPolicyEditor api={api} projectId={p.id} onNotice={onNotice} />
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
      <Triggers api={api} projectId={p.id} onNotice={onNotice} />
      <TrackerSettings api={api} projectId={p.id} onNotice={onNotice} />
      <button
        onClick={() => {
          if (confirm(t("settings.projects.deleteOne", { name: p.name })))
            void api
              .request(`/projects/${p.id}?cascade=true`, { method: "DELETE" })
              .then(onChanged);
        }}
      >
        {t("settings.projects.delete")}
      </button>
    </article>
  );
}
function Notifications({
  api,
  values,
  onEnablePush,
  pushAvailable = true,
  pushUnavailableReason,
  pushUnavailableReasonKind,
  pushEndpoint,
  onUnsubscribePush,
  onNotice,
}: {
  api: SettingsApi;
  values: Record<string, JsonValue>;
  onEnablePush(): void;
  pushAvailable?: boolean;
  pushUnavailableReason?: string;
  pushUnavailableReasonKind?: "insecure" | "ios-not-installed" | "unsupported";
  pushEndpoint?: string | null;
  onUnsubscribePush?(endpoint: string): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [discord, setDiscord] = useState(String(values.discord_webhook ?? "")),
    [server, setServer] = useState(String(values.ntfy_server ?? "")),
    [topic, setTopic] = useState(String(values.ntfy_topic ?? ""));
  // Per-kind session-alert toggles (docs/agent-events.md section 3): stored
  // as "0"/"1" strings, default ON — missing or anything but "0" means the
  // alert is enabled (internal/alerts.Watcher.enabled).
  const alertKeys: [string, string][] = [
    ["alert_waiting_permission", t("settings.notifications.alertPermission")],
    ["alert_waiting_input", t("settings.notifications.alertInput")],
    ["alert_idle", t("settings.notifications.alertIdle")],
    ["alert_error", t("settings.notifications.alertError")],
    ["alert_compacting", t("settings.notifications.alertCompacting")],
  ];
  const [alerts, setAlerts] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(alertKeys.map(([key]) => [key, String(values[key] ?? "") !== "0"])),
  );
  const [permissionMode, setPermissionMode] = useState(
    String(values.session_permission_mode ?? "") === "ask" ? "ask" : "bypass",
  );
  const [devices, setDevices] = useState<PushSubscriptionInfo[]>([]);
  const loadDevices = () =>
    void api
      .request<PushSubscriptionInfo[]>("/push/subscriptions")
      .then(setDevices)
      .catch(() => {}); // the section still works with sinks alone
  // Reload whenever this device's own subscription state settles (after
  // enabling or unsubscribing), plus once up front.
  useEffect(loadDevices, [pushEndpoint]);
  return (
    <article>
      <h3>{t("settings.section.notifications")}</h3>
      <div className="push-status" data-setting="notifications.push">
        {!pushAvailable ? (
          <>
            <p className="subhint" id="push-unavailable-reason">
              {pushUnavailableReason || t("settings.notifications.unavailable")}
            </p>
            {pushUnavailableReasonKind === "ios-not-installed" && (
              <ol className="push-ios-steps" id="push-ios-steps">
                <li>
                  {t("settings.notifications.iosStep1")} <strong>{t("settings.notifications.iosShare")}</strong> {t("settings.notifications.iosStep1After")}
                </li>
                <li>
                  {t("settings.notifications.iosStep2")} <strong>{t("settings.notifications.iosAddToHome")}</strong>{t("settings.notifications.iosStepEnd")}
                </li>
                <li>
                  {t("settings.notifications.iosStep3")} <strong>{t("settings.notifications.iosAdd")}</strong> {t("settings.notifications.iosStep3After")}
                </li>
                <li>{t("settings.notifications.iosStep4")}</li>
                <li>
                  {t("settings.notifications.iosStep5")}{" "}
                  <strong>{t("settings.notifications.enable")}</strong>{t("settings.notifications.iosStepEnd")}
                </li>
              </ol>
            )}
          </>
        ) : pushEndpoint ? (
          <p className="subhint" id="push-enabled-hint">
            {t("settings.notifications.enabled")}
          </p>
        ) : (
          <button id="s-enable-push" onClick={onEnablePush}>
            {t("settings.notifications.enable")}
          </button>
        )}
        {devices.length > 0 && (
          <ul className="push-devices" id="push-devices">
            {devices.map((d) => (
              <li key={d.id} data-endpoint={d.endpoint}>
                <span>
                  {shortEndpoint(d.endpoint)}
                  {d.endpoint === pushEndpoint && (
                    <em className="push-this-device"> · {t("settings.notifications.thisDevice")}</em>
                  )}
                </span>
                {onUnsubscribePush && (
                  <button
                    className="b"
                    aria-label={t("settings.notifications.unsubscribeName", { name: shortEndpoint(d.endpoint) })}
                    onClick={() => {
                      onUnsubscribePush(d.endpoint);
                      setDevices((old) => old.filter((row) => row.id !== d.id));
                    }}
                  >
                    {t("settings.notifications.unsubscribe")}
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
      <label data-setting="notifications.sinks">
        {t("settings.notifications.discord")}
        <input id="s-discord" value={discord} onChange={(e) => setDiscord(e.target.value)} />
      </label>
      <label>
        {t("settings.notifications.ntfyServer")}
        <input id="s-ntfy-server" value={server} onChange={(e) => setServer(e.target.value)} />
      </label>
      <label>
        {t("settings.notifications.ntfyTopic")}
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
            .then(() => onNotice(t("settings.notifications.sinksSaved")))
        }
      >
        {t("settings.notifications.saveSinks")}
      </button>
      <button
        onClick={() =>
          void api
            .request("/settings/test-notification", { method: "POST" })
            .then(() => onNotice(t("settings.notifications.testSent")))
        }
      >
        {t("settings.notifications.sendTest")}
      </button>

      <h4 data-setting="notifications.sessionAlerts">{t("settings.notifications.sessionAlerts")}</h4>
      <p className="subhint">
        {t("settings.notifications.sessionAlertsHint")}
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
            .then(() => onNotice(t("settings.notifications.alertsSaved")))
        }
      >
        {t("settings.notifications.saveAlerts")}
      </button>

      <h4 data-setting="projects.permission">{t("settings.notifications.permissionMode")}</h4>
      <p className="subhint">
        {t("settings.notifications.permissionHint")}
      </p>
      <label>
        <select
          id="s-permission-mode"
          value={permissionMode}
          onChange={(e) => setPermissionMode(e.target.value)}
        >
          <option value="bypass">{t("settings.notifications.bypass")}</option>
          <option value="ask">{t("settings.notifications.ask")}</option>
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
            .then(() => onNotice(t("settings.notifications.permissionSaved")))
        }
      >
        {t("settings.notifications.saveDefault")}
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
  useLocale();
  const [w, setW] = useState<Whoami>();
  useEffect(() => {
    void api.request<Whoami>("/whoami").then(setW);
  }, []);
  if (!w) return null;
  const identity =
    w.kind === "tailscale"
      ? `tailscale · ${w.login}${w.node ? ` (${w.node})` : ""}`
      : w.kind === "token"
        ? t("settings.about.accessToken")
        : w.kind === "device"
          ? (w.login ? t("settings.about.pairedDeviceAs", { login: w.login }) : t("settings.about.pairedDevice"))
          : t("settings.about.local");
  return (
    <article id="whoami">
      <h3 data-setting="about.signedIn">{t("settings.about.signedIn")}</h3>
      <p>
        {t("settings.about.identity", { identity, mode: w.mode })}
      </p>
    </article>
  );
}
function Build({ api }: { api: SettingsApi }) {
  useLocale();
  const [h, setH] = useState<Health>();
  useEffect(() => {
    void api.request<Health>("/health").then(setH);
  }, []);
  return (
    <article id="running-build">
      <h3 data-setting="about.build">{t("settings.about.build")}</h3>
      <p>
        {h
          ? t("settings.about.buildLine", { version: h.version, revision: h.build?.revision?.slice(0, 12) || t("settings.about.revisionUnknown"), state: h.build?.modified === true ? t("settings.about.localChanges") : h.build?.modified === false ? t("settings.about.clean") : t("settings.about.buildUnknown") })
          : t("settings.common.loading")}
      </p>
    </article>
  );
}
function Agents({
  api,
  agents,
  profiles,
  targets,
  onChanged,
  onNotice,
}: {
  api: SettingsApi;
  agents: Agent[];
  profiles: Profile[];
  targets: Target[];
  onChanged(): Promise<void>;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [editing, setEditing] = useState<Agent | "new">(),
    [profile, setProfile] = useState<Profile | "new">(),
    [startersOpen, setStartersOpen] = useState(false),
    [menu, setMenu] = useState<string[]>([]),
    [menuBusy, setMenuBusy] = useState(false);
  useEffect(() => {
    void fetchAgentMenu(api).then(setMenu);
  }, [agents.map((a) => a.name).join(",")]);
  async function persistMenu(next: string[]) {
    setMenu(next);
    setMenuBusy(true);
    try {
      const saved = await saveAgentMenu(api, next);
      setMenu(saved);
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setMenuBusy(false);
    }
  }
  function toggleShown(name: string, shown: boolean) {
    void persistMenu(shown ? [...menu, name] : menu.filter((n) => n !== name));
  }
  function move(name: string, dir: -1 | 1) {
    const i = menu.indexOf(name);
    if (i < 0) return;
    const j = i + dir;
    if (j < 0 || j >= menu.length) return;
    const next = [...menu];
    const a = next[i],
      b = next[j];
    if (a === undefined || b === undefined) return;
    next[i] = b;
    next[j] = a;
    void persistMenu(next);
  }
  const hidden = agents.filter((a) => !menu.includes(a.name));
  return (
    <section>
      <h3 data-setting="agents.menus">{t("settings.agentsTab.menus")}</h3>
      <p className="sub">
        {t("settings.agentsTab.menusHint")}
      </p>
      <ul className="agent-menu-order" aria-label={t("settings.agentsTab.shown")}>
        {menu.map((name, i) => (
          <li key={name}>
            <label>
              <input
                type="checkbox"
                checked
                disabled={menuBusy}
                onChange={() => toggleShown(name, false)}
              />{" "}
              {name}
            </label>
            <button
              type="button"
              aria-label={t("settings.moveUp", { name })}
              disabled={menuBusy || i === 0}
              onClick={() => move(name, -1)}
            >
              ↑
            </button>
            <button
              type="button"
              aria-label={t("settings.moveDown", { name })}
              disabled={menuBusy || i === menu.length - 1}
              onClick={() => move(name, 1)}
            >
              ↓
            </button>
          </li>
        ))}
      </ul>
      {hidden.length > 0 && (
        <>
          <p className="sub">{t("settings.agentsTab.hiddenHint")}</p>
          <ul className="agent-menu-hidden" aria-label={t("settings.agentsTab.hidden")}>
            {hidden.map((a) => (
              <li key={a.name}>
                <label>
                  <input
                    type="checkbox"
                    checked={false}
                    disabled={menuBusy}
                    onChange={() => toggleShown(a.name, true)}
                  />{" "}
                  {a.name}
                </label>
              </li>
            ))}
          </ul>
        </>
      )}
      <h3 data-setting="agents.runners">{t("settings.agentsTab.runners")}</h3>
      {agents.map((a) => (
        <article className="agent-card" key={a.name}>
          <b>
            {a.name}
            {a.builtin ? ` · ${t("settings.agentsTab.builtin")}` : ` · ${t("settings.agentsTab.custom")}`}
          </b>
          <span>
            {a.command} · {a.model_flag || t("settings.agentsTab.noModelFlag")}
          </span>
          <button onClick={() => setEditing(a)}>{t("settings.common.edit")}</button>
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
              {t("settings.common.delete")}
            </button>
          )}
        </article>
      ))}
      <button onClick={() => setEditing("new")}>{t("settings.agentsTab.addAgent")}</button>
      <h3 id="launch-profiles" data-setting="agents.profiles">{t("settings.agentsTab.profiles")}</h3>
      {profiles.map((p) => (
        <article key={p.id}>
          <b>{p.name}</b> · {p.agent} · {p.model}
          {p.description && <p>{p.description}</p>}
          <button onClick={() => setProfile(p)}>{t("settings.agentsTab.editProfile")}</button>
          <button
            onClick={() =>
              void api
                .request(`/launch-profiles/${p.id}`, { method: "DELETE" })
                .then(onChanged)
            }
          >
            {t("settings.agentsTab.deleteProfile")}
          </button>
        </article>
      ))}
      <button onClick={() => setProfile("new")}>{t("settings.agentsTab.newProfile")}</button>
      <button onClick={() => setStartersOpen(true)}>{t("settings.agentsTab.starters")}</button>
      {startersOpen && <LaunchProfiles api={api} onClose={() => setStartersOpen(false)} onChange={() => void onChanged()} />}
      {editing && (
        <AgentEditor
          api={api}
          source={editing === "new" ? undefined : editing}
          all={agents}
          targets={targets}
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
  useLocale();
  const [name, setName] = useState(source?.name || ""),
    [agent, setAgent] = useState(source?.agent || agents[0]?.name || "claude"),
    [command, setCommand] = useState(source?.command || ""),
    [model, setModel] = useState(source?.model || ""),
    [description, setDescription] = useState(source?.description || ""),
    [instructions, setInstructions] = useState(source?.instructions || ""),
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
      throw Error(t("settings.profileEditor.envInvalid"));
    }
    if (
      !parsed ||
      Array.isArray(parsed) ||
      typeof parsed !== "object" ||
      Object.values(parsed).some((v) => typeof v !== "string")
    )
      throw Error(t("settings.profileEditor.envShape"));
    if (!name.trim()) throw Error(t("settings.profileEditor.nameRequired"));
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
            description: description.trim(),
            instructions: instructions.trim(),
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
      aria-label={t("settings.agentsTab.profiles")}
      onCancel={(e) => {
        if (busy) e.preventDefault();
      }}
    >
      <h2>{source ? t("settings.profileEditor.edit") : t("settings.profileEditor.new")}</h2>
      <button disabled={busy} onClick={onClose}>
        {t("settings.common.close")}
      </button>
      <label>
        {t("settings.profileEditor.name")}
        <input value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label>
        {t("settings.profileEditor.agent")}
        <select value={agent} onChange={(e) => setAgent(e.target.value)}>
          {agents.map((a) => (
            <option key={a.name}>{a.name}</option>
          ))}
        </select>
      </label>
      <label>
        {t("settings.profileEditor.description")}
        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </label>
      <label>
        {t("settings.profileEditor.instructions")}
        <textarea
          value={instructions}
          onChange={(e) => setInstructions(e.target.value)}
        />
      </label>
      <p className="lp-hint">{instructionsHelp()}</p>
      <label>
        {t("settings.profileEditor.command")}
        <input value={command} onChange={(e) => setCommand(e.target.value)} />
      </label>
      <label>
        {t("settings.profileEditor.model")}
        <input value={model} onChange={(e) => setModel(e.target.value)} />
      </label>
      <label>
        {t("settings.profileEditor.environment")}
        <textarea
          value={environment}
          onChange={(e) => setEnvironment(e.target.value)}
        />
      </label>
      <button
        disabled={busy}
        onClick={() => void save().catch((e) => onNotice(String(e), true))}
      >
        {t("settings.profileEditor.save")}
      </button>
    </Modal>
  );
}
