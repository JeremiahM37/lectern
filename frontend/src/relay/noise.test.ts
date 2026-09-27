import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { x25519 } from "@noble/curves/ed25519.js";
import { Initiator, generateKeyPair, keyPairFromSecret, prologue, b64url, unb64url } from "./noise";

// Produced by internal/relay/vectors_test.go with github.com/flynn/noise.
interface Vectors {
  channel: string; device_static: string; device_ephemeral: string; host_public: string;
  hello: string; welcome: string; message1: string; message2: string;
  to_host: string[]; to_host_ciphertext: string[]; to_device: string[]; to_device_ciphertext: string[];
}
const vectors = JSON.parse(readFileSync(new URL("./testdata/noise-vectors.json", import.meta.url), "utf8")) as Vectors;
const hex = (s: string) => Uint8Array.from(s.match(/../g)!.map((b) => parseInt(b, 16)));
const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
const utf8 = (s: string) => new TextEncoder().encode(s);

function vectorInitiator() {
  return new Initiator(
    keyPairFromSecret(hex(vectors.device_static)),
    hex(vectors.host_public),
    prologue(vectors.channel),
    keyPairFromSecret(hex(vectors.device_ephemeral)),
  );
}

test("the browser handshake matches the Go host byte for byte", () => {
  const ini = vectorInitiator();
  assert.equal(toHex(ini.writeMessage1(utf8(vectors.hello))), vectors.message1);
  const { payload, transport } = ini.readMessage2(hex(vectors.message2));
  assert.equal(new TextDecoder().decode(payload), vectors.welcome);
  vectors.to_host.forEach((p, i) => {
    assert.equal(toHex(transport.send.seal(utf8(p))), vectors.to_host_ciphertext[i]!);
  });
  vectors.to_device.forEach((p, i) => {
    assert.equal(new TextDecoder().decode(transport.receive.open(hex(vectors.to_device_ciphertext[i]!))), p);
  });
});

test("a reply from any key but the pinned host is refused", () => {
  const ini = vectorInitiator();
  ini.writeMessage1(utf8(vectors.hello));
  const forged = hex(vectors.message2);
  forged[forged.length - 1]! ^= 1;
  assert.throws(() => ini.readMessage2(forged), /authentication/);
});

test("replayed and tampered transport messages poison the session", () => {
  const ini = vectorInitiator();
  ini.writeMessage1(utf8(vectors.hello));
  const { transport } = ini.readMessage2(hex(vectors.message2));
  const ct = hex(vectors.to_device_ciphertext[0]!);
  transport.receive.open(ct);
  assert.throws(() => transport.receive.open(ct), /authentication/); // replay
  assert.throws(() => transport.receive.open(ct), /session failed/); // and it stays dead

  const ini2 = vectorInitiator();
  ini2.writeMessage1(utf8(vectors.hello));
  const t2 = ini2.readMessage2(hex(vectors.message2)).transport;
  const bad = ct.slice();
  bad[0]! ^= 0x80;
  assert.throws(() => t2.receive.open(bad), /authentication/);
});

test("fresh key pairs and base64url round trip", () => {
  const a = generateKeyPair();
  assert.equal(a.publicKey.length, 32);
  for (const n of [0, 1, 2, 3, 31, 32, 33]) {
    const b = Uint8Array.from({ length: n }, (_, i) => (i * 37) & 255);
    assert.deepEqual(unb64url(b64url(b)), b);
  }
  assert.ok(!/[+/=]/.test(b64url(a.publicKey)));
});

test("a key held elsewhere (Android Keystore) gives the same handshake through its DH", () => {
  const secret = hex(vectors.device_static);
  const pair = keyPairFromSecret(secret);
  let calls = 0;
  const held = { publicKey: pair.publicKey, dh: (peer: Uint8Array) => { calls++; return x25519.getSharedSecret(secret, peer); } };
  const ini = new Initiator(held, hex(vectors.host_public), prologue(vectors.channel), keyPairFromSecret(hex(vectors.device_ephemeral)));
  assert.equal(toHex(ini.writeMessage1(utf8(vectors.hello))), vectors.message1);
  assert.equal(new TextDecoder().decode(ini.readMessage2(hex(vectors.message2)).payload), vectors.welcome);
  assert.equal(calls, 2); // ss and se, nothing else touches the static key
});

test("an all-zero DH result from a delegated key is refused", () => {
  const held = { publicKey: new Uint8Array(32).fill(9), dh: () => new Uint8Array(32) };
  const ini = new Initiator(held, hex(vectors.host_public), prologue(vectors.channel));
  assert.throws(() => ini.writeMessage1(utf8("x")), /invalid public key/);
});
