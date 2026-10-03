// The web side of mods (docs/mods.md): loads each enabled mod into its own
// sandboxed iframe, runs the handler chains, and does what a mod asks of `$`.
// Mods never touch the page; everything they draw is data that React renders.

import { ApiError, createClient, setRequestInterceptor, type RequestOptions } from "../api/client";
import { sandboxDocument } from "./sandbox";

export type ModInfo = { plugin: string; id: string; name: string; api?: string; hash: string; script?: string; error?: string };
export type ModElement = { type: string; props: Record<string, unknown> };
export type RenderResult = { hidden: boolean; append: ModElement[] };
type Matcher = { eq: string } | { re: string; flags: string } | null;
type Handler = { hid: number; event: string; matcher: Matcher };
type FnRef = { __fn: number; __mod?: string };

export type ModStatus = {
  key: string;
  plugin: string;
  id: string;
  name: string;
  state: "loading" | "running" | "paused" | "failed";
  error?: string;
  log: string[];
};

const HANDLER_MS = 2000;
const RENDER_MS = 100;
const FAILS = 5;
const STATE_BYTES = 256 * 1024;
export const MATCHED_EVENT: Record<string, (e: Record<string, unknown>) => string> = {
  "ui.render": (e) => String(e.component ?? ""),
  "command.run": (e) => String(e.id ?? ""),
  "server.event": (e) => String(e.type ?? ""),
};

// Paths a mod may never reach, whatever its api capability: installing or
// trusting plugins, deciding approvals, sign-in, pairing and secrets.
const FORBIDDEN = [
  /^\/api\/(plugins|plugin-sources|auth|pairing|devices|secrets|tokens?|whoami)\b/,
  /^\/api\/approvals\/[^/]+\/decision\b/,
  /secret|token|password|credential/i,
];
export function apiAllowed(api: string | undefined, method: string, path: string): string | null {
  if (!api) return "this mod has no api capability";
  if (!path.startsWith("/api/") || path.includes("..")) return "paths start with /api/";
  if (FORBIDDEN.some((re) => re.test(path.split("?")[0]!))) return `${path} is not available to mods`;
  if (method !== "GET" && api !== "write") return "this mod may only read (api: read)";
  return null;
}

class Mod {
  frame?: HTMLIFrameElement;
  port?: MessagePort;
  handlers: Handler[] = [];
  state: ModStatus["state"] = "loading";
  error?: string;
  log: string[] = [];
  fails: number[] = [];
  status: string | null = null;
  commands: { id: string; title: string }[] = [];
  data: Record<string, unknown>;
  waits = new Map<number, (msg: Record<string, unknown>) => void>();
  /** Button references this mod has drawn; another mod cannot forge one. */
  drawn = new Set<number>();
  constructor(readonly info: ModInfo) {
    this.data = readState(this.key);
  }
  get key() { return `${this.info.plugin}/${this.info.id}`; }
  note(line: string) {
    this.log.push(new Date().toLocaleTimeString() + " " + line);
    if (this.log.length > 50) this.log.shift();
  }
}

const stateKey = (key: string) => "lec-mod-state:" + key;
function readState(key: string): Record<string, unknown> {
  try { return JSON.parse(localStorage.getItem(stateKey(key)) || "{}"); } catch { return {}; }
}

let seq = 0;

export class ModHost {
  private mods: Mod[] = [];
  private listeners = new Set<() => void>();
  private version = 0;
  panes: { key: string; id: string; title: string }[] = [];
  notify: (text: string, error?: boolean) => void = () => {};
  private request = createClient({ bypassInterceptors: true });

  constructor(private readonly root: () => HTMLElement = () => document.body) {}

  subscribe = (fn: () => void) => { this.listeners.add(fn); return () => { this.listeners.delete(fn); }; };
  snapshot = () => this.version;
  private changed() { this.version++; for (const fn of this.listeners) fn(); }

  statuses(): ModStatus[] {
    return this.mods.map((m) => ({ key: m.key, plugin: m.info.plugin, id: m.info.id, name: m.info.name, state: m.state, error: m.error, log: [...m.log] }));
  }
  statusTexts(): { key: string; text: string }[] {
    return this.mods.filter((m) => m.status && m.state === "running").map((m) => ({ key: m.key, text: m.status! }));
  }
  commands(): { mod: string; id: string; title: string; name: string }[] {
    return this.mods.filter((m) => m.state === "running").flatMap((m) => m.commands.map((c) => ({ mod: m.key, id: c.id, title: c.title, name: m.info.name })));
  }
  has(event: string) { return this.mods.some((m) => m.state === "running" && m.handlers.some((h) => h.event === event)); }

  /** Starts, keeps or replaces mods so the running set matches list. */
  load(list: ModInfo[]) {
    const want = new Map(list.map((m) => [`${m.plugin}/${m.id}`, m]));
    for (const m of this.mods) {
      const next = want.get(m.key);
      if (!next || next.hash !== m.info.hash || next.script !== m.info.script) this.stop(m);
    }
    const kept = new Map(this.mods.filter((m) => m.frame || m.state === "failed").map((m) => [m.key, m]));
    this.mods = list.map((info) => {
      const key = `${info.plugin}/${info.id}`;
      const old = kept.get(key);
      if (old && old.info.hash === info.hash && old.info.script === info.script) return old;
      const mod = new Mod(info);
      if (info.error || !info.script) {
        mod.state = "failed";
        mod.error = info.error || "no code";
      } else this.start(mod);
      return mod;
    });
    this.panes = this.panes.filter((p) => this.mods.some((m) => m.key === p.key));
    this.changed();
  }

  async refresh() {
    const list = await this.request<ModInfo[]>("/plugins/mods?surface=web");
    this.load(list);
  }

  private stop(m: Mod) {
    m.port?.close();
    m.frame?.remove();
    m.frame = m.port = undefined;
    m.state = "paused";
  }

  private start(m: Mod) {
    const frame = document.createElement("iframe");
    frame.setAttribute("sandbox", "allow-scripts");
    frame.setAttribute("aria-hidden", "true");
    frame.dataset.mod = m.key;
    frame.style.display = "none";
    frame.srcdoc = sandboxDocument();
    frame.addEventListener("load", () => {
      const channel = new MessageChannel();
      m.port = channel.port1;
      m.port.onmessage = (event) => void this.receive(m, event.data);
      frame.contentWindow?.postMessage({ t: "init" }, "*", [channel.port2]);
      m.port.postMessage({ t: "load", script: m.info.script, mod: { plugin: m.info.plugin, id: m.info.id }, state: m.data });
    }, { once: true });
    m.frame = frame;
    this.root().appendChild(frame);
  }

  private async receive(m: Mod, msg: Record<string, any>) {
    switch (msg.t) {
      case "ready":
        m.state = "running";
        this.changed();
        void this.dispatch("app.start", { surface: "web", version: document.documentElement.dataset.version ?? "" }, async () => null, [m]);
        return;
      case "failed":
        m.state = "failed";
        m.error = msg.error;
        m.note("failed to start: " + msg.error);
        this.changed();
        return;
      case "on":
        m.handlers.push({ hid: msg.hid, event: msg.event, matcher: msg.matcher });
        return;
      case "result":
        m.waits.get(msg.rid)?.(msg);
        return;
      case "$":
        return this.dollar(m, msg);
    }
  }

  private async dollar(m: Mod, msg: Record<string, any>) {
    const [a, b, c] = msg.args ?? [];
    const reply = (value: unknown, error?: string) => msg.rid && m.port?.postMessage({ t: "reply", rid: msg.rid, value, error });
    switch (msg.op) {
      case "toast": this.notify(`${m.info.name}: ${a}`); return;
      case "status": m.status = a; this.changed(); return;
      case "open":
        if (!this.panes.some((p) => p.key === m.key && p.id === a.id)) this.panes = [...this.panes, { key: m.key, id: a.id, title: a.title }];
        this.changed();
        return;
      case "close": this.panes = this.panes.filter((p) => !(p.key === m.key && p.id === a)); this.changed(); return;
      case "render": this.changed(); return;
      case "state": {
        const next = { ...m.data, [a]: b };
        const text = JSON.stringify(next);
        if (text.length > STATE_BYTES) { m.note(`state.set(${a}) refused: more than 256 KB`); return; }
        m.data = next;
        try { localStorage.setItem(stateKey(m.key), text); } catch { /* private window: kept for this page */ }
        this.changed();
        return;
      }
      case "command":
        m.commands = [...m.commands.filter((x) => x.id !== a.id), a];
        this.changed();
        return;
      case "navigate": if (typeof a === "string" && a.startsWith("#")) location.hash = a; return;
      case "log": m.note(String(a)); this.changed(); return;
      case "api": {
        const refused = apiAllowed(m.info.api, a, b);
        if (refused) return reply(undefined, refused);
        try {
          reply(await this.request(b.replace(/^\/api/, ""), { method: a, body: c ?? undefined }));
        } catch (e) {
          reply(undefined, e instanceof Error ? e.message : String(e));
        }
        return;
      }
      case "next": {
        const cont = this.continuations.get(a);
        if (!cont) return reply(undefined, "next() was called after this handler finished");
        try { reply(await cont(b)); } catch (e) { reply(undefined, e instanceof Error ? e.message : String(e)); }
        return;
      }
    }
  }

  private continuations = new Map<number, (e: unknown) => Promise<unknown>>();

  private fail(m: Mod, what: string) {
    m.note(what);
    const now = Date.now();
    m.fails = [...m.fails.filter((t) => now - t < 60_000), now];
    if (m.fails.length >= FAILS && m.state === "running") {
      m.state = "paused";
      m.error = `paused after ${FAILS} failures in a minute: ${what}`;
      this.notify(`${m.info.name}: ${m.error}`, true);
    }
    this.changed();
  }

  private chain(event: string, e: Record<string, unknown>, only?: Mod[]) {
    const subject = MATCHED_EVENT[event]?.(e);
    const out: { m: Mod; h: Handler }[] = [];
    for (const m of only ?? this.mods) {
      if (m.state !== "running") continue;
      for (const h of m.handlers) {
        if (h.event !== event) continue;
        if (h.matcher && "eq" in h.matcher && h.matcher.eq !== subject) continue;
        if (h.matcher && "re" in h.matcher) {
          try { if (!new RegExp(h.matcher.re, h.matcher.flags).test(subject ?? "")) continue; } catch { continue; }
        }
        out.push({ m, h });
      }
    }
    return out;
  }

  /**
   * Runs event through every matching handler, in mod order, ending in def.
   * A handler that throws or runs over its time is skipped: the chain goes
   * on as if it had called next(e). If it had already called next, that
   * call's result stands, so Lectern's own action never runs twice.
   */
  async dispatch<T>(event: string, e: Record<string, unknown>, def: (e: Record<string, unknown>) => Promise<T>, only?: Mod[]): Promise<T> {
    const links = this.chain(event, e, only);
    const run = async (i: number, ev: Record<string, unknown>): Promise<unknown> => {
      if (i >= links.length) return def(ev);
      const { m, h } = links[i]!;
      const rid = ++seq;
      let downstream: Promise<unknown> | undefined;
      this.continuations.set(rid, (e2) => {
        if (downstream) return downstream;
        downstream = run(i + 1, (e2 && typeof e2 === "object" ? e2 : ev) as Record<string, unknown>);
        return downstream;
      });
      try {
        const msg = await new Promise<Record<string, any>>((resolve, reject) => {
          const timer = setTimeout(() => reject(new Error(`${event} handler took longer than ${HANDLER_MS / 1000}s`)), HANDLER_MS);
          m.waits.set(rid, (reply) => { clearTimeout(timer); resolve(reply); });
          m.port?.postMessage({ t: "invoke", rid, hid: h.hid, e: ev });
        }).finally(() => m.waits.delete(rid));
        if (msg.error !== undefined) throw new Error(msg.error);
        return tag(msg.value, m, this.mods);
      } catch (err) {
        this.fail(m, `${event}: ${err instanceof Error ? err.message : String(err)}`);
        return downstream ?? run(i + 1, ev);
      } finally {
        // Late next() calls get downstream if it exists; otherwise refused.
        if (!downstream) this.continuations.delete(rid);
        else setTimeout(() => this.continuations.delete(rid), HANDLER_MS);
      }
    };
    return run(0, e) as Promise<T>;
  }

  /** What mods add to (or whether they hide) a component. */
  async render(component: string, props: Record<string, unknown>, owner?: string): Promise<RenderResult> {
    const empty: RenderResult = { hidden: false, append: [] };
    if (!this.has("ui.render")) return empty;
    // A pane is drawn by the mod that opened it.
    const only = owner ? this.mods.filter((m) => m.key === owner) : undefined;
    const e = { component, props, surface: "web", viewport: { width: innerWidth, height: innerHeight } };
    const result = await Promise.race([
      this.dispatch<unknown>("ui.render", e, async () => ({ hidden: false, append: [] }), only),
      new Promise((r) => setTimeout(() => r(null), RENDER_MS)),
    ]);
    if (!result || typeof result !== "object") return empty;
    const r = result as Partial<RenderResult>;
    return { hidden: !!r.hidden, append: Array.isArray(r.append) ? r.append.filter(isElement) : [] };
  }

  closePane(key: string, id: string) {
    this.panes = this.panes.filter((p) => !(p.key === key && p.id === id));
    this.changed();
  }

  /** A Button's onPress, run in the mod that drew it. */
  press(ref: unknown, ...args: unknown[]) {
    const fn = ref as FnRef;
    const m = this.mods.find((x) => x.key === fn?.__mod);
    if (m?.state === "running" && m.drawn.has(fn.__fn)) m.port?.postMessage({ t: "fn", id: fn.__fn, args });
  }

  /** Runs a mod's palette command through the command.run chain. */
  runCommand(mod: string, id: string, args: Record<string, unknown> = {}) {
    const m = this.mods.find((x) => x.key === mod);
    return this.dispatch("command.run", { id, mod, args }, async (e) => {
      if (!m || m.state !== "running") return null;
      const rid = ++seq;
      const msg = await new Promise<Record<string, any>>((resolve) => {
        m.waits.set(rid, resolve);
        m.port?.postMessage({ t: "command", rid, id: e.id, args: e.args });
      }).finally(() => m.waits.delete(rid));
      if (msg.error !== undefined) this.fail(m, `command ${id}: ${msg.error}`);
      return msg.value ?? null;
    });
  }

  /**
   * prompt.submit and approval.decide, hooked where every screen sends them:
   * the API client. A deny becomes the request's error, which each screen
   * already shows.
   */
  intercept = async (path: string, options: RequestOptions, send: (p: string, o: RequestOptions) => Promise<unknown>) => {
    const body = options.body as Record<string, unknown> | undefined;
    const sendTo = /^\/sessions\/(\d+)\/send$/.exec(path);
    if (sendTo && options.method === "POST" && body && typeof body.text === "string" && this.has("prompt.submit")) {
      const result = await this.dispatch<unknown>("prompt.submit", { session_id: Number(sendTo[1]), text: body.text }, async (e) =>
        send(path, { ...options, body: { ...body, text: String(e.text ?? "") } }));
      return refuse(result);
    }
    const decide = /^\/approvals\/(\d+)\/decision$/.exec(path);
    if (decide && options.method === "POST" && body && this.has("approval.decide")) {
      const decision = body.decision === "denied" ? "deny" : "allow";
      const result = await this.dispatch<unknown>("approval.decide", { approval: { id: Number(decide[1]) }, decision }, async () => send(path, options));
      return refuse(result);
    }
    return send(path, options);
  };

  serverEvent(type: string, data: unknown) {
    if (this.has("server.event")) void this.dispatch("server.event", { type, data }, async () => null);
  }

  install() {
    setRequestInterceptor(this.intercept);
    return () => setRequestInterceptor(undefined);
  }
}

function refuse(result: unknown) {
  if (result && typeof result === "object" && "deny" in result) {
    const reason = String((result as { deny: unknown }).deny || "A mod stopped this.");
    throw new ApiError(409, reason, { detail: reason, mod_denied: true });
  }
  return result;
}

function isElement(v: unknown): v is ModElement {
  return !!v && typeof v === "object" && typeof (v as ModElement).type === "string";
}

// Marks every function reference a mod returned with the mod it belongs to.
// A reference passed along from a mod further down the chain keeps its
// owner, but only one that owner really drew.
function tag(v: unknown, m: Mod, mods: Mod[], depth = 0): unknown {
  if (depth > 40) return null;
  if (Array.isArray(v)) return v.map((x) => tag(x, m, mods, depth + 1));
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    if (typeof o.__fn === "number") {
      if (typeof o.__mod !== "string" || o.__mod === m.key) {
        m.drawn.add(o.__fn);
        if (m.drawn.size > 5000) m.drawn.delete(m.drawn.values().next().value as number);
        return { __fn: o.__fn, __mod: m.key };
      }
      const owner = mods.find((x) => x.key === o.__mod);
      return owner?.drawn.has(o.__fn) ? { __fn: o.__fn, __mod: owner.key } : null;
    }
    const out: Record<string, unknown> = {};
    for (const k of Object.keys(o)) out[k] = tag(o[k], m, mods, depth + 1);
    return out;
  }
  return v;
}

export const modHost = new ModHost();
