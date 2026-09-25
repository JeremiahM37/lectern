import assert from "node:assert/strict";
import { test } from "node:test";
import { appendTranscript, collectTranscript, speechCtor } from "./voice";

test("speechCtor finds the standard name first, then the webkit-prefixed one", () => {
  class Standard {}
  class Webkit {}
  assert.equal(
    speechCtor({ SpeechRecognition: Standard, webkitSpeechRecognition: Webkit } as unknown as Window),
    Standard,
  );
  assert.equal(
    speechCtor({ webkitSpeechRecognition: Webkit } as unknown as Window),
    Webkit,
  );
});

test("speechCtor is undefined where neither constructor exists", () => {
  assert.equal(speechCtor({} as unknown as Window), undefined);
});

test("appendTranscript adds one separating space between existing and new text", () => {
  assert.equal(appendTranscript("hello", "world"), "hello world");
  assert.equal(appendTranscript("", "world"), "world");
  assert.equal(appendTranscript("hello ", "world"), "hello world");
});

test("appendTranscript ignores whitespace-only or empty additions", () => {
  assert.equal(appendTranscript("hello", ""), "hello");
  assert.equal(appendTranscript("hello", "   "), "hello");
});

function result(transcript: string, isFinal: boolean) {
  return [{ transcript, isFinal }];
}

test("collectTranscript separates the live interim preview from newly-finalized pieces", () => {
  const got = collectTranscript([result("hel", false)], 0);
  assert.equal(got.interim, "hel");
  assert.deepEqual(got.finalized, []);
  assert.equal(got.committed, 0);
});

test("collectTranscript only reports a final result once, tracked by the committed count", () => {
  const results = [result("hello world", true)];
  const first = collectTranscript(results, 0);
  assert.deepEqual(first.finalized, ["hello world"]);
  assert.equal(first.committed, 1);
  // The API keeps re-delivering the same finalized result on later events —
  // passing the previous committed count back must not report it again.
  const second = collectTranscript(results, first.committed);
  assert.deepEqual(second.finalized, []);
  assert.equal(second.committed, 1);
});

test("collectTranscript reports one finalized result and the next interim together", () => {
  const got = collectTranscript([result("hello", true), result("wor", false)], 0);
  assert.deepEqual(got.finalized, ["hello"]);
  assert.equal(got.interim, "wor");
  assert.equal(got.committed, 1);
});

test("collectTranscript skips a result with no first alternative", () => {
  const got = collectTranscript([[]], 0);
  assert.deepEqual(got.finalized, []);
  assert.equal(got.interim, "");
});
