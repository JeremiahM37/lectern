import assert from "node:assert/strict";
import { test } from "node:test";
import type { NativeBridge } from "./bridge";
import { installNativeSpeech, NativeRecognition, NativeSynthesis, NativeUtterance, type NativeSpeechDetail } from "./speech";
import { collectTranscript } from "../voice";

// A stand-in for the app's bridge that records calls; tests answer as the
// app would by handing events to the recognition.
function fakeBridge(support = { recognition: true, tts: true }) {
  const calls: string[] = [];
  const bridge = {
    speechSupport: () => JSON.stringify(support),
    speechStart: (session: number, lang: string) => calls.push(`start ${session} ${lang}`),
    speechStop: () => calls.push("stop"),
    speechCancel: () => calls.push("cancel"),
    ttsSpeak: (id: string, text: string, lang: string, rate: number, voice: string) => calls.push(`say ${id} ${text} ${lang} ${rate} ${voice}`),
    ttsStop: () => calls.push("hush"),
    ttsVoices: () => JSON.stringify([{ name: "en-us-x-sfg-local", lang: "en-US" }]),
  } as unknown as NativeBridge;
  return { bridge, calls };
}

function recognition(bridge: NativeBridge, continuous: boolean) {
  const r = new NativeRecognition(bridge);
  r.lang = "en-US";
  r.interimResults = true;
  r.continuous = continuous;
  const seen: { interim: string; finals: string[] }[] = [];
  const ends: number[] = [];
  const errors: string[] = [];
  let committed = 0;
  r.onresult = (event) => {
    const got = collectTranscript(event.results, committed);
    committed = got.committed;
    seen.push({ interim: got.interim, finals: got.finalized });
  };
  r.onend = () => ends.push(1);
  r.onerror = (event) => errors.push(event.error);
  const send = (detail: Omit<NativeSpeechDetail, "session">) => r.receive({ session: (r as unknown as { session: number }).session, ...detail });
  return { r, seen, ends, errors, send };
}

test("one utterance: interim words, then the final one, then end", () => {
  const { bridge, calls } = fakeBridge();
  const { r, seen, ends, send } = recognition(bridge, false);
  r.start();
  assert.match(calls[0]!, /^start \d+ en-US$/);
  send({ type: "partial", text: "fix the" });
  send({ type: "partial", text: "fix the login" });
  send({ type: "final", text: "fix the login bug" });
  send({ type: "end" });
  assert.deepEqual(seen, [
    { interim: "fix the", finals: [] },
    { interim: "fix the login", finals: [] },
    { interim: "", finals: ["fix the login bug"] },
  ]);
  assert.equal(ends.length, 1);
});

test("continuous listening restarts the phone's recognizer after each utterance and keeps every result", () => {
  const { bridge, calls } = fakeBridge();
  const { r, seen, ends, errors, send } = recognition(bridge, true);
  r.start();
  send({ type: "final", text: "first" });
  (r as unknown as { started: number }).started = 0; // a long utterance, not a quick failure
  send({ type: "end" });
  assert.equal(calls.filter((c) => c.startsWith("start")).length, 2, "listens again");
  // Silence between utterances is not an error to a continuous listener.
  send({ type: "error", error: "no-speech" });
  (r as unknown as { started: number }).started = 0;
  send({ type: "end" });
  send({ type: "final", text: "second" });
  assert.deepEqual(seen.map((s) => s.finals), [["first"], ["second"]]);
  assert.deepEqual(errors, []);
  assert.equal(ends.length, 0);
  r.stop();
  assert.equal(calls.at(-1), "stop");
  send({ type: "end" });
  assert.equal(ends.length, 1);
});

test("a refused microphone ends listening instead of restarting", () => {
  const { bridge, calls } = fakeBridge();
  const { r, ends, errors, send } = recognition(bridge, true);
  r.start();
  send({ type: "error", error: "not-allowed" });
  send({ type: "end" });
  assert.deepEqual(errors, ["not-allowed"]);
  assert.equal(ends.length, 1);
  assert.equal(calls.filter((c) => c.startsWith("start")).length, 1);
});

test("a recognizer that keeps failing at once stops being restarted", () => {
  const { bridge, calls } = fakeBridge();
  const { r, ends, send } = recognition(bridge, true);
  r.start();
  for (let i = 0; i < 3; i++) send({ type: "end" });
  assert.equal(ends.length, 1);
  assert.equal(calls.filter((c) => c.startsWith("start")).length, 3);
});

test("starting twice is refused, as a browser refuses it", () => {
  const { bridge } = fakeBridge();
  const { r } = recognition(bridge, false);
  r.start();
  assert.throws(() => r.start(), /already started/);
});

test("speech synthesis speaks through the app and reports start, end and cancel", () => {
  const { bridge, calls } = fakeBridge();
  const synth = new NativeSynthesis(bridge);
  const events: string[] = [];
  const one = new NativeUtterance("Tests pass.");
  one.lang = "en-US";
  one.rate = 1.2;
  one.onstart = () => events.push("start 1");
  one.onend = () => events.push("end 1");
  const two = new NativeUtterance("Ready to merge.");
  two.voice = { name: "en-us-x-sfg-local", voiceURI: "en-us-x-sfg-local", lang: "en-US" };
  two.onerror = (e) => events.push(`error 2 ${e.error}`);
  synth.speak(one);
  synth.speak(two);
  assert.deepEqual(calls, ["say u1 Tests pass. en-US 1.2 ", "say u2 Ready to merge.  1 en-us-x-sfg-local"]);
  assert.equal(synth.speaking, true);
  synth.receive({ id: "u1", type: "start" });
  synth.receive({ id: "u1", type: "end" });
  synth.cancel();
  assert.deepEqual(events, ["start 1", "end 1", "error 2 canceled"]);
  assert.equal(calls.at(-1), "hush");
  assert.equal(synth.speaking, false);
  assert.deepEqual(synth.getVoices().map((v) => [v.name, v.lang]), [["en-us-x-sfg-local", "en-US"]]);
});

test("install puts the phone's speech where the page looks, and only what the phone has", () => {
  const listeners: Record<string, (e: Event) => void> = {};
  const win = {
    webkitSpeechRecognition: class Broken {},
    addEventListener: (name: string, fn: (e: Event) => void) => (listeners[name] = fn),
  } as unknown as Window;
  const { bridge } = fakeBridge({ recognition: true, tts: false });
  const support = installNativeSpeech(win, bridge);
  assert.deepEqual(support, { recognition: true, tts: false });
  const w = win as unknown as Record<string, unknown>;
  assert.equal(w.SpeechRecognition, w.webkitSpeechRecognition);
  assert.ok(new (w.SpeechRecognition as new () => object)() instanceof NativeRecognition);
  assert.equal(w.speechSynthesis, undefined, "no voices: leave synthesis alone");
  assert.ok(listeners["lectern-native-speech"]);
});

test("an app without speech changes nothing", () => {
  const win = {} as Window;
  assert.equal(installNativeSpeech(win, {} as NativeBridge), undefined);
  assert.equal(installNativeSpeech(win, undefined), undefined);
  assert.deepEqual(Object.keys(win), []);
});
