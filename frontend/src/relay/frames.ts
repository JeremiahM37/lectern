// Tunnel frames carried inside Noise transport messages. Mirrors
// internal/relay/tunnel.go exactly: [type u8][stream u32][length u32]
// [payload][zero padding to a multiple of 256].
export const Frame = {
  Request: 1,
  RequestBody: 2,
  RequestEnd: 3,
  Response: 4,
  ResponseBody: 5,
  ResponseEnd: 6,
  Cancel: 7,
  WSOpen: 8,
  WSOpened: 9,
  WSText: 10,
  WSBinary: 11,
  WSClose: 12,
} as const;

export const MAX_CHUNK = 16 << 10;
export const WS_FINAL = 0;
export const WS_MORE = 1;
const HEADER = 9;
const PAD = 256;
const MAX_PAYLOAD = 60000;

export interface TunnelFrame {
  type: number;
  stream: number;
  payload: Uint8Array;
}

export function encodeFrame(type: number, stream: number, payload: Uint8Array = new Uint8Array()): Uint8Array {
  if (payload.length > MAX_PAYLOAD) throw new Error("relay: frame payload too large");
  const size = Math.ceil((HEADER + payload.length) / PAD) * PAD;
  const out = new Uint8Array(size);
  const view = new DataView(out.buffer);
  out[0] = type;
  view.setUint32(1, stream);
  view.setUint32(5, payload.length);
  out.set(payload, HEADER);
  return out;
}

export function decodeFrame(b: Uint8Array): TunnelFrame {
  if (b.length < HEADER || b.length % PAD !== 0) throw new Error("relay: malformed frame");
  const view = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const n = view.getUint32(5);
  if (n > MAX_PAYLOAD || n > b.length - HEADER) throw new Error("relay: malformed frame");
  for (let i = HEADER + n; i < b.length; i++) if (b[i] !== 0) throw new Error("relay: malformed frame");
  const type = b[0]!;
  if (type < Frame.Request || type > Frame.WSClose) throw new Error("relay: malformed frame");
  return { type, stream: view.getUint32(1), payload: b.slice(HEADER, HEADER + n) };
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();
export const jsonBytes = (v: unknown) => encoder.encode(JSON.stringify(v));
export const fromJSON = <T>(b: Uint8Array): T => JSON.parse(decoder.decode(b)) as T;
