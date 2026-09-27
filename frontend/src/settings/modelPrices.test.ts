import test from "node:test";
import assert from "node:assert/strict";
import { rowsFromPrices, unpricedNotice, validatePrices } from "./modelPrices";

test("validatePrices builds the PUT body and keeps cached optional", () => {
  const { prices, error } = validatePrices([
    { name: " codex ", input: "1.25", cached: "0.125", output: "10" },
    { name: "gpt-6", input: "2", cached: "", output: "8" },
  ]);
  assert.equal(error, undefined);
  assert.deepEqual(prices, {
    codex: {
      input_per_1m: 1.25,
      output_per_1m: 10,
      cached_input_per_1m: 0.125,
    },
    "gpt-6": { input_per_1m: 2, output_per_1m: 8 },
  });
});

test("validatePrices rejects negative, missing, duplicate and unnamed rows", () => {
  assert.match(
    validatePrices([{ name: "m", input: "-1", cached: "", output: "1" }])
      .error ?? "",
    /0 or more/,
  );
  assert.match(
    validatePrices([{ name: "m", input: "1", cached: "-0.1", output: "1" }])
      .error ?? "",
    /0 or more/,
  );
  assert.match(
    validatePrices([{ name: "m", input: "", cached: "", output: "1" }]).error ??
      "",
    /input and output/,
  );
  assert.match(
    validatePrices([{ name: "", input: "1", cached: "", output: "1" }]).error ??
      "",
    /name/,
  );
  const dup = { name: "m", input: "1", cached: "", output: "1" };
  assert.match(validatePrices([dup, dup]).error ?? "", /twice/);
});

test("rowsFromPrices round-trips through validatePrices", () => {
  const prices = {
    b: { input_per_1m: 1, output_per_1m: 2 },
    a: { input_per_1m: 3, output_per_1m: 4, cached_input_per_1m: 0.5 },
  };
  const rows = rowsFromPrices(prices);
  assert.deepEqual(
    rows.map((r) => r.name),
    ["a", "b"],
  );
  assert.deepEqual(validatePrices(rows).prices, prices);
});

test("unpricedNotice names only unpriced models, per agent", () => {
  assert.equal(
    unpricedNotice([
      { agent: "codex", model: "gpt-5-codex", tokens: 5, priced: true },
    ]),
    "",
  );
  assert.equal(
    unpricedNotice([
      { agent: "codex", model: "gpt-5-codex", tokens: 5, priced: false },
      { agent: "codex", model: "", tokens: 1, priced: false },
    ]),
    "Codex spend isn't shown until you set a price for gpt-5-codex, codex.",
  );
});
