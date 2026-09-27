// The floating terminal: a shell that is always one chord away (Ctrl+` by
// default) over whatever view is open, with tabs of its own. It is not tied to
// any session: its tabs are scratch shells on the default machine (or a
// chosen one), kept across reloads and listed server-side so another device
// can reopen them. On a phone it is a bottom sheet; on a desk a window that
// can be moved, resized and maximized.
import type React from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { getPref, prefsLoaded, setPref, subscribePrefs } from "../prefs/store";
import { useShortcuts } from "../shortcuts/dispatch";
import { t, useLocale } from "../i18n";
import { TerminalFrame, terminalPath, type TerminalTab } from "../terminal/TerminalTabs";
import type { SessionView } from "../types";
import type { PaneServices } from "./registry";
import { uiZoom } from "../theme/appearance";
import "./floating.css";

const STORAGE = "lec-floating-v1";
const SERVER_KEY = "floating.tabs";

interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}
interface FloatingState {
  open: boolean;
  tabs: TerminalTab[];
  active: string | null;
  box: Box | null;
  maximized: boolean;
}

function cleanTabs(value: unknown): TerminalTab[] {
  if (!Array.isArray(value)) return [];
  const out: TerminalTab[] = [];
  for (const row of value.slice(0, 12)) {
    try {
      if (!row || typeof row !== "object" || typeof row.path !== "string") continue;
      const path = terminalPath(row.path);
      if (!out.some((tab) => tab.path === path)) out.push({ path, label: String(row.label || "Shell").slice(0, 80) });
    } catch {}
  }
  return out;
}

function load(): FloatingState {
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE) || "{}");
    const tabs = cleanTabs(raw.tabs);
    const box = raw.box && ["x", "y", "w", "h"].every((key) => Number.isFinite(raw.box[key])) ? (raw.box as Box) : null;
    // It starts hidden after a reload, like any drop-down terminal; its
    // shells are still there when the chord brings it back.
    return { open: false, tabs, active: tabs.some((tab) => tab.path === raw.active) ? raw.active : tabs[0]?.path || null, box, maximized: raw.maximized === true };
  } catch {
    return { open: false, tabs: [], active: null, box: null, maximized: false };
  }
}

function clampBox(box: Box): Box {
  const w = Math.max(320, Math.min(innerWidth - 16, box.w)),
    h = Math.max(200, Math.min(innerHeight - 16, box.h));
  return { w, h, x: Math.max(8, Math.min(innerWidth - w - 8, box.x)), y: Math.max(8, Math.min(innerHeight - h - 8, box.y)) };
}

export function FloatingTerminal({ services, onDock }: {
  services: PaneServices;
  onDock: (tab: TerminalTab) => void;
}) {
  useLocale();
  const [state, setState] = useState(load),
    [mobile, setMobile] = useState(() => matchMedia("(max-width:1023px)").matches),
    [busy, setBusy] = useState(false),
    [mounted, setMounted] = useState<string[]>([]);
  const latest = useRef(state);
  latest.current = state;
  const panel = useRef<HTMLDivElement>(null);
  const commit = useCallback((next: FloatingState) => {
    latest.current = next;
    setState(next);
    try {
      localStorage.setItem(STORAGE, JSON.stringify(next));
    } catch {}
    setPref(SERVER_KEY, next.tabs as never);
  }, []);
  useEffect(() => {
    const query = matchMedia("(max-width:1023px)"), changed = () => setMobile(query.matches);
    query.addEventListener("change", changed);
    return () => query.removeEventListener("change", changed);
  }, []);
  // A device with no floating tabs of its own picks up this person's list.
  useEffect(() => {
    if (latest.current.tabs.length) return;
    const adopt = () => {
      if (!prefsLoaded() || latest.current.tabs.length) return;
      const tabs = cleanTabs(getPref<unknown>(SERVER_KEY, null));
      if (tabs.length) {
        const next = { ...latest.current, tabs, active: tabs[0]!.path };
        latest.current = next;
        setState(next);
      }
      off();
    };
    const off = subscribePrefs(adopt);
    adopt();
    return off;
  }, []);
  useEffect(() => {
    if (state.open && state.active) setMounted((old) => (old.includes(state.active!) ? old : [...old, state.active!]));
  }, [state.open, state.active]);
  useEffect(() => {
    setMounted((old) => old.filter((path) => state.tabs.some((tab) => tab.path === path)));
  }, [state.tabs]);
  // Opening focuses the shell; closing gives focus back to the page.
  useEffect(() => {
    if (!state.open) return;
    requestAnimationFrame(() => panel.current?.querySelector<HTMLIFrameElement>(".floating-body iframe:not([hidden])")?.focus());
  }, [state.open, state.active]);

  const newTab = useCallback(async () => {
    if (busy) return;
    setBusy(true);
    try {
      const shell = await services.api.request<SessionView>("/shells", { method: "POST", body: {} });
      const response = await services.api.request<{ url: string; notice?: string }>(`/sessions/${shell.id}/terminal`, { method: "POST" });
      const path = terminalPath(response.url);
      const old = latest.current;
      commit({ ...old, open: true, tabs: [...old.tabs, { path, label: shell.name || t("floating.shell") }], active: path });
      if (response.notice) services.notice(response.notice);
    } catch (error) {
      services.notice(String(error), true);
    } finally {
      setBusy(false);
    }
  }, [busy, commit, services]);
  const toggle = useCallback(() => {
    const old = latest.current;
    if (!old.open && !old.tabs.length) {
      void newTab();
      return;
    }
    commit({ ...old, open: !old.open });
  }, [commit, newTab]);
  const closeTab = useCallback((path: string) => {
    const old = latest.current, index = old.tabs.findIndex((tab) => tab.path === path);
    if (index < 0) return;
    const tabs = old.tabs.filter((tab) => tab.path !== path);
    commit({ ...old, tabs, open: old.open && tabs.length > 0, active: old.active === path ? tabs[index]?.path || tabs[index - 1]?.path || null : old.active });
  }, [commit]);
  const cycle = (delta: number) => {
    const old = latest.current, index = old.tabs.findIndex((tab) => tab.path === old.active);
    const next = old.tabs[(index + delta + old.tabs.length) % old.tabs.length];
    if (next) commit({ ...old, open: true, active: next.path });
  };
  const dock = () => {
    const old = latest.current, tab = old.tabs.find((row) => row.path === old.active);
    if (!tab) return;
    closeTab(tab.path);
    onDock(tab);
  };
  useShortcuts({
    "floating.toggle": toggle,
    "floating.newTab": () => void newTab(),
    "floating.closeTab": () => { if (latest.current.active) closeTab(latest.current.active); else return false; },
    "floating.nextTab": () => cycle(1),
    "floating.prevTab": () => cycle(-1),
    "floating.dock": dock,
    "floating.maximize": () => commit({ ...latest.current, maximized: !latest.current.maximized, open: true }),
  });
  useEffect(() => {
    if (!state.open) return;
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && panel.current?.contains(document.activeElement) && document.activeElement?.tagName !== "IFRAME") commit({ ...latest.current, open: false });
    };
    addEventListener("keydown", escape);
    return () => removeEventListener("keydown", escape);
  }, [state.open, commit]);

  const box = clampBox(state.box || { x: innerWidth - Math.min(760, innerWidth - 32) - 24, y: innerHeight - 460 - 24, w: Math.min(760, innerWidth - 32), h: 460 });
  const drag = (event: React.PointerEvent<HTMLElement>, mode: "move" | "resize") => {
    if (mobile || state.maximized || (event.target as HTMLElement).closest("button")) return;
    event.preventDefault();
    const start = { x: event.clientX, y: event.clientY, box };
    const host = panel.current;
    host?.classList.add("floating-dragging");
    const move = (moved: PointerEvent) => {
      const dx = moved.clientX - start.x, dy = moved.clientY - start.y;
      const next = mode === "move" ? { ...start.box, x: start.box.x + dx, y: start.box.y + dy } : { ...start.box, w: start.box.w + dx, h: start.box.h + dy };
      const value = { ...latest.current, box: clampBox(next) };
      latest.current = value;
      setState(value);
    };
    const stop = () => {
      removeEventListener("pointermove", move);
      removeEventListener("pointerup", stop);
      host?.classList.remove("floating-dragging");
      commit(latest.current);
    };
    addEventListener("pointermove", move);
    addEventListener("pointerup", stop);
  };
  // The box is kept in screen pixels; under UI zoom, lengths set on the page
  // are scaled, so they are divided back.
  const zoom = uiZoom();
  const style: React.CSSProperties | undefined = mobile || state.maximized ? undefined : { left: box.x / zoom, top: box.y / zoom, width: box.w / zoom, height: box.h / zoom };

  return (
    <div id="floating-terminal" ref={panel} role="dialog" aria-label={t("floating.title")} hidden={!state.open} inert={!state.open}
      className={(mobile ? "floating-sheet" : "floating-window") + (state.maximized ? " floating-maximized" : "")} style={style}>
      <div className="floating-head" onPointerDown={(event) => drag(event, "move")} onDoubleClick={() => commit({ ...state, maximized: !state.maximized })}>
        <span className="floating-title" aria-hidden="true">⌨</span>
        <div className="floating-tabs" role="tablist" aria-label={t("floating.tabs")}>
          {state.tabs.map((tab) => (
            <div className="floating-tab-item" key={tab.path}>
              <button role="tab" className="floating-tab" aria-selected={tab.path === state.active} onClick={() => commit({ ...state, active: tab.path })}>{tab.label}</button>
              <button className="floating-tab-close" aria-label={t("floating.closeTab", { label: tab.label })} onClick={() => closeTab(tab.path)}>×</button>
            </div>
          ))}
        </div>
        <button className="floating-button" aria-label={t("floating.newTab")} title={t("floating.newTab")} disabled={busy} onClick={() => void newTab()}>＋</button>
        <button className="floating-button" aria-label={t("floating.dock")} title={t("floating.dock")} disabled={!state.active} onClick={dock}>⇲</button>
        {!mobile && <button className="floating-button" aria-label={state.maximized ? t("floating.restore") : t("floating.maximize")} title={state.maximized ? t("floating.restore") : t("floating.maximize")} onClick={() => commit({ ...state, maximized: !state.maximized })}>{state.maximized ? "⤡" : "⤢"}</button>}
        <button className="floating-button" aria-label={t("floating.hide")} title={t("floating.hideHint")} onClick={() => commit({ ...state, open: false })}>▾</button>
      </div>
      <div className="floating-body">
        {state.tabs.filter((tab) => mounted.includes(tab.path)).map((tab) => (
          <div key={tab.path} className="floating-frame" hidden={tab.path !== state.active}>
            <TerminalFrame tab={tab} shown={tab.path === state.active} visible={state.open} mobile={mobile} compact={false} primary={false} relative={() => false} />
          </div>
        ))}
        {!state.tabs.length && <p className="floating-empty">{busy ? t("floating.opening") : t("floating.empty")}</p>}
      </div>
      {!mobile && !state.maximized && <div className="floating-resize" aria-hidden="true" onPointerDown={(event) => drag(event, "resize")} />}
    </div>
  );
}
