// The workspace view: every open pane — attached terminals, chats, diffs and
// whatever else registers a pane type — in one tab strip, arranged as nested
// splits. Drag a tab onto a pane's edge to split it or onto its middle to join
// it; drag a boundary to resize; maximize a pane to give it the whole view.
// On a phone the tree is kept but only the active pane shows, and the strip
// (or a swipe) switches between panes.
//
// Every pane's content lives in one flat container and is positioned over its
// slot, never re-parented, so rearranging the layout never reloads a terminal.
import { useBackClose } from "../mobile/back";
import type React from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { getPref, prefsLoaded, setPref, subscribePrefs, usePref } from "../prefs/store";
import { useShortcuts } from "../shortcuts/dispatch";
import { t, useLocale } from "../i18n";
import { visibleViewport, virtualKeyboard } from "../terminal/viewport";
import {
  bindSwipe, buttonID, hashFor, NewTerminal, terminalPath,
  type NewTerminalChoice, type TerminalMachine, type TerminalProject, type TerminalTab,
} from "../terminal/TerminalTabs";
import {
  activatePane, activePane, cleanSavedLayouts, closePane, companion, cycleGroup, cycleTab, deserialize, emptyState,
  equalize, focusDirection, focusGroup, groupOf, groups, layoutRects, moveToAdjacentGroup, movePane,
  movePaneBy, newId, normalize, nudge, openPane, pane, reopenClosed, reorderPane, serialize, splitGroup, toggleMaximize,
  unsplit, visiblePanes, type Edge, type PaneRef, type SavedLayout, type WorkspaceState,
} from "./layout";
import { checkPane, paneType, usePaneTypes, type PaneServices } from "./registry";
import { browserRef, chatRef, diffRef, installBuiltinPanes, paneSession, terminalRef } from "./builtin-panes";
import { setFilesContext } from "./files-provider";
import { filesRef, searchRef } from "../files/panes";
import "./workspace.css";

installBuiltinPanes();

const STORAGE = "lec-workspace-v1";
const OLD_STORAGE = "lec-terminal-tabs-v1";
const OLD_DURABLE = "lec-terminal-tabs-durable-v1";
const SERVER_KEY = "workspace.current";
const LAYOUTS_KEY = "workspace.layouts";
const HEAD = 30;
const NO_LAYOUTS: unknown[] = [];

// The layout this device last had; before this view existed, terminals were
// a flat tab list with an optional second pane, which becomes the same tree.
function restoreWorkspace(): WorkspaceState {
  try {
    const raw = sessionStorage.getItem(STORAGE) || localStorage.getItem(STORAGE);
    if (raw) return deserialize(JSON.parse(raw), checkPane);
    const old: unknown = JSON.parse(sessionStorage.getItem(OLD_STORAGE) || localStorage.getItem(OLD_DURABLE) || "null");
    if (!old || typeof old !== "object" || !("tabs" in old) || !Array.isArray(old.tabs)) return emptyState();
    let state = emptyState();
    for (const row of old.tabs.slice(0, 30)) {
      try {
        if (!row || typeof row !== "object" || typeof row.path !== "string") continue;
        const path = terminalPath(row.path);
        state = openPane(state, terminalRef(path, String(row.label || "Terminal").slice(0, 160)), { activate: false });
      } catch {}
    }
    const active = "active" in old && typeof old.active === "string" ? old.active : null;
    const secondary = "secondary" in old && typeof old.secondary === "string" ? old.secondary : null;
    if (secondary && pane(state, secondary) && secondary !== active) state = movePane(state, secondary, groups(state.root)[0]!.id, "right");
    if (active && pane(state, active)) state = activatePane(state, active);
    return state;
  } catch {
    return emptyState();
  }
}

function persist(state: WorkspaceState) {
  const raw = JSON.stringify(serialize(state));
  try {
    sessionStorage.setItem(STORAGE, raw);
  } catch {}
  try {
    localStorage.setItem(STORAGE, raw);
  } catch {}
}

export interface TerminalTabsController {
  state: WorkspaceState;
  tabs: TerminalTab[];
  active: string | null;
  hash: string;
  open: (url: string, label?: string) => void;
  openPane: (ref: PaneRef, options?: { beside?: string; edge?: Edge }) => void;
  select: (id: string) => void;
  close: (id: string) => void;
  update: (fn: (state: WorkspaceState) => WorkspaceState, navigate?: boolean) => void;
}

export function useTerminalTabs(onActivate: (hash: string) => void): TerminalTabsController {
  const [state, setState] = useState(restoreWorkspace);
  const latest = useRef(state),
    activate = useRef(onActivate),
    serverTimer = useRef<number | undefined>(undefined);
  latest.current = state;
  activate.current = onActivate;
  const commit = useCallback((next: WorkspaceState, navigate = true) => {
    if (next === latest.current) return;
    latest.current = next;
    setState(next);
    persist(next);
    // The server copy lets another device open the same layout; it is only
    // read when a device has none of its own.
    clearTimeout(serverTimer.current);
    serverTimer.current = window.setTimeout(() => setPref(SERVER_KEY, serialize(latest.current) as never), 1500);
    if (navigate) activate.current(hashFor(activePane(next)));
  }, []);
  const update = useCallback((fn: (state: WorkspaceState) => WorkspaceState, navigate = true) => commit(fn(latest.current), navigate), [commit]);
  // A device with no layout of its own takes the one this person last used
  // elsewhere, once, when the preferences arrive.
  useEffect(() => {
    if (latest.current.panes.length) return;
    // Listen for a short while: the stored layout may arrive after the
    // first load (another device saving it moments ago).
    const until = Date.now() + 20000;
    const adopt = () => {
      if (latest.current.panes.length || Date.now() > until) return off();
      if (!prefsLoaded()) return;
      const stored = getPref<unknown>(SERVER_KEY, null);
      if (!stored) return;
      const next = deserialize(stored, checkPane);
      if (next.panes.length) {
        commit(next, false);
        off();
      }
    };
    const off = subscribePrefs(adopt);
    adopt();
    return off;
  }, [commit]);
  const open = useCallback((url: string, label?: string) => {
    const path = terminalPath(url);
    const existing = pane(latest.current, path);
    update((old) => openPane(old, terminalRef(path, String(label || existing?.title || "Terminal").slice(0, 160))));
  }, [update]);
  const openAny = useCallback((ref: PaneRef, options: { beside?: string; edge?: Edge } = {}) => {
    const checked = checkPane(ref);
    if (!checked) return;
    update((old) => {
      const group = options.beside ? groupOf(old, options.beside)?.id : undefined;
      return openPane(old, checked, { group, edge: group ? options.edge : undefined });
    });
  }, [update]);
  const select = useCallback((id: string) => update((old) => activatePane(old, id)), [update]);
  const close = useCallback((id: string) => {
    update((old) => closePane(old, id));
    requestAnimationFrame(() => {
      const shown = activePane(latest.current);
      if (shown) document.getElementById(buttonID(shown))?.focus({ preventScroll: true });
    });
  }, [update]);
  const active = activePane(state);
  const tabs = useMemo(() => state.panes.map((row) => ({ path: row.id, label: row.title })), [state.panes]);
  return { state, tabs, active, hash: hashFor(active), open, openPane: openAny, select, close, update };
}

// ---- saved layouts --------------------------------------------------------------

function LayoutsMenu({ state, projectId, projectName, onRestore, onNotice, onPlace }: {
  onPlace: () => void;
  state: WorkspaceState;
  projectId: number | null;
  projectName: string;
  onRestore: (layout: SavedLayout) => void;
  onNotice: (text: string, error?: boolean) => void;
}) {
  const [raw] = usePref<unknown>(LAYOUTS_KEY, NO_LAYOUTS);
  const saved = cleanSavedLayouts(raw);
  const [name, setName] = useState(""),
    [scoped, setScoped] = useState(false);
  const menu = useRef<HTMLDetailsElement>(null);
  // The menu's fields exist only while it is open, so a closed menu adds
  // nothing to the page's form fields.
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (open) onPlace();
  }, [open, onPlace]);
  const shown = saved.filter((row) => row.project_id === null || row.project_id === projectId);
  const save = () => {
    const label = name.trim();
    if (!label) return;
    const row: SavedLayout = { id: newId("l"), name: label.slice(0, 80), project_id: scoped && projectId ? projectId : null, saved_at: Date.now(), layout: serialize(state) };
    // Saving under an existing name (in the same scope) replaces it.
    setPref(LAYOUTS_KEY, [row, ...saved.filter((other) => !(other.name === row.name && other.project_id === row.project_id))].slice(0, 30) as never);
    setName("");
    onNotice(t("workspace.layoutSaved", { name: row.name }));
  };
  return (
    <details className="ws-layouts" ref={menu} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary aria-label={t("workspace.layouts")} title={t("workspace.layouts")}>{t("workspace.layoutsButton")}</summary>
      {open && <div className="terminal-actions-panel ws-layouts-panel" role="menu">
        {shown.length === 0 && <p className="ws-layouts-empty">{t("workspace.noLayouts")}</p>}
        {shown.map((row) => (
          <div className="ws-layout-row" key={row.id}>
            <button role="menuitem" className="ws-layout-restore" onClick={() => { onRestore(row); if (menu.current) menu.current.open = false; }}>
              <span>{row.name}</span>
              <small>{row.project_id ? t("workspace.projectOnly") : t("workspace.anyProject")} · {row.layout.panes.length} {t("workspace.panes")}</small>
            </button>
            <button className="ws-layout-delete" aria-label={t("workspace.deleteLayout", { name: row.name })} onClick={() => setPref(LAYOUTS_KEY, saved.filter((other) => other.id !== row.id) as never)}>×</button>
          </div>
        ))}
        <form className="ws-layout-save" onSubmit={(event) => { event.preventDefault(); save(); }}>
          <input aria-label={t("workspace.layoutName")} placeholder={t("workspace.layoutName")} value={name} maxLength={80} onChange={(event) => setName(event.target.value)} />
          {projectId !== null && (
            <label className="ws-layout-scope"><input type="checkbox" checked={scoped} onChange={(event) => setScoped(event.target.checked)} /> {t("workspace.onlyFor", { project: projectName })}</label>
          )}
          <button className="b" disabled={!name.trim() || !state.panes.length}>{t("workspace.saveLayout")}</button>
        </form>
      </div>}
    </details>
  );
}

// ---- recent-tab switcher --------------------------------------------------------

function MruSwitcher({ state, index, onMove, onChoose, onCancel }: {
  state: WorkspaceState;
  index: number;
  onMove: (delta: number) => void;
  onChoose: (id: string) => void;
  onCancel: () => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const order = [...state.mru, ...state.panes.map((row) => row.id)].filter((id, i, all) => all.indexOf(id) === i && pane(state, id));
  const chosen = order[((index % order.length) + order.length) % order.length];
  useEffect(() => {
    box.current?.focus({ preventScroll: true });
  }, []);
  return (
    <div className="ws-mru" role="dialog" aria-label={t("workspace.recentTabs")} tabIndex={-1} ref={box}
      onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node)) onCancel(); }}
      onKeyDown={(event) => {
        event.preventDefault();
        event.stopPropagation();
        if (event.key === "Escape") onCancel();
        else if (event.key === "Enter" && chosen) onChoose(chosen);
        else if (event.key === "Tab" || event.key === "`" || event.key === "~") onMove(event.shiftKey ? -1 : 1);
        else if (event.key === "ArrowDown" || event.key === "ArrowRight") onMove(1);
        else if (event.key === "ArrowUp" || event.key === "ArrowLeft") onMove(-1);
      }}
      onKeyUp={(event) => {
        if ((event.key === "Control" || event.key === "Alt" || event.key === "Meta") && chosen) onChoose(chosen);
      }}>
      <p>{t("workspace.recentTabs")}</p>
      <ol>
        {order.map((id) => {
          const row = pane(state, id)!;
          return (
            <li key={id} aria-selected={id === chosen} onPointerDown={(event) => event.preventDefault()} onClick={() => onChoose(id)}>
              <span className="ws-mru-icon" aria-hidden="true">{paneType(row.kind)?.icon || "▢"}</span>
              <span>{row.title}</span>
              <small>{paneType(row.kind)?.label}</small>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

// ---- the view ------------------------------------------------------------------

const pct = (value: number) => `${(value * 100).toFixed(4)}%`;

export function TerminalTabs({ controller, visible, machines, projects, onNew, onBrowse, onSearch, switcher, services }: {
  controller: TerminalTabsController;
  visible: boolean;
  machines: TerminalMachine[];
  projects: TerminalProject[];
  onNew: (choice?: NewTerminalChoice) => Promise<void>;
  onBrowse: () => void;
  onSearch: () => void;
  switcher?: React.ReactNode;
  services: PaneServices;
}) {
  useLocale();
  usePaneTypes();
  const { state, update } = controller;
  // The Android back key restores a maximized pane before anything else.
  useBackClose(!!state.maximized, () => update((old) => toggleMaximize(old)));
  const [mobile, setMobile] = useState(() => matchMedia("(max-width:1023px)").matches),
    [compact, setCompact] = useState(() => { try { return localStorage.getItem("lec-terminal-compact") !== "0"; } catch { return true; } }),
    [mounted, setMounted] = useState<string[]>([]),
    [dragging, setDragging] = useState<string | null>(null),
    [drop, setDrop] = useState<{ group: string; edge: Edge } | null>(null),
    [insertAt, setInsertAt] = useState<number | null>(null),
    [mru, setMru] = useState<number | null>(null);
  const dragActive = useRef(false);
  const list = useRef<HTMLDivElement>(null),
    actions = useRef<HTMLDetailsElement>(null),
    panels = useRef<HTMLDivElement>(null),
    latest = useRef({ controller, visible, mobile });
  latest.current = { controller, visible, mobile };
  const active = activePane(state);
  const activeRef = pane(state, active);
  const multi = !mobile && groups(state.root).length > 1;
  const shownIds = mobile ? (active ? [active] : []) : visiblePanes(state);

  // Menus open against the visible viewport, including the keyboard's pan.
  const placeActions = useCallback(() => {
    for (const details of document.querySelectorAll<HTMLDetailsElement>("#terminal-workspace .terminal-tabbar details.terminal-actions, #terminal-workspace .terminal-tabbar details.ws-layouts")) {
      const panel = details.querySelector<HTMLElement>(".terminal-actions-panel"),
        summary = details.querySelector("summary");
      if (!details.open || !panel || !summary) continue;
      const view = window.visualViewport, usable = visibleViewport(), edge = 8, left = view?.offsetLeft ?? 0, top = usable.top, width = view?.width ?? innerWidth, height = usable.height;
      panel.style.width = Math.min(details.classList.contains("ws-layouts") ? 300 : 240, Math.max(1, width - 2 * edge)) + "px";
      panel.style.maxHeight = Math.max(1, height - 2 * edge) + "px";
      const anchor = summary.getBoundingClientRect(), box = panel.getBoundingClientRect();
      panel.style.left = Math.max(left + edge, Math.min(anchor.right - box.width, left + width - edge - box.width)) + "px";
      const below = anchor.bottom + 6, above = anchor.top - 6 - box.height;
      panel.style.top = Math.max(top + edge, Math.min(below + box.height <= top + height - edge ? below : above, top + height - edge - box.height)) + "px";
    }
  }, []);
  useEffect(() => {
    const abort = new AbortController(), signal = abort.signal;
    addEventListener("resize", placeActions, { signal });
    addEventListener("scroll", placeActions, { signal, capture: true });
    visualViewport?.addEventListener("resize", placeActions, { signal });
    visualViewport?.addEventListener("scroll", placeActions, { signal });
    virtualKeyboard()?.addEventListener("geometrychange", placeActions, { signal });
    document.addEventListener("toggle", placeActions, { signal, capture: true });
    document.addEventListener("pointerdown", (event) => {
      for (const details of document.querySelectorAll<HTMLDetailsElement>("#terminal-workspace .terminal-tabbar details.terminal-actions[open], #terminal-workspace .terminal-tabbar details.ws-layouts[open]"))
        if (event.target instanceof Node && !details.contains(event.target)) details.open = false;
    }, { signal });
    document.addEventListener("keydown", (event) => {
      const open = document.querySelector<HTMLDetailsElement>("#terminal-workspace .terminal-tabbar details.terminal-actions[open], #terminal-workspace .terminal-tabbar details.ws-layouts[open]");
      if (event.key === "Escape" && open) {
        event.preventDefault();
        open.open = false;
        open.querySelector("summary")?.focus();
      }
    }, { signal });
    return () => abort.abort();
  }, [placeActions]);
  useEffect(() => {
    if (actions.current) actions.current.open = false;
  }, [active, visible]);

  const relative = useCallback((delta: number) => {
    const value = latest.current.controller, ids = value.state.panes.map((row) => row.id), index = ids.indexOf(value.active || "");
    const next = ids[index + delta];
    if (!next) return false;
    value.select(next);
    return true;
  }, []);
  useEffect(() => {
    const query = matchMedia("(max-width:1023px)"), changed = () => setMobile(query.matches);
    query.addEventListener("change", changed);
    return () => query.removeEventListener("change", changed);
  }, []);
  const focused = mobile && compact && visible && !!active;
  useEffect(() => {
    document.body.classList.toggle("terminal-compact", focused);
    return () => document.body.classList.remove("terminal-compact");
  }, [focused]);
  // A pane's content is mounted the first time it is shown, and kept.
  useEffect(() => {
    if (!visible || !shownIds.length) return;
    setMounted((old) => (shownIds.every((id) => old.includes(id)) ? old : [...old, ...shownIds.filter((id) => !old.includes(id))]));
    if (active) document.getElementById(buttonID(active))?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [visible, shownIds.join(" "), active]);
  useEffect(() => {
    setMounted((old) => old.filter((id) => pane(state, id)));
  }, [state.panes]);
  // Focus moving into a pane's frame blurs this document; the frame the
  // browser reports as active is how a pane claims focus.
  useEffect(() => {
    const claim = () => {
      const element = document.activeElement;
      if (!(element instanceof HTMLIFrameElement)) return;
      const group = element.closest("[data-group]")?.getAttribute("data-group");
      if (group && group !== latest.current.controller.state.focus) latest.current.controller.update((old) => focusGroup(old, group), false);
    };
    addEventListener("blur", claim);
    return () => removeEventListener("blur", claim);
  }, []);
  useEffect(() => {
    const strip = list.current;
    if (!strip) return;
    return bindSwipe(strip, {
      enabled: () => latest.current.mobile && latest.current.visible && !!latest.current.controller.active,
      relative,
      width: () => strip.clientWidth || innerWidth,
      selected: () => false,
      blocked: (target) => target instanceof Element && !!target.closest('.terminal-tab-close,.terminal-actions,.terminal-search,.terminal-focus,.terminal-split,.terminal-new,a,input,select,textarea,[contenteditable="true"]'),
    });
  }, [relative]);

  // ---- resizing ----
  const resize = useCallback((event: React.PointerEvent<HTMLElement>, split: string, index: number, dir: "row" | "column", box: { x: number; y: number; w: number; h: number }) => {
    const host = panels.current;
    if (!host) return;
    event.preventDefault();
    const handle = event.currentTarget;
    handle.setPointerCapture(event.pointerId);
    host.classList.add("resizing");
    const move = (moved: PointerEvent) => {
      const rect = host.getBoundingClientRect();
      if (!rect.width || !rect.height) return;
      const fraction = dir === "row" ? ((moved.clientX - rect.left) / rect.width - box.x) / box.w : ((moved.clientY - rect.top) / rect.height - box.y) / box.h;
      latest.current.controller.update((old) => {
        const node = findSplitIn(old, split);
        if (!node) return old;
        const before = node.sizes.slice(0, index + 1).reduce((sum, size) => sum + size, 0);
        return nudge(old, split, index, fraction - before);
      }, false);
    };
    const stop = () => {
      removeEventListener("pointermove", move);
      removeEventListener("pointerup", stop);
      removeEventListener("pointercancel", stop);
      host.classList.remove("resizing");
      try { handle.releasePointerCapture(event.pointerId); } catch {}
    };
    addEventListener("pointermove", move);
    addEventListener("pointerup", stop);
    addEventListener("pointercancel", stop);
  }, []);

  // ---- drag and drop ----
  const edgeAt = (event: React.DragEvent<HTMLElement>): Edge => {
    const box = event.currentTarget.getBoundingClientRect();
    const x = (event.clientX - box.left) / box.width, y = (event.clientY - box.top) / box.height;
    const distances: [Edge, number][] = [["left", x], ["right", 1 - x], ["top", y], ["bottom", 1 - y]];
    const [edge, distance] = distances.sort((a, b) => a[1] - b[1])[0]!;
    return distance < 0.25 ? edge : "center";
  };
  const startDrag = (event: React.DragEvent<HTMLElement>, id: string) => {
    if (mobile) return;
    event.dataTransfer.setData("application/x-lectern-pane", id);
    event.dataTransfer.setData("text/plain", pane(state, id)?.title || id);
    event.dataTransfer.effectAllowed = "move";
    // Rendering the drop targets in this same frame cancels the drag in
    // Chromium, so they appear on the next one — unless it already ended.
    dragActive.current = true;
    requestAnimationFrame(() => {
      if (dragActive.current) setDragging(id);
    });
  };
  const endDrag = () => {
    dragActive.current = false;
    setDragging(null);
    setDrop(null);
    setInsertAt(null);
  };
  // A drag that ends anywhere (Escape, a drop outside, a lost source) clears
  // the drop targets, which would otherwise cover the panes.
  useEffect(() => {
    if (!dragging) return;
    const clear = () => endDrag();
    document.addEventListener("dragend", clear, true);
    document.addEventListener("drop", clear);
    document.addEventListener("pointerdown", clear, true);
    return () => {
      document.removeEventListener("dragend", clear, true);
      document.removeEventListener("drop", clear);
      document.removeEventListener("pointerdown", clear, true);
    };
  }, [dragging]);
  const draggedId = (event: React.DragEvent) => event.dataTransfer.getData("application/x-lectern-pane") || dragging;

  // ---- shortcuts ----
  // An action with nothing to do lets its key through (Alt+Shift+Arrow also
  // reorders a focused tab when there is no pane in that direction).
  const layoutAction = (fn: (old: WorkspaceState) => WorkspaceState) => () => {
    const next = fn(latest.current.controller.state);
    if (next === latest.current.controller.state) return false;
    update(() => next);
  };
  const shownSession = paneSession(activeRef);
  const session = services.sessions.find((row) => row.id === shownSession);
  useEffect(() => {
    setFilesContext(shownSession ? { session: shownSession, name: session?.name || "", focused: active || "", mobile, open: (ref, options) => services.openPane(ref, options) } : null);
  }, [shownSession, services, active, mobile, session?.name]);
  useShortcuts({
    "workspace.splitRight": layoutAction((old) => splitGroup(old, "row")),
    "workspace.splitDown": layoutAction((old) => splitGroup(old, "column")),
    "workspace.closePane": () => { if (active) controller.close(active); },
    "workspace.reopenClosed": layoutAction(reopenClosed),
    "workspace.maximize": layoutAction((old) => toggleMaximize(old)),
    "workspace.focusLeft": layoutAction((old) => focusDirection(old, "left")),
    "workspace.focusRight": layoutAction((old) => focusDirection(old, "right")),
    "workspace.focusUp": layoutAction((old) => focusDirection(old, "up")),
    "workspace.focusDown": layoutAction((old) => focusDirection(old, "down")),
    "workspace.focusNextGroup": layoutAction((old) => cycleGroup(old, 1)),
    "workspace.focusPrevGroup": layoutAction((old) => cycleGroup(old, -1)),
    "workspace.nextTab": layoutAction((old) => cycleTab(old, 1)),
    "workspace.prevTab": layoutAction((old) => cycleTab(old, -1)),
    "workspace.mruNext": () => { if (state.panes.length < 2) return false; setMru((old) => (old === null ? 1 : old + 1)); },
    "workspace.mruPrev": () => { if (state.panes.length < 2) return false; setMru((old) => (old === null ? -1 : old - 1)); },
    "workspace.moveTabLeft": () => { if (active) update((old) => movePaneBy(old, active, -1)); },
    "workspace.moveTabRight": () => { if (active) update((old) => movePaneBy(old, active, 1)); },
    "workspace.moveToNextGroup": layoutAction((old) => moveToAdjacentGroup(old, 1)),
    "workspace.moveToPrevGroup": layoutAction((old) => moveToAdjacentGroup(old, -1)),
    "workspace.equalize": layoutAction(equalize),
    "workspace.unsplit": layoutAction(unsplit),
    "workspace.saveLayout": () => openLayouts(true),
    "workspace.layouts": () => openLayouts(false),
    "workspace.chatBeside": () => { if (session && active) controller.openPane(chatRef(session.id, session.name), { beside: active, edge: "right" }); else return false; },
    "workspace.browserBeside": () => { if (session && active) controller.openPane(browserRef(session.id, session.name), { beside: active, edge: "right" }); else return false; },
    "workspace.diffBeside": () => { if (session && active) controller.openPane(diffRef(session.id, session.name), { beside: active, edge: "right" }); else return false; },
    "workspace.filesBeside": () => { if (session && active) controller.openPane(filesRef(session.id, session.name), { beside: active, edge: "right" }); else return false; },
    "workspace.searchBeside": () => { if (session && active) controller.openPane(searchRef(session.id, session.name), { beside: active, edge: "right" }); else return false; },
    ...Object.fromEntries([1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => [`workspace.tab${n}`, () => {
      const target = n === 9 ? state.panes.at(-1) : state.panes[n - 1];
      if (target) controller.select(target.id);
      else return false;
    }])),
  }, visible);
  function openLayouts(focusName: boolean) {
    const details = document.querySelector<HTMLDetailsElement>("#terminal-workspace .ws-layouts");
    if (!details) return;
    details.open = true;
    placeActions();
    setTimeout(() => (focusName ? details.querySelector<HTMLInputElement>(".ws-layout-save input") : details.querySelector<HTMLElement>("button"))?.focus(), 60);
  }

  const restoreLayout = (row: SavedLayout) => {
    // Terminals of sessions that have since ended are left out.
    const live = new Set(services.sessions.filter((s) => !s.ended_at && s.status !== "dead").map((s) => s.id));
    const next = deserialize(row.layout, (ref) => {
      const checked = checkPane(ref);
      if (!checked) return null;
      const id = paneSession(checked);
      return id && services.sessions.length && !live.has(id) && checked.kind === "terminal" ? null : checked;
    });
    if (!next.panes.length) {
      services.notice(t("workspace.layoutEmpty"), true);
      return;
    }
    // Panes already open that the layout does not name stay open, in the
    // focused group, so restoring never closes someone's work.
    const kept = state.panes.filter((ref) => !pane(next, ref.id));
    update(() => normalize({ ...next, panes: [...next.panes, ...kept] }));
    services.notice(t("workspace.layoutRestored", { name: row.name }));
  };

  const { groups: rects, handles } = useMemo(() => layoutRects(state.root), [state.root]);
  const groupFor = useMemo(() => {
    const map = new Map<string, string>();
    for (const group of groups(state.root)) for (const id of group.panes) map.set(id, group.id);
    return map;
  }, [state.root]);
  const rectOf = (groupId: string | undefined) => {
    if (mobile) return { x: 0, y: 0, w: 1, h: 1 };
    if (state.maximized) return groupId === state.maximized ? { x: 0, y: 0, w: 1, h: 1 } : undefined;
    return groupId ? rects.get(groupId) : undefined;
  };
  const splitAvailable = !!companion(state) && state.panes.length > 1;
  const project = session?.project_id ?? null;
  const projectName = session?.project_name || "";
  const popout = activeRef ? paneType(activeRef.kind)?.popout?.(activeRef) : undefined;
  const splitCount = groups(state.root).length;

  return (
    <section id="terminal-workspace" hidden={!visible} inert={!visible} className={dragging ? "ws-dragging" : undefined}>
      <div className="terminal-tabbar">
        <div className="terminal-tablist" ref={list} role="tablist" aria-multiselectable={multi || undefined} aria-label={t("workspace.openTabs")}>
          {state.panes.map((tab, index) => {
            const duplicate = state.panes.filter((other) => other.kind === tab.kind && other.title === tab.title).length > 1;
            const label = tab.title + (duplicate ? " #" + (tab.id.split(/[/:]/).at(-1) || "") : "");
            const shown = shownIds.includes(tab.id);
            const type = paneType(tab.kind);
            const closeLabel = tab.kind === "terminal" ? t("workspace.closeView", { label }) : t("workspace.closeOther", { kind: type?.label || tab.kind, label });
            return (
              <div className={"terminal-tab-item" + (insertAt === index ? " ws-insert-before" : "")} role="presentation" key={tab.id} data-pane-kind={tab.kind}
                onDragOver={(event) => { if (!dragging) return; event.preventDefault(); const box = event.currentTarget.getBoundingClientRect(); setInsertAt(event.clientX < box.left + box.width / 2 ? index : index + 1); setDrop(null); }}
                onDrop={(event) => { const id = draggedId(event); if (!id) return; event.preventDefault(); const at = insertAt ?? index; const from = state.panes.findIndex((row) => row.id === id); update((old) => reorderPane(old, id, from < at ? at - 1 : at)); endDrag(); }}>
                <button className="terminal-tab" id={buttonID(tab.id)} role="tab" draggable={!mobile}
                  aria-label={tab.kind === "terminal" ? undefined : `${type?.label || tab.kind}: ${label}`}
                  data-group={multi ? groupFor.get(tab.id) : undefined}
                  title={label + (multi && shown ? " — " + t("workspace.showing") : "")}
                  aria-selected={shown || tab.id === active} aria-controls={buttonID(tab.id) + "-panel"} tabIndex={tab.id === active ? 0 : -1}
                  onDragStart={(event) => startDrag(event, tab.id)} onDragEnd={endDrag}
                  onClick={() => controller.select(tab.id)}
                  onKeyDown={(event) => {
                    let next: PaneRef | undefined;
                    if (event.altKey && event.shiftKey && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
                      event.preventDefault();
                      update((old) => reorderPane(old, tab.id, index + (event.key === "ArrowRight" ? 1 : -1)));
                      requestAnimationFrame(() => document.getElementById(buttonID(tab.id))?.focus({ preventScroll: true }));
                      return;
                    }
                    if (event.key === "ArrowRight") next = state.panes[(index + 1) % state.panes.length];
                    if (event.key === "ArrowLeft") next = state.panes[(index + state.panes.length - 1) % state.panes.length];
                    if (event.key === "Home") next = state.panes[0];
                    if (event.key === "End") next = state.panes.at(-1);
                    if (event.key === "Delete") { event.preventDefault(); controller.close(tab.id); return; }
                    if (next) { event.preventDefault(); controller.select(next.id); document.getElementById(buttonID(next.id))?.focus({ preventScroll: true }); }
                  }}>
                  {tab.kind !== "terminal" && <span className="ws-tab-icon" aria-hidden="true">{type?.icon} </span>}
                  {label}
                </button>
                <button className="terminal-tab-close" aria-label={closeLabel} title={t("workspace.closeViewHint")} onClick={() => controller.close(tab.id)}>×</button>
              </div>
            );
          })}
        </div>
        {switcher}
        <NewTerminal machines={machines} projects={projects} onNew={onNew} />
        <button className="b terminal-split" aria-pressed={multi} hidden={!active || mobile} disabled={!multi && !splitAvailable}
          title={!multi && !splitAvailable ? t("workspace.splitNeedsTwo") : multi ? t("workspace.unsplitHint") : t("workspace.splitHint")}
          onClick={() => update((old) => (groups(old.root).length > 1 ? unsplit(old) : splitGroup(old, "row")))}>
          {multi ? t("workspace.unsplit") : t("workspace.split")}
        </button>
        {!mobile && <LayoutsMenu state={state} projectId={project} projectName={projectName} onRestore={restoreLayout} onNotice={services.notice} onPlace={placeActions} />}
        <a className="b terminal-popout" target="_blank" rel="noopener" title={t("workspace.popoutHint")} hidden={!popout} href={popout}>{t("workspace.popout")}</a>
        <button className="terminal-search" aria-label={t("shell.searchLabel")} title={t("shell.searchLabel")} onClick={onSearch}>⌕</button>
        <details className="terminal-actions" ref={actions} hidden={!active} onToggle={placeActions}>
          <summary aria-label={t("workspace.actions")} title={t("workspace.actions")} onClick={(event) => { event.preventDefault(); if (actions.current) { actions.current.open = !actions.current.open; placeActions(); } }}>⋯</summary>
          <div className="terminal-actions-panel" role="menu" onClick={(event) => { if (event.target instanceof Element && event.target.closest("button,a") && actions.current) actions.current.open = false; }}>
            {popout && <a className="terminal-menu-popout" target="_blank" rel="noopener" role="menuitem" href={popout}>{t("workspace.openInNewTab")}</a>}
            <button className="terminal-menu-close" role="menuitem" onClick={() => { if (active) controller.close(active); }}>{t("workspace.closeThisView")}</button>
            {session && activeRef?.kind !== "chat" && <button role="menuitem" onClick={() => active && controller.openPane(chatRef(session.id, session.name), { beside: active, edge: mobile ? "center" : "right" })}>{t("workspace.chatBeside")}</button>}
            {session && activeRef?.kind !== "diff" && <button role="menuitem" onClick={() => active && controller.openPane(diffRef(session.id, session.name), { beside: active, edge: mobile ? "center" : "right" })}>{t("workspace.diffBeside")}</button>}
            {session && activeRef?.kind !== "browser" && <button role="menuitem" onClick={() => active && controller.openPane(browserRef(session.id, session.name), { beside: active, edge: mobile ? "center" : "right" })}>{t("workspace.browserBeside")}</button>}
            {session && activeRef?.kind !== "files" && <button role="menuitem" onClick={() => active && controller.openPane(filesRef(session.id, session.name), { beside: active, edge: mobile ? "center" : "right" })}>{t("workspace.filesBeside")}</button>}
            {session && activeRef?.kind !== "search" && <button role="menuitem" onClick={() => active && controller.openPane(searchRef(session.id, session.name), { beside: active, edge: mobile ? "center" : "right" })}>{t("workspace.searchBeside")}</button>}
            {!mobile && splitAvailable && <button role="menuitem" onClick={() => update((old) => splitGroup(old, "row"))}>{t("workspace.splitRight")}</button>}
            {!mobile && splitAvailable && <button role="menuitem" onClick={() => update((old) => splitGroup(old, "column"))}>{t("workspace.splitDown")}</button>}
            {!mobile && splitCount > 1 && <button role="menuitem" onClick={() => update((old) => toggleMaximize(old))}>{state.maximized ? t("workspace.restorePanes") : t("workspace.maximize")}</button>}
            {!mobile && splitCount > 1 && <button role="menuitem" onClick={() => update(equalize)}>{t("workspace.equalize")}</button>}
            {state.closed.length > 0 && <button role="menuitem" onClick={() => update(reopenClosed)}>{t("workspace.reopen", { name: state.closed[0]!.title })}</button>}
            {mobile && <LayoutsSheetButton onOpen={() => openLayouts(false)} />}
          </div>
        </details>
        {mobile && <LayoutsMenu state={state} projectId={project} projectName={projectName} onRestore={restoreLayout} onNotice={services.notice} onPlace={placeActions} />}
        <button className="terminal-focus" aria-label={focused ? t("workspace.showNavigation") : t("workspace.focusTerminal")} title={focused ? t("workspace.showNavigation") : t("workspace.focusTerminal")} aria-pressed={focused} hidden={!active}
          onClick={() => { setCompact(!compact); try { localStorage.setItem("lec-terminal-compact", compact ? "0" : "1"); } catch {} }}>{focused ? "☰" : "⤢"}</button>
      </div>
      <div className={"terminal-panels" + (multi ? " split" : "")} ref={panels} hidden={!state.panes.length}>
        {multi && groups(state.root).map((group) => {
          const rect = rectOf(group.id);
          if (!rect) return null;
          const focusedHere = group.id === state.focus;
          return (
            <div key={group.id} className={"ws-group-head" + (focusedHere ? " focused" : "")} data-group-head={group.id}
              style={{ left: pct(rect.x), top: pct(rect.y), width: pct(rect.w), height: HEAD }}
              onPointerDown={() => { if (!focusedHere) update((old) => focusGroup(old, group.id), false); }}
              onDoubleClick={(event) => { if (event.target === event.currentTarget) update((old) => toggleMaximize(old, group.id)); }}>
              <div className="ws-chips">
                {group.panes.map((id) => {
                  const row = pane(state, id)!;
                  return (
                    <button key={id} className="ws-chip" aria-pressed={group.active === id} draggable
                      onDragStart={(event) => startDrag(event, id)} onDragEnd={endDrag}
                      onClick={() => controller.select(id)} title={row.title}>
                      <span aria-hidden="true">{paneType(row.kind)?.icon} </span>{row.title}
                    </button>
                  );
                })}
              </div>
              <button className="ws-head-button" aria-label={state.maximized === group.id ? t("workspace.restorePanes") : t("workspace.maximizePane")} title={state.maximized === group.id ? t("workspace.restorePanes") : t("workspace.maximizePane")}
                onClick={() => update((old) => toggleMaximize(old, group.id))}>{state.maximized === group.id ? "⤡" : "⤢"}</button>
              {group.active && <button className="ws-head-button" aria-label={t("workspace.closePaneTab", { label: pane(state, group.active)?.title || "" })} title={t("workspace.closeViewHint")} onClick={() => controller.close(group.active!)}>×</button>}
            </div>
          );
        })}
        {state.panes.filter((row) => mounted.includes(row.id) || (visible && shownIds.includes(row.id))).map((row) => {
          const groupId = groupFor.get(row.id);
          const shown = shownIds.includes(row.id);
          const rect = rectOf(groupId) || { x: 0, y: 0, w: 1, h: 1 };
          const head = multi ? HEAD : 0;
          const type = paneType(row.kind);
          const primary = row.id === active;
          return (
            <div key={row.id} className="terminal-tabpanel ws-pane" id={buttonID(row.id) + "-panel"} role="tabpanel" aria-labelledby={buttonID(row.id)}
              data-group={groupId} data-pane-kind={row.kind} data-focused={multi ? groupId === state.focus : undefined}
              hidden={!shown} inert={!shown || !visible}
              style={{ left: pct(rect.x), top: `calc(${pct(rect.y)} + ${head}px)`, width: pct(rect.w), height: `calc(${pct(rect.h)} - ${head}px)` }}
              onPointerDownCapture={() => { if (groupId && groupId !== state.focus) update((old) => focusGroup(old, groupId), false); }}>
              {type ? type.render({
                pane: row, shown, visible, focused: groupId === state.focus, primary, mobile, compact, services, relative,
                close: () => controller.close(row.id),
                focus: () => { const group = groupFor.get(row.id); if (group && group !== latest.current.controller.state.focus) latest.current.controller.update((old) => focusGroup(old, group), false); },
              }) : <p className="ws-missing">{t("workspace.unknownPane", { kind: row.kind })}</p>}
            </div>
          );
        })}
        {multi && !state.maximized && handles.map((handle) => {
          const node = findSplitIn(state, handle.split);
          const before = node ? node.sizes.slice(0, handle.index + 1).reduce((sum, size) => sum + size, 0) : 0.5;
          const style: React.CSSProperties = handle.dir === "row"
            ? { left: pct(handle.rect.x), top: pct(handle.rect.y), height: pct(handle.rect.h) }
            : { top: pct(handle.rect.y), left: pct(handle.rect.x), width: pct(handle.rect.w) };
          return (
            <div key={handle.split + ":" + handle.index} className={"ws-handle ws-handle-" + handle.dir} role="separator"
              aria-label={t("workspace.resize")} aria-orientation={handle.dir === "row" ? "vertical" : "horizontal"}
              aria-valuenow={Math.round(before * 100)} aria-valuemin={0} aria-valuemax={100} tabIndex={0} style={style}
              onPointerDown={(event) => resize(event, handle.split, handle.index, handle.dir, handle.box)}
              onDoubleClick={() => update(equalize, false)}
              onKeyDown={(event) => {
                const back = handle.dir === "row" ? "ArrowLeft" : "ArrowUp", forward = handle.dir === "row" ? "ArrowRight" : "ArrowDown";
                if (event.key !== back && event.key !== forward) return;
                event.preventDefault();
                update((old) => nudge(old, handle.split, handle.index, event.key === back ? -0.02 : 0.02), false);
              }} />
          );
        })}
        {dragging && !mobile && groups(state.root).map((group) => {
          const rect = rectOf(group.id);
          if (!rect) return null;
          const target = drop?.group === group.id ? drop.edge : null;
          return (
            <div key={"drop-" + group.id} className="ws-drop" data-drop-group={group.id}
              style={{ left: pct(rect.x), top: pct(rect.y), width: pct(rect.w), height: pct(rect.h) }}
              onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = "move"; const edge = edgeAt(event); setInsertAt(null); if (drop?.group !== group.id || drop.edge !== edge) setDrop({ group: group.id, edge }); }}
              onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDrop(null); }}
              onDrop={(event) => { event.preventDefault(); const id = draggedId(event); const edge = edgeAt(event); if (id) update((old) => movePane(old, id, group.id, edge)); endDrag(); }}>
              {target && <div className={"ws-drop-preview ws-drop-" + target} />}
            </div>
          );
        })}
      </div>
      <div className="terminal-empty" hidden={!!state.panes.length}>
        <h2>{t("workspace.emptyTitle")}</h2>
        <p>{t("workspace.emptyBody")}</p>
        <div className="terminal-empty-actions">
          <NewTerminal machines={machines} projects={projects} onNew={onNew} />
          <button className="b" onClick={onBrowse}>{t("workspace.openSessions")}</button>
        </div>
      </div>
      {mru !== null && (
        <MruSwitcher state={state} index={mru} onMove={(delta) => setMru((old) => (old ?? 0) + delta)}
          onCancel={() => setMru(null)}
          onChoose={(id) => { setMru(null); controller.select(id); }} />
      )}
    </section>
  );
}

function LayoutsSheetButton({ onOpen }: { onOpen: () => void }) {
  return <button role="menuitem" onClick={() => requestAnimationFrame(onOpen)}>{t("workspace.layouts")}</button>;
}

function findSplitIn(state: WorkspaceState, id: string) {
  const walk = (node: WorkspaceState["root"]): Extract<WorkspaceState["root"], { type: "split" }> | undefined =>
    node.type === "group" ? undefined : node.id === id ? node : node.children.map(walk).find(Boolean);
  return walk(state.root);
}


