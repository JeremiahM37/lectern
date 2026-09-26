import test from "node:test";
import assert from "node:assert/strict";
import { ed25519 } from "@noble/curves/ed25519.js";
import { SHELL_LABEL, verifyManifest, sha256Hex } from "./shell";
import { b64url } from "./noise";

function sign(manifest: object, secret: Uint8Array) {
  const bytes = new TextEncoder().encode(JSON.stringify(manifest));
  const label = new TextEncoder().encode(SHELL_LABEL);
  const msg = new Uint8Array([...label, ...bytes]);
  return { manifest: b64url(bytes), sig: b64url(ed25519.sign(msg, secret)) };
}

test("a manifest signed by the pinned key is accepted, anything else is not", () => {
  const host = ed25519.utils.randomSecretKey();
  const other = ed25519.utils.randomSecretKey();
  const pinned = b64url(ed25519.getPublicKey(host));
  const files = { "/": "ab", "/react/assets/app.js": "cd" };
  assert.deepEqual(verifyManifest(sign({ v: 1, files }, host), pinned), files);
  // Signed by another key (the relay, a tunnel, anyone): refused.
  assert.equal(verifyManifest(sign({ v: 1, files }, other), pinned), null);
  // Altered after signing: refused.
  const signed = sign({ v: 1, files }, host);
  const tampered = { ...signed, manifest: b64url(new TextEncoder().encode(JSON.stringify({ v: 1, files: { ...files, "/": "ee" } }))) };
  assert.equal(verifyManifest(tampered, pinned), null);
  assert.equal(verifyManifest({ manifest: "!!", sig: "!!" }, pinned), null);
});

test("sha256Hex", async () => {
  assert.equal(await sha256Hex(new TextEncoder().encode("abc").buffer as ArrayBuffer),
    "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
});
