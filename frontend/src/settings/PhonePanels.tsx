// Two small per-device panels for phones: where dictation is transcribed
// (Settings → Notifications), and, in the Android app, which Lecterns this
// app is paired with (Settings → Devices). docs/mobile-sessions.md,
// docs/android.md.
import { useEffect, useState } from "react";
import { speechCtor } from "../voice";
import { hostVoice, setVoicePreference, voicePreference, type HostVoice, type VoicePreference } from "../voice-host";
import { appHosts, nativeBridge } from "../native/bridge";

export function VoiceSettings() {
  const [pref, setPref] = useState<VoicePreference>(voicePreference);
  const [host, setHost] = useState<HostVoice>();
  useEffect(() => {
    void hostVoice(true).then(setHost);
  }, []);
  const device = typeof window !== "undefined" && !!speechCtor(window);
  const choose = (value: VoicePreference) => {
    setPref(value);
    setVoicePreference(value);
  };
  return (
    <article className="voice-settings" data-testid="voice-settings">
      <h3>Voice input</h3>
      <p className="subhint">Where the 🎙 button turns speech into text. Either way the words land in the box for you to check; nothing is sent until you tap Send.</p>
      <label className="voice-choice">
        <input type="radio" name="voice-engine" value="device" checked={pref === "device"} disabled={!device} onChange={() => choose("device")} />
        <span>
          <b>On this device</b> — the browser's own speech recognition.{" "}
          {device ? "Available here." : "Not available in this browser (the Android app has none)."}
        </span>
      </label>
      <label className="voice-choice">
        <input type="radio" name="voice-engine" value="host" checked={pref === "host"} disabled={!host?.available} onChange={() => choose("host")} />
        <span>
          <b>On your Lectern</b> — recorded here, transcribed by whisper.cpp on the Lectern host; the audio goes nowhere else.{" "}
          {host === undefined ? "Checking…" : host.available ? `Available (model ${host.model}).` : host.hint || "Not installed on the host."}
        </span>
      </label>
      <label className="voice-choice">
        <input type="radio" name="voice-engine" value="auto" checked={pref === "auto"} onChange={() => choose("auto")} />
        <span>
          <b>Automatic</b> — this device when it can, otherwise your Lectern.
        </span>
      </label>
    </article>
  );
}

/** The Android app's paired Lecterns. Nothing in a browser. */
export function AppHosts() {
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
      <h3>Lecterns in this app</h3>
      <p className="subhint">This app can be paired with several Lecterns. Notifications say which one they came from.</p>
      <ul className="device-list">
        {hosts.map((h) => (
          <li key={h.id} className="device-row" data-host-id={h.id}>
            <div className="device-info">
              <strong>{h.label}</strong>
              <span className="subhint">{h.mode === "relay" ? "Encrypted relay" : h.origin}{h.active ? " · open now" : ""}</span>
            </div>
            {!h.active && <button className="b" onClick={() => bridge.switchHost?.(h.id)}>Switch</button>}
          </li>
        ))}
      </ul>
      <button className="b" onClick={() => bridge.openHosts?.()}>Add or manage Lecterns…</button>
    </article>
  );
}
