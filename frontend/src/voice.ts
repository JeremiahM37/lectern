// Web Speech dictation, shared behind a small pure core so the chat composer
// and the Needs-you "deny with reason" field can each offer a mic button
// without duplicating the SpeechRecognition wiring. Recording never sends
// anything on its own — every word it hears only ever lands in the box a
// person can still edit before they submit it themselves.
import { useCallback, useEffect, useRef, useState } from "react";
import { chooseEngine, hostVoice, record, transcribe, voicePreference, type Recording, type VoiceEngine } from "./voice-host";

export interface RecognitionResult {
  transcript: string;
  isFinal: boolean;
}
// The subset of the real (webkit)SpeechRecognition surface this needs —
// narrow enough to fake in a test, wide enough to drive the real API.
export interface Recognition {
  lang: string;
  interimResults: boolean;
  continuous?: boolean;
  onresult: ((event: { results: ArrayLike<ArrayLike<RecognitionResult>> }) => void) | null;
  onend: (() => void) | null;
  onerror: ((event?: { error?: string }) => void) | null;
  start(): void;
  stop(): void;
}
export type RecognitionCtor = new () => Recognition;

// speechCtor is the one bit of feature detection: undefined anywhere the API
// does not exist (most non-Chromium browsers, and any browser without a
// microphone permission model for it), which is what hides a mic button
// entirely rather than showing one that only errors.
export function speechCtor(win: Window): RecognitionCtor | undefined {
  const w = win as unknown as {
    SpeechRecognition?: RecognitionCtor;
    webkitSpeechRecognition?: RecognitionCtor;
  };
  return w.SpeechRecognition || w.webkitSpeechRecognition;
}

// collectTranscript turns one onresult event into the live interim preview
// (everything Speech has not finalized yet) and the pieces newly finalized
// since the last event. The API keeps re-delivering already-final results on
// every subsequent event, so the caller must track how many it has already
// committed — that count is threaded through rather than kept as hidden
// module state, which is what makes this testable without a real
// SpeechRecognition.
export function collectTranscript(
  results: ArrayLike<ArrayLike<RecognitionResult>>,
  committed: number,
): { interim: string; finalized: string[]; committed: number } {
  let interim = "";
  const finalized: string[] = [];
  let next = committed;
  for (let i = 0; i < results.length; i++) {
    const alt = results[i]?.[0];
    if (!alt) continue;
    if (alt.isFinal) {
      if (i >= committed) {
        finalized.push(alt.transcript.trim());
        next = i + 1;
      }
    } else {
      interim += alt.transcript;
    }
  }
  return { interim, finalized, committed: next };
}

// appendTranscript joins newly-recognized text onto whatever was already in
// the box, adding a separating space only when one is not already there.
// Empty recognitions (silence, a discarded interim) leave the base alone.
export function appendTranscript(base: string, addition: string): string {
  const clean = addition.trim();
  if (!clean) return base;
  return base + (base && !/\s$/.test(base) ? " " : "") + clean;
}

export interface DictationHandlers {
  /** Called with the box's full text every time recognition changes it — live interim words, a newly-finalized piece, or the cleared preview on stop/error. */
  onChange(text: string): void;
  onNotice(text: string, error?: boolean): void;
}

// useDictation owns one mic button's state: whether Speech is available in
// this browser, whether it is currently recording, and turning
// SpeechRecognition's callback soup into "the box's text, live" updates.
// Interim words appear as they are heard and are replaced — never
// duplicated — once Speech finalizes them. Nothing is ever submitted here;
// recording only ever changes what is in the box, exactly like typing would.
export function useDictation({ onChange, onNotice }: DictationHandlers) {
  const [dictating, setDictating] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const recognition = useRef<Recognition | undefined>(undefined);
  const recording = useRef<Recording | undefined>(undefined);
  const startedWith = useRef("");
  const finalText = useRef("");
  const committed = useRef(0);
  const Ctor = typeof window !== "undefined" ? speechCtor(window) : undefined;
  // Transcription on the Lectern host (voice-host.ts) is the other engine:
  // the only one in the Android app's WebView, which has no speech
  // recognition, and a choice anywhere else (Settings → Notifications).
  const [host, setHost] = useState(false);
  const [pref, setPref] = useState(voicePreference);
  useEffect(() => {
    let alive = true;
    void hostVoice().then((status) => alive && setHost(status.available));
    const changed = () => setPref(voicePreference());
    window.addEventListener("lec-voice-engine", changed);
    return () => {
      alive = false;
      window.removeEventListener("lec-voice-engine", changed);
    };
  }, []);
  const engine: VoiceEngine | undefined = chooseEngine(pref, !!Ctor, host);

  const apply = useCallback(
    (interim: string) => {
      onChange(appendTranscript(appendTranscript(startedWith.current, finalText.current), interim));
    },
    [onChange],
  );

  const finishRecording = useCallback(async () => {
    const current = recording.current;
    recording.current = undefined;
    if (!current) return;
    setDictating(false);
    setTranscribing(true);
    try {
      const text = await transcribe(await current.stop());
      onChange(appendTranscript(startedWith.current, text));
      if (!text.trim()) onNotice("Nothing was heard.");
    } catch (error) {
      onNotice(error instanceof Error ? error.message : String(error), true);
    } finally {
      setTranscribing(false);
    }
  }, [onChange, onNotice]);

  const stop = useCallback(() => {
    if (recording.current) void finishRecording();
    recognition.current?.stop();
  }, [finishRecording]);

  useEffect(() => () => recording.current?.cancel(), []);

  const start = useCallback(
    (currentText: string) => {
      startedWith.current = currentText;
      finalText.current = "";
      committed.current = 0;
      if (engine === "host") {
        void record(() => void finishRecording())
          .then((r) => {
            recording.current = r;
            setDictating(true);
          })
          .catch((error: unknown) =>
            onNotice(error instanceof Error && error.name === "NotAllowedError"
              ? "Microphone access was refused. Allow it for this app and try again."
              : `Recording could not start: ${error instanceof Error ? error.message : String(error)}`, true),
          );
        return;
      }
      if (!Ctor) return;
      const listener = new Ctor();
      recognition.current = listener;
      listener.lang = navigator.language;
      listener.interimResults = true;
      listener.continuous = true;
      listener.onresult = (event) => {
        const result = collectTranscript(event.results, committed.current);
        committed.current = result.committed;
        for (const piece of result.finalized) finalText.current = appendTranscript(finalText.current, piece);
        apply(result.interim);
      };
      listener.onend = () => {
        setDictating(false);
        apply("");
      };
      listener.onerror = () => {
        setDictating(false);
        apply("");
        onNotice("Dictation could not start. Check microphone permission.", true);
      };
      try {
        listener.start();
        setDictating(true);
      } catch (error) {
        onNotice(String(error), true);
      }
    },
    [Ctor, apply, engine, finishRecording, onNotice],
  );

  const toggle = useCallback(
    (currentText: string) => {
      if (transcribing) return;
      if (dictating) stop();
      else start(currentText);
    },
    [dictating, start, stop, transcribing],
  );

  return { supported: !!engine, engine, dictating, transcribing, toggle, stop };
}
