import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";
import { t, useLocale } from "../i18n";

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
  lead_agent?: string;
  lead_model?: string;
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
  useLocale();
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
      onNotice(t("settings.delegation.loadError", { error: (e as Error).message }), true);
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
      onNotice(t("settings.delegation.presetInstalled"));
      onChanged();
    } catch (e) {
      onNotice((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  async function runCheck() {
    setBusy(true);
    setCheck(t("settings.delegation.checking"));
    try {
      const r = await api.request<{ ok: boolean; seconds: number; error?: string; tail?: string }>("/delegation/check", {
        method: "POST",
        body: {},
      });
      setCheck(r.ok ? t("settings.delegation.answered", { seconds: r.seconds.toFixed(1) }) : t("settings.delegation.noAnswer", { error: r.error || (r.tail || "").trim().slice(-300) }));
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
      onNotice(t("settings.delegation.skillInstalled", { agent }));
    } catch (e) {
      onNotice((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  // An API without the route (an older server, a harness) answers with
  // something else; the banner then says nothing rather than breaking the page.
  if (!view || !view.settings || typeof view.settings.enabled !== "boolean") return null;
  const s = view.settings;
  const workers = agents.filter((a) => a.task || a.builtin);
  const on = s.enabled;
  return (
    <section id="delegation" data-setting="delegation" className={"delegation-banner" + (on ? " on" : "")} aria-label={t("settings.delegation.title")}>
      <div className="delegation-head">
        <div>
          <h3>
            {t("settings.delegation.title")} <span className="delegation-state">{on ? t("settings.delegation.stateOn") : t("settings.delegation.stateOff")}</span>
          </h3>
          <p>
            {on
              ? t("settings.delegation.onText", { worker: `${s.worker_agent}${s.worker_model ? ` (${s.worker_model})` : ""}` })
              : t("settings.delegation.offText")}
            {on && !view.worker_ready && <strong> {t("settings.delegation.notRunnable", { problem: view.worker_problem })}</strong>}
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
          <span>{on ? t("settings.delegation.on") : t("settings.delegation.off")}</span>
        </label>
      </div>
      <button className="b" type="button" onClick={() => setOpen(!open)} aria-expanded={open}>
        {open ? t("settings.delegation.hideSetup") : t("settings.delegation.setup")}
      </button>
      {open && (
        <div className="delegation-setup">
          <label>
            {t("settings.delegation.workerAgent")}
            <select value={s.worker_agent} disabled={busy} onChange={(e) => void save({ worker_agent: e.target.value })}>
              <option value="">{t("settings.delegation.choose")}</option>
              {workers.map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("settings.delegation.workerModel")}
            <input value={s.worker_model} disabled={busy} placeholder={t("settings.delegation.agentDefault")} onBlur={(e) => e.target.value !== s.worker_model && void save({ worker_model: e.target.value })} onChange={(e) => setView({ ...view, settings: { ...s, worker_model: e.target.value } })} />
          </label>
          <label>
            {t("settings.delegation.permissions")}
            <select value={s.permission_mode} disabled={busy} onChange={(e) => void save({ permission_mode: e.target.value })}>
              <option value="acceptEdits">{t("settings.delegation.acceptEdits")}</option>
              <option value="plan">{t("settings.delegation.plan")}</option>
              <option value="bypassPermissions">bypassPermissions</option>
            </select>
          </label>
          <label>
            {t("settings.delegation.leadAgent")}
            <select value={s.lead_agent || ""} disabled={busy} onChange={(e) => void save({ lead_agent: e.target.value })}>
              <option value="">{t("settings.delegation.projectDefault")}</option>
              <option value="claude">Claude Code</option>
              <option value="codex">Codex</option>
            </select>
          </label>
          <label>
            {t("settings.delegation.leadModel")}
            <input value={s.lead_model || ""} disabled={busy} placeholder={t("settings.delegation.agentDefault")} onBlur={(e) => e.target.value !== (s.lead_model || "") && void save({ lead_model: e.target.value })} onChange={(e) => setView({ ...view, settings: { ...s, lead_model: e.target.value } })} />
          </label>
          <div className="delegation-preset">
            <p>
              <strong>{t("settings.delegation.presetTitle")}</strong> {t("settings.delegation.presetBefore")} <code>flash-builder</code> {t("settings.delegation.presetAfter")}
            </p>
            <input type="password" value={key} placeholder={t("settings.delegation.keyPlaceholder")} autoComplete="off" onChange={(e) => setKey(e.target.value)} />
            <button className="b ok" type="button" disabled={busy || (!key && s.worker_agent !== "flash-builder")} onClick={() => void preset()}>
              {t("settings.delegation.installPreset")}
            </button>
          </div>
          <div className="delegation-check">
            <button className="b" type="button" disabled={busy || !view.worker_ready} onClick={() => void runCheck()}>
              {t("settings.delegation.check")}
            </button>
            {check && <span>{check}</span>}
          </div>
          <div className="delegation-install">
            <p>
              {t("settings.delegation.installBefore")}<code>lectern-delegate</code>{t("settings.delegation.installAfter")}
            </p>
            <select value={installProject} onChange={(e) => setInstallProject(Number(e.target.value))}>
              <option value={0}>{t("settings.delegation.project")}</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
            <button className="b" type="button" disabled={busy || !installProject} onClick={() => void installSkill("claude")}>
              {t("settings.delegation.forClaude")}
            </button>
            <button className="b" type="button" disabled={busy || !installProject} onClick={() => void installSkill("codex")}>
              {t("settings.delegation.forCodex")}
            </button>
          </div>
          <p className="delegation-note">
            {t("settings.delegation.note1")} <code>lectern</code> {t("settings.delegation.note2")} <em>{t("settings.delegation.noteSession")}</em> {t("settings.delegation.note3")} <code>tool_timeout_sec = 3600</code> {t("settings.delegation.note4")} <code>wait_build</code> {t("settings.delegation.note5")}
          </p>
        </div>
      )}
    </section>
  );
}
