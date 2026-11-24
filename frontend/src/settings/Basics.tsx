import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { SettingsApi } from "./Settings";
import { PhoneWizard } from "./PhoneWizard";
import { saveAppearance, useAppearance } from "../theme/appearance";
import { usePref } from "../prefs/store";
import { ENTER_PREF, type EnterMode } from "../sessions/enter";
import { LANGUAGES, t, useLocale } from "../i18n";

interface AgentCheck {
  name: string;
  found: boolean;
}

// Settings → Basics (docs/design/simple-ui.md "Settings"): the handful of
// choices a person actually makes, each saved the moment it changes.
// Everything else is in the Advanced sections, one click away, and in search.
export function Basics({
  api,
  values,
  onNotice,
  onSection,
  onEnablePush,
  pushAvailable,
  pushUnavailableReason,
  pushEndpoint,
  onChanged,
}: {
  api: SettingsApi;
  values: Record<string, JsonValue>;
  onNotice(text: string, error?: boolean): void;
  onSection(name: string): void;
  onEnablePush(): void;
  pushAvailable: boolean;
  pushUnavailableReason?: string;
  pushEndpoint?: string | null;
  onChanged(): void;
}) {
  useLocale();
  const appearance = useAppearance();
  const [agents, setAgents] = useState<AgentCheck[]>();
  const [ask, setAsk] = useState(String(values.session_permission_mode ?? "") === "ask");
  const [phone, setPhone] = useState(false);
  const [enter, setEnter] = usePref<EnterMode>(ENTER_PREF, "auto");
  useEffect(() => setAsk(String(values.session_permission_mode ?? "") === "ask"), [values.session_permission_mode]);
  useEffect(() => {
    void api
      .request<{ agents: AgentCheck[] }>("/onboarding")
      .then((row) => setAgents(row.agents))
      .catch(() => setAgents([]));
  }, []);
  async function saveAsk(next: boolean) {
    setAsk(next);
    try {
      await api.request("/settings", { method: "PUT", body: { session_permission_mode: next ? "ask" : "bypass" } });
      onNotice(next ? t("basics.askOn") : t("basics.askOff"));
      onChanged();
    } catch (error) {
      setAsk(!next);
      onNotice(String(error), true);
    }
  }
  return (
    <article className="basics" id="settings-basics">
      <h3>{t("settings.section.basics")}</h3>
      <div className="basics-row" data-setting="basics.agents">
        <span className="basics-label">{t("basics.agents")}</span>
        <span className="basics-value" id="basics-agents">
          {!agents ? (
            "…"
          ) : (
            <>
              {agents
                .filter((row) => row.found)
                .map((row) => (
                  <span key={row.name} className="chip ok" data-found="true">
                    ✓ {row.name}
                  </span>
                ))}
              {!agents.some((row) => row.found) && <span className="basics-hint">{t("basics.noAgents")}</span>}
              {agents.some((row) => !row.found) && (
                <span className="basics-hint" id="basics-agents-missing">
                  {t("basics.notInstalledList", { names: agents.filter((row) => !row.found).map((row) => row.name).join(", ") })}
                </span>
              )}
            </>
          )}
          <button type="button" className="linkish" onClick={() => onSection("agents")}>
            {t("basics.manageAgents")}
          </button>
        </span>
      </div>
      <label className="basics-row" data-setting="basics.ask">
        <span className="basics-label">
          {t("start.sheet.ask")}
          <span className="basics-hint">{t("basics.askHint")}</span>
        </span>
        <input type="checkbox" role="switch" id="basics-ask" checked={ask} onChange={(e) => void saveAsk(e.target.checked)} />
      </label>
      <RemoteControlRows api={api} values={values} onNotice={onNotice} onChanged={onChanged} />
      <div className="basics-row" data-setting="basics.phone">
        <span className="basics-label">
          {t("basics.phone")}
          <span className="basics-hint">{t("basics.phoneHint")}</span>
        </span>
        <button type="button" className="b ok" id="basics-phone" onClick={() => setPhone(true)}>
          {t("phone.title")}
        </button>
      </div>
      <div className="basics-row" data-setting="basics.alerts">
        <span className="basics-label">
          {t("basics.alerts")}
          <span className="basics-hint">{t("basics.alertsHint")}</span>
        </span>
        {!pushAvailable ? (
          <span className="basics-hint" id="basics-alerts-why">
            {pushUnavailableReason || t("settings.notifications.unavailable")}
          </span>
        ) : pushEndpoint ? (
          <span className="chip ok" id="basics-alerts-on">
            {t("basics.alertsOn")}
          </span>
        ) : (
          <button type="button" className="b" id="basics-alerts" onClick={onEnablePush}>
            {t("settings.notifications.enable")}
          </button>
        )}
      </div>
      <label className="basics-row" data-setting="appearance.theme">
        <span className="basics-label">{t("settings.appearance.theme")}</span>
        <select id="basics-theme" value={appearance.theme} onChange={(e) => saveAppearance({ theme: e.target.value as "system" | "light" | "dark" })}>
          <option value="system">{t("basics.themeSystem")}</option>
          <option value="light">{t("basics.themeLight")}</option>
          <option value="dark">{t("basics.themeDark")}</option>
        </select>
      </label>
      <label className="basics-row" data-setting="appearance.language">
        <span className="basics-label">{t("settings.appearance.language")}</span>
        <select id="basics-language" value={appearance.language} onChange={(e) => saveAppearance({ language: e.target.value })}>
          {LANGUAGES.filter((row) => row.tag !== "en-XA").map((row) => (
            <option key={row.tag} value={row.tag}>
              {row.tag ? row.name : t("settings.appearance.browserLanguage")}
            </option>
          ))}
        </select>
      </label>
      <label className="basics-row" data-setting="basics.enter">
        <span className="basics-label">
          {t("basics.enter")}
          <span className="basics-hint">{t("basics.enterHint")}</span>
        </span>
        <select id="basics-enter" value={enter} onChange={(e) => setEnter(e.target.value as EnterMode)}>
          <option value="auto">{t("basics.enterAuto")}</option>
          <option value="send">{t("basics.enterSend")}</option>
          <option value="newline">{t("basics.enterNewline")}</option>
        </select>
      </label>
      <p className="subhint basics-more">{t("basics.more")}</p>
      {phone && <PhoneWizard api={api} onNotice={onNotice} onClose={() => setPhone(false)} />}
    </article>
  );
}

interface RemoteControlOutcome {
  id: number;
  name: string;
  status: "on" | "already" | "pending" | "skipped" | "consent" | "failed";
  reason?: string;
}

// Claude Remote Control for every session (docs/remote-control.md "Every
// session"): the default for new claude sessions, and a button that turns it
// on in the ones already running.
function RemoteControlRows({
  api,
  values,
  onNotice,
  onChanged,
}: {
  api: SettingsApi;
  values: Record<string, JsonValue>;
  onNotice(text: string, error?: boolean): void;
  onChanged(): void;
}) {
  const [on, setOn] = useState(String(values.remote_control_default ?? "") !== "0");
  const [busy, setBusy] = useState(false);
  const [outcomes, setOutcomes] = useState<RemoteControlOutcome[]>();
  useEffect(() => setOn(String(values.remote_control_default ?? "") !== "0"), [values.remote_control_default]);
  async function save(next: boolean) {
    setOn(next);
    try {
      await api.request("/settings", { method: "PUT", body: { remote_control_default: next ? "1" : "0" } });
      onNotice(next ? t("basics.remoteControlOn") : t("basics.remoteControlOff"));
      onChanged();
    } catch (error) {
      setOn(!next);
      onNotice(String(error), true);
    }
  }
  async function all() {
    setBusy(true);
    try {
      const res = await api.request<{ sessions: RemoteControlOutcome[] }>("/remote-control/all", { method: "POST", body: {} });
      // Sessions of other agents are not claude sessions to report on.
      const rows = res.sessions.filter((row) => row.status !== "skipped" || !row.reason?.includes("built-in claude"));
      setOutcomes(rows);
      const count = (status: string) => rows.filter((row) => row.status === status).length;
      onNotice(
        t("basics.remoteControlAllDone", {
          on: count("on") + count("already"),
          pending: count("pending"),
          skipped: count("skipped") + count("consent") + count("failed"),
        }),
      );
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <label className="basics-row" data-setting="basics.remoteControl">
        <span className="basics-label">
          {t("basics.remoteControl")}
          <span className="basics-hint">{t("basics.remoteControlHint")}</span>
        </span>
        <input type="checkbox" role="switch" id="basics-remote-control" checked={on} onChange={(e) => void save(e.target.checked)} />
      </label>
      <div className="basics-row" data-setting="basics.remoteControlAll">
        <span className="basics-label">
          {t("basics.remoteControlAll")}
          <span className="basics-hint">{t("basics.remoteControlAllHint")}</span>
          {outcomes && (
            <ul className="basics-hint" id="basics-remote-control-outcomes">
              {outcomes.map((row) => (
                <li key={row.id} data-status={row.status}>
                  {row.name || `#${row.id}`}: {t(`basics.remoteControlStatus.${row.status}`)}
                  {row.reason && row.status !== "pending" && row.status !== "consent" ? ` (${row.reason})` : ""}
                </li>
              ))}
            </ul>
          )}
        </span>
        <button type="button" className="b" id="basics-remote-control-all" disabled={busy} onClick={() => void all()}>
          {busy ? "…" : t("basics.remoteControlAllButton")}
        </button>
      </div>
    </>
  );
}
