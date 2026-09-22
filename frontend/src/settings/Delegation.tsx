import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";

// Delegated builds is the one setting that changes what every lead session
// does with a substantial task, and it sends code to a second provider. So it
// is not a row in a tab: it is a banner above the tabs, on every Settings
// view, with its state in large type.

type Settings = {
  enabled: boolean;
  worker_agent: string;
  worker_model: string;
  permission_mode: string;
  correction_cycles: number;
};
type View = { settings: Settings; worker_ready: boolean; worker_problem: string; worker_command?: string };
type Agent = { name: string; builtin?: boolean; task?: unknown };
type Project = { id: number; name: string; default_agent?: string };

export function Delegation({
  api,
  agents,
  projects,
  onNotice,
  onChanged,
}: {
  api: SettingsApi;
  agents: Agent[];
  projects: Project[];
  onNotice(t: string, e?: boolean): void;
  onChanged(): void;
}) {
  const [view, setView] = useState<View>();
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const [key, setKey] = useState("");
  const [check, setCheck] = useState("");
  const [installProject, setInstallProject] = useState(0);

  async function load() {
    try {
      setView(await api.request<View>("/delegation"));
    } catch (e) {
      onNotice(`Delegated builds: ${(e as Error).message}`, true);
    }
  }
  useEffect(() => {
    void load();
  }, []);

  async function save(patch: Partial<Settings>) {
    setBusy(true);
    try {
      setView(await api.request<View>("/delegation", { method: "PUT", body: patch }));
      onChanged();
    } catch (e) {
      onNotice((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  async function preset() {
    setBusy(true);
    try {
      setView(await api.request<View>("/delegation/preset", { method: "POST", body: { api_key: key } }));
      setKey("");
      onNotice("DeepSeek Flash worker installed and selected");
      onChanged();
    } catch (e) {
      onNotice((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  async function runCheck() {
    setBusy(true);
    setCheck("Asking the worker for one fixed reply…");
    try {
      const r = await api.request<{ ok: boolean; seconds: number; error?: string; tail?: string }>("/delegation/check", {
        method: "POST",
        body: {},
      });
      setCheck(r.ok ? `Worker answered in ${r.seconds.toFixed(1)}s.` : `Worker did not answer: ${r.error || (r.tail || "").trim().slice(-300)}`);
    } catch (e) {
      setCheck((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function installSkill(agent: "claude" | "codex") {
    if (!installProject) return;
    setBusy(true);
    try {
      await api.request(`/projects/${installProject}/workflows/delegate`, { method: "PUT", body: { agent, enabled: true } });
      onNotice(`Lead skill installed for ${agent} — start a new session to use $lectern-delegate`);
    } catch (e) {
      onNotice((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  if (!view) return null;
  const s = view.settings;
  const workers = agents.filter((a) => a.task || a.builtin);
  const on = s.enabled;
  return (
    <section id="delegation" className={"delegation-banner" + (on ? " on" : "")} aria-label="Delegated builds">
      <div className="delegation-head">
        <div>
          <h3>
            Delegated builds <span className="delegation-state">{on ? "ON" : "OFF"}</span>
          </h3>
          <p>
            {on
              ? `Lead sessions plan and review; ${s.worker_agent}${s.worker_model ? ` (${s.worker_model})` : ""} builds each substantial change as a task in its own worktree.`
              : "Off: every session does its own implementation. Turn on to have a cheaper worker agent build what a lead session plans and reviews."}
            {on && !view.worker_ready && <strong> The worker is not runnable: {view.worker_problem}.</strong>}
          </p>
        </div>
        <label className="delegation-switch">
          <input
            id="delegation-enabled"
            type="checkbox"
            role="switch"
            aria-checked={on}
            checked={on}
            disabled={busy}
            onChange={(e) => void save({ enabled: e.target.checked })}
          />
          <span>{on ? "On" : "Off"}</span>
        </label>
      </div>
      <button className="b" type="button" onClick={() => setOpen(!open)} aria-expanded={open}>
        {open ? "Hide setup" : "Set up the worker"}
      </button>
      {open && (
        <div className="delegation-setup">
          <label>
            Worker agent
            <select value={s.worker_agent} disabled={busy} onChange={(e) => void save({ worker_agent: e.target.value })}>
              <option value="">— choose —</option>
              {workers.map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Worker model
            <input value={s.worker_model} disabled={busy} placeholder="agent default" onBlur={(e) => e.target.value !== s.worker_model && void save({ worker_model: e.target.value })} onChange={(e) => setView({ ...view, settings: { ...s, worker_model: e.target.value } })} />
          </label>
          <label>
            Permissions
            <select value={s.permission_mode} disabled={busy} onChange={(e) => void save({ permission_mode: e.target.value })}>
              <option value="acceptEdits">acceptEdits (sandboxed writes)</option>
              <option value="plan">plan (read-only)</option>
              <option value="bypassPermissions">bypassPermissions</option>
            </select>
          </label>
          <div className="delegation-preset">
            <p>
              <strong>Preset: DeepSeek Flash.</strong> Installs a <code>flash-builder</code> agent that runs Codex against DeepSeek's API with the
              Flash model, configured by flags only; your own Codex config is untouched. The key is stored masked in the agent registry.
            </p>
            <input type="password" value={key} placeholder="DeepSeek API key (sk-…)" autoComplete="off" onChange={(e) => setKey(e.target.value)} />
            <button className="b ok" type="button" disabled={busy || (!key && s.worker_agent !== "flash-builder")} onClick={() => void preset()}>
              Install preset
            </button>
          </div>
          <div className="delegation-check">
            <button className="b" type="button" disabled={busy || !view.worker_ready} onClick={() => void runCheck()}>
              Check the worker answers
            </button>
            {check && <span>{check}</span>}
          </div>
          <div className="delegation-install">
            <p>
              Install the lead skill (<code>lectern-delegate</code>) into a project so a session there knows the workflow:
            </p>
            <select value={installProject} onChange={(e) => setInstallProject(Number(e.target.value))}>
              <option value={0}>— project —</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
            <button className="b" type="button" disabled={busy || !installProject} onClick={() => void installSkill("claude")}>
              for Claude Code
            </button>
            <button className="b" type="button" disabled={busy || !installProject} onClick={() => void installSkill("codex")}>
              for Codex
            </button>
          </div>
          <p className="delegation-note">
            Codex needs <code>tool_timeout_sec = 3600</code> on its <code>lectern</code> MCP server entry so <code>wait_build</code> can block for a whole build.
          </p>
        </div>
      )}
    </section>
  );
}
