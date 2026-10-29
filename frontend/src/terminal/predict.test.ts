import assert from "node:assert/strict";
import { test } from "node:test";
import { Predictor } from "./predict";

// Read through a call, so an earlier assert does not narrow the type away.
const textOf = (p: Predictor) => p.shown?.text;

const clock = () => {
  let now = 0;
  return { now: () => now, advance: (ms: number) => (now += ms) };
};

test("typed letters show at the cursor until their echo moves it", () => {
  const p = new Predictor();
  p.typed("hel", { x: 2, y: 5 }, 80);
  assert.deepEqual(p.shown, { x: 2, y: 5, text: "hel" });
  // The echo of "h" lands: the cursor moved one cell right.
  p.drawn({ x: 3, y: 5 });
  assert.deepEqual(p.shown, { x: 3, y: 5, text: "el" });
  p.drawn({ x: 5, y: 5 });
  assert.equal(p.shown, undefined);
});

test("a redraw that ends elsewhere drops every guess", () => {
  const p = new Predictor();
  p.typed("ab", { x: 2, y: 5 }, 80);
  p.drawn({ x: 0, y: 6 });
  assert.equal(p.shown, undefined);
  p.typed("ab", { x: 4, y: 5 }, 80);
  p.drawn({ x: 1, y: 5 });
  assert.equal(p.shown, undefined);
});

test("output that leaves the cursor where it was keeps the guesses", () => {
  const p = new Predictor();
  p.typed("ab", { x: 2, y: 5 }, 80);
  p.drawn({ x: 2, y: 5 });
  assert.deepEqual(p.shown, { x: 2, y: 5, text: "ab" });
});

test("backspace removes a letter still in flight, and clears otherwise", () => {
  const p = new Predictor();
  p.typed("abc\x7f", { x: 0, y: 0 }, 80);
  assert.equal(textOf(p), "ab");
  p.typed("\x7f\x7f\x7f", { x: 0, y: 0 }, 80);
  assert.equal(p.shown, undefined);
});

test("Enter, escapes and wide characters are never guessed", () => {
  const p = new Predictor();
  p.typed("ab\r", { x: 0, y: 0 }, 80);
  assert.equal(p.shown, undefined);
  p.typed("\x1b[A", { x: 0, y: 0 }, 80);
  assert.equal(p.shown, undefined);
  p.typed("漢", { x: 0, y: 0 }, 80);
  assert.equal(p.shown, undefined);
  // Guessing resumes once the cursor shows where those keys left it.
  p.drawn({ x: 2, y: 0 });
  p.typed("café", { x: 2, y: 0 }, 80);
  assert.equal(textOf(p), "café");
});

test("no guess runs past the end of the row", () => {
  const p = new Predictor();
  p.typed("abcd", { x: 6, y: 0 }, 10);
  assert.equal(p.shown, undefined);
});

test("an echo that never comes stops guessing until Enter", () => {
  const c = clock();
  const p = new Predictor(c.now, 1000);
  p.typed("s", { x: 10, y: 3 }, 80);
  c.advance(999);
  assert.equal(p.expire(), false);
  c.advance(1);
  assert.equal(p.expire(), true);
  assert.equal(p.shown, undefined);
  // A password prompt: the rest is not drawn either.
  p.typed("ecret", { x: 10, y: 3 }, 80);
  assert.equal(p.shown, undefined);
  p.typed("\r", { x: 10, y: 3 }, 80);
  p.drawn({ x: 2, y: 4 });
  p.typed("ls", { x: 2, y: 4 }, 80);
  assert.equal(textOf(p), "ls");
});

test("after Enter nothing is guessed until the cursor reaches a new row", () => {
  const p = new Predictor();
  p.typed("ab", { x: 2, y: 5 }, 80);
  p.typed("\r", { x: 2, y: 5 }, 80);
  p.typed("ne", { x: 2, y: 5 }, 80);
  assert.equal(p.shown, undefined);
  // The echo of "ab" lands on the old row: still waiting for the new one.
  p.drawn({ x: 4, y: 5 });
  p.typed("x", { x: 4, y: 5 }, 80);
  assert.equal(p.shown, undefined);
  p.drawn({ x: 2, y: 6 });
  p.typed("xt", { x: 2, y: 6 }, 80);
  assert.deepEqual(p.shown, { x: 2, y: 6, text: "xt" });
});

test("after an arrow key guessing waits for the cursor to move", () => {
  const c = clock();
  const p = new Predictor(c.now, 1000);
  p.typed("\x1b[D", { x: 9, y: 1 }, 80);
  p.typed("a", { x: 9, y: 1 }, 80);
  assert.equal(p.shown, undefined);
  p.drawn({ x: 8, y: 1 });
  p.typed("a", { x: 8, y: 1 }, 80);
  assert.equal(textOf(p), "a");
  // An arrow that moves nothing (the start of the line) waits out its patience.
  p.typed("\x1b[D", { x: 0, y: 2 }, 80);
  p.drawn({ x: 0, y: 2 });
  p.typed("b", { x: 0, y: 2 }, 80);
  assert.equal(p.shown, undefined);
  c.advance(1000);
  p.typed("b", { x: 0, y: 2 }, 80);
  assert.equal(textOf(p), "b");
});
