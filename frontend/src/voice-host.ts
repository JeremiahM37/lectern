// Dictation transcribed on the Lectern host (whisper.cpp, internal/voice),
// the alternative to the browser's own speech recognition. The phone records
// plain 16 kHz mono WAV, the format whisper.cpp reads directly, so the host
// needs no audio converter; the recording goes to this Lectern only (through
// the relay when paired over it) and the text comes back into the same box
// a person reviews before sending.
import { authToken } from "./api/client";
import { t } from "./i18n";

export type VoiceEngine = "device" | "host";
export type VoicePreference = VoiceEngine | "auto";

const PREF = "lec-voice-engine";

export function voicePreference(): VoicePreference {
  try {
    const value = localStorage.getItem(PREF);
    return value === "device" || value === "host" ? value : "auto";
  } catch {
    return "auto";
  }
}

export function setVoicePreference(value: VoicePreference) {
  try {
    if (value === "auto") localStorage.removeItem(PREF);
    else localStorage.setItem(PREF, value);
  } catch {
    /* storage blocked: the choice lasts this visit */
  }
  window.dispatchEvent(new Event("lec-voice-engine"));
}

/** Which engine a mic button uses: the chosen one when it is available,
 * otherwise whichever one is. */
export function chooseEngine(pref: VoicePreference, device: boolean, host: boolean): VoiceEngine | undefined {
  if (pref === "host" && host) return "host";
  if (pref === "device" && device) return "device";
  return device ? "device" : host ? "host" : undefined;
}

export interface HostVoice {
  available: boolean;
  model?: string;
  hint?: string;
}

let status: Promise<HostVoice> | undefined;

function headers(): Headers {
  const h = new Headers();
  const token = authToken();
  if (token) h.set("Authorization", `Bearer ${token}`);
  return h;
}

/** Whether this Lectern can transcribe. Asked once per page. */
export function hostVoice(refresh = false): Promise<HostVoice> {
  if (!status || refresh)
    status = fetch("/api/voice", { headers: headers() })
      .then((r) => (r.ok ? (r.json() as Promise<HostVoice>) : { available: false }))
      .catch(() => ({ available: false }));
  return status;
}

export async function transcribe(wav: Blob, lang = navigator.language): Promise<string> {
  const code = (lang || "").slice(0, 2).toLowerCase();
  const h = headers();
  h.set("Content-Type", "audio/wav");
  const response = await fetch(`/api/voice/transcribe${/^[a-z]{2}$/.test(code) ? `?lang=${code}` : ""}`, {
    method: "POST",
    headers: h,
    body: wav,
  });
  const body = (await response.json().catch(() => ({}))) as { text?: string; detail?: string };
  if (!response.ok) throw new Error(body.detail || t("voice.transcribeFailed", { status: response.status }));
  return body.text || "";
}

/** Averages the input down to the target rate (a phone records at 44.1 or
 * 48 kHz; whisper wants 16 kHz). */
export function downsample(input: Float32Array, from: number, to = 16000): Float32Array {
  if (from <= to) return input;
  const ratio = from / to,
    out = new Float32Array(Math.floor(input.length / ratio));
  for (let i = 0; i < out.length; i++) {
    const start = Math.floor(i * ratio),
      end = Math.min(input.length, Math.floor((i + 1) * ratio));
    let sum = 0;
    for (let j = start; j < end; j++) sum += input[j]!;
    out[i] = end > start ? sum / (end - start) : 0;
  }
  return out;
}

/** 16-bit PCM mono WAV. */
export function encodeWav(samples: Float32Array, rate = 16000): Uint8Array {
  const bytes = new Uint8Array(44 + samples.length * 2),
    view = new DataView(bytes.buffer);
  const text = (at: number, s: string) => [...s].forEach((c, i) => view.setUint8(at + i, c.charCodeAt(0)));
  text(0, "RIFF");
  view.setUint32(4, 36 + samples.length * 2, true);
  text(8, "WAVE");
  text(12, "fmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); // PCM
  view.setUint16(22, 1, true); // mono
  view.setUint32(24, rate, true);
  view.setUint32(28, rate * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  text(36, "data");
  view.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const v = Math.max(-1, Math.min(1, samples[i]!));
    view.setInt16(44 + i * 2, v < 0 ? v * 0x8000 : v * 0x7fff, true);
  }
  return bytes;
}

export const MAX_SECONDS = 180;

export interface Recording {
  /** Stops and returns the WAV. */
  stop(): Promise<Blob>;
  cancel(): void;
}

/** Starts recording from the microphone. onLimit fires when the recording
 * reaches MAX_SECONDS, so the caller can stop and transcribe it. */
export async function record(onLimit: () => void): Promise<Recording> {
  if (!navigator.mediaDevices?.getUserMedia) throw new Error(t("voice.noRecorder"));
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
  const ctx = new AudioContext();
  const source = ctx.createMediaStreamSource(stream);
  // ScriptProcessor is deprecated but is the one capture path every mobile
  // browser and Android WebView still has, with no worklet file to serve.
  const node = ctx.createScriptProcessor(4096, 1, 1);
  const chunks: Float32Array[] = [];
  let samples = 0;
  node.onaudioprocess = (event) => {
    const data = new Float32Array(event.inputBuffer.getChannelData(0));
    chunks.push(data);
    samples += data.length;
    if (samples / ctx.sampleRate >= MAX_SECONDS) onLimit();
  };
  source.connect(node);
  node.connect(ctx.destination);
  const release = () => {
    node.onaudioprocess = null;
    try {
      source.disconnect();
      node.disconnect();
    } catch {
      /* already disconnected */
    }
    stream.getTracks().forEach((t) => t.stop());
    void ctx.close().catch(() => undefined);
  };
  return {
    async stop() {
      const rate = ctx.sampleRate;
      release();
      const all = new Float32Array(samples);
      let at = 0;
      for (const c of chunks) all.set(c, (at += c.length) - c.length);
      return new Blob([encodeWav(downsample(all, rate)) as BlobPart], { type: "audio/wav" });
    },
    cancel: release,
  };
}
