// Hands-free "voice mode" for a session's Chat view — a free, browser-native
// full-duplex loop (SpeechRecognition in, speechSynthesis out) with no vendor
// keys. voice.ts's dictation only ever fills the text box for a person to
// review; this file is the layer that also SENDS, READS ALOUD and DECIDES on
// the spoken word, so every action it takes is deliberately narrow, always
// echoed back on screen, and confirmed out loud before it takes effect.
import { useCallback, useEffect, useRef, useState } from "react";
import {
  appendTranscript,
  collectTranscript,
  speechCtor,
  type Recognition,
  type RecognitionResult,
} from "../voice";
import type { SessionsApi } from "./Sessions";

// ---- text hygiene for read-back --------------------------------------------

const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]/g;
// Box-drawing and block-element glyphs (─│┌┐└┘├┤┬┴┼═║╔…, progress bars) are
// pure rendering noise a listener never asked to hear.
const BOX_DRAWING = /[─-▟]/g;
const FENCED_CODE = /```[\s\S]*?```/g;

// stripForSpeech turns raw terminal/pane text into something worth saying:
// fenced code becomes one short marker instead of dozens of unreadable
// syntax lines, and escape codes / box-drawing are removed rather than
// mangled into stray words.
export function stripForSpeech(text: string): string {
  return text
    .replace(FENCED_CODE, " Code omitted. ")
    .replace(ANSI, "")
    .replace(BOX_DRAWING, " ")
    .replace(/[ \t]+/g, " ")
    .replace(/ *\n */g, "\n")
    .replace(/\n{2,}/g, "\n")
    .trim();
}

// sentenceChunks splits read-back text into speakable units, so a long reply
// is many short, independently-interruptible utterances rather than one
// enormous block queued atomically.
export function sentenceChunks(text: string): string[] {
  return text
    .split(/(?<=[.!?])\s+|\n+/)
    .map((chunk) => chunk.trim())
    .filter(Boolean);
}

// ---- spoken command matching ------------------------------------------------

function normalizeWords(text: string): string[] {
  return text
    .toLowerCase()
    .replace(/[^a-z\s]/g, "")
    .split(/\s+/)
    .filter(Boolean);
}

// isBareCommand requires the utterance to BE the keyword — alone, or padded
// only with "please" — so a sentence that merely mentions the word ("don't
// approve that yet") is never mistaken for the command itself.
function isBareCommand(text: string, keywords: string[]): boolean {
  const words = normalizeWords(text).filter((word) => word !== "please");
  return words.length === 1 && keywords.includes(words[0]!);
}

const APPROVE_WORDS = ["approve", "allow", "yes"];
const DENY_WORDS = ["deny", "no", "stop"];

export function matchesApprove(text: string): boolean {
  return isBareCommand(text, APPROVE_WORDS);
}
export function matchesDeny(text: string): boolean {
  return isBareCommand(text, DENY_WORDS);
}

const SEND_TRIGGERS = ["send it", "over"];

// stripSendTrigger lets "...deploy the fix, send it" end the utterance the
// instant it is heard instead of waiting out the silence gap; the trigger
// words themselves never end up in the message that actually gets sent.
export function stripSendTrigger(text: string): { text: string; triggered: boolean } {
  const trimmed = text.trim();
  const lower = trimmed.toLowerCase();
  for (const trigger of SEND_TRIGGERS) {
    if (lower === trigger) return { text: "", triggered: true };
    if (lower.endsWith(` ${trigger}`))
      return { text: trimmed.slice(0, trimmed.length - trigger.length).trim(), triggered: true };
  }
  return { text: trimmed, triggered: false };
}

export type VoiceCommand =
  | { type: "exit" }
  | { type: "interrupt" }
  | { type: "read-again" }
  | { type: "approve" }
  | { type: "deny" }
  | { type: "send"; text: string }
  | { type: "ignore" };

// classifyUtterance is the single place a finished utterance (one silence
// gap, or an explicit trigger word) becomes an action. Global commands are
// checked first so they always work; a pending approval then narrows
// everything else to yes/no before it ever falls through to "send this as a
// message to the agent".
export function classifyUtterance(rawText: string, hasPendingApproval: boolean): VoiceCommand {
  const { text } = stripSendTrigger(rawText);
  if (!text.trim()) return { type: "ignore" };
  const normalized = normalizeWords(text).join(" ");
  if (normalized === "exit voice mode") return { type: "exit" };
  if (normalized === "interrupt" || normalized === "stop that") return { type: "interrupt" };
  if (normalized === "read that again" || normalized === "say that again") return { type: "read-again" };
  if (hasPendingApproval) {
    if (matchesApprove(text)) return { type: "approve" };
    if (matchesDeny(text)) return { type: "deny" };
  }
  return { type: "send", text: text.trim() };
}

// ---- per-device settings ----------------------------------------------------

export interface VoiceSettings {
  voiceURI: string;
  rate: number;
  autoRead: boolean;
  lang: string;
}
const SETTINGS_KEY = "lec-voice-settings";
const DEFAULT_SETTINGS: VoiceSettings = { voiceURI: "", rate: 1, autoRead: true, lang: "" };

export function loadVoiceSettings(): VoiceSettings {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(SETTINGS_KEY) || "null");
    if (!raw || typeof raw !== "object") return { ...DEFAULT_SETTINGS };
    const row = raw as Partial<VoiceSettings>;
    return {
      voiceURI: typeof row.voiceURI === "string" ? row.voiceURI : DEFAULT_SETTINGS.voiceURI,
      rate: typeof row.rate === "number" && row.rate > 0 ? row.rate : DEFAULT_SETTINGS.rate,
      autoRead: typeof row.autoRead === "boolean" ? row.autoRead : DEFAULT_SETTINGS.autoRead,
      lang: typeof row.lang === "string" ? row.lang : DEFAULT_SETTINGS.lang,
    };
  } catch {
    return { ...DEFAULT_SETTINGS };
  }
}
export function saveVoiceSettings(settings: VoiceSettings): void {
  try {
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings));
  } catch {
    // Best-effort: a full or blocked localStorage just means settings reset
    // to defaults next load, never a broken voice mode this load.
  }
}

// ---- capability detection ----------------------------------------------------

export type ListenMode = "continuous" | "push-to-talk" | "unsupported";

// iOS's SpeechRecognition (every browser there runs Apple's WebKit engine
// under its own wrapper, including inside an installed PWA on some versions)
// does not deliver results for a long-lived "continuous" session, so
// hands-free listening never reaches a pause to react to. Detected once per
// mount and told to the person plainly, rather than silently going deaf.
export function detectListenMode(win: Window): ListenMode {
  if (!speechCtor(win)) return "unsupported";
  const ua = win.navigator?.userAgent || "";
  const isIOS = /iPad|iPhone|iPod/.test(ua);
  return isIOS ? "push-to-talk" : "continuous";
}

export function speechOutputSupported(win: Window): boolean {
  return "speechSynthesis" in win && typeof win.speechSynthesis?.speak === "function";
}

// ---- the hook ----------------------------------------------------------------

export interface PendingApproval {
  id: number;
  toolName: string;
  summary: string;
}

export interface VoiceModeInputs {
  api: SessionsApi;
  sessionId: number;
  sessionText: string;
  pendingApproval?: PendingApproval;
  disabled?: boolean;
  onNotice(text: string, error?: boolean): void;
}

const SILENCE_MS = 1500;
const SEND_DELAY_MS = 2000;

// useVoiceMode owns the whole loop for one session's Chat view: recognition
// lifecycle, silence/trigger-word utterance boundaries, the send-delay
// cancel window, spoken read-back of new pane output, and spoken approve/
// deny — everything the React component below only needs to render.
export function useVoiceMode(inputs: VoiceModeInputs) {
  const [active, setActive] = useState(false);
  const [listening, setListening] = useState(false);
  const [speaking, setSpeaking] = useState(false);
  const [transcript, setTranscript] = useState("");
  const [pendingSend, setPendingSend] = useState<string>();
  const [statusMessage, setStatusMessage] = useState("");
  const [settings, setSettings] = useState<VoiceSettings>(() => loadVoiceSettings());

  // Long-lived callbacks (recognition events, timers) always read the latest
  // props/state through this ref rather than closing over a stale render —
  // the same pattern Conversation.tsx uses for its own draft ref.
  const latest = useRef({ ...inputs, settings, pendingApproval: inputs.pendingApproval });
  latest.current = { ...inputs, settings, pendingApproval: inputs.pendingApproval };
  const activeRef = useRef(false);
  activeRef.current = active;

  const Ctor = typeof window !== "undefined" ? speechCtor(window) : undefined;
  const listenMode = typeof window !== "undefined" ? detectListenMode(window) : "unsupported";
  const outputSupported = typeof window !== "undefined" && speechOutputSupported(window);
  const supported = listenMode !== "unsupported" && outputSupported;

  const recognition = useRef<Recognition | undefined>(undefined);
  const bufferText = useRef("");
  const committed = useRef(0);
  const silenceTimer = useRef<number | undefined>(undefined);
  const sendTimer = useRef<number | undefined>(undefined);
  const lastSpoken = useRef<string[]>([]);
  const spokenLength = useRef<number | null>(null);
  const announcedApproval = useRef<number | null>(null);
  const wakeLock = useRef<{ release(): Promise<void> } | null>(null);

  const speakChunks = useCallback((chunks: string[]) => {
    const synth = typeof window !== "undefined" ? window.speechSynthesis : undefined;
    if (!synth) return;
    const clean = chunks.map((chunk) => chunk.trim()).filter(Boolean);
    if (!clean.length) return;
    lastSpoken.current = clean;
    const { rate, voiceURI, lang } = latest.current.settings;
    const voices = synth.getVoices?.() || [];
    const voice = voiceURI ? voices.find((row) => row.voiceURI === voiceURI) : undefined;
    for (const chunk of clean) {
      const utterance = new SpeechSynthesisUtterance(chunk);
      utterance.rate = rate || 1;
      if (lang) utterance.lang = lang;
      if (voice) utterance.voice = voice;
      utterance.onstart = () => setSpeaking(true);
      utterance.onend = () => setSpeaking(false);
      utterance.onerror = () => setSpeaking(false);
      synth.speak(utterance);
    }
  }, []);
  const speak = useCallback((text: string) => speakChunks([text]), [speakChunks]);

  const cancelPendingSend = useCallback(() => {
    if (sendTimer.current !== undefined) {
      window.clearTimeout(sendTimer.current);
      sendTimer.current = undefined;
    }
    setPendingSend(undefined);
    setStatusMessage("Cancelled — nothing was sent.");
  }, []);

  const queueSend = useCallback((text: string) => {
    if (!text.trim()) return;
    setPendingSend(text);
    setStatusMessage("Sending… tap to cancel");
    sendTimer.current = window.setTimeout(() => {
      sendTimer.current = undefined;
      setPendingSend(undefined);
      const { api, sessionId, onNotice } = latest.current;
      void api
        .request(`/sessions/${sessionId}/send`, { method: "POST", body: { text } })
        .then(() => setStatusMessage("Sent to the session."))
        .catch((error) => {
          onNotice(String(error), true);
          setStatusMessage(`Could not send: ${String(error)}`);
        });
    }, SEND_DELAY_MS);
  }, []);

  const act = useCallback(
    (command: VoiceCommand) => {
      const { api, sessionId, onNotice, pendingApproval } = latest.current;
      switch (command.type) {
        case "ignore":
          return;
        case "exit":
          setStatusMessage("Exiting voice mode.");
          speak("Exiting voice mode.");
          setActive(false);
          return;
        case "read-again":
          if (lastSpoken.current.length) speakChunks(lastSpoken.current);
          else setStatusMessage("Nothing to read back yet.");
          return;
        case "interrupt":
          void api
            .request(`/sessions/${sessionId}/send`, { method: "POST", body: { key: "escape" } })
            .then(() => setStatusMessage("Interrupt sent."))
            .catch((error) => onNotice(String(error), true));
          return;
        case "approve":
        case "deny": {
          if (!pendingApproval) return;
          const decision = command.type === "approve" ? "approved" : "denied";
          void api
            .request(`/approvals/${pendingApproval.id}/decision`, { method: "POST", body: { decision } })
            .then(() => {
              const echo = decision === "approved" ? "Approved." : "Denied.";
              setStatusMessage(echo);
              speak(echo);
              // Leave announcedApproval pointed at this id: the poll that
              // still has this approval briefly lagging behind the decision
              // must not re-announce the very thing just decided. A genuinely
              // new approval (a different id) is announced normally.
            })
            .catch((error) => onNotice(String(error), true));
          return;
        }
        case "send":
          queueSend(command.text);
      }
    },
    [queueSend, speak, speakChunks],
  );

  const finishUtterance = useCallback(() => {
    if (silenceTimer.current !== undefined) {
      window.clearTimeout(silenceTimer.current);
      silenceTimer.current = undefined;
    }
    const text = bufferText.current.trim();
    bufferText.current = "";
    setTranscript("");
    if (!text) return;
    act(classifyUtterance(text, !!latest.current.pendingApproval));
  }, [act]);

  const resetSilenceTimer = useCallback(() => {
    if (silenceTimer.current !== undefined) window.clearTimeout(silenceTimer.current);
    silenceTimer.current = window.setTimeout(finishUtterance, SILENCE_MS);
  }, [finishUtterance]);

  const handleResult = useCallback(
    (event?: { results: ArrayLike<ArrayLike<RecognitionResult>> }) => {
      if (!event) return;
      const result = collectTranscript(event.results, committed.current);
      committed.current = result.committed;
      for (const piece of result.finalized) bufferText.current = appendTranscript(bufferText.current, piece);
      setTranscript(appendTranscript(bufferText.current, result.interim));
      // Barge-in: the person started talking, so whatever we were saying stops
      // being relevant immediately.
      if (typeof window !== "undefined" && window.speechSynthesis?.speaking) window.speechSynthesis.cancel();
      if (result.finalized.length) {
        if (stripSendTrigger(bufferText.current).triggered) {
          finishUtterance();
          return;
        }
      }
      resetSilenceTimer();
    },
    [finishUtterance, resetSilenceTimer],
  );

  const startContinuous = useCallback(() => {
    if (!Ctor) return;
    const listener = new Ctor();
    recognition.current = listener;
    listener.lang = latest.current.settings.lang || navigator.language;
    listener.interimResults = true;
    listener.continuous = true;
    let deniedPermission = false;
    listener.onresult = handleResult;
    listener.onerror = (event) => {
      setListening(false);
      const code = event?.error;
      if (code === "not-allowed" || code === "service-not-allowed") {
        deniedPermission = true;
        setActive(false);
        latest.current.onNotice("Microphone permission was denied.", true);
      } else if (code && code !== "no-speech" && code !== "aborted") {
        latest.current.onNotice(`Voice mode: ${code}.`, true);
      }
    };
    listener.onend = () => {
      setListening(false);
      // Some browsers end a "continuous" session on their own after a
      // stretch of silence. Restart it while voice mode is still on so
      // "continuous" stays true rather than going quietly deaf.
      if (activeRef.current && !deniedPermission) {
        try {
          listener.start();
          setListening(true);
        } catch {
          // Will be retried the next time voice mode is toggled on.
        }
      }
    };
    try {
      listener.start();
      setListening(true);
    } catch (error) {
      latest.current.onNotice(String(error), true);
    }
  }, [Ctor, handleResult]);

  // Push-to-talk (iOS): one single-shot recognition per press, ended
  // explicitly by release rather than by a silence timer.
  const pressStart = useCallback(() => {
    if (!Ctor || listenMode !== "push-to-talk") return;
    const listener = new Ctor();
    recognition.current = listener;
    listener.lang = latest.current.settings.lang || navigator.language;
    listener.interimResults = true;
    listener.continuous = false;
    let finalText = "";
    listener.onresult = (event) => {
      const result = collectTranscript(event.results, 0);
      if (result.finalized.length) finalText = result.finalized.join(" ");
      setTranscript(appendTranscript(finalText, result.interim));
      if (typeof window !== "undefined" && window.speechSynthesis?.speaking) window.speechSynthesis.cancel();
    };
    listener.onend = () => {
      setListening(false);
      setTranscript("");
      if (finalText.trim()) act(classifyUtterance(finalText, !!latest.current.pendingApproval));
    };
    listener.onerror = (event) => {
      setListening(false);
      if (event?.error === "not-allowed" || event?.error === "service-not-allowed")
        latest.current.onNotice("Microphone permission was denied.", true);
    };
    try {
      listener.start();
      setListening(true);
    } catch (error) {
      latest.current.onNotice(String(error), true);
    }
  }, [Ctor, listenMode, act]);
  const pressEnd = useCallback(() => {
    recognition.current?.stop();
  }, []);

  // Toggling on/off: start or tear down recognition, request/release the
  // wake lock, and reset every piece of visible state.
  useEffect(() => {
    if (!active) {
      recognition.current?.stop();
      if (typeof window !== "undefined") window.speechSynthesis?.cancel();
      setListening(false);
      setSpeaking(false);
      setTranscript("");
      setPendingSend(undefined);
      setStatusMessage("");
      if (sendTimer.current !== undefined) {
        window.clearTimeout(sendTimer.current);
        sendTimer.current = undefined;
      }
      if (silenceTimer.current !== undefined) {
        window.clearTimeout(silenceTimer.current);
        silenceTimer.current = undefined;
      }
      spokenLength.current = null;
      announcedApproval.current = null;
      void wakeLock.current?.release().catch(() => {});
      wakeLock.current = null;
      return;
    }
    if (listenMode === "continuous") startContinuous();
    const nav = navigator as Navigator & {
      wakeLock?: { request(type: "screen"): Promise<{ release(): Promise<void> }> };
    };
    nav.wakeLock
      ?.request("screen")
      .then((lock) => {
        wakeLock.current = lock;
      })
      .catch(() => {
        // Rejected wake locks (backgrounded tab, unsupported browser) are
        // tolerated — the screen may just turn off during a long reply.
      });
    return () => {
      recognition.current?.stop();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, listenMode]);

  useEffect(
    () => () => {
      recognition.current?.stop();
      if (typeof window !== "undefined") window.speechSynthesis?.cancel();
      if (sendTimer.current !== undefined) window.clearTimeout(sendTimer.current);
      if (silenceTimer.current !== undefined) window.clearTimeout(silenceTimer.current);
      void wakeLock.current?.release().catch(() => {});
    },
    [],
  );

  // Disabled sessions (ended / unavailable) cannot be voiced into.
  useEffect(() => {
    if (inputs.disabled && active) setActive(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inputs.disabled]);

  // Auto-read: speak whatever pane text arrived since voice mode turned on
  // (or since the last chunk read), never the whole prior history.
  useEffect(() => {
    if (!active) {
      spokenLength.current = null;
      return;
    }
    if (spokenLength.current === null) {
      spokenLength.current = inputs.sessionText.length;
      return;
    }
    if (!settings.autoRead) {
      spokenLength.current = inputs.sessionText.length;
      return;
    }
    if (inputs.sessionText.length < spokenLength.current) {
      spokenLength.current = inputs.sessionText.length;
      return;
    }
    const added = inputs.sessionText.slice(spokenLength.current);
    spokenLength.current = inputs.sessionText.length;
    const clean = stripForSpeech(added);
    if (clean) speakChunks(sentenceChunks(clean));
  }, [inputs.sessionText, active, settings.autoRead, speakChunks]);

  // Announce a newly-pending approval once, by id, so a poll that repeats
  // the same approval never repeats the announcement.
  useEffect(() => {
    const approval = inputs.pendingApproval;
    if (!active || !approval) {
      if (!approval) announcedApproval.current = null;
      return;
    }
    if (announcedApproval.current === approval.id) return;
    announcedApproval.current = approval.id;
    const line = `Claude wants to run ${approval.summary || approval.toolName}. Say approve or deny.`;
    setStatusMessage(line);
    speak(line);
  }, [active, inputs.pendingApproval, speak]);

  const toggleActive = useCallback(() => {
    setActive((old) => {
      if (old) return false;
      return supported;
    });
  }, [supported]);

  const updateSettings = useCallback((patch: Partial<VoiceSettings>) => {
    setSettings((old) => {
      const next = { ...old, ...patch };
      saveVoiceSettings(next);
      return next;
    });
  }, []);

  return {
    supported,
    listenMode,
    active,
    listening,
    speaking,
    transcript,
    pendingSend,
    statusMessage,
    settings,
    toggleActive,
    cancelPendingSend,
    updateSettings,
    pressStart,
    pressEnd,
  };
}

// A future paid backend (e.g. an OpenAI Realtime session) could implement
// this same shape and be swapped in behind a setting without touching the
// free browser-native path above. Not implemented anywhere yet — no paid
// provider ships in this change.
export interface VoiceProvider {
  start(): void;
  stop(): void;
}
