import assert from "node:assert/strict";
import { test } from "node:test";
import { isThisDevice, pushAvailability, shortEndpoint, type PushEnv } from "./push";

const FULL: PushEnv = {
  isSecureContext: true,
  hasServiceWorker: true,
  hasPushManager: true,
  hasNotification: true,
  isIOS: false,
  isStandalone: false,
};

test("a fully-supported secure-context browser is available", () => {
  assert.deepEqual(pushAvailability(FULL), { available: true });
});

test("an insecure origin is the first thing reported, even on iOS", () => {
  const got = pushAvailability({ ...FULL, isSecureContext: false, isIOS: true });
  assert.equal(got.available, false);
  assert.match(got.reason!, /https/i);
});

test("iOS Safari not installed to the Home Screen gets its own explanation", () => {
  const got = pushAvailability({ ...FULL, isIOS: true, isStandalone: false });
  assert.equal(got.available, false);
  assert.match(got.reason!, /home screen/i);
  assert.equal(got.reasonKind, "ios-not-installed");
});

test("reasonKind tells apart the three ways push can be unavailable", () => {
  assert.equal(pushAvailability({ ...FULL, isSecureContext: false }).reasonKind, "insecure");
  assert.equal(
    pushAvailability({ ...FULL, isIOS: true, isStandalone: false }).reasonKind,
    "ios-not-installed",
  );
  assert.equal(pushAvailability({ ...FULL, hasPushManager: false }).reasonKind, "unsupported");
});

test("an installed iOS PWA (standalone) is treated as available", () => {
  assert.deepEqual(pushAvailability({ ...FULL, isIOS: true, isStandalone: true }), {
    available: true,
  });
});

test("a browser secure but missing PushManager/serviceWorker/Notification is unsupported", () => {
  for (const key of ["hasServiceWorker", "hasPushManager", "hasNotification"] as const) {
    const got = pushAvailability({ ...FULL, [key]: false });
    assert.equal(got.available, false, key);
    assert.match(got.reason!, /does not support/i);
  }
});

test("shortEndpoint keeps the host and a short tail, never the full opaque URL", () => {
  const short = shortEndpoint("https://fcm.googleapis.com/fcm/send/abcdefghijklmnopqrstuvwxyz0123456789");
  assert.match(short, /^fcm\.googleapis\.com\/…/);
  assert.ok(short.length < 40, short);
});

test("shortEndpoint degrades gracefully for a non-URL string", () => {
  assert.equal(shortEndpoint("not-a-url"), "not-a-url");
});

test("isThisDevice matches only the current browser's own subscription endpoint", () => {
  assert.equal(isThisDevice("https://a", "https://a"), true);
  assert.equal(isThisDevice("https://a", "https://b"), false);
  assert.equal(isThisDevice("https://a", null), false);
});
