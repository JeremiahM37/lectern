import assert from "node:assert/strict";
import { test } from "node:test";
import {
  conflictsPath,
  isFresh,
  semanticRiskFor,
  validateBody,
  validationOutcome,
  type ConflictPair,
} from "./conflictModel";

const sess = (id: number, title = `s${id}`) => ({ kind: "session" as const, id, title });
const attempt = (id: number) => ({ kind: "attempt" as const, id, title: `t${id}` });

const semantic: ConflictPair = {
  a: sess(12),
  b: sess(14),
  level: "semantic",
  reasons: ["Session #12 changes Parse (internal/x/parse.go); Session #14's new code uses Parse"],
};
const textual: ConflictPair = { a: sess(12), b: sess(20), level: "textual", reasons: ["both change a.go"] };

test("semanticRiskFor names the other side from either position", () => {
  const fromA = semanticRiskFor([semantic], 12);
  assert.equal(fromA?.other.id, 14);
  assert.match(fromA?.reason ?? "", /Parse/);
  const fromB = semanticRiskFor([semantic], 14);
  assert.equal(fromB?.other.id, 12);
});

test("semanticRiskFor ignores textual overlap and unrelated sessions", () => {
  assert.equal(semanticRiskFor([textual], 12), null);
  assert.equal(semanticRiskFor([semantic], 99), null);
  assert.equal(semanticRiskFor([], 12), null);
});

test("semanticRiskFor skips a pair with no reasons rather than crashing", () => {
  const r = semanticRiskFor([{ ...semantic, reasons: [] }], 12);
  assert.equal(r?.reason, "");
});

test("validateBody addresses an attempt by attempt_id and a session by session_id", () => {
  assert.deepEqual(validateBody(12, sess(14)), { a: { session_id: 12 }, b: { session_id: 14 } });
  assert.deepEqual(validateBody(12, attempt(7)), { a: { session_id: 12 }, b: { attempt_id: 7 } });
});

test("validationOutcome shows a textual conflict before any check status", () => {
  assert.equal(
    validationOutcome({ applied: false, textual_conflict: true, check_status: "skipped", output_tail: "" }),
    "textual",
  );
  assert.equal(
    validationOutcome({ applied: true, textual_conflict: false, check_status: "failed", output_tail: "" }),
    "failed",
  );
  assert.equal(
    validationOutcome({ applied: true, textual_conflict: false, check_status: "passed", output_tail: "" }),
    "passed",
  );
});

test("conflictsPath needs a real project id", () => {
  assert.equal(conflictsPath(3), "/conflicts?project_id=3");
  assert.equal(conflictsPath(null), null);
  assert.equal(conflictsPath(undefined), null);
  assert.equal(conflictsPath(0), null);
});

test("isFresh honours the cache window", () => {
  assert.equal(isFresh(1_000, 1_000 + 29_999), true);
  assert.equal(isFresh(1_000, 1_000 + 30_000), false);
  assert.equal(isFresh(1_000, 1_050, 100), true);
  assert.equal(isFresh(1_000, 1_500, 100), false);
});
