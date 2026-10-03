// Two small per-device panels for phones: where dictation is transcribed
// (Settings → Notifications), and, in the Android app, which Lecterns this
// app is paired with (Settings → Devices). docs/mobile-sessions.md,
// docs/android.md.
import { t, useLocale } from "../i18n";
import { useEffect, useState } from "react";
import { speechCtor } from "../voice";
import { hostVoice, setVoicePreference, voicePreference, type HostVoice, type VoicePreference } from "../voice-host";
import { appHosts, inApp, nativeBridge } from "../native/bridge";
import { canUpdate, checkForUpdate, installUpdate, useUpdateStatus, type UpdateStatus } from "../native/update";

export function VoiceSettings() {
  useLocale();
  const [pref, setPref] = useState<VoicePreference>(voicePreference);
  const [host, setHost] = useState<HostVoice>();
  useEffect(() => {
    void hostVoice(true).then(setHost);
  }, []);
  const device = typeof window !== "undefined" && !inApp() && !!speechCtor(window);
  const choose = (value: VoicePreference) => {
    setPref(value);
    setVoicePreference(value);
  };
  return (
    <article className="voice-settings" data-testid="voice-settings">
      <h3>{t("voice.title")}</h3>
      <p className="subhint">{t("voice.help")}</p>
      <label className="voice-choice">
        <input type="radio" name="voice-engine" value="device" checked={pref === "device"} disabled={!device} onChange={() => choose("device")} />
        <span>
          <b>{t("voice.device")}</b> — {t("voice.deviceHelp")}{" "}
          {device ? t("voice.availableHere") : t("voice.deviceMissing")}
        </span>
      </label>
      <label className="voice-choice">
        <input type="radio" name="voice-engine" value="host" checked={pref === "host"} disabled={!host?.available} onChange={() => choose("host")} />
        <span>
          <b>{t("voice.host")}</b> — {t("voice.hostHelp")}{" "}
          {host === undefined ? t("voice.checking") : host.available ? t("voice.hostAvailable", { model: host.model || "" }) : host.hint || t("voice.hostMissing")}
        </span>
      </label>
      <label className="voice-choice">
        <input type="radio" name="voice-engine" value="auto" checked={pref === "auto"} onChange={() => choose("auto")} />
        <span>
          <b>{t("voice.auto")}</b> — {t("voice.autoHelp")}
        </span>
      </label>
    </article>
  );
}

/** The Android app's paired Lecterns. Nothing in a browser. */
export function AppHosts() {
  useLocale();
  const bridge = nativeBridge();
  const [hosts, setHosts] = useState(appHosts);
  useEffect(() => {
    const refresh = () => setHosts(appHosts());
    window.addEventListener("focus", refresh);
    return () => window.removeEventListener("focus", refresh);
  }, []);
  if (!bridge?.hosts || !hosts.length) return null;
  return (
    <article className="devices-panel app-hosts" data-testid="app-hosts">
      <h3>{t("appHosts.title")}</h3>
      <p className="subhint">{t("appHosts.help")}</p>
      <ul className="device-list">
        {hosts.map((h) => (
          <li key={h.id} className="device-row" data-host-id={h.id}>
            <div className="device-info">
              <strong>{h.label}</strong>
              <span className="subhint">{h.mode === "relay" ? t("appHosts.relay") : h.origin}{h.active ? " · " + t("appHosts.open") : ""}</span>
            </div>
            {!h.active && <button className="b" onClick={() => bridge.switchHost?.(h.id)}>{t("appHosts.switch")}</button>}
          </li>
        ))}
      </ul>
      <button className="b" onClick={() => bridge.openHosts?.()}>{t("appHosts.manage")}</button>
    </article>
  );
}

function updateLine(s: UpdateStatus): string {
  switch (s.state) {
    case "checking": return t("appUpdate.checking");
    case "current": return t("appUpdate.current");
    case "available": return t("appUpdate.available", { version: s.latest ?? "" });
    case "downloading": return t("appUpdate.downloading", { progress: s.progress ?? 0 });
    case "installing": return t("appUpdate.installing");
    case "error": return t("appUpdate.failed", { error: s.error ?? "" });
    default: return "";
  }
}

/** The Android app's version, and updating it from the newest release. */
export function AppUpdate() {
  useLocale();
  const status = useUpdateStatus();
  if (!canUpdate()) return null;
  const busy = status.state === "checking" || status.state === "downloading" || status.state === "installing";
  const version = status.current || appVersion();
  return (
    <article className="devices-panel app-update" data-testid="app-update" data-state={status.state}>
      <h3>{t("appUpdate.title")}</h3>
      <p className="subhint">{t("appUpdate.version", { version })}</p>
      {status.state !== "idle" && <p role="status">{updateLine(status)}</p>}
      <div className="btnrow">
        {status.state === "available" || (status.state === "error" && status.latest) ? (
          <button className="b ok" disabled={busy} onClick={installUpdate}>{t("appUpdate.install", { version: status.latest ?? "" })}</button>
        ) : (
          <button className="b" disabled={busy} onClick={checkForUpdate}>{t("appUpdate.check")}</button>
        )}
      </div>
    </article>
  );
}

function appVersion(): string {
  try {
    return (JSON.parse(nativeBridge()?.version() ?? "{}") as { app?: string }).app ?? "";
  } catch {
    return "";
  }
}
