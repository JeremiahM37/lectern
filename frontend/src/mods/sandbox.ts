// The code that runs inside a mod's iframe (docs/mods.md). The iframe is
// sandboxed without allow-same-origin and carries its own CSP with no
// network, so a mod reaches Lectern only through the messages below. Keep
// everything inside sandboxMain: it is sent as source text, so nothing
// outside the function exists in the iframe.

/* eslint-disable @typescript-eslint/no-explicit-any */
function sandboxMain() {
  type Msg = Record<string, any>;
  let port: MessagePort | undefined;
  const handlers: Array<(...args: any[]) => any> = [];
  const commands = new Map<string, (...args: any[]) => any>();
  const fns = new Map<number, (...args: any[]) => any>();
  const pending = new Map<number, { res: (v: any) => void; rej: (e: any) => void }>();
  let fnSeq = 0;
  let callSeq = 0;
  let state: Record<string, unknown> = {};
  let mod = { plugin: "", id: "" };

  const post = (msg: Msg) => port?.postMessage(msg);
  const call = (op: string, args: unknown[]) =>
    new Promise<any>((res, rej) => {
      const rid = ++callSeq;
      pending.set(rid, { res, rej });
      post({ t: "$", rid, op, args });
    });
  const fire = (op: string, args: unknown[]) => post({ t: "$", op, args });
  const clone = (v: unknown) => (v === undefined ? undefined : JSON.parse(JSON.stringify(v)));
  // Functions cannot cross postMessage: a Button's onPress becomes a
  // reference the host calls back through.
  const ser = (v: any, depth = 0): any => {
    if (depth > 40) return null;
    if (typeof v === "function") {
      const id = ++fnSeq;
      fns.set(id, v);
      if (fns.size > 2000) fns.delete(fns.keys().next().value as number);
      return { __fn: id };
    }
    if (Array.isArray(v)) return v.map((x) => ser(x, depth + 1));
    if (v && typeof v === "object") {
      if (v instanceof RegExp) return String(v);
      const out: Record<string, any> = {};
      for (const k of Object.keys(v)) out[k] = ser(v[k], depth + 1);
      return out;
    }
    return v;
  };
  const el = (type: string) => (props: Record<string, any> = {}) => ({ type, props });
  const elements = { Box: el("Box"), Text: el("Text"), Badge: el("Badge"), Button: el("Button"), Link: el("Link") };
  const $ = {
    surface: "web",
    get mod() { return { ...mod }; },
    ui: {
      resolve: () => elements,
      toast: (text: unknown) => fire("toast", [String(text)]),
      status: (text: unknown) => fire("status", [text == null ? null : String(text)]),
      open: (o: { id: string; title?: string; focus?: boolean }) => fire("open", [{ id: String(o?.id), title: String(o?.title ?? o?.id) }]),
      close: (id: string) => fire("close", [String(id)]),
      render: () => fire("render", []),
    },
    state: {
      get: (key: string) => clone(state[key]),
      set: (key: string, value: unknown) => {
        state[key] = clone(value);
        fire("state", [key, state[key] ?? null]);
      },
    },
    command: {
      register: (c: { id: string; title: string; run: (...a: any[]) => any }) => {
        commands.set(String(c.id), c.run);
        fire("command", [{ id: String(c.id), title: String(c.title ?? c.id) }]);
      },
    },
    navigate: (hash: string) => fire("navigate", [String(hash)]),
    api: {
      get: (path: string) => call("api", ["GET", path]),
      post: (path: string, body?: unknown) => call("api", ["POST", path, clone(body)]),
      put: (path: string, body?: unknown) => call("api", ["PUT", path, clone(body)]),
      delete: (path: string) => call("api", ["DELETE", path]),
    },
    sleep: (ms: number) => new Promise((r) => setTimeout(r, Math.max(0, Number(ms) || 0))),
    log: (...args: unknown[]) => fire("log", [args.map((a) => (typeof a === "string" ? a : JSON.stringify(a))).join(" ")]),
  };
  const on = (event: string, a: any, b?: any) => {
    const fn = typeof b === "function" ? b : a;
    const m = typeof b === "function" ? a : undefined;
    if (typeof fn !== "function") throw new TypeError("on(event, [matcher], handler): handler must be a function");
    const hid = handlers.push(fn) - 1;
    const matcher = m instanceof RegExp ? { re: m.source, flags: m.flags } : m == null ? null : { eq: String(m) };
    post({ t: "on", hid, event: String(event), matcher });
  };
  const errorText = (e: any) => (e && e.stack ? String(e.stack).split("\n").slice(0, 3).join("\n") : String(e));

  async function receive(msg: Msg) {
    switch (msg.t) {
      case "load": {
        mod = msg.mod; state = msg.state || {};
        try {
          const register = new Function(msg.script + "\n;return typeof register === 'function' ? register : null;")();
          if (!register) throw new Error("the mod does not define register");
          await register(on, {});
          post({ t: "ready" });
        } catch (e) {
          post({ t: "failed", error: errorText(e) });
        }
        return;
      }
      case "invoke": {
        const next = (e2?: unknown) => call("next", [msg.rid, e2 === undefined ? msg.e : clone(e2)]);
        try {
          const value = await handlers[msg.hid]!($, msg.e, next);
          post({ t: "result", rid: msg.rid, value: ser(value) });
        } catch (e) {
          post({ t: "result", rid: msg.rid, error: errorText(e) });
        }
        return;
      }
      case "reply": {
        const p = pending.get(msg.rid);
        pending.delete(msg.rid);
        if (msg.error !== undefined) p?.rej(new Error(msg.error));
        else p?.res(msg.value);
        return;
      }
      case "fn": {
        try { await fns.get(msg.id)?.(...(msg.args || [])); } catch (e) { fire("log", ["error: " + errorText(e)]); }
        return;
      }
      case "command": {
        try {
          const value = await commands.get(msg.id)?.($, msg.args || {});
          post({ t: "result", rid: msg.rid, value: ser(value) });
        } catch (e) {
          post({ t: "result", rid: msg.rid, error: errorText(e) });
        }
        return;
      }
    }
  }
  window.addEventListener("message", (event) => {
    if (port || event.data?.t !== "init" || !event.ports[0]) return;
    port = event.ports[0];
    port.onmessage = (m) => void receive(m.data);
  });
}

/** The iframe document a mod runs in. */
export const sandboxDocument = () =>
  "<!doctype html><meta charset=utf-8>" +
  `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'">` +
  "<script>(" + sandboxMain.toString() + ")()</script>";
