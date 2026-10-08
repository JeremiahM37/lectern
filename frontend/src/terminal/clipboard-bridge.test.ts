import assert from "node:assert/strict";
import { test } from "node:test";
import { clientId, listenUrl, mirrorEligible, newClientId, throttle } from "./clipboard-bridge";

test("mirror eligibility by mime and size", () => {
  assert.equal(mirrorEligible({ type: "image/png", size: 10 }), true);
  assert.equal(mirrorEligible({ type: "IMAGE/JPEG", size: 10 }), true);
  assert.equal(mirrorEligible({ type: "image/svg+xml", size: 10 }), false);
  assert.equal(mirrorEligible({ type: "text/plain", size: 10 }), false);
  assert.equal(mirrorEligible({ type: "image/png", size: 0 }), false);
  assert.equal(mirrorEligible({ type: "image/png", size: 20 * 1024 * 1024 }), true);
  assert.equal(mirrorEligible({ type: "image/png", size: 20 * 1024 * 1024 + 1 }), false);
});

test("client ids are valid and stable per storage", () => {
  assert.match(newClientId(), /^[A-Za-z0-9_.-]{4,64}$/);
  const data = new Map<string, string>();
  const storage = { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
  assert.equal(clientId(storage), clientId(storage));
  const broken = { getItem: () => { throw new Error("no"); }, setItem: () => { throw new Error("no"); } };
  assert.match(clientId(broken), /^[A-Za-z0-9_.-]{4,64}$/);
});

test("listen url registers a web client that cannot read", () => {
  assert.equal(listenUrl("web-abc1", 7), "/api/clipboard/listen?client=web-abc1&session=7&kind=web&can_read=0");
});

test("throttle allows one call per interval", () => {
  let t = 1000;
  const ok = throttle(2000, () => t);
  assert.equal(ok(), true);
  t += 1999;
  assert.equal(ok(), false);
  t += 1;
  assert.equal(ok(), true);
});
