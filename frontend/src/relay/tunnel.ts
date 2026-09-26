// The phone's end of the encrypted relay (docs/relay.md): one WebSocket to
// the relay, one Noise session to the host, and any number of HTTP exchanges
// and WebSockets multiplexed over it. The relay sees only ciphertext.
import { Initiator, keyPairFromSecret, generateKeyPair, prologue, b64url, unb64url, type Transport } from "./noise";
import { Frame, MAX_CHUNK, WS_FINAL, WS_MORE, decodeFrame, encodeFrame, fromJSON, jsonBytes, type TunnelFrame } from "./frames";

// Captured before any shim replaces the global: the tunnel itself always
// talks to the relay with the browser's own WebSocket.
const NativeWebSocket = globalThis.WebSocket;

/** What a paired device keeps, in IndexedDB (store.ts). */
export interface Pairing {
  v: 1;
  relay: string;
  ch: string;
  hk: string; // host X25519 public key, pinned
  sk: string; // shell signing key, pinned
  deviceSecret: string; // this device's X25519 private key
  routeToken: string;
  deviceId: number;
  name: string;
}

/** The QR code's fragment payload (internal/api/relay.go relayPairPayload). */
export interface PairPayload {
  v: number;
  relay: string;
  ch: string;
  hk: string;
  sk: string;
  c: string;
  rt: string;
}

interface Welcome {
  ok: boolean;
  error?: string;
  device_id?: number;
  name?: string;
  route_token?: string;
}

export type TunnelStatus = "idle" | "connecting" | "connected" | "offline" | "revoked";

function deviceURL(relay: string, ch: string): string {
  const base = relay.replace(/\/+$/, "").replace(/^http(s?):/, "ws$1:");
  return `${base}/v1/device?ch=${encodeURIComponent(ch)}`;
}

/** Why a relay connection failed, from its close code (internal/relay/server). */
export function closeReason(code: number): string {
  switch (code) {
    case 4401: return "The relay did not recognise this device.";
    case 4404: return "Lectern is not connected to the relay.";
    case 4408: return "The relay timed out.";
    case 4429: return "The relay is busy; retrying.";
    default: return "The relay connection dropped.";
  }
}

class HandshakeError extends Error {
  constructor(readonly code: string) {
    super(code);
  }
}

interface Conn {
  ws: WebSocket;
  transport: Transport;
  welcome: Welcome;
}

// connect opens the relay socket, authenticates to the relay with the route
// token and runs Noise IK against the pinned host key.
function connect(o: { relay: string; ch: string; hk: string; routeToken: string; secret: Uint8Array; hello: unknown; timeoutMs?: number }): Promise<Conn> {
  return new Promise((resolve, reject) => {
    const ws = new NativeWebSocket(deviceURL(o.relay, o.ch));
    ws.binaryType = "arraybuffer";
    let initiator: Initiator | undefined;
    let settled = false;
    const fail = (err: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try { ws.close(); } catch { /* already closed */ }
      reject(err);
    };
    const timer = setTimeout(() => fail(new Error("The relay did not answer in time.")), o.timeoutMs ?? 20000);
    ws.onopen = () => ws.send(JSON.stringify({ t: "auth", token: o.routeToken }));
    ws.onclose = (ev) => fail(Object.assign(new Error(closeReason(ev.code)), { closeCode: ev.code }));
    ws.onerror = () => { /* onclose follows with the code */ };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        let msg: { t?: string; error?: string } = {};
        try { msg = JSON.parse(ev.data); } catch { /* handled below */ }
        if (msg.t !== "ok" || initiator) return; // an "error" is followed by a close
        initiator = new Initiator(keyPairFromSecret(o.secret), unb64url(o.hk), prologue(o.ch));
        ws.send(initiator.writeMessage1(jsonBytes(o.hello)));
        return;
      }
      if (!initiator) return fail(new Error("relay: unexpected data before the handshake"));
      try {
        const { payload, transport } = initiator.readMessage2(new Uint8Array(ev.data as ArrayBuffer));
        const welcome = fromJSON<Welcome>(payload);
        if (!welcome.ok) return fail(new HandshakeError(welcome.error || "refused"));
        settled = true;
        clearTimeout(timer);
        ws.onclose = ws.onmessage = null;
        resolve({ ws, transport, welcome });
      } catch (err) {
        fail(err instanceof Error ? err : new Error(String(err)));
      }
    };
  });
}

/** Pairs this device using a scanned QR payload and returns what to store.
 * The pairing code only ever leaves the phone inside Noise message 1,
 * encrypted to the pinned host key. */
export async function pairDevice(payload: PairPayload, name: string): Promise<Pairing> {
  if (payload.v !== 1) throw new Error("This pairing code is from a newer Lectern.");
  const keys = generateKeyPair();
  let conn: Conn;
  try {
    conn = await connect({ relay: payload.relay, ch: payload.ch, hk: payload.hk, routeToken: payload.rt,
      secret: keys.secretKey, hello: { v: 1, pair: { code: payload.c, name } } });
  } catch (err) {
    if (err instanceof HandshakeError && err.code === "invalid_code") throw new Error("That pairing code was already used or has expired.");
    throw err;
  }
  conn.ws.close();
  const w = conn.welcome;
  if (!w.route_token || !w.device_id) throw new Error("Lectern did not complete the pairing.");
  return { v: 1, relay: payload.relay, ch: payload.ch, hk: payload.hk, sk: payload.sk,
    deviceSecret: b64url(keys.secretKey), routeToken: w.route_token, deviceId: w.device_id, name: w.name || name };
}

type StreamHandler = (f: TunnelFrame | null) => void;

/** One long-lived tunnel with reconnection. */
export class RelayTunnel extends EventTarget {
  status: TunnelStatus = "idle";
  detail = "";
  private conn?: Conn;
  private nextStream = 1;
  private readonly streams = new Map<number, StreamHandler>();
  private waiters: (() => void)[] = [];
  private stopped = false;
  private backoff = 1000;
  private refusals = 0;
  private pairing?: Pairing;

  constructor(private readonly load: () => Promise<Pairing | undefined>) {
    super();
  }

  private setStatus(status: TunnelStatus, detail = "") {
    this.status = status;
    this.detail = detail;
    this.dispatchEvent(new Event("status"));
    if (status === "connected") {
      const waiters = this.waiters;
      this.waiters = [];
      waiters.forEach((w) => w());
    }
  }

  async start() {
    this.stopped = false;
    this.pairing = await this.load();
    if (!this.pairing) {
      this.setStatus("revoked", "This device is not paired.");
      return;
    }
    void this.loop();
  }

  stop() {
    this.stopped = true;
    this.conn?.ws.close();
  }

  private async loop() {
    while (!this.stopped && this.pairing) {
      this.setStatus("connecting");
      const p = this.pairing;
      try {
        const conn = await connect({ relay: p.relay, ch: p.ch, hk: p.hk, routeToken: p.routeToken,
          secret: unb64url(p.deviceSecret), hello: { v: 1 } });
        this.backoff = 1000;
        this.refusals = 0;
        await this.run(conn);
      } catch (err) {
        if (err instanceof HandshakeError && (err.code === "unknown_device" || err.code === "version")) {
          this.setStatus("revoked", err.code === "version" ? "This app is older than Lectern; reinstall it." : "Lectern no longer recognises this device. It was revoked or idle too long.");
          return;
        }
        // The relay refuses an unknown route with 4401. The host registers
        // its routes the moment it connects, so a refusal can be momentary
        // at most once; twice in a row means the route was removed.
        if ((err as { closeCode?: number }).closeCode === 4401 && ++this.refusals >= 2) {
          this.setStatus("revoked", "The relay no longer accepts this device. It was probably revoked.");
          return;
        }
        this.setStatus("offline", err instanceof Error ? err.message : String(err));
      }
      if (this.stopped) return;
      await new Promise((r) => setTimeout(r, this.backoff * (0.75 + Math.random() / 2)));
      this.backoff = Math.min(this.backoff * 2, 30000);
    }
  }

  private run(conn: Conn): Promise<void> {
    return new Promise((resolve) => {
      this.conn = conn;
      this.setStatus("connected");
      const end = (why: string) => {
        if (this.conn !== conn) return;
        this.conn = undefined;
        try { conn.ws.close(); } catch { /* closed */ }
        const streams = [...this.streams.values()];
        this.streams.clear();
        streams.forEach((h) => h(null));
        this.setStatus("offline", why);
        resolve();
      };
      conn.ws.onclose = (ev) => end(closeReason(ev.code));
      conn.ws.onmessage = (ev) => {
        if (typeof ev.data === "string") return; // relay notices precede a close
        let frame: TunnelFrame;
        try {
          frame = decodeFrame(conn.transport.receive.open(new Uint8Array(ev.data as ArrayBuffer)));
        } catch {
          // Tampered, replayed or reordered: the session cannot continue.
          end("A message from the relay failed authentication; reconnecting.");
          return;
        }
        this.streams.get(frame.stream)?.(frame);
      };
    });
  }

  /** Resolves once connected; rejects after timeoutMs or on abort. */
  ready(signal?: AbortSignal | null, timeoutMs = 20000): Promise<void> {
    if (this.conn) return Promise.resolve();
    if (this.status === "revoked") return Promise.reject(new TypeError("Lectern relay: " + this.detail));
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new TypeError("Lectern relay: not connected")), timeoutMs);
      const done = () => { clearTimeout(timer); resolve(); };
      this.waiters.push(done);
      signal?.addEventListener("abort", () => { clearTimeout(timer); reject(new DOMException("Aborted", "AbortError")); }, { once: true });
    });
  }

  private send(type: number, stream: number, payload?: Uint8Array) {
    const conn = this.conn;
    if (!conn) throw new TypeError("Lectern relay: not connected");
    conn.ws.send(conn.transport.send.seal(encodeFrame(type, stream, payload)));
  }

  private openStream(handler: StreamHandler): number {
    const id = this.nextStream++;
    this.streams.set(id, handler);
    return id;
  }

  private closeStream(id: number) {
    this.streams.delete(id);
  }

  /** fetch() for this Lectern's own paths, over the tunnel. */
  async fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const request = new Request(input, init);
    await this.ready(request.signal);
    if (request.signal.aborted) throw new DOMException("Aborted", "AbortError");
    const url = new URL(request.url);
    const body = request.method === "GET" || request.method === "HEAD" ? new Uint8Array() : new Uint8Array(await request.arrayBuffer());
    const headers: [string, string][] = [];
    request.headers.forEach((value, name) => headers.push([name, value]));
    return new Promise<Response>((resolve, reject) => {
      let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
      let answered = false;
      let finished = false;
      const id = this.openStream((f) => {
        if (!f) {
          finished = true;
          if (!answered) reject(new TypeError("Lectern relay: connection lost"));
          else controller?.error(new TypeError("Lectern relay: connection lost"));
          return;
        }
        switch (f.type) {
          case Frame.Response: {
            const head = fromJSON<{ s: number; h?: [string, string][] }>(f.payload);
            const h = new Headers();
            for (const [k, v] of head.h || []) {
              try { h.append(k, v); } catch { /* a header the browser forbids here */ }
            }
            const nullBody = [101, 204, 205, 304].includes(head.s) || request.method === "HEAD";
            const stream = nullBody ? null : new ReadableStream<Uint8Array>({
              start: (c) => { controller = c; },
              cancel: () => {
                if (!finished) {
                  finished = true;
                  this.closeStream(id);
                  try { this.send(Frame.Cancel, id); } catch { /* offline */ }
                }
              },
            });
            answered = true;
            resolve(new Response(stream, { status: head.s, headers: h }));
            break;
          }
          case Frame.ResponseBody:
            try { controller?.enqueue(f.payload); } catch { /* consumer went away */ }
            break;
          case Frame.ResponseEnd:
            finished = true;
            this.closeStream(id);
            try { controller?.close(); } catch { /* already closed */ }
            break;
          case Frame.Cancel:
            finished = true;
            this.closeStream(id);
            if (!answered) reject(new TypeError("Lectern relay: request failed"));
            else controller?.error(new TypeError("Lectern relay: response interrupted"));
            break;
        }
      });
      request.signal.addEventListener("abort", () => {
        if (finished) return;
        finished = true;
        this.closeStream(id);
        try { this.send(Frame.Cancel, id); } catch { /* offline */ }
        const abort = new DOMException("Aborted", "AbortError");
        if (!answered) reject(abort);
        else controller?.error(abort);
      }, { once: true });
      try {
        this.send(Frame.Request, id, jsonBytes({ m: request.method, u: url.pathname + url.search, host: location.host, h: headers }));
        for (let at = 0; at < body.length; at += MAX_CHUNK) this.send(Frame.RequestBody, id, body.subarray(at, at + MAX_CHUNK));
        this.send(Frame.RequestEnd, id);
      } catch (err) {
        this.closeStream(id);
        reject(err);
      }
    });
  }

  /** A WebSocket to one of this Lectern's own paths, over the tunnel. */
  socket(url: string, protocols?: string | string[]): TunnelWebSocket {
    return new TunnelWebSocket(this, url, protocols);
  }

  /** @internal used by TunnelWebSocket */
  _open(handler: StreamHandler): number { return this.openStream(handler); }
  /** @internal */
  _send(type: number, stream: number, payload?: Uint8Array) { this.send(type, stream, payload); }
  /** @internal */
  _close(id: number) { this.closeStream(id); }
}

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

/** Enough of the WebSocket interface for Lectern's terminal and anything
 * else that opens a socket to its own origin. */
export class TunnelWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  readyState = 0;
  protocol = "";
  extensions = "";
  bufferedAmount = 0;
  binaryType: BinaryType = "blob";
  readonly url: string;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  private id = 0;
  private partial: Uint8Array[] = [];
  private sendQueue: Promise<void> = Promise.resolve();

  constructor(private readonly tunnel: RelayTunnel, url: string, protocols?: string | string[]) {
    super();
    const u = new URL(url, location.href);
    this.url = u.href;
    const list = protocols === undefined ? [] : Array.isArray(protocols) ? protocols : [protocols];
    void tunnel.ready().then(() => {
      if (this.readyState !== 0) return;
      this.id = tunnel._open((f) => this.onFrame(f));
      tunnel._send(Frame.WSOpen, this.id, jsonBytes({ u: u.pathname + u.search, host: location.host, p: list, h: [["Origin", location.origin]] }));
    }).catch(() => this.finish(1006, "", false, true));
  }

  private fire(ev: Event) {
    this.dispatchEvent(ev);
    const handler = (this as unknown as Record<string, unknown>)["on" + ev.type];
    if (typeof handler === "function") handler.call(this, ev);
  }

  private finish(code: number, reason: string, clean: boolean, error = false) {
    if (this.readyState === 3) return;
    this.readyState = 3;
    if (this.id) this.tunnel._close(this.id);
    if (error) this.fire(new Event("error"));
    this.fire(new CloseEvent("close", { code, reason, wasClean: clean }));
  }

  private onFrame(f: TunnelFrame | null) {
    if (!f) return this.finish(1006, "", false, true);
    switch (f.type) {
      case Frame.WSOpened: {
        this.protocol = fromJSON<{ p?: string }>(f.payload).p || "";
        this.readyState = 1;
        this.fire(new Event("open"));
        break;
      }
      case Frame.WSText:
      case Frame.WSBinary: {
        this.partial.push(f.payload.slice(1));
        if (f.payload[0] !== WS_FINAL) break;
        const total = this.partial.reduce((n, p) => n + p.length, 0);
        const data = new Uint8Array(total);
        let at = 0;
        for (const p of this.partial) { data.set(p, at); at += p.length; }
        this.partial = [];
        const payload = f.type === Frame.WSText ? textDecoder.decode(data)
          : this.binaryType === "arraybuffer" ? data.buffer : new Blob([data]);
        this.fire(new MessageEvent("message", { data: payload, origin: location.origin }));
        break;
      }
      case Frame.WSClose: {
        const c = fromJSON<{ c?: number; r?: string }>(f.payload);
        this.finish(c.c || 1005, c.r || "", true);
        break;
      }
    }
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
    if (this.readyState === 0) throw new DOMException("WebSocket is still connecting", "InvalidStateError");
    if (this.readyState !== 1) return;
    // A Blob must be read asynchronously; queue every send so order holds.
    this.sendQueue = this.sendQueue.then(async () => {
      let type: number = Frame.WSBinary;
      let bytes: Uint8Array;
      if (typeof data === "string") { type = Frame.WSText; bytes = textEncoder.encode(data); }
      else if (data instanceof Blob) bytes = new Uint8Array(await data.arrayBuffer());
      else if (ArrayBuffer.isView(data)) bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
      else bytes = new Uint8Array(data as ArrayBuffer);
      if (this.readyState !== 1) return;
      let at = 0;
      do {
        const chunk = bytes.subarray(at, at + MAX_CHUNK);
        at += chunk.length;
        const flag = at < bytes.length ? WS_MORE : WS_FINAL;
        const payload = new Uint8Array(chunk.length + 1);
        payload[0] = flag;
        payload.set(chunk, 1);
        this.tunnel._send(type, this.id, payload);
      } while (at < bytes.length);
    }).catch(() => this.finish(1006, "", false, true));
  }

  close(code = 1000, reason = "") {
    if (this.readyState >= 2) return;
    if (this.readyState === 0 || !this.id) return this.finish(code, reason, true);
    this.readyState = 2;
    try { this.tunnel._send(Frame.WSClose, this.id, jsonBytes({ c: code, r: reason })); } catch { /* offline */ }
    setTimeout(() => this.finish(code, reason, true), 1500);
  }
}

/** EventSource over the tunnel, following the HTML spec's parsing and
 * reconnection rules closely enough for Lectern's streams. */
export class TunnelEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readyState = 0;
  readonly url: string;
  readonly withCredentials = false;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private abort?: AbortController;
  private retry = 3000;
  private lastId = "";

  constructor(private readonly tunnel: RelayTunnel, url: string | URL) {
    super();
    this.url = new URL(String(url), location.href).href;
    void this.connect();
  }

  private fire(ev: Event) {
    this.dispatchEvent(ev);
    const handler = (this as unknown as Record<string, unknown>)["on" + ev.type];
    if (typeof handler === "function") handler.call(this, ev);
  }

  close() {
    this.readyState = 2;
    this.abort?.abort();
  }

  private async connect() {
    if (this.readyState === 2) return;
    this.abort = new AbortController();
    const headers: Record<string, string> = { Accept: "text/event-stream", "Cache-Control": "no-cache" };
    if (this.lastId) headers["Last-Event-ID"] = this.lastId;
    try {
      const response = await this.tunnel.fetch(this.url, { headers, signal: this.abort.signal });
      if (response.status !== 200 || !response.body) {
        this.readyState = 2;
        this.fire(new Event("error"));
        return;
      }
      this.readyState = 1;
      this.fire(new Event("open"));
      await this.read(response.body);
    } catch {
      /* reconnect below */
    }
    if (this.readyState === 2) return;
    this.readyState = 0;
    this.fire(new Event("error"));
    setTimeout(() => void this.connect(), this.retry);
  }

  private async read(body: ReadableStream<Uint8Array>) {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let data: string[] = [];
    let event = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      buffer += decoder.decode(value, { stream: true });
      let nl: number;
      while ((nl = buffer.search(/\r\n|\r|\n/)) >= 0) {
        const line = buffer.slice(0, nl);
        buffer = buffer.slice(nl + (buffer.startsWith("\r\n", nl) ? 2 : 1));
        if (line === "") {
          if (data.length) {
            this.fire(new MessageEvent(event || "message", { data: data.join("\n"), lastEventId: this.lastId, origin: location.origin }));
          }
          data = [];
          event = "";
          continue;
        }
        if (line.startsWith(":")) continue;
        const colon = line.indexOf(":");
        const field = colon < 0 ? line : line.slice(0, colon);
        let value = colon < 0 ? "" : line.slice(colon + 1);
        if (value.startsWith(" ")) value = value.slice(1);
        if (field === "data") data.push(value);
        else if (field === "event") event = value;
        else if (field === "id" && !value.includes("\0")) this.lastId = value;
        else if (field === "retry" && /^\d+$/.test(value)) this.retry = Number(value);
      }
    }
  }
}
