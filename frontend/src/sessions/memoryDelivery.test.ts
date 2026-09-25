import test from "node:test";
import assert from "node:assert/strict";
import {
  deliveryItems,
  deliverySummary,
  formatBytes,
  itemCountLabel,
  itemLabel,
  itemPath,
  modeLabel,
  type MemoryDelivery,
} from "./memoryDelivery";

test("formatBytes keeps small sizes exact and large ones readable", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(null), "0 B");
  assert.equal(formatBytes(undefined), "0 B");
  assert.equal(formatBytes(NaN), "0 B");
  assert.equal(formatBytes(-5), "0 B");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1023), "1023 B");
  assert.equal(formatBytes(2400), "2.3 KB");
  assert.equal(formatBytes(24000), "23 KB");
  assert.equal(formatBytes(2400000), "2.3 MB");
});

test("itemLabel prefers title, then source, then id, never nothing", () => {
  assert.equal(itemLabel({ title: "Kestrel", source: "memory/k.md", id: "a" }), "Kestrel");
  assert.equal(itemLabel({ source: "memory/k.md", id: "a" }), "memory/k.md");
  assert.equal(itemLabel({ id: "memory/k.md#1" }), "memory/k.md#1");
  assert.equal(itemLabel({}), "untitled memory");
  assert.equal(itemLabel({ title: "   " }), "untitled memory");
});

test("modeLabel spells out what the agent could read and passes through the unknown", () => {
  assert.equal(modeLabel("scoped"), "this project's notes");
  assert.equal(modeLabel("all"), "the whole store");
  assert.equal(modeLabel("something-new"), "something-new");
  assert.equal(modeLabel(""), "");
  assert.equal(modeLabel("  "), "");
});

test("deliverySummary drops empty parts instead of a dangling separator", () => {
  const base: MemoryDelivery = {
    id: 1, session_id: 2, task_id: null, attempt_id: null, at: 0, mode: "scoped", bytes: 2400,
    items: [{ id: "a" }, { id: "b" }, { id: "c" }],
  };
  assert.equal(deliverySummary(base), "3 items · 2.3 KB · this project's notes");
  assert.equal(deliverySummary({ ...base, items: [], bytes: 0, mode: "" }), "no items · 0 B");
  assert.equal(deliverySummary({ ...base, items: [{}] }), "1 item · 2.3 KB · this project's notes");
});

test("itemCountLabel counts", () => {
  assert.equal(itemCountLabel(0), "no items");
  assert.equal(itemCountLabel(1), "1 item");
  assert.equal(itemCountLabel(4), "4 items");
});

test("deliveryItems keeps each item with the delivery it arrived in", () => {
  const a: MemoryDelivery = { id: 1, session_id: 1, task_id: null, attempt_id: null, at: 0, mode: "", bytes: 0, items: [{ id: "a1" }] };
  const b: MemoryDelivery = { id: 2, session_id: 1, task_id: null, attempt_id: null, at: 0, mode: "", bytes: 0, items: [{ id: "b1" }, { id: "b2" }] };
  const flat = deliveryItems([a, b]);
  assert.deepEqual(
    flat.map(({ delivery, item }) => `${delivery.id}:${item.id ?? ""}`),
    ["1:a1", "2:b1", "2:b2"],
  );
  assert.deepEqual(deliveryItems([{ ...a, items: [] }]), []);
});

test("itemPath encodes an id a store may have salted with a path or fingerprint", () => {
  assert.equal(itemPath("kestrel-1", "feedback"), "/memory/items/kestrel-1/feedback");
  assert.equal(itemPath("memory/kestrel.md#1", "challenge"), "/memory/items/memory%2Fkestrel.md%231/challenge");
});
