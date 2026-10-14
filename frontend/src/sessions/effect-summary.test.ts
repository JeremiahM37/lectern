import assert from "node:assert/strict";
import { test } from "node:test";
import type { EffectCheck } from "../types";
import { effectClaims, summarizeEffect } from "./effect-summary";

function check(verified: number, contradicted: number, unknown: number): EffectCheck {
  return {
    id: 1, session_id: 1, attempt_id: null, created_at: 0, results: [],
    verified, contradicted, unknown, enforced: 0,
  };
}

test("a contradicted claim makes the badge red, whatever else verified", () => {
  assert.equal(summarizeEffect(check(3, 1, 0)).tone, "contradicted");
  assert.equal(summarizeEffect(check(0, 1, 2)).tone, "contradicted");
});

test("only verified claims make the badge green", () => {
  const s = summarizeEffect(check(3, 0, 0));
  assert.equal(s.tone, "verified");
  assert.equal(s.verified, 3);
});

test("unknown only is grey, not green", () => {
  assert.equal(summarizeEffect(check(0, 0, 2)).tone, "unknown");
});

test("no evaluation, or one with no claims, has nothing to show", () => {
  assert.equal(summarizeEffect(null).tone, "none");
  assert.equal(summarizeEffect(undefined).tone, "none");
  assert.equal(summarizeEffect(check(0, 0, 0)).tone, "none");
});

test("claims tolerate a missing or malformed results list", () => {
  assert.deepEqual(effectClaims(null), []);
  const bad = { ...check(1, 0, 0), results: "oops" as unknown as EffectCheck["results"] };
  assert.deepEqual(effectClaims(bad), []);
  const mixed = {
    ...check(1, 0, 0),
    results: [
      { kind: "pushed", sentence: "Pushed.", status: "verified" as const, evidence: "ok" },
      null as unknown as EffectCheck["results"][number],
    ],
  };
  assert.equal(effectClaims(mixed).length, 1);
});
