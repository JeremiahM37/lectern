import test from "node:test";
import assert from "node:assert/strict";
import { encodeFrame, decodeFrame, Frame } from "./frames";
import { tunnelled } from "./paths";
import { parsePairFragment, fingerprint } from "./RelayPair";
import { b64url } from "./noise";

test("frames match the Go layout and padding", () => {
  const enc = encodeFrame(Frame.ResponseBody, 42, Uint8Array.of(1, 2, 3));
  assert.equal(enc.length, 256);
  assert.deepEqual(Array.from(enc.slice(0, 12)), [5, 0, 0, 0, 42, 0, 0, 0, 3, 1, 2, 3]);
  const f = decodeFrame(enc);
  assert.equal(f.type, Frame.ResponseBody);
  assert.equal(f.stream, 42);
  assert.deepEqual(Array.from(f.payload), [1, 2, 3]);
  assert.equal(encodeFrame(Frame.Request, 1, new Uint8Array(248)).length, 512);
  const bad = enc.slice();
  bad[255] = 1;
  assert.throws(() => decodeFrame(bad), /malformed/);
  assert.throws(() => decodeFrame(enc.slice(0, 100)), /malformed/);
});

test("only this Lectern's own API, terminal and A2A paths are tunnelled", () => {
  const base = "https://phone.example:8443/board";
  for (const u of ["/api/tasks", "/api", "/term/session/3/ws", "wss://phone.example:8443/term/session/3/ws", "/a2a/v1"]) {
    assert.ok(tunnelled(u, base), u);
  }
  for (const u of ["/", "/react/assets/app.js", "/apix", "https://other.example/api/tasks", "wss://phone.example:9999/api/x", "/shell-manifest.json"]) {
    assert.ok(!tunnelled(u, base), u);
  }
});

test("the pairing fragment is parsed and validated", () => {
  const payload = { v: 1, relay: "wss://relay.example", ch: "c", hk: "h", sk: "s", c: "code", rt: "route" };
  const frag = "#p=" + b64url(new TextEncoder().encode(JSON.stringify(payload)));
  assert.deepEqual(parsePairFragment(frag), payload);
  assert.equal(parsePairFragment("#p=%%%"), undefined);
  assert.equal(parsePairFragment(""), undefined);
  const http = { ...payload, relay: "javascript:alert(1)" };
  assert.equal(parsePairFragment("#p=" + b64url(new TextEncoder().encode(JSON.stringify(http)))), undefined);
});

test("fingerprints match internal/relay.Fingerprint", () => {
  assert.equal(fingerprint("D6poTtKIZ7l_Smot7l34zpdOdrcBjj8iocTPJnhXDyA"), "65cf 5c9b 1de5 d41f");
});
