import assert from "node:assert/strict";
import { test } from "node:test";
import {
  classifyUtterance,
  detectListenMode,
  loadVoiceSettings,
  matchesApprove,
  matchesDeny,
  saveVoiceSettings,
  sentenceChunks,
  stripForSpeech,
  stripSendTrigger,
} from "./voice-mode";

// ---- terminal-noise stripping for read-back --------------------------------

test("stripForSpeech removes ANSI escape codes", () => {
  assert.equal(stripForSpeech("\x1b[32mhello\x1b[0m world"), "hello world");
});

test("stripForSpeech removes box-drawing characters", () => {
  assert.equal(stripForSpeech("┌─── Build ───┐\n│ ok │\n└─────┘"), "Build\nok");
});

test("stripForSpeech replaces a fenced code block with a short marker", () => {
  const text = "Here is the fix:\n```py\ndef f():\n    return 1\n```\nDone.";
  const cleaned = stripForSpeech(text);
  assert.ok(cleaned.includes("Code omitted."));
  assert.ok(!cleaned.includes("def f()"));
  assert.ok(cleaned.includes("Here is the fix"));
  assert.ok(cleaned.includes("Done."));
});

test("stripForSpeech collapses repeated blank lines and runs of spaces", () => {
  assert.equal(stripForSpeech("a   b\n\n\n\nc"), "a b\nc");
});

test("sentenceChunks splits on sentence punctuation and newlines", () => {
  assert.deepEqual(sentenceChunks("First step. Second step! Third?\nFourth line"), [
    "First step.",
    "Second step!",
    "Third?",
    "Fourth line",
  ]);
});

test("sentenceChunks drops empty pieces", () => {
  assert.deepEqual(sentenceChunks("  \n\nHello.  "), ["Hello."]);
});

// ---- approve/deny keyword matching -----------------------------------------

test("matchesApprove accepts the bare keyword, case-insensitively, with punctuation", () => {
  assert.ok(matchesApprove("approve"));
  assert.ok(matchesApprove("Approve!"));
  assert.ok(matchesApprove("allow"));
  assert.ok(matchesApprove("yes"));
});

test("matchesApprove accepts the keyword padded only with please", () => {
  assert.ok(matchesApprove("please approve"));
  assert.ok(matchesApprove("approve please"));
});

test("matchesApprove rejects a sentence that only mentions the word", () => {
  assert.ok(!matchesApprove("don't approve that yet"));
  assert.ok(!matchesApprove("I will approve it later"));
  assert.ok(!matchesApprove("approve the next one"));
});

test("matchesDeny accepts the bare keyword or with please, and rejects sentences", () => {
  assert.ok(matchesDeny("deny"));
  assert.ok(matchesDeny("no"));
  assert.ok(matchesDeny("stop"));
  assert.ok(matchesDeny("please deny"));
  assert.ok(!matchesDeny("don't deny it"));
  assert.ok(!matchesDeny("stop that"));
});

test("matchesApprove and matchesDeny never both match the same utterance", () => {
  for (const text of ["approve", "deny", "please approve", "no thanks", "stop that", "yes"]) {
    assert.ok(!(matchesApprove(text) && matchesDeny(text)), text);
  }
});

// ---- send trigger words -----------------------------------------------------

test("stripSendTrigger recognizes a trailing 'send it' and strips it", () => {
  const { text, triggered } = stripSendTrigger("run the deploy script send it");
  assert.equal(triggered, true);
  assert.equal(text, "run the deploy script");
});

test("stripSendTrigger recognizes a trailing 'over' and strips it", () => {
  const { text, triggered } = stripSendTrigger("continue with the migration over");
  assert.equal(triggered, true);
  assert.equal(text, "continue with the migration");
});

test("stripSendTrigger leaves ordinary text untouched", () => {
  const { text, triggered } = stripSendTrigger("run the tests again");
  assert.equal(triggered, false);
  assert.equal(text, "run the tests again");
});

test("stripSendTrigger does not strip 'over' or 'send it' out of the middle of a sentence", () => {
  const { text, triggered } = stripSendTrigger("send it over to staging");
  assert.equal(triggered, false);
  assert.equal(text, "send it over to staging");
});

// ---- utterance -> action classification ------------------------------------

test("classifyUtterance sends ordinary text as a message with the trigger word removed", () => {
  assert.deepEqual(classifyUtterance("run the build send it", false), {
    type: "send",
    text: "run the build",
  });
});

test("classifyUtterance recognizes global voice commands regardless of a pending approval", () => {
  assert.deepEqual(classifyUtterance("exit voice mode", false), { type: "exit" });
  assert.deepEqual(classifyUtterance("interrupt", true), { type: "interrupt" });
  assert.deepEqual(classifyUtterance("stop that", false), { type: "interrupt" });
  assert.deepEqual(classifyUtterance("read that again", false), { type: "read-again" });
});

test("classifyUtterance only treats approve/deny words as decisions when an approval is pending", () => {
  assert.deepEqual(classifyUtterance("approve", true), { type: "approve" });
  assert.deepEqual(classifyUtterance("deny", true), { type: "deny" });
  // With nothing pending, the same words are just message text.
  assert.deepEqual(classifyUtterance("approve", false), { type: "send", text: "approve" });
});

test("classifyUtterance does not mistake 'don't approve that yet' for a decision", () => {
  assert.deepEqual(classifyUtterance("don't approve that yet", true), {
    type: "send",
    text: "don't approve that yet",
  });
});

test("classifyUtterance ignores an empty or trigger-only utterance", () => {
  assert.deepEqual(classifyUtterance("send it", false), { type: "ignore" });
  assert.deepEqual(classifyUtterance("   ", false), { type: "ignore" });
});

// ---- per-device settings -----------------------------------------------------

function fakeStorage() {
  const store = new Map<string, string>();
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => void store.set(key, value),
    removeItem: (key: string) => void store.delete(key),
    clear: () => store.clear(),
    key: () => null,
    get length() {
      return store.size;
    },
  } as Storage;
}

test("loadVoiceSettings returns defaults when nothing is stored", () => {
  (globalThis as { localStorage?: Storage }).localStorage = fakeStorage();
  const settings = loadVoiceSettings();
  assert.equal(settings.autoRead, true);
  assert.equal(settings.rate, 1);
  assert.equal(settings.voiceURI, "");
});

test("saveVoiceSettings round-trips through loadVoiceSettings", () => {
  (globalThis as { localStorage?: Storage }).localStorage = fakeStorage();
  saveVoiceSettings({ voiceURI: "v1", rate: 1.5, autoRead: false, lang: "es-ES" });
  const settings = loadVoiceSettings();
  assert.deepEqual(settings, { voiceURI: "v1", rate: 1.5, autoRead: false, lang: "es-ES" });
});

test("loadVoiceSettings tolerates corrupt JSON and a full/blocked localStorage", () => {
  (globalThis as { localStorage?: Storage }).localStorage = {
    getItem: () => "{not json",
    setItem: () => {
      throw new Error("quota exceeded");
    },
  } as unknown as Storage;
  assert.doesNotThrow(() => saveVoiceSettings({ voiceURI: "", rate: 1, autoRead: true, lang: "" }));
  const settings = loadVoiceSettings();
  assert.equal(settings.rate, 1);
});

// ---- capability / fallback detection -----------------------------------------

function fakeWindow(overrides: { ctor?: boolean; ua?: string }) {
  return {
    SpeechRecognition: overrides.ctor === false ? undefined : function () {},
    navigator: { userAgent: overrides.ua ?? "" },
  } as unknown as Window;
}

test("detectListenMode is unsupported when no SpeechRecognition constructor exists", () => {
  assert.equal(detectListenMode(fakeWindow({ ctor: false })), "unsupported");
});

test("detectListenMode is continuous on a desktop/Android Chrome-shaped user agent", () => {
  const ua = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/128.0 Mobile Safari/537.36";
  assert.equal(detectListenMode(fakeWindow({ ua })), "continuous");
});

test("detectListenMode falls back to push-to-talk on any iOS browser (all wrap the same WebKit engine)", () => {
  const safari =
    "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";
  const chromeOnIOS =
    "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/128.0 Mobile/15E148 Safari/604.1";
  assert.equal(detectListenMode(fakeWindow({ ua: safari })), "push-to-talk");
  assert.equal(detectListenMode(fakeWindow({ ua: chromeOnIOS })), "push-to-talk");
});
