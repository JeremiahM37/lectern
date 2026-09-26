// Noise_IK_25519_ChaChaPoly_SHA256, initiator side only: the phone's half of
// the relay handshake (docs/relay.md). The primitives come from the audited
// noble libraries; this file is only the Noise state machine from the spec
// (noiseprotocol.org, revision 34), kept small enough to read in one sitting.
// noise.test.ts checks it byte for byte against vectors produced by the Go
// host's implementation (github.com/flynn/noise).
import { x25519 } from "@noble/curves/ed25519.js";
import { chacha20poly1305 } from "@noble/ciphers/chacha.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { hmac } from "@noble/hashes/hmac.js";

const PROTOCOL = new TextEncoder().encode("Noise_IK_25519_ChaChaPoly_SHA256");
const DHLEN = 32;
const MAX_NONCE = 2n ** 64n - 1n;

export interface KeyPair {
  secretKey: Uint8Array;
  publicKey: Uint8Array;
}

export function generateKeyPair(): KeyPair {
  const secretKey = x25519.utils.randomSecretKey();
  return { secretKey, publicKey: x25519.getPublicKey(secretKey) };
}

export function keyPairFromSecret(secretKey: Uint8Array): KeyPair {
  return { secretKey, publicKey: x25519.getPublicKey(secretKey) };
}

function concat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

function dh(secret: Uint8Array, pub: Uint8Array): Uint8Array {
  const shared = x25519.getSharedSecret(secret, pub);
  // A low-order public key gives an all-zero result; refuse it like the Go
  // side does.
  if (shared.every((b) => b === 0)) throw new Error("noise: invalid public key");
  return shared;
}

function hkdf2(ck: Uint8Array, ikm: Uint8Array): [Uint8Array, Uint8Array] {
  const temp = hmac(sha256, ck, ikm);
  const out1 = hmac(sha256, temp, Uint8Array.of(1));
  const out2 = hmac(sha256, temp, concat(out1, Uint8Array.of(2)));
  return [out1, out2];
}

function nonceBytes(n: bigint): Uint8Array {
  const out = new Uint8Array(12);
  new DataView(out.buffer).setBigUint64(4, n, true);
  return out;
}

/** One direction of an established transport. Nonces are implicit
 * counters: every message must be opened in order, exactly once. */
export class CipherState {
  private n = 0n;
  private failed = false;
  constructor(private readonly key: Uint8Array) {}

  seal(plaintext: Uint8Array, ad: Uint8Array = new Uint8Array()): Uint8Array {
    if (this.n >= MAX_NONCE) throw new Error("noise: nonce exhausted");
    const out = chacha20poly1305(this.key, nonceBytes(this.n), ad).encrypt(plaintext);
    this.n++;
    return out;
  }

  /** Any failure is final: a replayed, reordered or tampered message poisons
   * the state, so the caller must drop the connection. */
  open(ciphertext: Uint8Array, ad: Uint8Array = new Uint8Array()): Uint8Array {
    if (this.failed) throw new Error("noise: session failed");
    try {
      const out = chacha20poly1305(this.key, nonceBytes(this.n), ad).decrypt(ciphertext);
      this.n++;
      return out;
    } catch {
      this.failed = true;
      throw new Error("noise: message failed authentication");
    }
  }
}

class SymmetricState {
  h: Uint8Array;
  ck: Uint8Array;
  k: Uint8Array | null = null;
  n = 0n;

  constructor() {
    // The protocol name is exactly HASHLEN (32) bytes, so it is used as is.
    this.h = PROTOCOL.slice();
    this.ck = PROTOCOL.slice();
  }

  mixHash(data: Uint8Array) {
    this.h = sha256(concat(this.h, data));
  }

  mixKey(ikm: Uint8Array) {
    const [ck, k] = hkdf2(this.ck, ikm);
    this.ck = ck;
    this.k = k;
    this.n = 0n;
  }

  encryptAndHash(plaintext: Uint8Array): Uint8Array {
    let out = plaintext;
    if (this.k) {
      out = chacha20poly1305(this.k, nonceBytes(this.n), this.h).encrypt(plaintext);
      this.n++;
    }
    this.mixHash(out);
    return out;
  }

  decryptAndHash(ciphertext: Uint8Array): Uint8Array {
    let out = ciphertext;
    if (this.k) {
      out = chacha20poly1305(this.k, nonceBytes(this.n), this.h).decrypt(ciphertext);
      this.n++;
    }
    this.mixHash(ciphertext);
    return out;
  }

  split(): [CipherState, CipherState] {
    const [k1, k2] = hkdf2(this.ck, new Uint8Array());
    return [new CipherState(k1), new CipherState(k2)];
  }
}

export interface Transport {
  /** Encrypts phone → host. */
  send: CipherState;
  /** Decrypts host → phone. */
  receive: CipherState;
}

/** The phone's side of Noise IK. */
export class Initiator {
  private readonly ss = new SymmetricState();
  private readonly e: KeyPair;
  private written = false;

  constructor(
    private readonly s: KeyPair,
    private readonly rs: Uint8Array,
    prologue: Uint8Array,
    ephemeral?: KeyPair, // tests pin it to reproduce vectors
  ) {
    if (rs.length !== DHLEN) throw new Error("noise: host key must be 32 bytes");
    this.e = ephemeral ?? generateKeyPair();
    this.ss.mixHash(prologue);
    this.ss.mixHash(rs); // pre-message: <- s
  }

  /** -> e, es, s, ss, payload */
  writeMessage1(payload: Uint8Array): Uint8Array {
    if (this.written) throw new Error("noise: message 1 already written");
    this.written = true;
    const ss = this.ss;
    ss.mixHash(this.e.publicKey);
    ss.mixKey(dh(this.e.secretKey, this.rs));
    const encS = ss.encryptAndHash(this.s.publicKey);
    ss.mixKey(dh(this.s.secretKey, this.rs));
    const encPayload = ss.encryptAndHash(payload);
    return concat(this.e.publicKey, encS, encPayload);
  }

  /** <- e, ee, se, payload. Throws if the reply was not made by the host
   * whose key was pinned. */
  readMessage2(message: Uint8Array): { payload: Uint8Array; transport: Transport } {
    if (!this.written || message.length < DHLEN + 16) throw new Error("noise: bad message 2");
    const ss = this.ss;
    const re = message.slice(0, DHLEN);
    ss.mixHash(re);
    ss.mixKey(dh(this.e.secretKey, re));
    ss.mixKey(dh(this.s.secretKey, re));
    let payload: Uint8Array;
    try {
      payload = ss.decryptAndHash(message.slice(DHLEN));
    } catch {
      throw new Error("noise: message 2 failed authentication");
    }
    const [send, receive] = ss.split();
    return { payload, transport: { send, receive } };
  }
}

export function prologue(channel: string): Uint8Array {
  return new TextEncoder().encode("lectern-relay-v1\0" + channel);
}

export function b64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function unb64url(text: string): Uint8Array {
  const s = atob(text.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((text.length + 3) % 4));
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}
