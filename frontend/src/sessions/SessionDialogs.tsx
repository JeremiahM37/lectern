import { RepositoryPicker, type RepositorySelection } from "./RepositoryPicker";
import { LaunchProfiles } from "../settings/LaunchProfiles";
import { Modal } from "./Modal";
import { useEffect, useState } from "react";
import type { Project, SessionView, Target } from "../types";
import {
  orderProjectsByRecency,
  readProjectPreference,
  rememberProjectSelection,
  rememberRecentProject,
} from "../project-preference";
import type { SessionsApi } from "./Sessions";
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
  id: number;
  name: string;
  agent: string;
  model?: string;
  description?: string;
  instructions?: string;
};
export function NewSession({
  api,
  projects,
  onClose,
  onCreated,
  onNotice,
}: {
  api: SessionsApi;
  projects: Project[];
  onClose(): void;
  onCreated(s: SessionView): void;
  onNotice(t: string, e?: boolean): void;
}) {
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
    [project, setProject] = useState<number | null>(() => {
      const remembered = preference.last;
      if (remembered === undefined) return projects[0]?.id ?? null;
      if (remembered === null) return null;
      return projects.some((row) => row.id === remembered)
        ? remembered
        : (projects[0]?.id ?? null);
    }),
    [name, setName] = useState(""),
    [group, setGroup] = useState(""),
    [agent, setAgent] = useState("claude"),
    [model, setModel] = useState(""),
    [mode, setMode] = useState("fresh"),
    [yolo, setYolo] = useState(true),
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
    [busy, setBusy] = useState(false);
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
        onNotice("Could not load launch options: " + String(error), true),
      );
    // The operator's global default (docs/agent-events.md section 3) is
    // what this dialog's Yolo checkbox opens set to — its own explicit
    // choice, once touched, is still what actually launches: the request
    // always sends a concrete `yolo` boolean, never omits it.
    void api
      .request<Record<string, string>>("/settings")
      .then((settings) => {
        if (settings.session_permission_mode === "ask") setYolo(false);
      })
      .catch(() => {});
  }, []);
  useEffect(() => {
    const selected = profiles.find((p) => p.id === profileId);
    if (selected) setAgent(selected.agent);
  }, [profileId, profiles]);
  useEffect(() => {
    let current = true;
    setMemoryStatus(
      mode === "brief" && project ? "Checking project memory…" : "",
    );
    setMemoryKind("");
    if (mode === "brief" && project)
      void api
        .request<{ memory?: { message?: string; status?: string } }>(
          `/projects/${project}/brief`,
        )
        .then((r) => {
          if (current) {
            setMemoryStatus(r.memory?.message || "Memory status unavailable");
            setMemoryKind(r.memory?.status || "unavailable");
          }
        })
        .catch(
          () =>
            current &&
            setMemoryStatus(
              "Could not preview project context. You can still start the session.",
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
  const selectedProject = projects.find((row) => row.id === project) ?? null;
  // The collapsed sheet still has to say what pressing Start will do: the
  // permission mode above all, plus anything else hidden behind Advanced.
  const { recent: recentProjects, rest: otherProjects } = orderProjectsByRecency(
    projects,
    preference.recent,
  );
  const launchNotes = [
    !yoloSupported
      ? `${agent} has no way to skip its prompts — it will ask`
      : yolo
        ? "Yolo — no approval prompts"
        : "Asks before it acts",
    profile ? `launch profile “${profile.name}” (${profile.agent})` : "",
    isolated ? "isolated Git worktree" : "",
    mode === "brief" ? "primed with project memory" : "",
    mode === "resume" ? "resumes the agent’s last conversation" : "",
  ].filter(Boolean);
  const launchSummary = launchNotes.join(" · ");
  async function start() {
    if (busy) return;
    setBusy(true);
    try {
      const s = await api.request<SessionView>("/sessions", {
        method: "POST",
        body: {
          background: isolated,
          profile_id: profileId,
          project_id: project,
          scratch: project === null,
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
      if (project !== null) rememberRecentProject(project);
      onCreated(s);
      onNotice(
        s.setup_state === "creating"
          ? "Workspace setup started. You can keep using Lectern."
          : "Session started",
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
        aria-label="New session"
        onCancel={onClose}
      >
        <div className="sheet-head">
          <h2>New session</h2>
          <button
            className="x"
            aria-label="Close new session"
            onClick={onClose}
          >
            ✕
          </button>
        </div>
        <p>
          An interactive agent you attach to and work with — not a dispatched
          task.
        </p>
        <div className="session-field">
          <label htmlFor="ns-project">Project</label>
          <select
            id="ns-project"
            aria-label="Project"
            value={project ?? ""}
            onChange={(e) => {
              const next = e.target.value ? Number(e.target.value) : null;
              setProject(next);
              // An explicit choice is remembered even if the sheet is closed
              // again, so a deliberate blank room does not snap back to a
              // project next time.
              rememberProjectSelection(next);
            }}
          >
            <option value="">▢ Blank room — no project yet</option>
            {recentProjects.length > 0 ? (
              <>
                <optgroup label="Recent">
                  {recentProjects.map((p) => (
                    <option value={p.id} key={p.id}>
                      {p.name} — {p.target_name}
                    </option>
                  ))}
                </optgroup>
                <optgroup label="Other projects">
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
          </select>
        </div>
        <div id="ns-proj-hint">
          {selectedProject === null ? (
            "Starts the agent in a fresh throwaway directory. Turn it into a project later."
          ) : (
            <>
              <span className="ns-proj-path">{selectedProject.repo_path}</span>
              {selectedProject.target_name
                ? ` · ${selectedProject.target_name}`
                : ""}
              {isolated && selectedProject.setup_cmd ? (
                <span className="ns-proj-setup">
                  This project’s setup command runs in the new checkout before
                  the agent starts.
                </span>
              ) : null}
            </>
          )}
        </div>
        <div className="session-field">
          <label htmlFor="ns-agent">Agent</label>
          <select
            id="ns-agent"
            value={agent}
            disabled={profileId > 0}
            onChange={(e) => setAgent(e.target.value)}
          >
            {(agents.length ? agents : [{ name: "claude" }]).map((a) => (
              <option key={a.name}>{a.name}</option>
            ))}
          </select>
        </div>
        {profile && (
          <div className="subhint" id="ns-agent-profile">
            Locked to {profile.agent} by the “{profile.name}” launch profile —
            pick another profile under Advanced options.
          </div>
        )}
        <div id="ns-agent-hint" className="subhint">
          {spec
            ? [
                !spec.model_flag
                  ? "no model switch — the Model field is ignored"
                  : "",
                !spec.resume_args ? "cannot resume its own history" : "",
              ]
                .filter(Boolean)
                .join(" · ")
            : ""}
        </div>
        <details id="ns-advanced" className="session-advanced">
          <summary>Advanced options</summary>
          <div className="session-field">
            <label htmlFor="ns-name">Name</label>
            <input
              id="ns-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="session-field">
            <label htmlFor="ns-group">Group</label>
            <input
              id="ns-group"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
            />
          </div>
          <div className="session-field">
            <label htmlFor="ns-profile">Launch profile</label>
            <select
              id="ns-profile"
              value={profileId}
              onChange={(e) => setProfileId(Number(e.target.value))}
            >
              <option value={0}>Agent and project defaults</option>
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
            Manage launch profiles
          </button>
          <div className="subhint" id="ns-profile-hint" role="status">
            {profile && (
              <>
                {`${profile.name} · ${profile.agent}${profile.model ? " · default model: " + profile.model : ""}. Settings are captured when the session starts.`}
                {profile.description && (
                  <span className="ns-profile-description">
                    {profile.description}
                  </span>
                )}
              </>
            )}
          </div>
          <div className="session-field">
            <label htmlFor="ns-model">Model</label>
            <input
              placeholder={
                profile?.model
                  ? profile.model + " — or override"
                  : models[agent] === undefined
                    ? "this agent has no model switch"
                    : models[agent]?.length
                      ? "default — or type any model name"
                      : "type the model name"
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
            Isolate in a new Git worktree
          </label>
          {isolated && (
            <div id="ns-worktree-options">
              <p className="subhint">
                A fresh session with separate files on a new branch. Starts from a
                committed revision; uncommitted edits stay in the original
                directory.
              </p>
              <input
                id="ns-worktree-base"
                aria-label="Base branch, tag or commit"
                value={base}
                onChange={(e) => setBase(e.target.value)}
              />
              <input
                id="ns-worktree-branch"
                aria-label="New branch name"
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
          <label htmlFor="ns-start">Start from</label>
          <select
            id="ns-start"
            value={mode}
            onChange={(e) => setMode(e.target.value)}
          >
            <option value="fresh">Fresh context</option>
            <option value="brief" disabled={!project}>
              Fresh, primed with what this project knows
            </option>
            <option value="resume" disabled={isolated}>
              Resume the agent's own last conversation
            </option>
          </select>
          <div className="subhint" id="ns-hint">
            {mode === "brief"
              ? "Pulls the project’s durable memory and its last handoff into the first message."
              : mode === "resume"
                ? "Reopens the agent’s own previous conversation in this directory."
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
          <label className="f check">
            <input
              id="ns-yolo"
              type="checkbox"
              disabled={!yoloSupported}
              checked={yolo}
              onChange={(e) => setYolo(e.target.checked)}
            />{" "}
            Yolo — no approval prompts
          </label>
          <div className="subhint" id="ns-yolo-hint">
            {!yoloSupported
              ? `${agent} has no way to skip its prompts — it will ask.`
              : yolo
                ? "The agent acts without stopping to ask. You are the supervision."
                : agent === "claude"
                  ? // Session permission mode (docs/agent-events.md section
                    // 3): unchecking Yolo is what launches claude in "ask"
                    // mode, which is also what registers the PermissionRequest
                    // hook — so this is the same checkbox that used to only
                    // mean "prompt in the terminal" and now also means "or
                    // from my phone".
                    "The agent stops and asks before it edits or runs anything — from the terminal, or Approve/Deny on your phone."
                  : "The agent stops and asks before it edits or runs anything."}
          </div>
          <label htmlFor="ns-isolation">Isolation</label>
          <select
            id="ns-isolation"
            value={sandboxMode}
            onChange={(e) => setSandboxMode(e.target.value as typeof sandboxMode)}
          >
            <option value="">Project default</option>
            <option value="none">None (today's behavior)</option>
            <option value="bwrap">bwrap — fast, no daemon</option>
            <option value="docker">Docker container</option>
          </select>
          {(sandboxMode === "bwrap" || sandboxMode === "docker") && (
            <>
              <label htmlFor="ns-isolation-network">Network</label>
              <select
                id="ns-isolation-network"
                value={sandboxNetwork}
                onChange={(e) =>
                  setSandboxNetwork(e.target.value as typeof sandboxNetwork)
                }
              >
                <option value="allow">Allow — unrestricted, like today</option>
                <option value="deny">
                  Deny — allowlist proxy only (see docs/isolation.md)
                </option>
              </select>
            </>
          )}
          <div className="subhint" id="ns-isolation-hint">
            {sandboxMode === "bwrap"
              ? "Runs the agent inside bubblewrap: its own filesystem view, this project's directory and its own auth read-write, the rest of $HOME hidden."
              : sandboxMode === "docker"
                ? "Runs the agent inside a disposable Docker container with the same mounts."
                : ""}
          </div>
          <div className="session-field">
            <label htmlFor="ns-prime">First message (optional)</label>
            <textarea
              id="ns-prime"
              value={prime}
              onChange={(e) => setPrime(e.target.value)}
            />
          </div>
        </details>
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
            disabled={busy}
            onClick={() => void start()}
          >
            ▶ Start session
          </button>
        </div>
      </Modal>
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
      aria-label="Running agents"
      onCancel={onClose}
    >
      <h2>Running agents</h2>
      <button onClick={onClose}>✕</button>
      <p>Adopting does not restart or disturb it.</p>
      {!rows.length && <p>No agents found running on any target.</p>}
      {rows.map((c) => (
        <article className="cand" key={`${c.target_id}-${c.tmux_session}`}>
          <b>{c.tmux_session}</b>
          <p>
            {c.agent} · {c.workdir}
          </p>
          <select aria-label={`Project for ${c.tmux_session}`} defaultValue="">
            <option value="">Unassigned</option>
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
            Adopt
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
  const [successor, setSuccessor] = useState(true),
    [agent, setAgent] = useState(session.agent),
    [model, setModel] = useState(""),
    [kill, setKill] = useState(true),
    [agents, setAgents] = useState<Agent[]>([]),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    void api
      .request<Agent[]>("/agents")
      .then(setAgents)
      .catch(() => setAgents([{ name: session.agent }]));
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
        "Asked for a handoff — it lands when the agent finishes its turn",
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
      aria-label="Hand off"
      onCancel={onClose}
    >
      <div className="sheet-head">
        <h2>Hand off</h2>
        <button className="x" onClick={onClose}>
          ✕
        </button>
      </div>
      <div className="sub">
        <b className="hs-name">{session.name}</b> writes down where it got to —
        what it did, what it learned, what it was about to do — and the next
        session starts primed with it.
      </div>
      <label className="f" htmlFor="ho-mode">
        Then
      </label>
      <select
        className="f"
        id="ho-mode"
        value={successor ? "successor" : "note"}
        onChange={(event) => setSuccessor(event.target.value === "successor")}
      >
        <option value="successor">Start a new session with it</option>
        <option value="note">
          Just write it down, keep this session running
        </option>
      </select>
      <div id="ho-successor" hidden={!successor}>
        <label className="f" htmlFor="ho-agent">
          Hand it to
        </label>
        <select
          className="f"
          id="ho-agent"
          value={agent}
          onChange={(event) => setAgent(event.target.value)}
        >
          {agents.map((row) => (
            <option key={row.name} value={row.name}>
              {row.name}
              {row.name === session.agent ? " — same agent, clean context" : ""}
            </option>
          ))}
        </select>
        <div className="subhint" id="ho-agent-hint">
          {agent !== session.agent
            ? `The work moves to ${agent}. It starts fresh, knowing only what the handoff says.`
            : "Same agent, clean context — for when the window is full."}
        </div>
        <label className="f" htmlFor="ho-model">
          Model
        </label>
        <input
          className="f"
          id="ho-model"
          value={model}
          disabled={!modelEnabled}
          placeholder={
            modelEnabled ? "default" : agent + " has no model switch"
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
            Retire <span className="hs-name2">{session.name}</span> once the
            handoff is written
          </span>
        </label>
        <div className="subhint" id="ho-kill-hint">
          {kill
            ? "Its tmux session ends. The handoff and its history stay."
            : "Both sessions keep running — useful if you want to compare them."}
        </div>
      </div>
      <div className="btnrow">
        <button
          className="b ok grow"
          id="ho-go"
          disabled={busy}
          onClick={() => void go()}
        >
          {successor ? "⇥ Write it and hand over" : "⇥ Write the handoff"}
        </button>
      </div>
    </Modal>
  );
}
