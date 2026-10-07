// The phone's own speech recognition and voices in the Android app
// (mobile/android Speech.kt), presented as the SpeechRecognition and
// speechSynthesis a browser has. The app's WebView defines
// webkitSpeechRecognition but has no service behind it, and its
// speechSynthesis says nothing; with these in their place, dictation
// (voice.ts) and voice mode (sessions/voice-mode.ts) work unchanged.
//
// Android's recognizer hears one utterance at a time. A continuous
// recognition here restarts it after each one, and keeps every finished
// utterance in `results`, as a browser's continuous recognition does.
import { nativeBridge, type NativeBridge } from "./bridge";

export interface NativeSpeechDetail {
  session: number;
  type: "start" | "partial" | "final" | "error" | "end";
  text?: string;
  error?: string;
}

export interface NativeTtsDetail {
  id?: string;
  type: "start" | "end" | "error" | "voices";
  error?: string;
}

interface Alternative {
  transcript: string;
  confidence: number;
  isFinal: boolean;
}
type Result = Alternative[] & { isFinal: boolean };

function result(transcript: string, isFinal: boolean): Result {
  const alternative = { transcript, confidence: isFinal ? 1 : 0, isFinal };
  return Object.assign([alternative], { isFinal });
}

// Errors after which listening again would only fail again.
const FATAL = new Set(["not-allowed", "service-not-allowed", "audio-capture", "language-not-supported"]);
// A restart that fails this fast this many times running is a broken
// recognizer, not a pause in speech.
const QUICK_MS = 1500;
const QUICK_LIMIT = 3;

let serial = 0;
const live = new Map<number, NativeRecognition>();

export class NativeRecognition {
  lang = "";
  interimResults = false;
  continuous = false;
  maxAlternatives = 1;
  onresult: ((event: { results: Result[]; resultIndex: number }) => void) | null = null;
  onend: (() => void) | null = null;
  onerror: ((event: { error: string }) => void) | null = null;
  onstart: (() => void) | null = null;

  private session = 0;
  private finals: Result[] = [];
  private stopping = false;
  private started = 0;
  private quick = 0;
  private fatal = false;

  constructor(private bridge: NativeBridge = nativeBridge()!) {}

  start() {
    if (this.session) throw new DOMException("recognition has already started", "InvalidStateError");
    this.session = ++serial;
    live.set(this.session, this);
    this.finals = [];
    this.stopping = false;
    this.fatal = false;
    this.quick = 0;
    this.listen();
  }

  /** Ends listening, keeping what was heard. */
  stop() {
    if (!this.session) return;
    this.stopping = true;
    this.bridge.speechStop?.();
    this.settle();
  }

  /** Ends listening, dropping what was being heard. */
  abort() {
    if (!this.session) return;
    this.stopping = true;
    this.bridge.speechCancel?.();
    this.settle();
  }

  // The app answers a stop with a final result and "end". If the
  // recognizer was already done (between two utterances) it has nothing
  // to answer, so the recognition ends here instead of never.
  private settle() {
    const session = this.session;
    setTimeout(() => {
      if (this.session === session) this.receive({ session, type: "end" });
    }, 3000);
  }

  private listen() {
    this.started = Date.now();
    this.bridge.speechStart?.(this.session, this.lang || navigator.language || "");
  }

  private emit(interim?: string) {
    const results = interim ? [...this.finals, result(interim, false)] : [...this.finals];
    this.onresult?.({ results, resultIndex: Math.max(0, results.length - 1) });
  }

  /** @internal An event from the app for this recognition. */
  receive(detail: NativeSpeechDetail) {
    switch (detail.type) {
      case "start":
        if (this.finals.length === 0 && this.quick === 0) this.onstart?.();
        return;
      case "partial":
        if (this.interimResults && detail.text) this.emit(detail.text);
        return;
      case "final":
        if (detail.text) {
          this.finals.push(result(detail.text, true));
          this.emit();
        }
        return;
      case "error": {
        const error = detail.error || "unknown";
        if (FATAL.has(error)) this.fatal = true;
        // Silence is not an error to a continuous listener; a browser's
        // continuous recognition would simply keep listening.
        if (this.continuous && !this.stopping && (error === "no-speech" || error === "aborted")) return;
        this.onerror?.({ error });
        return;
      }
      case "end": {
        const quick = Date.now() - this.started < QUICK_MS;
        this.quick = quick ? this.quick + 1 : 0;
        if (this.continuous && !this.stopping && !this.fatal && this.quick < QUICK_LIMIT) {
          this.listen();
          return;
        }
        live.delete(this.session);
        this.session = 0;
        this.onend?.();
      }
    }
  }
}

// ---- speech synthesis --------------------------------------------------------

export class NativeUtterance {
  lang = "";
  rate = 1;
  pitch = 1;
  volume = 1;
  voice: { name: string; voiceURI: string; lang: string } | null = null;
  onstart: (() => void) | null = null;
  onend: (() => void) | null = null;
  onerror: ((event: { error: string }) => void) | null = null;
  constructor(public text = "") {}
}

interface NativeVoice {
  name: string;
  voiceURI: string;
  lang: string;
  localService: boolean;
  default: boolean;
}

export class NativeSynthesis extends EventTarget {
  private queue = new Map<string, NativeUtterance>();
  private next = 0;
  private cachedVoices: NativeVoice[] = [];
  onvoiceschanged: (() => void) | null = null;

  constructor(private bridge: NativeBridge = nativeBridge()!) {
    super();
  }

  get speaking() {
    return this.queue.size > 0;
  }
  get pending() {
    return this.queue.size > 1;
  }
  get paused() {
    return false;
  }

  speak(utterance: NativeUtterance) {
    const id = `u${++this.next}`;
    this.queue.set(id, utterance);
    this.bridge.ttsSpeak?.(id, utterance.text, utterance.lang || "", utterance.rate || 1, utterance.voice?.name || "");
  }

  cancel() {
    if (!this.queue.size) return;
    const dropped = [...this.queue.values()];
    this.queue.clear();
    this.bridge.ttsStop?.();
    for (const u of dropped) u.onerror?.({ error: "canceled" });
  }

  pause() {}
  resume() {}

  getVoices(): NativeVoice[] {
    if (!this.cachedVoices.length) {
      try {
        const list = JSON.parse(this.bridge.ttsVoices?.() || "[]") as { name: string; lang: string }[];
        this.cachedVoices = list.map((v) => ({ name: v.name, voiceURI: v.name, lang: v.lang, localService: true, default: false }));
      } catch {
        this.cachedVoices = [];
      }
    }
    return this.cachedVoices;
  }

  /** @internal An event from the app's voice. */
  receive(detail: NativeTtsDetail) {
    if (detail.type === "voices") {
      this.cachedVoices = [];
      this.onvoiceschanged?.();
      this.dispatchEvent(new Event("voiceschanged"));
      return;
    }
    const utterance = detail.id ? this.queue.get(detail.id) : undefined;
    if (!utterance) return;
    if (detail.type === "start") return utterance.onstart?.();
    this.queue.delete(detail.id!);
    if (detail.type === "end") utterance.onend?.();
    else utterance.onerror?.({ error: detail.error || "synthesis-failed" });
  }
}

// ---- installing --------------------------------------------------------------

export interface NativeSpeechSupport {
  recognition: boolean;
  tts: boolean;
}

declare global {
  interface Window {
    __lecternNativeSpeech?: NativeSpeechSupport;
  }
}

/** What the app said it can do, or undefined outside the app (or in an app
 * from before 2.9.0, whose bridge has no speech). */
export function nativeSpeech(win: Window = window): NativeSpeechSupport | undefined {
  return win.__lecternNativeSpeech;
}

/** In the app, puts the phone's speech where the page looks for it. */
export function installNativeSpeech(win: Window = window, bridge: NativeBridge | undefined = nativeBridge()): NativeSpeechSupport | undefined {
  if (!bridge?.speechSupport) return undefined;
  if (win.__lecternNativeSpeech) return win.__lecternNativeSpeech;
  let support: NativeSpeechSupport = { recognition: false, tts: false };
  try {
    support = { recognition: false, tts: false, ...(JSON.parse(bridge.speechSupport()) as Partial<NativeSpeechSupport>) };
  } catch {
    /* an unreadable answer: neither */
  }
  const define = (name: string, value: unknown) =>
    Object.defineProperty(win, name, { value, configurable: true, writable: true });
  if (support.recognition) {
    const Bound = class extends NativeRecognition {
      constructor() {
        super(bridge);
      }
    };
    define("SpeechRecognition", Bound);
    define("webkitSpeechRecognition", Bound);
  }
  let synthesis: NativeSynthesis | undefined;
  if (support.tts) {
    synthesis = new NativeSynthesis(bridge);
    define("speechSynthesis", synthesis);
    define("SpeechSynthesisUtterance", NativeUtterance);
  }
  win.addEventListener("lectern-native-speech", (event) => {
    const detail = (event as CustomEvent<NativeSpeechDetail>).detail;
    if (detail) live.get(detail.session)?.receive(detail);
  });
  win.addEventListener("lectern-native-tts", (event) => {
    const detail = (event as CustomEvent<NativeTtsDetail>).detail;
    if (detail) synthesis?.receive(detail);
  });
  win.__lecternNativeSpeech = support;
  return support;
}
