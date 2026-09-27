import "./browser.css";
import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { authToken, withToken, type JsonValue } from "../api";
import { relayTunnel } from "../relay/boot";
import { t, useLocale } from "../i18n";
import type { LiveView } from "../types";
import {
  DEVICES,
  device,
  fromFrame,
  localPath,
  loopbackPort,
  mapPoint,
  mergeSelection,
  modifiers,
  normalizeAddress,
  readPicker,
  viewFramable,
  viewOrigin,
  viewURL,
  type DesignElement,
  type PickerMessage,
  type ViewInfo,
} from "./model";

/** The slice of the app API the pane needs; Conversation passes it through. */
export interface BrowserApi {
  request<T>(path: string, options?: { method?: string; body?: JsonValue; signal?: AbortSignal }): Promise<T>;
}

interface PageState {
  url: string;
  title: string;
  viewport: { width: number; height: number; mobile: boolean; scale: number };
  design: boolean;
}

interface Status {
  running: boolean;
  where?: string;
  binary?: string;
  state?: PageState;
  control: "agent" | "user" | "stopped";
  agent_active: boolean;
  last_action?: string;
  views: ViewInfo[];
  views_enabled: boolean;
  views_disabled_reason?: string;
  tabs: { id: number; url: string; title: string; active: boolean }[];
  active_tab?: number;
  downloads: { url: string; name: string; path: string; state: string; received: number; total: number }[];
  profile: string;
}

interface Profiles {
  profiles: string[];
  current: string;
  default: string;
  where: string;
}

function size(n: number) {
  return n >= 1 << 20
    ? t("browser.size.mb", { size: (n / (1 << 20)).toFixed(1) })
    : n >= 1024
      ? t("browser.size.kb", { size: Math.round(n / 1024) })
      : t("browser.size.bytes", { size: n });
}

interface Port {
  port: number;
  command: string;
  in_workspace: boolean;
  http: boolean;
}

interface DeskStatus {
  live_id: number;
  allowed: boolean;
  reason: string;
  allowed_here: boolean;
  stopped: boolean;
  agent_active: boolean;
  last_action: string;
}

type Mode = "shared" | "direct";

const PHONE = typeof matchMedia === "function" && matchMedia("(max-width: 700px)").matches;

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * The Browser pane: a session's dev server beside its conversation.
 *
 * "Shared" shows the session's own browser, a real Chromium on the agent's
 * machine, streamed here; the agent drives the same one through its tools, and
 * pressing, typing or scrolling in the picture takes over. "Live page" frames
 * the dev server directly through a Lectern view, for native scrolling and
 * typing, where this device can reach one. Design Mode works in both.
 */
export function BrowserPane({
  api,
  sessionId,
  name,
  onClose,
  onNotice,
}: {
  api: BrowserApi;
  sessionId: number;
  name: string;
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  useLocale();
  // The parent may pass new function identities on every render; nothing
  // here should reconnect or reload because of that.
  const noticeRef = useRef(onNotice);
  noticeRef.current = onNotice;
  const apiRef = useRef(api);
  apiRef.current = api;
  const notice = useCallback((text: string, error?: boolean) => noticeRef.current(text, error), []);
  const stableApi = useRef<BrowserApi>({ request: (path, options) => apiRef.current.request(path, options) }).current;
  const [status, setStatus] = useState<Status>(),
    [canDrive, setCanDrive] = useState(true),
    [ports, setPorts] = useState<Port[]>(),
    [portsError, setPortsError] = useState(""),
    [showPorts, setShowPorts] = useState(false),
    [allPorts, setAllPorts] = useState(false),
    [address, setAddress] = useState(""),
    [deviceId, setDeviceId] = useState(PHONE ? "phone" : "desktop"),
    [mode, setMode] = useState<Mode>(),
    [view, setView] = useState<ViewInfo>(),
    [frameSrc, setFrameSrc] = useState(""),
    [busy, setBusy] = useState(""),
    [design, setDesign] = useState(false),
    [hover, setHover] = useState(""),
    [picked, setPicked] = useState<DesignElement[]>([]),
    [multi, setMulti] = useState(false),
    [note, setNote] = useState(""),
    [frame, setFrame] = useState(""),
    [tab, setTab] = useState<"page" | "desktop">("page"),
    [finding, setFinding] = useState(false),
    [findText, setFindText] = useState(""),
    [findResult, setFindResult] = useState<{ matches: number; index: number }>(),
    [profiles, setProfiles] = useState<Profiles>(),
    [showCookies, setShowCookies] = useState(false),
    [cookieDomains, setCookieDomains] = useState(""),
    [chromeDir, setChromeDir] = useState(""),
    [cookieResult, setCookieResult] = useState(""),
    [desks, setDesks] = useState<LiveView[]>([]);
  const frameRef = useRef<HTMLIFrameElement>(null),
    shotRef = useRef<HTMLImageElement>(null),
    socket = useRef<WebSocket | null>(null),
    captures = useRef(new Map<string, (png?: string) => void>()),
    history = useRef({ back: 0, forward: 0, moving: false }),
    pageScale = useRef(1),
    wrapRef = useRef<HTMLDivElement>(null);
  const [, redraw] = useState(0);
  const dev = device(deviceId);
  const relay = !!relayTunnel;
  const directPossible = !!status?.views_enabled && !relay;

  const refresh = useCallback(async () => {
    try {
      const next = await stableApi.request<Status>(`/sessions/${sessionId}/browser`);
      setStatus(next);
      return next;
    } catch (error) {
      notice(errorText(error), true);
      return undefined;
    }
  }, [stableApi, sessionId, notice]);

  const loadPorts = useCallback(() => {
    setPortsError("");
    stableApi
      .request<{ ports: Port[] }>(`/sessions/${sessionId}/ports`)
      .then((out) => setPorts(out.ports))
      .catch((error) => setPortsError(errorText(error)));
  }, [stableApi, sessionId]);

  useEffect(() => {
    void refresh().then((st) => {
      if (!st) return;
      if (st.running && st.state?.url && st.state.url !== "about:blank") setAddress(st.state.url);
      setMode(st.running || !st.views_enabled || relay ? "shared" : "direct");
    });
    loadPorts();
    void stableApi
      .request<Profiles>(`/sessions/${sessionId}/browser/profiles`)
      .then(setProfiles)
      .catch(() => undefined);
    void stableApi
      .request<{ views: LiveView[] }>("/live")
      .then((out) => setDesks(out.views.filter((v) => v.kind === "desktop" && v.session_id === sessionId)))
      .catch(() => undefined);
  }, [stableApi, sessionId, refresh, loadPorts, relay]);

  // ---- shared browser: the stream ----
  const handlePicker = useCallback((msg: PickerMessage | null, additive = false) => {
    if (!msg) return;
    if (msg.type === "hover") setHover(msg.breadcrumb);
    else if (msg.type === "select") setPicked((list) => mergeSelection(list, msg.element, msg.additive || additive));
    else if (msg.type === "cancel") setPicked([]);
    else if (msg.type === "capture") {
      captures.current.get(msg.id)?.(msg.png);
      captures.current.delete(msg.id);
    }
  }, []);
  const multiRef = useRef(multi);
  multiRef.current = multi;

  useEffect(() => {
    if (mode !== "shared" || !status?.running) return;
    let closed = false,
      retry: number | undefined,
      url = "";
    const open = () => {
      const scheme = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(withToken(`${scheme}//${location.host}/api/sessions/${sessionId}/browser/stream`));
      ws.binaryType = "blob";
      socket.current = ws;
      ws.onmessage = (event) => {
        if (typeof event.data !== "string") {
          const next = URL.createObjectURL(event.data as Blob);
          if (url) URL.revokeObjectURL(url);
          url = next;
          setFrame(next);
          return;
        }
        try {
          const msg = JSON.parse(event.data) as { type: string; status?: Status; can_drive?: boolean; design?: unknown; page_scale?: number };
          if (msg.type === "frame" && typeof msg.page_scale === "number" && msg.page_scale > 0) pageScale.current = msg.page_scale;
          else if (msg.type === "state" && msg.status) {
            setStatus(msg.status);
            if (typeof msg.can_drive === "boolean") setCanDrive(msg.can_drive);
            const u = msg.status.state?.url;
            if (u && u !== "about:blank") setAddress((old) => (document.activeElement?.id === "browser-address" ? old : u));
          } else if (msg.type === "design") handlePicker(readPicker(msg.design), multiRef.current);
        } catch {
          /* not ours */
        }
      };
      ws.onclose = () => {
        if (socket.current === ws) socket.current = null;
        if (!closed) retry = window.setTimeout(() => void refresh().then((st) => st?.running && !closed && open()), 1500);
      };
    };
    open();
    return () => {
      closed = true;
      window.clearTimeout(retry);
      socket.current?.close();
      socket.current = null;
      if (url) URL.revokeObjectURL(url);
    };
  }, [mode, status?.running, sessionId, refresh, handlePicker]);

  const act = useCallback(
    async (body: Record<string, JsonValue>) => {
      setBusy(String(body.action));
      try {
        const out = await stableApi.request<{ status?: Status }>(`/sessions/${sessionId}/browser`, { method: "POST", body });
        if (out.status) setStatus(out.status);
        return out;
      } catch (error) {
        notice(errorText(error), true);
        return undefined;
      } finally {
        setBusy("");
      }
    },
    [stableApi, sessionId, notice],
  );

  const vpBody = (d = dev) => ({ ...d.viewport }) as unknown as Record<string, JsonValue>;

  // ---- live page: a view of the dev server ----
  const openView = useCallback(
    async (port: number, path: string, wantDesign: boolean) => {
      setBusy("view");
      try {
        const v = await stableApi.request<ViewInfo>(`/sessions/${sessionId}/browser/views`, {
          method: "POST",
          body: { port, design: wantDesign, parent_origin: location.origin },
        });
        if (!viewFramable(location, v)) {
          notice(t("browser.notice.viewNotSecure"));
          window.open(viewURL(location, v, path), "_blank", "noopener");
          return undefined;
        }
        history.current = { back: 0, forward: 0, moving: false };
        setView(v);
        setFrameSrc(viewURL(location, v, path));
        return v;
      } catch (error) {
        notice(errorText(error), true);
        return undefined;
      } finally {
        setBusy("");
      }
    },
    [stableApi, sessionId, notice],
  );

  useEffect(() => {
    if (mode !== "direct" || !view) return;
    const origin = viewOrigin(location, view);
    const listen = (event: MessageEvent) =>
      handlePicker(fromFrame(event, frameRef.current?.contentWindow, origin), multiRef.current);
    window.addEventListener("message", listen);
    return () => window.removeEventListener("message", listen);
  }, [mode, view, handlePicker]);

  // A framed page renders at the device's real width and is scaled to fit.
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap || typeof ResizeObserver !== "function") return;
    const fit = () => wrap.style.setProperty("--frame-scale", String(Math.min(1, wrap.clientWidth / dev.viewport.width)));
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(wrap);
    return () => ro.disconnect();
  }, [frameSrc, dev.viewport.width, mode]);

  // ---- navigation ----
  const go = async (raw = address) => {
    const target = normalizeAddress(raw);
    if (!target) return;
    setAddress(target);
    setShowPorts(false);
    if (mode === "direct") {
      const port = loopbackPort(target);
      if (port === null) {
        notice(t("browser.notice.liveOnlyLocal"));
        setMode("shared");
        await act({ action: "navigate", url: target, ...vpBody() });
        return;
      }
      await openView(port, localPath(target), design);
      return;
    }
    await act({ action: status?.running ? "navigate" : "open", url: target, ...vpBody() });
    if (!status?.running) void refresh();
  };

  const nav = (action: "back" | "forward" | "reload") => {
    if (mode === "shared") return void act({ action });
    if (action === "reload") return void (view && openView(view.port, localPath(address), design));
    // A framed page's history is part of this tab's own: step it only as far
    // as the frame itself has gone, so the app never leaves this page.
    const h = history.current;
    if (action === "back" && h.back > 0) {
      h.back--;
      h.forward++;
      h.moving = true;
      window.history.back();
    } else if (action === "forward" && h.forward > 0) {
      h.forward--;
      h.back++;
      h.moving = true;
      window.history.forward();
    }
    redraw((n) => n + 1);
  };

  const pickDevice = (id: string) => {
    setDeviceId(id);
    if (mode === "shared" && status?.running) void act({ action: "resize", ...vpBody(device(id)) });
  };

  const toggleDesign = async () => {
    const on = !design;
    setDesign(on);
    setHover("");
    if (!on) setPicked([]);
    if (mode === "shared") {
      if (!status?.running) {
        notice(t("browser.notice.openPageFirst"), true);
        setDesign(false);
        return;
      }
      await act({ action: "design", on });
    } else if (view) {
      // Only a document loaded with Design Mode on carries the picker.
      await openView(view.port, localPath(address), on);
    }
  };

  const switchMode = async (next: Mode) => {
    setMode(next);
    setDesign(false);
    setPicked([]);
    if (next === "direct") {
      const port = loopbackPort(normalizeAddress(address));
      if (port !== null) await openView(port, localPath(normalizeAddress(address)), false);
    } else if (!status?.running && address) {
      await act({ action: "open", url: normalizeAddress(address), ...vpBody() });
      void refresh();
    }
  };

  const openInTab = async () => {
    const target = normalizeAddress(address || status?.state?.url || "");
    const port = loopbackPort(target);
    if (port !== null && status?.views_enabled && !relay) {
      try {
        const v = await stableApi.request<ViewInfo>(`/sessions/${sessionId}/browser/views`, {
          method: "POST",
          body: { port, design: false, parent_origin: location.origin },
        });
        window.open(viewURL(location, v, localPath(target)), "_blank", "noopener");
      } catch (error) {
        notice(errorText(error), true);
      }
    } else if (/^https?:/.test(target) && port === null) {
      window.open(target, "_blank", "noopener");
    } else {
      notice(status?.views_disabled_reason || t("browser.notice.onlyOnAgentMachine"), true);
    }
  };

  // ---- the operator's input on the streamed picture ----
  const send = (event: Record<string, JsonValue>) => {
    const ws = socket.current;
    if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: "input", event }));
  };
  const point = (e: { clientX: number; clientY: number }) => {
    const img = shotRef.current,
      vp = status?.state?.viewport;
    if (!img || !vp) return null;
    const r = img.getBoundingClientRect();
    const p = mapPoint(e.clientX, e.clientY, { left: r.left, top: r.top, width: r.width, height: r.height }, vp);
    // A zoomed-out page (no viewport meta on a phone) takes input in its own
    // layout pixels, not the picture's.
    return p && { x: p.x / pageScale.current, y: p.y / pageScale.current };
  };
  const lastMove = useRef(0),
    pendingMove = useRef<number | undefined>(undefined);
  const pointer = (kind: "mousedown" | "mouseup" | "mousemove", e: PointerEvent<HTMLImageElement>) => {
    if (!canDrive) return;
    const p = point(e);
    if (!p) return;
    if (kind === "mousemove") {
      // At most one move per 40 ms, but always the last one: hover in Design
      // Mode must land where the pointer stopped.
      const now = performance.now(),
        mods = modifiers(e);
      window.clearTimeout(pendingMove.current);
      if (now - lastMove.current < 40) {
        pendingMove.current = window.setTimeout(() => {
          lastMove.current = performance.now();
          send({ type: "mousemove", x: p.x, y: p.y, modifiers: mods });
        }, 40);
        return;
      }
      lastMove.current = now;
    } else {
      window.clearTimeout(pendingMove.current);
      e.preventDefault();
      (e.currentTarget.parentElement as HTMLElement | null)?.focus();
    }
    const button = e.button === 2 ? "right" : e.button === 1 ? "middle" : "left";
    send({ type: kind, x: p.x, y: p.y, button, clicks: e.detail > 1 ? Math.min(e.detail, 3) : 1, modifiers: modifiers(e) });
  };
  const key = (kind: "keydown" | "keyup", e: KeyboardEvent) => {
    if (!canDrive || mode !== "shared") return;
    if (e.key === "Tab" && e.shiftKey && kind === "keydown" && e.ctrlKey) return;
    e.preventDefault();
    send({ type: kind, key: e.key, code: e.code, key_code: e.keyCode, modifiers: modifiers(e) });
  };

  // ---- Design Mode: send ----
  const capture = (selector: string) =>
    new Promise<string | undefined>((resolve) => {
      const win = frameRef.current?.contentWindow;
      if (!win || !view) return resolve(undefined);
      const id = Math.random().toString(36).slice(2);
      const timer = window.setTimeout(() => {
        captures.current.delete(id);
        resolve(undefined);
      }, 4000);
      captures.current.set(id, (png) => {
        window.clearTimeout(timer);
        resolve(png);
      });
      win.postMessage({ lecternDesign: { type: "capture", id, selector } }, viewOrigin(location, view));
    });

  const sendDesign = async () => {
    if (!picked.length) return;
    setBusy("design");
    try {
      let elements = picked;
      if (mode === "direct") {
        // The pane's own capture is only the fallback; the server renders the
        // page headlessly first and says which one it used.
        elements = await Promise.all(picked.map(async (el) => ({ ...el, client_png: await capture(el.selector) })));
      }
      const out = await stableApi.request<{ files: { name: string; path: string }[]; sent: boolean; screenshots: { source: string; error: string }[] }>(
        `/sessions/${sessionId}/design`,
        {
          method: "POST",
          body: {
            note,
            source: mode === "direct" ? "frame" : "browser",
            view_id: view?.id ?? "",
            elements: elements as unknown as JsonValue,
          },
        },
      );
      const shot = out.screenshots.find((s) => s.source)?.source;
      notice(
        shot
          ? t("browser.notice.designSentShot", { name, files: out.files.length, source: shot })
          : t("browser.notice.designSentNoShot", { name, files: out.files.length }),
      );
      setPicked([]);
      setNote("");
    } catch (error) {
      notice(errorText(error), true);
    } finally {
      setBusy("");
    }
  };

  // ---- live desktops (computer use) ----
  const [deskState, setDeskState] = useState<Record<number, DeskStatus>>({});
  const [deskShot, setDeskShot] = useState<Record<number, string>>({});
  useEffect(() => {
    if (tab !== "desktop" || !desks.length) return;
    let stop = false;
    const urls: string[] = [];
    const poll = async () => {
      for (const d of desks) {
        try {
          const st = await stableApi.request<DeskStatus>(`/live/${d.id}/computer`);
          if (stop) return;
          setDeskState((old) => ({ ...old, [d.id]: st }));
          const token = authToken();
          const res = await fetch(`/api/live/${d.id}/screenshot`, token ? { headers: { Authorization: `Bearer ${token}` } } : {});
          if (!res.ok || stop) continue;
          const url = URL.createObjectURL(await res.blob());
          urls.push(url);
          setDeskShot((old) => ({ ...old, [d.id]: url }));
          while (urls.length > 2) URL.revokeObjectURL(urls.shift() as string);
        } catch {
          /* the next poll tries again */
        }
      }
      if (!stop) window.setTimeout(() => void poll(), 1500);
    };
    void poll();
    return () => {
      stop = true;
      urls.forEach((u) => URL.revokeObjectURL(u));
    };
  }, [tab, desks, stableApi]);
  const deskControl = async (id: number, body: Record<string, JsonValue>) => {
    try {
      const st = await stableApi.request<DeskStatus>(`/live/${id}/computer`, { method: "POST", body });
      setDeskState((old) => ({ ...old, [id]: st }));
    } catch (error) {
      notice(errorText(error), true);
    }
  };

  const find = async (backwards = false) => {
    if (!findText) return;
    const out = await act({ action: "find", text: findText, backwards });
    const r = (out as { find?: { matches: number; index: number } } | undefined)?.find;
    if (r) setFindResult(r);
  };
  const pickProfile = async (label: string) => {
    let name = label;
    if (label === "__new") {
      name = (window.prompt(t("browser.profile.prompt")) || "").trim().toLowerCase();
      if (!name) return;
    }
    const target = normalizeAddress(address || status?.state?.url || "") || "about:blank";
    await act({ action: "open", profile: name, url: target === "about:blank" ? "" : target, ...vpBody() });
    await refresh();
    const p = await stableApi.request<Profiles>(`/sessions/${sessionId}/browser/profiles`).catch(() => undefined);
    if (p) setProfiles(p);
  };
  const importCookies = async (from: "file" | "chrome", file?: File) => {
    setCookieResult(t("browser.cookies.importing"));
    try {
      let body: FormData | Record<string, JsonValue>;
      if (from === "file" && file) {
        const form = new FormData();
        form.append("file", file);
        form.append("domains", cookieDomains);
        body = form;
      } else body = { profile_dir: chromeDir.trim() || "auto", domains: cookieDomains };
      const out = await stableApi.request<{ imported: number; skipped: Record<string, number>; sites: number }>(
        `/sessions/${sessionId}/browser/cookies`,
        { method: "POST", body: body as JsonValue },
      );
      const skipped = Object.entries(out.skipped || {}).map(([why, n]) => `${n} ${why}`).join(", ");
      setCookieResult(
        skipped
          ? t("browser.cookies.importedSkipped", { imported: out.imported, sites: out.sites, skipped })
          : t("browser.cookies.imported", { imported: out.imported, sites: out.sites }),
      );
      void refresh();
    } catch (error) {
      setCookieResult(errorText(error));
    }
  };
  const saveDownload = async (d: Status["downloads"][number]) => {
    const i = d.path.indexOf("/.lectern/downloads/");
    const rel = i >= 0 ? d.path.slice(i + 1) : d.path;
    try {
      const token = authToken();
      const res = await fetch(`/api/term/session/${sessionId}/file?path=${encodeURIComponent(rel)}`, token ? { headers: { Authorization: `Bearer ${token}` } } : {});
      if (!res.ok) throw new Error(t("browser.notice.fetchFailed", { name: d.name, status: res.status }));
      const url = URL.createObjectURL(await res.blob());
      const a = document.createElement("a");
      a.href = url;
      a.download = d.name;
      a.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (error) {
      notice(errorText(error), true);
    }
  };

  const shared = mode === "shared";
  const running = !!status?.running;
  const control = status?.control ?? "agent";
  const agentOn = shared && running && control === "agent" && status?.agent_active;
  const pageTitle = status?.state?.title;
  const workspacePorts = ports?.filter((p) => p.in_workspace) ?? [];
  const otherPorts = ports?.filter((p) => !p.in_workspace) ?? [];

  return (
    <section className={`browser-pane${agentOn ? " agent-driving" : ""}`} aria-label={t("browser.pane.label", { name })} data-mode={mode}>
      <header className="browser-bar">
        <div className="browser-tabs" role="tablist">
          <button role="tab" aria-selected={tab === "page"} className="b" onClick={() => setTab("page")}>
            {t("browser.pane.tabBrowser")}
          </button>
          {desks.length > 0 && (
            <button role="tab" aria-selected={tab === "desktop"} className="b" onClick={() => setTab("desktop")}>
              {t("browser.pane.tabDesktop")}
            </button>
          )}
        </div>
        <button className="b browser-close" aria-label={t("browser.pane.close")} onClick={onClose}>
          ✕
        </button>
      </header>
      {tab === "page" && (
        <>
          <div className="browser-toolbar">
            <button className="b" aria-label={t("browser.toolbar.back")} disabled={!shared && history.current.back === 0} onClick={() => nav("back")}>
              ◀
            </button>
            <button className="b" aria-label={t("browser.toolbar.forward")} disabled={!shared && history.current.forward === 0} onClick={() => nav("forward")}>
              ▶
            </button>
            <button className="b" aria-label={t("browser.toolbar.reload")} onClick={() => nav("reload")}>
              ⟳
            </button>
            <form
              className="browser-address"
              onSubmit={(e) => {
                e.preventDefault();
                void go();
              }}
            >
              <input
                id="browser-address"
                aria-label={t("browser.toolbar.address")}
                placeholder={t("browser.toolbar.addressPlaceholder")}
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
              />
              <button className="b" disabled={!!busy || !address.trim()}>
                {t("browser.toolbar.go")}
              </button>
            </form>
            <button className="b" aria-expanded={showPorts} onClick={() => { setShowPorts(!showPorts); if (!showPorts) loadPorts(); }}>
              {t("browser.toolbar.ports")}
            </button>
            <select aria-label={t("browser.toolbar.deviceSize")} value={deviceId} onChange={(e) => pickDevice(e.target.value)}>
              {DEVICES.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.label} {d.viewport.width}×{d.viewport.height}
                </option>
              ))}
            </select>
            <button className={design ? "b on" : "b"} aria-pressed={design} onClick={() => void toggleDesign()}>
              {t("browser.toolbar.design")}
            </button>
            <button className="b" aria-label={t("browser.toolbar.openInTab")} onClick={() => void openInTab()}>
              ⤢
            </button>
            <button
              className={finding ? "b on" : "b"}
              aria-pressed={finding}
              onClick={() =>
                shared ? setFinding(!finding) : notice(t("browser.notice.liveFind"))
              }
            >
              {t("browser.toolbar.find")}
            </button>
          </div>
          {shared && running && status && status.tabs.length > 0 && (
            <div className="browser-tabstrip" role="tablist" aria-label={t("browser.tabs.label")}>
              {status.tabs.map((tb) => (
                <span key={tb.id} className={tb.active ? "browser-tab on" : "browser-tab"}>
                  <button role="tab" aria-selected={tb.active} title={tb.url} onClick={() => void act({ action: "tab_select", tab: tb.id })}>
                    {tb.title || (tb.url === "about:blank" ? t("browser.tabs.untitled") : tb.url)}
                  </button>
                  <button aria-label={t("browser.tabs.close", { title: tb.title || tb.url })} onClick={() => void act({ action: "tab_close", tab: tb.id })}>
                    ×
                  </button>
                </span>
              ))}
              <button className="browser-tab-new" aria-label={t("browser.tabs.new")} onClick={() => void act({ action: "tab_new" })}>
                +
              </button>
            </div>
          )}
          {finding && shared && (
            <form
              className="browser-find"
              onSubmit={(e) => {
                e.preventDefault();
                void find(false);
              }}
            >
              <input
                aria-label={t("browser.find.label")}
                placeholder={t("browser.find.label")}
                value={findText}
                autoFocus
                onChange={(e) => {
                  setFindText(e.target.value);
                  setFindResult(undefined);
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && e.shiftKey) {
                    e.preventDefault();
                    void find(true);
                  } else if (e.key === "Escape") setFinding(false);
                }}
              />
              <button type="button" className="b" aria-label={t("browser.find.previous")} onClick={() => void find(true)}>
                ▲
              </button>
              <button className="b" aria-label={t("browser.find.next")}>
                ▼
              </button>
              <span role="status">
                {findResult ? (findResult.matches ? t("browser.find.position", { index: findResult.index, matches: findResult.matches }) : t("browser.find.none")) : ""}
              </span>
            </form>
          )}
          <div className="browser-modes">
            <label>
              <input type="radio" name={`browser-mode-${sessionId}`} checked={shared} onChange={() => void switchMode("shared")} /> {t("browser.mode.shared")}
            </label>
            <label title={relay ? t("browser.mode.relayOnlyShared") : status?.views_disabled_reason || t("browser.mode.liveTitle")}>
              <input type="radio" name={`browser-mode-${sessionId}`} checked={mode === "direct"} disabled={!directPossible} onChange={() => void switchMode("direct")} /> {t("browser.mode.live")}
            </label>
            {profiles && (
              <label className="browser-profile" title={t("browser.profile.where", { where: profiles.where })}>
                {t("browser.profile.label")}{" "}
                <select
                  aria-label={t("browser.profile.select")}
                  value={status?.running ? status.profile : profiles.default}
                  onChange={(e) => void pickProfile(e.target.value)}
                >
                  {profiles.profiles.map((p) => (
                    <option key={p} value={p}>
                      {p === "temporary" ? t("browser.profile.temporary") : p}
                    </option>
                  ))}
                  <option value="__new">{t("browser.profile.new")}</option>
                </select>
              </label>
            )}
            <button className="b" aria-expanded={showCookies} onClick={() => setShowCookies(!showCookies)}>
              {t("browser.cookies.button")}
            </button>
            {shared && running && (
              <span className="browser-where">
                {status?.where === "host" ? t("browser.where.host") : t("browser.where.session")}
              </span>
            )}
          </div>
          {showCookies && (
            <div className="browser-cookies" aria-label={t("browser.cookies.label")}>
              <p>{t("browser.cookies.intro")}</p>
              <label>
                {t("browser.cookies.onlySitesOptional")}{" "}
                <input
                  aria-label={t("browser.cookies.onlySites")}
                  placeholder="example.com, github.com"
                  value={cookieDomains}
                  onChange={(e) => setCookieDomains(e.target.value)}
                />
              </label>
              <label className="b">
                {t("browser.cookies.fromFile")}
                <input
                  type="file"
                  aria-label={t("browser.cookies.file")}
                  accept=".txt,.json,text/plain,application/json"
                  hidden
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    e.target.value = "";
                    if (f) void importCookies("file", f);
                  }}
                />
              </label>
              <span className="browser-chrome-import">
                <input
                  aria-label={t("browser.cookies.chromeDir")}
                  placeholder={t("browser.cookies.chromeDirPlaceholder")}
                  value={chromeDir}
                  onChange={(e) => setChromeDir(e.target.value)}
                />
                <button className="b" onClick={() => void importCookies("chrome")}>
                  {t("browser.cookies.fromChrome")}
                </button>
              </span>
              {cookieResult && <p role="status">{cookieResult}</p>}
            </div>
          )}
          {showPorts && (
            <div className="browser-ports" role="list" aria-label={t("browser.ports.label")}>
              {!ports && !portsError && <span>{t("browser.ports.looking")}</span>}
              {portsError && <span className="browser-error">{portsError}</span>}
              {ports && ports.length === 0 && <span>{t("browser.ports.none")}</span>}
              {[...workspacePorts, ...(allPorts ? otherPorts : otherPorts.filter((p) => p.http).slice(0, 8))].map((p) => (
                <button
                  key={p.port}
                  role="listitem"
                  className={p.in_workspace ? "b port in-workspace" : "b port"}
                  title={p.command}
                  onClick={() => {
                    setShowPorts(false);
                    void go(`http://localhost:${p.port}/`);
                  }}
                >
                  :{p.port}
                  <small>{p.in_workspace ? t("browser.ports.thisWorkspace") : (p.command.split(" ")[0] ?? "").split("/").pop()}</small>
                  {!p.http && <small className="dim">{t("browser.ports.notHttp")}</small>}
                </button>
              ))}
              {!allPorts && otherPorts.length > otherPorts.filter((p) => p.http).slice(0, 8).length && (
                <button className="b" onClick={() => setAllPorts(true)}>
                  {t("browser.ports.all", { total: ports?.length ?? "" })}
                </button>
              )}
              <button className="b" onClick={loadPorts} aria-label={t("browser.ports.lookAgain")}>
                ⟳
              </button>
            </div>
          )}
          {shared && running && (
            <div className={`browser-control control-${control}`} role="status">
              {control === "agent" && (
                <>
                  <span>
                    {status?.agent_active
                      ? status.last_action
                        ? t("browser.control.agentDrivingAction", { action: status.last_action })
                        : t("browser.control.agentDriving")
                      : t("browser.control.agentMay")}
                  </span>
                  {canDrive && (
                    <>
                      <button className="b" onClick={() => void act({ action: "control", mode: "user" })}>{t("browser.control.takeOver")}</button>
                      <button className="b warn" onClick={() => void act({ action: "control", mode: "stopped" })}>{t("browser.control.stopAgent")}</button>
                    </>
                  )}
                </>
              )}
              {control === "user" && (
                <>
                  <span>{t("browser.control.userHas")}</span>
                  {canDrive && <button className="b" onClick={() => void act({ action: "control", mode: "agent" })}>{t("browser.control.handBack")}</button>}
                </>
              )}
              {control === "stopped" && (
                <>
                  <span>{t("browser.control.stopped")}</span>
                  {canDrive && <button className="b" onClick={() => void act({ action: "control", mode: "agent" })}>{t("browser.control.allow")}</button>}
                </>
              )}
            </div>
          )}
          <div className="browser-stage" data-device={deviceId}>
            {shared && running && (
              <div
                className="browser-screen"
                tabIndex={0}
                aria-label={t("browser.stage.page", { title: pageTitle || status?.state?.url || "" })}
                onKeyDown={(e) => key("keydown", e)}
                onKeyUp={(e) => key("keyup", e)}
                onPaste={(e) => {
                  const text = e.clipboardData.getData("text");
                  if (text && canDrive) {
                    e.preventDefault();
                    send({ type: "text", text });
                  }
                }}
                onWheel={(e) => {
                  const p = point(e);
                  if (p && canDrive) send({ type: "wheel", x: p.x, y: p.y, dx: e.deltaX, dy: e.deltaY, modifiers: modifiers(e) });
                }}
              >
                {frame ? (
                  <img
                    ref={shotRef}
                    src={frame}
                    alt={pageTitle || t("browser.stage.sessionBrowser")}
                    draggable={false}
                    onPointerDown={(e) => pointer("mousedown", e)}
                    onPointerUp={(e) => pointer("mouseup", e)}
                    onPointerMove={(e) => pointer("mousemove", e)}
                    onContextMenu={(e) => e.preventDefault()}
                  />
                ) : (
                  <p className="browser-empty">{t("browser.stage.connecting")}</p>
                )}
              </div>
            )}
            {shared && !running && (
              <div className="browser-empty">
                <p>{t("browser.stage.sharedEmpty", { name })}</p>
                {workspacePorts.length > 0 && (
                  <p>
                    {workspacePorts.map((p) => (
                      <button key={p.port} className="b" onClick={() => void go(`http://localhost:${p.port}/`)}>
                        {t("browser.ports.open", { port: p.port })}
                      </button>
                    ))}
                  </p>
                )}
              </div>
            )}
            {!shared && frameSrc && (
              <div ref={wrapRef} className="browser-frame-wrap" style={{ aspectRatio: `${dev.viewport.width} / ${dev.viewport.height}` }}>
                <iframe
                  ref={frameRef}
                  key={frameSrc}
                  title={t("browser.stage.frameTitle", { name })}
                  src={frameSrc}
                  style={{ width: dev.viewport.width, height: dev.viewport.height }}
                  onLoad={(e) => {
                    const h = history.current;
                    const loads = Number(e.currentTarget.dataset.loads || 0) + 1;
                    e.currentTarget.dataset.loads = String(loads);
                    if (h.moving) h.moving = false;
                    else if (loads > 1) {
                      h.back++;
                      h.forward = 0;
                    }
                    redraw((n) => n + 1);
                  }}
                  allow="clipboard-read; clipboard-write"
                />
              </div>
            )}
            {!shared && !frameSrc && (
              <div className="browser-empty">
                <p>{t("browser.stage.liveEmpty")}</p>
              </div>
            )}
          </div>
          {shared && running && status && status.downloads.length > 0 && (
            <ul className="browser-downloads" aria-label={t("browser.downloads.label")}>
              {status.downloads.slice(-5).map((d) => (
                <li key={d.path + d.state}>
                  <span title={d.path}>{d.name}</span>
                  <small>
                    {d.state === "completed"
                      ? size(d.received)
                      : d.state === "canceled"
                        ? t("browser.downloads.canceled")
                        : d.total
                          ? t("browser.downloads.progressOf", { received: size(d.received), total: size(d.total) })
                          : t("browser.downloads.progress", { received: size(d.received) })}
                  </small>
                  {d.state === "completed" && (
                    <button className="b" onClick={() => void saveDownload(d)}>
                      {t("browser.downloads.save")}
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {design && (
            <div className="browser-design" aria-label={t("browser.design.label")}>
              <p className="browser-hover">{hover ? hover : t("browser.design.hint")}</p>
              <label className="browser-multi">
                <input type="checkbox" checked={multi} onChange={(e) => setMulti(e.target.checked)} /> {t("browser.design.pickSeveral")}
              </label>
              {picked.length > 0 && (
                <ol className="browser-picked">
                  {picked.map((el) => (
                    <li key={el.selector}>
                      <code>{el.breadcrumb}</code>
                      <small>
                        {t("browser.design.elementInfo", { width: Math.round(el.rect.width), height: Math.round(el.rect.height), styles: Object.keys(el.css).length })}
                        {el.source ? ` · ${el.source.file.split("/").pop()}${el.source.line ? ":" + el.source.line : ""}` : ""}
                      </small>
                      <button className="b" aria-label={t("browser.design.remove", { selector: el.selector })} onClick={() => setPicked((l) => l.filter((x) => x !== el))}>
                        ×
                      </button>
                    </li>
                  ))}
                </ol>
              )}
              <textarea
                aria-label={t("browser.design.note")}
                placeholder={t("browser.design.notePlaceholder")}
                value={note}
                onChange={(e) => setNote(e.target.value)}
                rows={2}
              />
              <button className="b ok" disabled={!picked.length || busy === "design"} onClick={() => void sendDesign()}>
                {busy === "design" ? t("browser.design.sending") : t("browser.design.send", { picked: picked.length || "" })}
              </button>
            </div>
          )}
        </>
      )}
      {tab === "desktop" && (
        <div className="browser-desktops">
          {desks.map((d) => {
            const st = deskState[d.id];
            return (
              <article key={d.id} className={st?.agent_active ? "desk agent-driving" : "desk"}>
                <header>
                  <strong>{d.title}</strong> <span className="chip">{d.detail?.display}</span>
                </header>
                <p role="status" className="browser-control">
                  {!st
                    ? "…"
                    : st.stopped
                      ? t("browser.desk.stopped")
                      : st.allowed
                        ? st.agent_active
                          ? t("browser.desk.agentActive", { action: st.last_action })
                          : t("browser.desk.agentMay")
                        : t("browser.desk.off")}
                  {st && !st.allowed && (
                    <button className="b" onClick={() => void deskControl(d.id, { allow: true })}>{t("browser.desk.allow")}</button>
                  )}
                  {st?.allowed && (
                    <button className="b warn" onClick={() => void deskControl(d.id, { stop: true })}>{t("browser.control.stopAgent")}</button>
                  )}
                </p>
                {deskShot[d.id] ? <img className="desk-shot" src={deskShot[d.id]} alt={t("browser.desk.alt", { display: String(d.detail?.display) })} /> : <p className="browser-empty">{t("browser.desk.capturing")}</p>}
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
