import test from "node:test";
import assert from "node:assert/strict";
import { groupCode } from "./Devices";

test("groupCode hyphenates a raw code into 4-character blocks", () => {
  assert.equal(
    groupCode("a1b2c3d4e5f60718293a4b5c6d7e8f90"),
    "A1B2-C3D4-E5F6-0718-293A-4B5C-6D7E-8F90",
  );
});

test("groupCode uppercases", () => {
  assert.equal(groupCode("abcd"), "ABCD");
});
