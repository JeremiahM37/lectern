import { useEffect, useState } from "react";
import type { Approval } from "../types";
import type { SessionsApi } from "./Sessions";
import { approvalSummary } from "./approval-summary";
import { useVoiceMode, type PendingApproval } from "./voice-mode";
import "./voice-mode.css";

// VoiceMode is the one hook point this feature adds to Conversation.tsx: a
// mic/voice-mode toggle plus its panel, mounted for a live session's Chat
// view. Everything it does — listening, reading replies aloud, spoken
// approve/deny, voice commands — lives in useVoiceMode (voice-mode.ts); this
// component only renders what that hook reports.
export function VoiceMode({
  api,
  sessionId,
  sessionText,
  approvals,
  disabled,
  onNotice,
}: {
  api: SessionsApi;
  sessionId: number;
  sessionText: string;
  approvals: Approval[];
  disabled?: boolean;
  onNotice(text: string, error?: boolean): void;
}) {
  const first = approvals[0];
  const pendingApproval: PendingApproval | undefined = first
    ? { id: first.id, toolName: first.tool_name, summary: approvalSummary(first) || first.tool_name }
    : undefined;
  const voice = useVoiceMode({ api, sessionId, sessionText, pendingApproval, disabled, onNotice });
  const [showSettings, setShowSettings] = useState(false);
  const [voices, setVoices] = useState<SpeechSynthesisVoice[]>([]);

  useEffect(() => {
    if (!voice.supported || typeof window === "undefined") return;
    const load = () => setVoices(window.speechSynthesis.getVoices());
    load();
    window.speechSynthesis.addEventListener?.("voiceschanged", load);
    return () => window.speechSynthesis.removeEventListener?.("voiceschanged", load);
  }, [voice.supported]);

  if (!voice.supported)
    return (
      <p className="voice-mode-unsupported sub" id="voice-mode-unsupported">
        Voice mode needs a browser with the Web Speech API (Chrome or Edge on
        desktop and Android). It is hidden here because this browser does not
        support it.
      </p>
    );

  const pushToTalk = voice.listenMode === "push-to-talk";

  return (
    <div className="voice-mode" id="voice-mode">
      <button
        type="button"
        className={voice.active ? "b ok" : "b"}
        id="voice-mode-toggle"
        aria-pressed={voice.active}
        disabled={disabled}
        onClick={voice.toggleActive}
      >
        {voice.active ? "🔊 Voice mode on" : "🎧 Voice mode"}
      </button>
      {voice.active && (
        <div className="voice-mode-panel" id="voice-mode-panel">
          <div className="voice-mode-state" role="status">
            <span
              className={`voice-mode-dot${voice.speaking ? " speaking" : voice.listening ? " listening" : ""}`}
              aria-hidden="true"
            />
            {voice.speaking ? "Speaking…" : voice.listening ? "Listening…" : pushToTalk ? "Hold the mic to talk" : "Idle"}
          </div>
          {pushToTalk ? (
            <p className="sub">
              This browser cannot listen continuously, so voice mode falls back to
              push-to-talk: hold the button below, speak, then release.
            </p>
          ) : null}
          {!!voice.transcript && (
            <p id="voice-mode-transcript" className="voice-mode-transcript">
              {voice.transcript}
            </p>
          )}
          {pushToTalk && (
            <button
              type="button"
              className="b voice-mode-hold"
              id="voice-mode-hold-to-talk"
              onPointerDown={(event) => {
                event.preventDefault();
                voice.pressStart();
              }}
              onPointerUp={voice.pressEnd}
              onPointerLeave={voice.pressEnd}
              onPointerCancel={voice.pressEnd}
            >
              🎙 Hold to talk
            </button>
          )}
          {!!voice.statusMessage && (
            <p id="voice-mode-status" role="status" className="voice-mode-status">
              {voice.statusMessage}
            </p>
          )}
          {voice.pendingSend !== undefined && (
            <div className="voice-mode-pending" id="voice-mode-pending">
              <span>Sending: “{voice.pendingSend}”</span>
              <button type="button" className="b warn" id="voice-mode-cancel-send" onClick={voice.cancelPendingSend}>
                Cancel
              </button>
            </div>
          )}
          <div className="voice-mode-hints sub">
            Say “send it” or pause to send · “approve”/“deny” when asked ·
            “interrupt” to stop the agent · “read that again” · “exit voice mode”
          </div>
          <details
            id="voice-mode-settings"
            onToggle={(event) => setShowSettings(event.currentTarget.open)}
          >
            <summary>⚙ Voice settings</summary>
            {showSettings && (
              <div className="voice-mode-settings-body">
                <label>
                  Voice
                  <select
                    id="voice-mode-voice"
                    value={voice.settings.voiceURI}
                    onChange={(event) => voice.updateSettings({ voiceURI: event.target.value })}
                  >
                    <option value="">Browser default</option>
                    {voices.map((row) => (
                      <option key={row.voiceURI} value={row.voiceURI}>
                        {row.name} ({row.lang})
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Speaking rate
                  <input
                    id="voice-mode-rate"
                    type="range"
                    min={0.5}
                    max={2}
                    step={0.1}
                    value={voice.settings.rate}
                    onChange={(event) => voice.updateSettings({ rate: Number(event.target.value) })}
                  />
                  <span>{voice.settings.rate.toFixed(1)}×</span>
                </label>
                <label>
                  Language
                  <input
                    id="voice-mode-lang"
                    type="text"
                    placeholder={navigator.language}
                    value={voice.settings.lang}
                    onChange={(event) => voice.updateSettings({ lang: event.target.value })}
                  />
                </label>
                <label className="voice-mode-autoread">
                  <input
                    id="voice-mode-autoread"
                    type="checkbox"
                    checked={voice.settings.autoRead}
                    onChange={(event) => voice.updateSettings({ autoRead: event.target.checked })}
                  />
                  Read replies aloud automatically
                </label>
              </div>
            )}
          </details>
        </div>
      )}
    </div>
  );
}
