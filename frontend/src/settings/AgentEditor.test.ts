import { test } from "node:test";
import assert from "node:assert/strict";
import { targetHasBinary } from "./AgentEditor";
import type { Target } from "../types";

function target(info: Record<string, unknown> | null): Target {
  return {
    id: 1,
    name: "t",
    kind: "local",
    status: "online",
    info_json: info ? JSON.stringify(info) : "",
  } as unknown as Target;
}

test("targetHasBinary is true when any target's probe found the binary", () => {
  assert.equal(
    targetHasBinary([target({ npx: null }), target({ npx: "10.0.0" })], "npx"),
    true,
  );
});

test("targetHasBinary is false when every probed target lacks the binary", () => {
  assert.equal(targetHasBinary([target({ npx: null })], "npx"), false);
});

test("targetHasBinary is undefined (unknown) rather than false when nothing has probed for the key", () => {
  assert.equal(targetHasBinary([], "npx"), undefined);
  assert.equal(targetHasBinary([target(null)], "npx"), undefined);
  assert.equal(targetHasBinary([target({ git: "2.43" })], "npx"), undefined);
});
