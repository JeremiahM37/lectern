import { RepositoryPicker, type RepositorySelection } from "./RepositoryPicker";
import { LaunchProfiles } from "../settings/LaunchProfiles";
import { Modal } from "./Modal";
import { useEffect, useState } from "react";
import type { Claim, Project, SessionView, Target } from "../types";
import { FolderPicker, defaultMachine, type PickedFolder } from "./FolderPicker";
import { claimScopeLabel } from "../claims/ClaimsPanel";
import { fetchAgentMenu, splitAgentMenu } from "../agents/menu";
import { AllAgentsPicker } from "../agents/AllAgentsPicker";
import {
  defaultProject,
  orderProjectsByRecency,
  readProjectPreference,
  rememberProjectSelection,
  rememberRecentProject,
} from "../project-preference";
import type { SessionsApi } from "./Sessions";
import { t, useLocale } from "../i18n";
interface Candidate {
  target_id: number;
  tmux_session: string;
  agent: string;
  model: string;
  workdir: string;
}
type Agent = {
  name: string;
  builtin?: boolean;
  model_flag?: string;
  resume_args?: string[];
  yolo_args?: string[];
};
type LaunchProfile = {
  command?: string;
  env_json?: string;
  id: number;
  name: string;
  agent: string;
  model?: string;
  description?: string;
  instructions?: string;
};
// "Start an agent" (docs/design/simple-ui.md): a folder, an agent and one
// safety toggle, with everything else under More options. Starting in a
// folder that is not a project yet registers it first.
export function NewSession({
  api,
  projects,
  targets = [],
  initialProject,
  onClose,
  onCreated,
  onNotice,
}: {
  api: SessionsApi;
  projects: Project[];
  targets?: Target[];
  /** Open on this project (e.g. the folder `lectern up` ran in). */
  initialProject?: number;
  onClose(): void;
  // Also called with no session after the sheet added a machine.
  onCreated(s?: SessionView): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [agents, setAgents] = useState<Agent[]>([]),
    [profiles, setProfiles] = useState<LaunchProfile[]>([]),
    [manageProfiles, setManageProfiles] = useState(false),
    [profileId, setProfileId] = useState(0),
    [models, setModels] = useState<Record<string, string[]>>({}),
    [memoryStatus, setMemoryStatus] = useState(""),
    [memoryKind, setMemoryKind] = useState(""),
    // What this device opened last: a project, or a deliberate blank room.
    // Nothing usable stored (first visit, deleted project, corrupt value)
    // keeps the old default of the first project.
    [preference] = useState(() => readProjectPreference()),
    [project, setProject] = useState<number | null>(() => defaultProject(projects, preference.last, initialProject)),
    // Until the person picks a folder, the default follows the project list,
    // which may still be loading when the sheet opens (the landing link).
    [projectTouched, setProjectTouched] = useState(false),
    [name, setName] = useState(""),
    [group, setGroup] = useState(""),
    [agent, setAgent] = useState("claude"),
    // A folder chosen in the picker that is not a project yet.
    [picked, setPicked] = useState<PickedFolder>(),
    [browsing, setBrowsing] = useState(false),
    // The machines to choose from. A brand-new server has none; the first
    // folder or start then adds this computer (ensureMachine).
    [machines, setMachines] = useState<Target[]>(targets),
    // Whether the person chose the agent themselves; until then the sheet
    // may switch to one that is actually installed.
    [agentTouched, setAgentTouched] = useState(false),
    // Which agent commands the folder's machine has: available, missing or
    // unchecked (GET /api/targets/{id}/agents). Undefined until known.
    [installed, setInstalled] = useState<Record<string, string>>(),
    [model, setModel] = useState(""),
    [mode, setMode] = useState("fresh"),
    // Asks until the install's default says otherwise: the safe side while
    // the setting loads (a new install asks; an unset one runs freely).
    [yolo, setYolo] = useState(false),
    [prime, setPrime] = useState(""),
    [isolated, setIsolated] = useState(false),
    // Sandbox isolation (internal/isolation, docs/isolation.md) — distinct
    // from `isolated` above, which is the git-worktree checkout, not a
    // process sandbox. "" means "use the project's default".
    [sandboxMode, setSandboxMode] = useState<"" | "none" | "bwrap" | "docker">(""),
    [sandboxNetwork, setSandboxNetwork] = useState<"allow" | "deny">("allow"),
    [base, setBase] = useState(""),
    [branch, setBranch] = useState(""),
    [extra, setExtra] = useState<RepositorySelection[]>([]),
    [busy, setBusy] = useState(false),
    [agentMenu, setAgentMenu] = useState<string[]>([]),
    [showAllAgents, setShowAllAgents] = useState(false),
    [overlaps, setOverlaps] = useState<Claim[]>([]);
  useEffect(() => {
    void Promise.all([
      api.request<Agent[]>("/agents"),
      api.request<LaunchProfile[]>("/launch-profiles"),
      api.request<Record<string, string[]>>("/models").catch(() => ({})),
    ])
      .then(([a, p, m]) => {
        setAgents(a);
        setProfiles(p);
        setModels(m);
      })
      .catch((error) =>
        onNotice(t("sessions.dialogs.newSession.loadFailed", { error: String(error) }), true),
      );
    // The operator's global default (docs/agent-events.md section 3) is
    // what the "Ask before risky actions" toggle opens set to — its own explicit
    // choice, once touched, is still what actually launches: the request
    // always sends a concrete `yolo` boolean, never omits it.
    void api
      .request<Record<string, string>>("/settings")
      .then((settings) => setYolo(settings.session_permission_mode !== "ask"))
      .catch(() => {});
    void fetchAgentMenu(api).then(setAgentMenu);
  }, []);
  useEffect(() => {
    const selected = profiles.find((p) => p.id === profileId);
    if (selected) setAgent(selected.agent);
  }, [profileId, profiles]);
  useEffect(() => {
    let current = true;
    setMemoryStatus(
      mode === "brief" && project ? t("sessions.dialogs.newSession.checkingMemory") : "",
    );
    setMemoryKind("");
    if (mode === "brief" && project)
      void api
        .request<{ memory?: { message?: string; status?: string } }>(
          `/projects/${project}/brief`,
        )
        .then((r) => {
          if (current) {
            setMemoryStatus(r.memory?.message || t("sessions.dialogs.newSession.memoryUnavailable"));
            setMemoryKind(r.memory?.status || "unavailable");
          }
        })
        .catch(
          () =>
            current &&
            setMemoryStatus(
              t("sessions.dialogs.newSession.memoryPreviewFailed"),
            ),
        );
    return () => {
      current = false;
    };
  }, [mode, project]);
  const spec = agents.find((row) => row.name === agent);
  const profile = profiles.find((row) => row.id === profileId);
  const yoloSupported = !spec || !!spec.yolo_args?.length;
  useEffect(() => {
    if (!project) {
      setIsolated(false);
      if (mode === "brief") setMode("fresh");
    }
    if (isolated && mode === "resume") setMode("fresh");
  }, [project, isolated, mode]);
  useEffect(() => {
    if (!yoloSupported) setYolo(false);
  }, [yoloSupported]);
  // Same advisory topic check the New task dialog runs (docs/claims.md):
  // warn before launch when the name or first message resembles work another
  // session has claimed in this project's repository. Never blocks.
  useEffect(() => {
    const text = `${name} ${prime}`.trim();
    if (!project || text.length < 8) {
      setOverlaps([]);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      void api
        .request<Claim[]>(
          `/claims/topic-overlap?project_id=${project}&text=${encodeURIComponent(text)}`,
          { signal: controller.signal },
        )
        .then(setOverlaps)
        .catch(() => {});
    }, 500);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [project, name, prime]);
  useEffect(() => {
    if (!projectTouched && !picked) setProject(defaultProject(projects, preference.last, initialProject));
  }, [projects, initialProject]);
  const selectedProject = projects.find((row) => row.id === project) ?? null;
  useEffect(() => {
    if (targets.length) setMachines(targets);
  }, [targets]);
  const machine = selectedProject?.target_id ?? picked?.targetId ?? defaultMachine(machines);
  // With no machine at all, this computer is the one: add it, once.
  async function ensureMachine(): Promise<void> {
    if (machines.length) return;
    const created = await api.request<Target>("/targets", { method: "POST", body: { name: "local", kind: "local" } });
    setMachines([created]);
    onCreated();
  }
  async function browse() {
    try {
      await ensureMachine();
      setBrowsing(true);
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  useEffect(() => {
    if (!machine) return;
    let live = true;
    void api
      .request<{ name: string; state: string }[]>(`/targets/${machine}/agents`)
      .then((rows) => live && setInstalled(Object.fromEntries(rows.map((row) => [row.name, row.state]))))
      .catch(() => live && setInstalled(undefined));
    return () => {
      live = false;
    };
  }, [machine]);
  // Only agents whose command is on that machine (or that cannot be checked)
  // are offered; the rest wait behind "More agents…".
  const usable = (name: string) => !installed || installed[name] !== "missing";
  // A profile can choose a different executable or PATH; the default-agent
  // probe cannot decide its availability. The server checks that exact launch.
  const profileCommand = !!profile?.command?.trim() || (() => {
    try { return Object.hasOwn(JSON.parse(profile?.env_json || "{}"), "PATH"); } catch { return false; }
  })();
  const unavailable = agent !== "demo" && !profileCommand && !usable(agent);
  const noAgent = !!installed && agents.length > 0 && !agents.some((row) => usable(row.name));
  useEffect(() => {
    if (agentTouched || !installed || profileId > 0 || usable(agent) || agent === "demo") return;
    const ordered = [...agentMenu, ...agents.map((row) => row.name)];
    const first = ordered.find((name) => agents.some((row) => row.name === name) && usable(name));
    setAgent(first || "demo");
  }, [installed, agents, agentMenu, agentTouched, profileId]);
  // The collapsed sheet still has to say what pressing Start will do: the
  // permission mode above all, plus anything else hidden behind Advanced.
  const { recent: recentProjects, rest: otherProjects } = orderProjectsByRecency(
    projects,
    preference.recent,
  );
  const launchNotes = [
    !yoloSupported
      ? t("sessions.dialogs.newSession.noteNoYolo", { agent })
      : yolo
        ? t("sessions.dialogs.newSession.yolo")
        : t("sessions.dialogs.newSession.noteAsks"),
    profile ? t("sessions.dialogs.newSession.noteProfile", { name: profile.name, agent: profile.agent }) : "",
    isolated ? t("sessions.dialogs.newSession.noteWorktree") : "",
    mode === "brief" ? t("sessions.dialogs.newSession.noteBrief") : "",
    mode === "resume" ? t("sessions.dialogs.newSession.noteResume") : "",
  ].filter(Boolean);
  const launchSummary = launchNotes.join(" · ");
  // A picked folder that is not a project yet becomes one first, so the
  // session is in a project from its first moment (and shows as one).
  async function folderProject(): Promise<number | null> {
    if (!picked) return project;
    if (picked.projectId) return picked.projectId;
    const out = await api.request<{ imported: Project[]; skipped: { project_id?: number }[] }>("/projects/import", {
      method: "POST",
      body: { target_id: picked.targetId, paths: [picked.path] },
    });
    const id = out.imported[0]?.id ?? out.skipped[0]?.project_id;
    if (!id) throw new Error(t("start.sheet.registerFailed", { path: picked.path }));
    return id;
  }
  async function start() {
    if (busy || unavailable) return;
    setBusy(true);
    try {
      await ensureMachine();
      const projectID = await folderProject();
      const s = await api.request<SessionView>("/sessions", {
        method: "POST",
        body: {
          background: isolated,
          profile_id: profileId,
          project_id: projectID,
          scratch: projectID === null,
          worktree: isolated
            ? {
                base: base.trim(),
                branch: branch.trim(),
                extra_repositories: extra.map((row) => ({
                  project_id: row.project_id,
                  base: row.base.trim(),
                })),
              }
            : null,
          name: name.trim(),
          group_path: group.trim(),
          agent,
          model: model.trim(),
          resume: mode === "resume",
          brief: mode === "brief",
          yolo,
          prime: prime.trim(),
          // null means "use the project's (or built-in) default"; "none" is
          // an explicit opt-out even when that default is sandboxed.
          isolation:
            sandboxMode === ""
              ? null
              : {
                  mode: sandboxMode === "none" ? "" : sandboxMode,
                  network: sandboxNetwork,
                },
        },
      });
      // Only a session that actually started counts as a recent project.
      if (projectID !== null) {
        rememberRecentProject(projectID);
        rememberProjectSelection(projectID);
      }
      onCreated(s);
      onNotice(
        s.setup_state === "creating"
          ? t("sessions.dialogs.newSession.setupStarted")
          : t("sessions.dialogs.newSession.started"),
      );
      onClose();
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      {" "}
      <Modal
        id="sheet"
        open
        className="sheet"
        aria-label={t("sessions.dialogs.newSession.title")}
        onCancel={onClose}
      >
        <div className="sheet-head">
          <h2>{t("sessions.dialogs.newSession.title")}</h2>
          <button
            className="x"
            aria-label={t("sessions.dialogs.newSession.close")} data-close
            onClick={onClose}
          >
            ✕
          </button>
        </div>
        <p>
          {t("sessions.dialogs.newSession.intro")}
        </p>
        <div className="session-field">
          <label htmlFor="ns-project">{t("start.sheet.folder")}</label>
          <div className="ns-folder-row">
            <select
              id="ns-project"
              aria-label={t("start.sheet.folder")}
              value={picked ? "__folder__" : (project ?? "")}
              onChange={(e) => {
                if (e.target.value === "__browse__") {
                  void browse();
                  return;
                }
                if (e.target.value === "__folder__") return;
                const next = e.target.value ? Number(e.target.value) : null;
                setProjectTouched(true);
                setPicked(undefined);
                setProject(next);
                // An explicit choice is remembered even if the sheet is closed
                // again, so a deliberate empty folder does not snap back to a
                // project next time.
                rememberProjectSelection(next);
              }}
            >
              <option value="">{t("sessions.dialogs.newSession.blankRoom")}</option>
              {picked && <option value="__folder__">{t("start.sheet.pickedFolder", { path: picked.path })}</option>}
              {recentProjects.length > 0 ? (
                <>
                  <optgroup label={t("sessions.dialogs.newSession.recentProjects")}>
                    {recentProjects.map((p) => (
                      <option value={p.id} key={p.id}>
                        {p.name} — {p.target_name}
                      </option>
                    ))}
                  </optgroup>
                  <optgroup label={t("sessions.dialogs.newSession.otherProjects")}>
                    {otherProjects.map((p) => (
                      <option value={p.id} key={p.id}>
                        {p.name} — {p.target_name}
                      </option>
                    ))}
                  </optgroup>
                </>
              ) : (
                otherProjects.map((p) => (
                  <option value={p.id} key={p.id}>
                    {p.name} — {p.target_name}
                  </option>
                ))
              )}
              <option value="__browse__">{t("start.sheet.chooseOption")}</option>
            </select>
            <button type="button" className="b" id="ns-browse" onClick={() => void browse()}>
              {t("start.sheet.choose")}
            </button>
          </div>
        </div>
        <div id="ns-proj-hint">
          {picked ? (
            <>
              <span className="ns-proj-path">{picked.path}</span>
              {picked.projectId ? "" : ` · ${t("start.sheet.becomesProject")}`}
            </>
          ) : selectedProject === null ? (
            t("sessions.dialogs.newSession.blankRoomHint")
          ) : (
            <>
              <span className="ns-proj-path">{selectedProject.repo_path}</span>
              {selectedProject.target_name
                ? ` · ${selectedProject.target_name}`
                : ""}
              {isolated && selectedProject.setup_cmd ? (
                <span className="ns-proj-setup">
                  {t("sessions.dialogs.newSession.setupCommandHint")}
                </span>
              ) : null}
            </>
          )}
        </div>
        <div className="session-field">
          <label htmlFor="ns-agent">{t("sessions.dialogs.newSession.agent")}</label>
          <select
            id="ns-agent"
            value={agent}
            disabled={profileId > 0}
            onChange={(e) => {
              if (e.target.value === "__more__") {
                setShowAllAgents(true);
                return;
              }
              setAgentTouched(true);
              setAgent(e.target.value);
            }}
          >
            {(() => {
              const all = agents.length ? agents : [{ name: "claude" }];
              const { shown, more } = splitAgentMenu(all, agentMenu);
              // Installed first; an agent not found on the folder's machine
              // is one step away, under More agents.
              const offered = shown.filter((a) => usable(a.name));
              const options = offered.some((a) => a.name === agent)
                ? offered
                : [...offered, ...all.filter((a) => a.name === agent)];
              const hidden = more.length + shown.length - offered.length;
              return (
                <>
                  {options.map((a) => (
                    <option key={a.name} value={a.name} disabled={!usable(a.name)}>
                      {usable(a.name) ? a.name : t("start.sheet.notInstalled", { name: a.name })}
                    </option>
                  ))}
                  {agent === "demo" && !options.some((a) => a.name === "demo") && (
                    <option value="demo">{t("start.sheet.demoAgent")}</option>
                  )}
                  {hidden > 0 && (
                    <option value="__more__">{t("sessions.dialogs.moreAgents")}</option>
                  )}
                </>
              );
            })()}
          </select>
        </div>
        {noAgent && agent !== "demo" && (
          <p className="subhint" id="ns-no-agent" role="status">
            {t("start.sheet.noAgent")}{" "}
            <button
              type="button"
              className="linkish"
              id="ns-use-demo"
              onClick={() => {
                setAgentTouched(true);
                setAgent("demo");
              }}
            >
              {t("start.sheet.useDemo")}
            </button>
          </p>
        )}
        {showAllAgents && (
          <AllAgentsPicker
            agents={agents}
            onPick={(name) => {
              setAgentTouched(true);
              setAgent(name);
            }}
            onClose={() => setShowAllAgents(false)}
          />
        )}
        {profile && (
          <div className="subhint" id="ns-agent-profile">
            {t("sessions.dialogs.newSession.lockedByProfile", { agent: profile.agent, name: profile.name })}
          </div>
        )}
        <div id="ns-agent-hint" className="subhint">
          {spec
            ? [
                !spec.model_flag
                  ? t("sessions.dialogs.newSession.hintNoModel")
                  : "",
                !spec.resume_args ? t("sessions.dialogs.newSession.hintNoResume") : "",
              ]
                .filter(Boolean)
                .join(" · ")
            : ""}
        </div>
        <label className="f check ns-ask">
          <input
            id="ns-ask"
            type="checkbox"
            disabled={!yoloSupported}
            checked={!yolo}
            onChange={(e) => setYolo(!e.target.checked)}
          />{" "}
          {t("start.sheet.ask")}
        </label>
        <div className="subhint" id="ns-yolo-hint">
          {!yoloSupported
            ? t("sessions.dialogs.newSession.yoloUnsupported", { agent })
            : yolo
              ? t("sessions.dialogs.newSession.yoloHint")
              : agent === "claude" || agent === "codex"
                ? // Session permission mode (docs/agent-events.md section
                  // 3): asking launches claude/codex in "ask" mode, which is
                  // also what registers the PermissionRequest hook — so the
                  // question can be answered here or from a phone.
                  t("sessions.dialogs.newSession.askHintPhone")
                : t("sessions.dialogs.newSession.askHint")}
        </div>
        <details id="ns-advanced" className="session-advanced">
          <summary>{t("sessions.dialogs.newSession.advanced")}</summary>
          <div className="session-field">
            <label htmlFor="ns-name">{t("sessions.dialogs.newSession.name")}</label>
            <input
              id="ns-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="session-field">
            <label htmlFor="ns-group">{t("sessions.dialogs.newSession.group")}</label>
            <input
              id="ns-group"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
            />
          </div>
          <div className="session-field">
            <label htmlFor="ns-profile">{t("sessions.dialogs.newSession.launchProfile")}</label>
            <select
              id="ns-profile"
              value={profileId}
              onChange={(e) => setProfileId(Number(e.target.value))}
            >
              <option value={0}>{t("sessions.dialogs.newSession.noProfile")}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} · {p.agent}
                </option>
              ))}
            </select>
          </div>
          <button
            className="b"
            id="ns-manage-profiles"
            type="button"
            onClick={() => setManageProfiles(true)}
          >
            {t("sessions.dialogs.newSession.manageProfiles")}
          </button>
          <div className="subhint" id="ns-profile-hint" role="status">
            {profile && (
              <>
                {profile.model
                  ? t("sessions.dialogs.newSession.profileSummaryModel", { name: profile.name, agent: profile.agent, model: profile.model })
                  : t("sessions.dialogs.newSession.profileSummary", { name: profile.name, agent: profile.agent })}
                {profile.description && (
                  <span className="ns-profile-description">
                    {profile.description}
                  </span>
                )}
              </>
            )}
          </div>
          <div className="session-field">
            <label htmlFor="ns-model">{t("sessions.dialogs.model")}</label>
            <input
              placeholder={
                profile?.model
                  ? t("sessions.dialogs.newSession.modelOverride", { model: profile.model })
                  : models[agent] === undefined
                    ? t("sessions.dialogs.newSession.modelNone")
                    : models[agent]?.length
                      ? t("sessions.dialogs.newSession.modelDefault")
                      : t("sessions.dialogs.newSession.modelType")
              }
              id="ns-model"
              list="lec-models"
              disabled={
                models[agent] === undefined &&
                !agents.find((a) => a.name === agent)?.model_flag
              }
              value={model}
              onChange={(e) => setModel(e.target.value)}
            />
            <datalist id="lec-models">
              {(models[agent] || []).map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </div>

          <label>
            <input
              type="checkbox"
              id="ns-worktree"
              checked={isolated}
              disabled={!project}
              onChange={(e) => setIsolated(e.target.checked)}
            />{" "}
            {t("sessions.dialogs.newSession.worktree")}
            {!project && <span className="subhint disabled-why"> — {t("start.sheet.worktreeNeedsProject")}</span>}
          </label>
          {isolated && (
            <div id="ns-worktree-options">
              <p className="subhint">
                {t("sessions.dialogs.newSession.worktreeHint")}
              </p>
              <input
                id="ns-worktree-base"
                aria-label={t("sessions.dialogs.newSession.worktreeBase")}
                value={base}
                onChange={(e) => setBase(e.target.value)}
              />
              <input
                id="ns-worktree-branch"
                aria-label={t("sessions.dialogs.newSession.worktreeBranch")}
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
              />
              <div id="ns-repositories">
                <RepositoryPicker
                  projects={projects}
                  primaryID={project}
                  value={extra}
                  onChange={setExtra}
                />
              </div>
            </div>
          )}
          <label htmlFor="ns-start">{t("sessions.dialogs.newSession.startFrom")}</label>
          <select
            id="ns-start"
            value={mode}
            onChange={(e) => setMode(e.target.value)}
          >
            <option value="fresh">{t("sessions.dialogs.newSession.startFresh")}</option>
            <option value="brief" disabled={!project}>
              {t("sessions.dialogs.newSession.startBrief")}
            </option>
            <option value="resume" disabled={isolated}>
              {t("sessions.dialogs.newSession.startResume")}
            </option>
          </select>
          <div className="subhint" id="ns-hint">
            {mode === "brief"
              ? t("sessions.dialogs.newSession.briefHint")
              : mode === "resume"
                ? t("sessions.dialogs.newSession.resumeHint")
                : ""}
          </div>
          <div
            className="subhint"
            id="ns-memory-status"
            data-status={memoryKind}
            role="status"
          >
            {memoryStatus}
          </div>
          <label htmlFor="ns-isolation">{t("sessions.dialogs.newSession.isolation")}</label>
          <select
            id="ns-isolation"
            value={sandboxMode}
            onChange={(e) => setSandboxMode(e.target.value as typeof sandboxMode)}
          >
            <option value="">{t("sessions.dialogs.newSession.isolationDefault")}</option>
            <option value="none">{t("sessions.dialogs.newSession.isolationNone")}</option>
            <option value="bwrap">{t("sessions.dialogs.newSession.isolationBwrap")}</option>
            <option value="docker">{t("sessions.dialogs.newSession.isolationDocker")}</option>
          </select>
          {(sandboxMode === "bwrap" || sandboxMode === "docker") && (
            <>
              <label htmlFor="ns-isolation-network">{t("sessions.dialogs.newSession.network")}</label>
              <select
                id="ns-isolation-network"
                value={sandboxNetwork}
                onChange={(e) =>
                  setSandboxNetwork(e.target.value as typeof sandboxNetwork)
                }
              >
                <option value="allow">{t("sessions.dialogs.newSession.networkAllow")}</option>
                <option value="deny">
                  {t("sessions.dialogs.newSession.networkDeny")}
                </option>
              </select>
            </>
          )}
          <div className="subhint" id="ns-isolation-hint">
            {sandboxMode === "bwrap"
              ? t("sessions.dialogs.newSession.bwrapHint")
              : sandboxMode === "docker"
                ? t("sessions.dialogs.newSession.dockerHint")
                : ""}
          </div>
          <div className="session-field">
            <label htmlFor="ns-prime">{t("sessions.dialogs.newSession.firstMessage")}</label>
            <textarea
              id="ns-prime"
              value={prime}
              onChange={(e) => setPrime(e.target.value)}
            />
          </div>
        </details>
        {overlaps.length > 0 && (
          <div className="claims-overlap-warning" id="new-session-claim-overlap" role="status">
            {t("sessions.dialogs.newSession.overlapWarning")}
            <ul>
              {overlaps.map((c) => (
                <li key={c.id}>
                  <b>{c.holder}</b>{c.agent ? ` (${c.agent})` : ""} — {claimScopeLabel(c)}
                  {c.intent && <> — “{c.intent}”</>}
                </li>
              ))}
            </ul>
            {t("sessions.dialogs.newSession.overlapAdvice")}
          </div>
        )}
        <div className="session-launch-actions">
          <div
            className="ns-launch-summary"
            id="ns-launch-summary"
            role="status"
          >
            {launchSummary}
          </div>
          <button
            className="b ok grow"
            id="ns-go"
            disabled={busy || unavailable}
            onClick={() => void start()}
          >
            {t("sessions.dialogs.newSession.start")}
          </button>
        </div>
      </Modal>
      {browsing && (
        <FolderPicker
          request={api.request}
          targets={machines}
          initialTarget={machine}
          onClose={() => setBrowsing(false)}
          onEmpty={() => {
            setProjectTouched(true);
            setBrowsing(false);
            setPicked(undefined);
            setProject(null);
            rememberProjectSelection(null);
          }}
          onPick={(folder) => {
            setProjectTouched(true);
            setBrowsing(false);
            const known = folder.projectId ?? projects.find((row) => row.target_id === folder.targetId && row.repo_path.replace(/\/+$/, "") === folder.path)?.id;
            if (known) {
              setPicked(undefined);
              setProject(known);
              rememberProjectSelection(known);
            } else {
              setProject(null);
              setPicked({ ...folder, path: folder.path });
            }
          }}
        />
      )}
      {manageProfiles && (
        <LaunchProfiles
          api={api}
          onClose={() => setManageProfiles(false)}
          onChange={(saved) => {
            void api
              .request<LaunchProfile[]>("/launch-profiles")
              .then((rows) => {
                setProfiles(rows);
                if (saved) setProfileId(saved.id);
                else if (!rows.some((row) => row.id === profileId))
                  setProfileId(0);
              })
              .catch((error) => onNotice(String(error), true));
          }}
        />
      )}
    </>
  );
}
export function Discover({
  api,
  projects,
  onClose,
  onCreated,
  onNotice,
}: {
  api: SessionsApi;
  projects: Project[];
  onClose(): void;
  onCreated(): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [rows, setRows] = useState<Candidate[]>([]);
  useEffect(() => {
    void api
      .request<Candidate[]>("/sessions/discover")
      .then(setRows)
      .catch((e) => onNotice(String(e), true));
  }, []);
  return (
    <Modal
      id="sheet"
      open
      className="sheet"
      aria-label={t("sessions.dialogs.discover.title")}
      onCancel={onClose}
    >
      <h2>{t("sessions.dialogs.discover.title")}</h2>
      <button onClick={onClose}>✕</button>
      <p>{t("sessions.dialogs.discover.intro")}</p>
      {!rows.length && <p>{t("sessions.dialogs.discover.empty")}</p>}
      {rows.map((c) => (
        <article className="cand" key={`${c.target_id}-${c.tmux_session}`}>
          <b>{c.tmux_session}</b>
          <p>
            {c.agent} · {c.workdir}
          </p>
          <select aria-label={t("sessions.dialogs.discover.projectFor", { name: c.tmux_session })} defaultValue="">
            <option value="">{t("sessions.dialogs.discover.unassigned")}</option>
            {projects.map((p) => (
              <option value={p.id} key={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          <button
            onClick={(e) => {
              const select = e.currentTarget
                .previousElementSibling as HTMLSelectElement;
              void api
                .request("/sessions/adopt", {
                  method: "POST",
                  body: {
                    target_id: c.target_id,
                    tmux_session: c.tmux_session,
                    project_id: select.value ? Number(select.value) : null,
                    agent: c.agent,
                    model: c.model,
                    workdir: c.workdir,
                    name: c.tmux_session,
                  },
                })
                .then(() => {
                  onCreated();
                  onClose();
                })
                .catch((x) => onNotice(String(x), true));
            }}
          >
            {t("sessions.dialogs.discover.adopt")}
          </button>
        </article>
      ))}
    </Modal>
  );
}
export function Handoff({
  api,
  session,
  onClose,
  onCreated,
  onNotice,
}: {
  api: SessionsApi;
  session: SessionView;
  onClose(): void;
  onCreated(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  useLocale();
  const [successor, setSuccessor] = useState(true),
    [agent, setAgent] = useState(session.agent),
    [model, setModel] = useState(""),
    [kill, setKill] = useState(true),
    [agents, setAgents] = useState<Agent[]>([]),
    [busy, setBusy] = useState(false),
    [agentMenu, setAgentMenu] = useState<string[]>([]),
    [showAllAgents, setShowAllAgents] = useState(false);
  useEffect(() => {
    void api
      .request<Agent[]>("/agents")
      .then(setAgents)
      .catch(() => setAgents([{ name: session.agent }]));
    void fetchAgentMenu(api).then(setAgentMenu);
  }, []);
  const spec = agents.find((row) => row.name === agent),
    modelEnabled = !spec || !!spec.model_flag;
  async function go() {
    if (busy) return;
    setBusy(true);
    try {
      await api.request(`/sessions/${session.id}/handoff`, {
        method: "POST",
        body: {
          successor,
          kill_old: successor && kill,
          agent: successor ? agent : "",
          model: successor && modelEnabled ? model.trim() : "",
        },
      });
      onNotice(
        t("sessions.dialogs.handoff.requested"),
      );
      onCreated();
      onClose();
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      id="sheet"
      className="sheet"
      aria-label={t("sessions.dialogs.handoff.title")}
      onCancel={onClose}
    >
      <div className="sheet-head">
        <h2>{t("sessions.dialogs.handoff.title")}</h2>
        <button className="x" onClick={onClose}>
          ✕
        </button>
      </div>
      <div className="sub">
        <b className="hs-name">{session.name}</b>{" "}
        {t("sessions.dialogs.handoff.intro")}
      </div>
      <label className="f" htmlFor="ho-mode">
        {t("sessions.dialogs.handoff.then")}
      </label>
      <select
        className="f"
        id="ho-mode"
        value={successor ? "successor" : "note"}
        onChange={(event) => setSuccessor(event.target.value === "successor")}
      >
        <option value="successor">{t("sessions.dialogs.handoff.modeSuccessor")}</option>
        <option value="note">
          {t("sessions.dialogs.handoff.modeNote")}
        </option>
      </select>
      <div id="ho-successor" hidden={!successor}>
        <label className="f" htmlFor="ho-agent">
          {t("sessions.dialogs.handoff.handTo")}
        </label>
        <select
          className="f"
          id="ho-agent"
          value={agent}
          onChange={(event) => {
            if (event.target.value === "__more__") {
              setShowAllAgents(true);
              return;
            }
            setAgent(event.target.value);
          }}
        >
          {(() => {
            const { shown, more } = splitAgentMenu(agents, agentMenu);
            const options = shown.some((a) => a.name === agent)
              ? shown
              : [...shown, ...agents.filter((a) => a.name === agent)];
            return (
              <>
                {options.map((row) => (
                  <option key={row.name} value={row.name}>
                    {row.name}
                    {row.name === session.agent
                      ? t("sessions.dialogs.handoff.sameAgentSuffix")
                      : ""}
                  </option>
                ))}
                {more.length > 0 && (
                  <option value="__more__">{t("sessions.dialogs.moreAgents")}</option>
                )}
              </>
            );
          })()}
        </select>
        {showAllAgents && (
          <AllAgentsPicker
            agents={agents}
            onPick={setAgent}
            onClose={() => setShowAllAgents(false)}
          />
        )}
        <div className="subhint" id="ho-agent-hint">
          {agent !== session.agent
            ? t("sessions.dialogs.handoff.otherAgentHint", { agent })
            : t("sessions.dialogs.handoff.sameAgentHint")}
        </div>
        <label className="f" htmlFor="ho-model">
          {t("sessions.dialogs.model")}
        </label>
        <input
          className="f"
          id="ho-model"
          value={model}
          disabled={!modelEnabled}
          placeholder={
            modelEnabled ? t("sessions.dialogs.handoff.modelDefault") : t("sessions.dialogs.handoff.modelNone", { agent })
          }
          onChange={(event) => setModel(event.target.value)}
        />
        <label className="f check">
          <input
            id="ho-kill"
            type="checkbox"
            checked={kill}
            onChange={(event) => setKill(event.target.checked)}
          />
          <span>
            {t("sessions.dialogs.handoff.retire")}{" "}
            <span className="hs-name2">{session.name}</span>{" "}
            {t("sessions.dialogs.handoff.retireAfter")}
          </span>
        </label>
        <div className="subhint" id="ho-kill-hint">
          {kill
            ? t("sessions.dialogs.handoff.killHint")
            : t("sessions.dialogs.handoff.keepHint")}
        </div>
      </div>
      <div className="btnrow">
        <button
          className="b ok grow"
          id="ho-go"
          disabled={busy}
          onClick={() => void go()}
        >
          {successor ? t("sessions.dialogs.handoff.goSuccessor") : t("sessions.dialogs.handoff.goNote")}
        </button>
      </div>
    </Modal>
  );
}
