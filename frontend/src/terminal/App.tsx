import { subscribeLayout, subscribeViewport } from "./layout";
import { applyVisibleHeight, localViewportSlice, virtualKeyboard } from "./viewport";
import { placeSheet } from "./sheet";
import { buildKeyboardReport, formatKeyboardReport, rectSnapshot } from "./keyboard-report";
import { errorMessage } from "./model";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Engine, type Snapshot } from "./engine";
import { type Mods } from "./keys";
import { Keybar, KeybarEditor } from "./Keybar";
import type { TerminalLink } from "./links";
import { LiveSelectionBar } from "./SelectionBar";
import { openExternal } from "../mobile/open";
import { defaultKeybar, loadKeybar, saveKeybar } from "./keybar";
import {
  Appearance,
  Desktop,
  Dialog,
  HistoryDialog,
  Snippets,
} from "./dialogs";
import { Workbench, type FileRequest } from "../files/Workbench";
import { FileApi, isOutside } from "../files/api";
import { existenceCheck } from "../files/linkCheck";
import { LinkMenu } from "./LinkMenu";
import { ApprovalStrip } from "./ApprovalStrip";
import {
  copyClipboard,
  downloadBlob,
  shellPath,
  FONT_MAX,
  FONT_MIN,
  json,
  loadPrefs,
  quote,
  request,
  type Attachment,
  type Prefs,
  type TerminalInfo,
} from "./model";
import { GLOBAL_KEY, commandBytes, commandsFor, projectKey, readGlobal, writeScope } from "../quick/commands";
import { usePluginContributions } from "../plugins/contributions";
import { getPref, usePref } from "../prefs/store";
import { resolveTerminalTheme, saveTerminalPrefs, useTerminalPrefs, readTerminalPrefs } from "../theme/terminal-prefs";
import { allTerminalThemes } from "../theme/terminal-themes";
import { customThemes } from "../theme/terminal-prefs";
import { installShortcutListener, useShortcuts, chordsFor } from "../shortcuts/dispatch";
import { displayChord } from "../shortcuts/chords";
import { t, useLocale } from "../i18n";
import { useAppearance } from "../theme/appearance";
import { resolveMode } from "../theme/app-theme";
import "@xterm/xterm/css/xterm.css";
import "./terminal.css";
import "./approval-strip.css";
export type SharedTool = "review" | "saved" | "search";
interface PaneSpec {
  id: string;
  url: string;
  label: string;
}
interface Callbacks {
  engine: (id: string, engine: Engine | null) => void;
  state: (id: string, state: Snapshot) => void;
  select: (id: string) => void;
  notice: (text: string) => void;
  history: (id: string) => void;
  controls: () => void;
  matches: (id: string, index: number, count: number) => void;
  preview: (path: string, line?: number, column?: number) => void;
  swipe: (id: string, direction: 1 | -1) => void;
  link: (link: TerminalLink) => void;
  linkMenu: (id: string, link: TerminalLink, point: { x: number; y: number }) => void;
  linkExists: (path: string) => boolean | Promise<boolean>;
  clipboard: (text: string) => void;
}
function Pane({
  spec,
  prefs,
  active,
  info,
  callbacks,
}: {
  spec: PaneSpec;
  prefs: Prefs;
  active: boolean;
  info: TerminalInfo;
  callbacks: Callbacks;
}) {
  const el = useRef<HTMLElement>(null),
    host = useRef<HTMLDivElement>(null),
    frozen = useRef<HTMLPreElement>(null),
    engine = useRef<Engine | null>(null);
  const latest = useRef({ prefs, callbacks });
  latest.current = { prefs, callbacks };
  const [state, setState] = useState<Snapshot>({
    connected: false,
    paused: false,
    status: t("terminalPage.status.connecting"),
    frozen: "",
    retained: false,
    unresponsive: false,
    offline: false,
    selection: false,
  });
  useEffect(() => {
    if (!el.current || !host.current || !frozen.current) return;
    const instance = new Engine({
      url: spec.url,
      host: host.current,
      pane: el.current,
      frozen: frozen.current,
      prefs: () => latest.current.prefs,
      select: () => latest.current.callbacks.select(spec.id),
      change: (state) => {
        setState(state);
        latest.current.callbacks.state(spec.id, state);
      },
      notice: (text) => latest.current.callbacks.notice(text),
      history: () => latest.current.callbacks.history(spec.id),
      controls: () => latest.current.callbacks.controls(),
      swipe: (direction) => latest.current.callbacks.swipe(spec.id, direction),
      openLink: (link) => latest.current.callbacks.link(link),
      linkMenu: (link, point) => latest.current.callbacks.linkMenu(spec.id, link, point),
      linkExists: (path) => latest.current.callbacks.linkExists(path),
      workdir: () => (info.files_available ? info.workdir : ""),
      matches: (index, count) =>
        latest.current.callbacks.matches(spec.id, index, count),
      osc52: () => readTerminalPrefs().osc52,
      extendedKeys: () => readTerminalPrefs().extendedKeys,
      clipboard: (text) => latest.current.callbacks.clipboard(text),
    });
    engine.current = instance;
    latest.current.callbacks.engine(spec.id, instance);
    return () => {
      instance.dispose();
      latest.current.callbacks.engine(spec.id, null);
      engine.current = null;
    };
  }, [spec.id, spec.url, info.files_available, info.workdir]);
  useEffect(() => {
    engine.current?.applyPrefs();
  }, [prefs]);
  useLayoutEffect(() => {
    engine.current?.syncFrozenLayout();
  }, [state.retained, state.frozen]);
  // A paused view opens where the live one was: at the latest output.
  useLayoutEffect(() => {
    if (state.paused && !state.retained && frozen.current)
      frozen.current.scrollTop = frozen.current.scrollHeight;
  }, [state.paused, state.retained]);
  return (
    <section
      ref={el}
      id={spec.id === "agent" ? "agent-pane" : "shell-pane"}
      className={`pane ${active ? "active" : ""} ${state.connected ? "connected" : ""}`}
      onPointerDown={() => callbacks.select(spec.id)}
      onFocus={() => callbacks.select(spec.id)}
    >
      <div className="pane-title">
        <span id={spec.id === "agent" ? "agent-label" : undefined}>
          {spec.label}
        </span>
        <span className="pane-status">{state.status}</span>
      </div>
      <div
        id={spec.id === "agent" ? "agent-terminal" : "shell-terminal"}
        className="terminal-host"
        ref={host}
        hidden={state.paused}
        style={{
          background: resolveTerminalTheme(prefs.theme).background,
        }}
      />
      <pre
        className={`frozen ${state.retained ? "retained-history" : ""}`}
        ref={frozen}
        hidden={!state.paused && !state.retained}
        style={{ fontSize: prefs.fontSize }}
        onScroll={(event) => {
          const node = event.currentTarget;
          if (
            state.retained &&
            node.scrollHeight - node.clientHeight - node.scrollTop <= 2
          )
            engine.current?.leaveRetainedHistory();
        }}
        onClick={() => {
          if (state.retained && !window.getSelection()?.toString())
            engine.current?.term.focus();
        }}
      >
        {state.frozen}
      </pre>
    </section>
  );
}
declare global {
  interface Window {
    __lecTerminalState?: () => {
      hasSelection: boolean;
      mouseTrackingMode: string;
      // What the buffer still holds, for the tests that prove a reconnect
      // keeps the session's output instead of wiping the screen.
      bufferLines: number;
      bufferContains: (text: string) => boolean;
      // Where on screen text is drawn (its first character, bottom-most
      // occurrence on the visible screen), for tests that touch output.
      pointOf: (text: string) => { x: number; y: number } | null;
      selection: string;
    };
  }
}
export function TerminalApp({
  kind,
  id,
  onShared,
  externalNotice = "",
}: {
  kind: string;
  id: string;
  onShared: (tool: SharedTool, info: TerminalInfo) => void;
  externalNotice?: string;
}) {
  const base = `/api/term/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`;
  useLocale();
  const embedded = new URLSearchParams(location.search).get("embed") === "1";
  const [info, setInfo] = useState<TerminalInfo>();
  const [infoAttempt, setInfoAttempt] = useState(0);
  const [prefs, setPrefs] = useState(loadPrefs);
  const [active, setActive] = useState("agent");
  const [split, setSplit] = useState(false);
  const [notice, setNotice] = useState("");
  const [uploading, setUploading] = useState(0);
  const [dialog, setDialog] = useState<
    "appearance" | "history" | "desktop" | "snippets" | "compose" | "keyboard-report" | "keybar" | null
  >(null);
  const [keyboardReport, setKeyboardReport] = useState("");
  const [keybar, setKeybar] = useState(loadKeybar);
  const [projectId, setProjectId] = useState<number | null>(kind === "project" && /^\d+$/.test(id) ? Number(id) : null);
  // The key row's quick-command keys come from the same server-stored list
  // as the Snippets sheet; re-read when it changes on any device.
  const [quickGlobal] = usePref<unknown>(GLOBAL_KEY, undefined);
  const [quickProject] = usePref<unknown>(projectId ? projectKey(projectId) : "quick-commands:none", undefined);
  const pluginUI = usePluginContributions();
  const quick = useMemo(() => commandsFor(projectId).map((row) => row.command), [quickGlobal, quickProject, projectId, pluginUI]);
  const openKeybar = () => {
    // Commands carried over from before they were stored on the server get
    // their ids when saved; save them now so a key can name one.
    if (getPref<unknown>(GLOBAL_KEY, undefined) === undefined) writeScope({ kind: "global" }, readGlobal());
    setDialog("keybar");
  };
  const [draft, setDraft] = useState("");
  const [keyboardFocused, setKeyboardFocused] = useState(false);
  const [historyPane, setHistoryPane] = useState("agent");
  // Workspace files beside the terminal (files/Workbench.tsx).
  const [filesOpen, setFilesOpen] = useState(false);
  const [fileRequest, setFileRequest] = useState<FileRequest>();
  const [fileCommand, setFileCommand] = useState<{ name: "quick" | "search"; nonce: number }>();
  const [search, setSearch] = useState(false);
  const [toolsOpen, setToolsOpen] = useState(false);
  const [mobile, setMobile] = useState(false);
  const [mods, setMods] = useState<Mods>({ ctrl: false, alt: false });
  const modsRef = useRef(mods);
  modsRef.current = mods;
  const [query, setQuery] = useState("");
  const [matches, setMatches] = useState("");
  const [states, setStates] = useState<Record<string, Snapshot>>({});
  const engines = useRef(new Map<string, Engine>()),
    activeRef = useRef(active),
    infoRef = useRef(info),
    file = useRef<HTMLInputElement>(null),
    searchInput = useRef<HTMLInputElement>(null),
    tools = useRef<HTMLDetailsElement>(null);
  activeRef.current = active;
  infoRef.current = info;
  useEffect(() => {
    if (externalNotice) setNotice(externalNotice);
  }, [externalNotice]);
  const current = () => engines.current.get(activeRef.current);
  const state = states[active];
  const [unresponsiveDismissed, setUnresponsiveDismissed] = useState(false);
  const [reviving, setReviving] = useState(false);
  // A dismissed warning comes back if the agent answers and then hangs again.
  useEffect(() => {
    if (!state?.unresponsive) setUnresponsiveDismissed(false);
  }, [state?.unresponsive]);
  // An agent that exited leaves this terminal at a shell prompt. The server's
  // probe notices; the terminal asks every ten seconds while it is visible.
  const [agentExited, setAgentExited] = useState(false);
  const [exitDismissed, setExitDismissed] = useState(false);
  useEffect(() => {
    if (kind !== "session") return;
    let stopped = false;
    const check = async () => {
      if (document.hidden) return;
      try {
        const response = await request(`/api/sessions/${encodeURIComponent(id)}`);
        const row = (await response.json()) as { agent_exited_at?: number | null; ended_at?: number | null; project_id?: number | null };
        if (!stopped) {
          setAgentExited(!!row.agent_exited_at && !row.ended_at);
          setProjectId(row.project_id ?? null);
        }
      } catch {}
    };
    void check();
    const timer = window.setInterval(() => void check(), 10000);
    return () => { stopped = true; clearInterval(timer); };
  }, [kind, id]);
  useEffect(() => {
    if (!agentExited) setExitDismissed(false);
  }, [agentExited]);
  async function revive() {
    setReviving(true);
    try {
      const response = await request(`/api/sessions/${encodeURIComponent(id)}/revive`, { method: "POST", body: "{}", headers: { "Content-Type": "application/json" } });
      const next = (await response.json()) as { id: number };
      location.replace(`/terminal/session/${next.id}${location.search}`);
    } catch (error) {
      setNotice(t("terminalPage.notice.reviveFailed", { error: (error as Error).message }));
      setReviving(false);
    }
  }
  // The terminal is drawn at the size for the device it is on; the two are
  // stored apart so resizing on the phone never shrinks the desk.
  // The scheme and spacing chosen for this person (Settings → Workspace &
  // terminal) win over what this device last had.
  const person = useTerminalPrefs();
  // With no scheme chosen anywhere, the terminal follows the app: the default
  // dark scheme in the dark theme, Paper in the light one.
  const appearance = useAppearance();
  const appMode = resolveMode(appearance.theme, matchMedia("(prefers-color-scheme: dark)").matches);
  const shown = useMemo(() => {
    const base = mobile ? { ...prefs, fontSize: prefs.mobileFontSize } : prefs;
    const fallback = base.theme === "slate" && appMode === "light" ? "light" : base.theme;
    return { ...base, theme: person.theme || fallback, lineHeight: person.lineHeight || base.lineHeight };
  }, [prefs, mobile, person.theme, person.lineHeight, appMode]);
  const [fontHint, setFontHint] = useState("");
  const savePrefs = useCallback((next: Prefs) => {
    setPrefs(next);
    try {
      localStorage.setItem("lec-terminal-prefs", JSON.stringify(next));
    } catch {}
  }, []);
  const setShownFont = useCallback(
    (size: number, persist = true) => {
      const fontSize = Math.round(Math.max(mobile ? FONT_MIN : 10, Math.min(FONT_MAX, size)));
      const next = mobile ? { ...prefs, mobileFontSize: fontSize } : { ...prefs, fontSize };
      if (persist) savePrefs(next);
      else setPrefs(next);
    },
    [mobile, prefs, savePrefs],
  );
  // The toolbar clips its own dropdown (it scrolls horizontally, which clips
  // vertically too), so the open menu is placed against the viewport instead.
  // The compact layout already pins the menu to the screen edge; leave it alone.
  const placeTools = useCallback(() => {
    const details = tools.current,
      panel = details?.querySelector<HTMLElement>(".action-menu-panel"),
      summary = details?.querySelector("summary");
    if (!details || !panel || !summary) return;
    // A phone gets a sheet over the terminal, resting on the key row (which
    // the fitted viewport already keeps above the on-screen keyboard).
    const keybar = document.getElementById("terminal-keybar"),
      fitted = parseFloat(document.documentElement.style.getPropertyValue("--lec-visible-height")),
      visible = fitted > 0 ? Math.min(fitted, innerHeight) : innerHeight,
      keys = keybar?.getClientRects().length ? keybar.getBoundingClientRect() : undefined,
      bottom = keys && keys.top > 0 ? Math.min(keys.top, visible) : visible;
    if (placeSheet(details, panel, { top: 0, bottom, left: 0, width: innerWidth, anchor: summary.getBoundingClientRect() })) {
      details.classList.remove("menu-fixed");
      return;
    }
    if (!details.open || document.body.classList.contains("compact-chrome")) {
      details.classList.remove("menu-fixed");
      return;
    }
    const anchor = summary.getBoundingClientRect(),
      gap = 7,
      edge = 8,
      width = panel.offsetWidth || 230;
    details.style.setProperty(
      "--menu-left",
      Math.round(
        Math.max(edge, Math.min(anchor.right - width, innerWidth - width - edge)),
      ) + "px",
    );
    details.style.setProperty("--menu-top", Math.round(anchor.bottom + gap) + "px");
    details.style.setProperty(
      "--menu-max-height",
      Math.round(Math.max(140, innerHeight - anchor.bottom - gap - edge)) + "px",
    );
    details.classList.add("menu-fixed");
  }, []);
  useEffect(() => {
    placeTools();
    if (!toolsOpen) return;
    const abort = new AbortController(),
      signal = abort.signal;
    addEventListener("resize", placeTools, { signal });
    window.visualViewport?.addEventListener("resize", placeTools, { signal });
    tools.current
      ?.closest("nav")
      ?.addEventListener("scroll", placeTools, { signal });
    // The fitted viewport (a keyboard opening or closing) resizes the body
    // and moves the key row the sheet rests on.
    const observer = new ResizeObserver(() => placeTools());
    observer.observe(document.body);
    const keybar = document.getElementById("terminal-keybar");
    if (keybar) observer.observe(keybar);
    return () => {
      observer.disconnect();
      abort.abort();
    };
  }, [toolsOpen, placeTools]);
  // An embedded frame tells the page around it when Tools is a sheet, so the
  // page dims with it; a tap on the page's dimming closes Tools here.
  useEffect(() => {
    if (!embedded) return;
    const sheet = toolsOpen && !!tools.current?.classList.contains("menu-sheet");
    parent.postMessage({ type: "lec-terminal-sheet", open: sheet }, location.origin);
  }, [toolsOpen, embedded]);
  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      const data: unknown = event.data;
      if (event.source !== parent || event.origin !== location.origin || !data || typeof data !== "object" || !("type" in data)) return;
      if (data.type === "lec-terminal-close-menu" && tools.current?.open) tools.current.open = false;
    };
    addEventListener("message", onMessage);
    return () => removeEventListener("message", onMessage);
  }, []);
  const showHistory = useCallback((pane: string) => {
    setHistoryPane(pane);
    setDialog("history");
  }, []);
  // The "Report keyboard layout" diagnostic (keyboard-report.ts): captures
  // exactly what this device's viewport/keyboard APIs report, plus where the
  // key row, Tools toggle and the terminal's real focus target actually are
  // on screen, for a person to paste back when the covered-input issue shows
  // up on hardware no emulator has reproduced.
  const reportKeyboard = useCallback(() => {
    const named = (selector: string) => {
      const el = document.querySelector(selector);
      return el ? rectSnapshot(el.getBoundingClientRect()) : null;
    };
    const vv = window.visualViewport;
    const api = (
      navigator as Navigator & {
        virtualKeyboard?: { overlaysContent?: boolean; boundingRect: DOMRectReadOnly };
      }
    ).virtualKeyboard;
    const report = buildKeyboardReport({
      userAgent: navigator.userAgent,
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
      visualViewport: vv
        ? {
            width: Math.round(vv.width),
            height: Math.round(vv.height),
            offsetTop: Math.round(vv.offsetTop),
            offsetLeft: Math.round(vv.offsetLeft),
            scale: vv.scale,
          }
        : null,
      virtualKeyboard: api
        ? { overlaysContent: !!api.overlaysContent, rect: rectSnapshot(api.boundingRect) }
        : null,
      elements: {
        keybar: named("#terminal-keybar"),
        toolsToggle: named("#terminal-tools-summary"),
        focusTarget: named(".xterm-helper-textarea"),
      },
      fittedHeightVar:
        document.documentElement.style.getPropertyValue("--lec-visible-height") || null,
      now: Date.now(),
    });
    setKeyboardReport(formatKeyboardReport(report));
    setDialog("keyboard-report");
  }, []);
  const preview = useCallback((path: string, line?: number, column?: number) => {
    setFileRequest({ path, target: line ? { line, column } : undefined, nonce: Date.now() });
    setFilesOpen(true);
  }, []);
  const register = useCallback((id: string, engine: Engine | null) => {
    if (engine) {
      // An armed modifier applies to the next thing typed on the phone's own
      // keyboard, then lets go — the same as the key bar's own keys.
      engine.inputFilter = (text) => {
        const armed = modsRef.current;
        if (!armed.ctrl && !armed.alt) return text;
        setMods({ ctrl: false, alt: false });
        return engine.modify(text, armed);
      };
      // A hardware key that the terminal reports as an extended key takes an
      // armed modifier too (engine.extendedKey).
      engine.stickyModifiers = {
        get: () => modsRef.current,
        release: () => setMods({ ctrl: false, alt: false }),
      };
      engines.current.set(id, engine);
    } else engines.current.delete(id);
  }, []);
  const update = useCallback(
    (id: string, value: Snapshot) =>
      setStates((old) => ({ ...old, [id]: value })),
    [],
  );
  const match = useCallback((id: string, index: number, count: number) => {
    if (id === activeRef.current)
      setMatches(count ? `${index + 1} / ${count}` : t("terminal.noMatches"));
  }, []);
  // The tab bar owns the tab list; a deliberate flick inside the terminal body
  // is relayed to it, because only the frame knows the gesture was a flick.
  const swipe = useCallback((_id: string, direction: 1 | -1) => {
    if (!embedded) return;
    parent.postMessage({ type: "lec-terminal-swipe", direction }, location.origin);
  }, [embedded]);
  const openControls = useCallback(() => {
    const details = tools.current;
    if (!details) return;
    details.open = true;
    placeTools();
    setToolsOpen(true);
    const firstAction = [...details.querySelectorAll<HTMLElement>("button:not(:disabled), a[href]")]
      .find((element) => element.getClientRects().length > 0);
    (firstAction ?? details.querySelector<HTMLElement>("summary"))?.focus();
  }, [placeTools]);
  // A clicked or tapped link: web addresses leave for the browser; files
  // open in the file viewer, those outside the workspace read-only. A path
  // that names nothing says which path was tried instead of opening.
  const fileApi = useMemo(() => new FileApi(base), [base]);
  const linkExists = useMemo(() => existenceCheck(fileApi), [fileApi]);
  const [linkMenu, setLinkMenu] = useState<{ id: string; link: TerminalLink; point: { x: number; y: number } }>();
  const link = useCallback(
    async (target: TerminalLink) => {
      if (target.kind === "url") return openExternal(target.url);
      try {
        if (target.external) {
          const stat = await fileApi.outsideStat(target.path);
          if (!stat.exists) return setNotice(t("files.link.missing", { path: target.path }));
          if (!stat.regular) return setNotice(t("files.link.notFile", { path: stat.path }));
        } else if (target.verify && !(await linkExists(target.path))) return setNotice(t("files.link.missing", { path: target.path }));
      } catch (error) {
        return setNotice(t("files.link.failed", { path: target.path, message: errorMessage(error) }));
      }
      preview(target.path, target.line, target.column);
    },
    [fileApi, linkExists, preview],
  );
  const closeLinkMenu = useCallback(() => setLinkMenu(undefined), []);
  const linkActions = useMemo(
    () => ({
      open: (target: TerminalLink) => void link(target),
      absolute: (path: string) => (isOutside(path) ? path : (info?.workdir || "").replace(/\/+$/, "") + "/" + path),
      copy: (text: string) =>
        void copyClipboard(text).then(
          () => setNotice(t("files.link.copied", { text })),
          (error) => setNotice(errorMessage(error)),
        ),
      download: (path: string) =>
        void fileApi.open(path).then(
          (opened) => {
            downloadBlob(opened.blob, path.slice(path.lastIndexOf("/") + 1));
            setNotice(t("files.link.downloaded", { name: path.slice(path.lastIndexOf("/") + 1) }));
          },
          (error) => setNotice(t("files.link.failed", { path, message: errorMessage(error) })),
        ),
      send: (path: string) => {
        const engine = engines.current.get(linkMenu?.id || activeRef.current) ?? current();
        if (!engine) return setNotice(t("files.link.sendFailed"));
        engine.paste(shellPath(info?.workdir || "", path) + " ");
        setNotice(t("files.link.sent", { path }));
      },
      beside:
        embedded && parent !== window
          ? (path: string, line?: number, column?: number) => parent.postMessage({ type: "lec-terminal-open-beside", path, line, column }, location.origin)
          : undefined,
    }),
    [embedded, fileApi, info?.workdir, link, linkMenu?.id],
  );
  const callbacks: Callbacks = {
    link: (target) => void link(target),
    linkMenu: (id, target, point) => setLinkMenu({ id, link: target, point }),
    linkExists,
    engine: register,
    state: update,
    select: setActive,
    notice: setNotice,
    history: showHistory,
    controls: openControls,
    matches: match,
    preview,
    swipe,
    clipboard: (text) => {
      void copyClipboard(text, () => current()?.term.focus())
        .then(() => setNotice(t("terminal.copiedByProgram", { count: text.length })))
        .catch(() => setNotice(t("terminal.copyBlocked")));
    },
  };
  useEffect(() => {
    const abort = new AbortController();
    let retryTimer: number | undefined;
    void json<TerminalInfo>(base + "/info", { signal: abort.signal })
      .then((data) => {
        setInfo(data);
        setNotice("");
        document.title = data.tmux_session + " · Lectern";
      })
      .catch((error) => {
        if (!abort.signal.aborted) {
          setNotice(errorMessage(error));
          // An online event can arrive before a failed bootstrap settles, or
          // a server can recover without any network-state event at all.
          retryTimer = window.setTimeout(() => setInfoAttempt(attempt => attempt + 1),
            Math.min(10000, 1000 * 2 ** Math.min(infoAttempt, 4)));
        }
      });
    return () => { abort.abort(); clearTimeout(retryTimer); };
  }, [base, infoAttempt]);
  // A terminal opened while the phone had no network failed its one bootstrap
  // fetch. Without info there is no pane and no engine to retry anything, so
  // the status would sit at "Connecting" for good. The network coming back
  // retries the bootstrap itself: recovery without a manual reload.
  useEffect(() => {
    if (info) return;
    const retry = () => setInfoAttempt((attempt) => attempt + 1);
    window.addEventListener("online", retry);
    return () => window.removeEventListener("online", retry);
  }, [info]);
  // Two fingers resize the type, as they do in every phone terminal worth using.
  // The size follows the fingers live and is saved when they lift.
  const pinch = useRef({ size: shown.fontSize, set: setShownFont });
  pinch.current = { size: shown.fontSize, set: setShownFont };
  useEffect(() => {
    const host = document.getElementById("workspace");
    if (!host) return;
    const abort = new AbortController(),
      options = { signal: abort.signal, passive: false, capture: true };
    let start: { spread: number; size: number } | undefined,
      hide: number | undefined;
    const spread = (touches: TouchList) =>
      Math.hypot(
        touches[0]!.clientX - touches[1]!.clientX,
        touches[0]!.clientY - touches[1]!.clientY,
      );
    host.addEventListener(
      "touchstart",
      (event) => {
        if (event.touches.length === 2)
          start = { spread: spread(event.touches), size: pinch.current.size };
      },
      options,
    );
    host.addEventListener(
      "touchmove",
      (event) => {
        if (!start || event.touches.length !== 2 || !start.spread) return;
        event.preventDefault(); // otherwise the browser zooms the whole page
        const size = Math.round((start.size * spread(event.touches)) / start.spread);
        if (size === pinch.current.size) return;
        pinch.current.set(size, false);
        setFontHint(`${Math.max(FONT_MIN, Math.min(FONT_MAX, size))}px`);
      },
      options,
    );
    const finish = (event: TouchEvent) => {
      if (!start || event.touches.length >= 2) return;
      start = undefined;
      pinch.current.set(pinch.current.size, true);
      clearTimeout(hide);
      hide = window.setTimeout(() => setFontHint(""), 900);
    };
    host.addEventListener("touchend", finish, options);
    host.addEventListener("touchcancel", finish, options);
    return () => {
      clearTimeout(hide);
      abort.abort();
    };
  }, []);
  useEffect(() => {
    const bufferContains = (text: string) => {
      const term = current()?.term;
      if (!term) return false;
      const buffer = term.buffer.active;
      for (let i = 0; i < buffer.length; i++)
        if (buffer.getLine(i)?.translateToString(true).includes(text))
          return true;
      return false;
    };
    const pointOf = (text: string) => {
      const term = current()?.term,
        screen = term?.element?.querySelector(".xterm-screen");
      if (!term || !screen) return null;
      const buffer = term.buffer.active,
        box = screen.getBoundingClientRect();
      for (let row = term.rows - 1; row >= 0; row--) {
        const col = buffer.getLine(buffer.viewportY + row)?.translateToString(true).indexOf(text) ?? -1;
        if (col >= 0)
          return { x: box.left + ((col + 0.5) * box.width) / term.cols, y: box.top + ((row + 0.5) * box.height) / term.rows };
      }
      return null;
    };
    window.__lecTerminalState = () => ({
      pointOf,
      selection: current()?.term.getSelection() || "",
      hasSelection: !!current()?.term.hasSelection(),
      mouseTrackingMode: current()?.term.modes.mouseTrackingMode || "none",
      bufferLines: current()?.term.buffer.active.length || 0,
      bufferContains,
    });
    return () => {
      delete window.__lecTerminalState;
    };
  }, []);
  useEffect(() => {
    const abort = new AbortController(),
      signal = abort.signal,
      mobile = matchMedia("(max-width:1023px)"),
      short = matchMedia("(max-height:380px)");
    document.body.classList.toggle("embedded", embedded);
    const chrome = () =>
      document.body.classList.toggle(
        "compact-chrome",
        document.body.classList.contains("compact-terminal") ||
          (document.body.classList.contains("mobile-terminal") &&
            short.matches),
      );
    const layout = () => {
      if (!embedded) {
        document.body.classList.toggle("mobile-terminal", mobile.matches);
        setMobile(mobile.matches);
      }
      chrome();
    };
    layout();
    mobile.addEventListener("change", layout, { signal });
    short.addEventListener("change", chrome, { signal });
    const fit = () => engines.current.forEach((engine) => engine.scheduleFit());
    // The phone keyboard overlays the embedded frame instead of resizing it, so
    // the parent measures the slice a phone can actually see; a standalone page
    // measures its own visual viewport.
    const relate = (box: { top: number; height: number } | null | undefined) => {
      applyVisibleHeight(box ?? null);
      fit();
    };
    let viewportCleanup = () => {};
    if (embedded) {
      viewportCleanup = subscribeViewport(relate);
      parent.postMessage({ type: "lec-terminal-need-viewport" }, location.origin);
    } else {
      const measure = () => relate(localViewportSlice());
      window.addEventListener("resize", measure, { signal });
      window.addEventListener("orientationchange", measure, { signal });
      window.visualViewport?.addEventListener("resize", measure, { signal });
      window.visualViewport?.addEventListener("scroll", measure, { signal });
      virtualKeyboard()?.addEventListener("geometrychange", measure, { signal });
      measure();
    }
    // Coming into view also takes the shared tmux window's size (see
    // Engine.claim), so a hidden or sleeping client cannot shrink this one.
    const show = () =>
      engines.current.forEach((engine) => engine.scheduleFit(true));
    const focus = () => setKeyboardFocused(document.activeElement?.classList.contains("xterm-helper-textarea") === true);
    document.addEventListener("focusin", focus, { signal });
    document.addEventListener("focusout", () => queueMicrotask(focus), { signal });
    // Android's system Back dismisses the IME without blurring its textarea.
    // Release that stale focus when the viewport grows back, so the keyboard
    // button can reopen it in one tap. Rotation changes width and is ignored.
    let height = innerHeight, width = innerWidth;
    window.addEventListener("resize", () => {
      if (/Android/i.test(navigator.userAgent) && innerWidth === width &&
          innerHeight - height > 150 &&
          document.activeElement?.classList.contains("xterm-helper-textarea"))
        current()?.term.blur();
      height = innerHeight;
      width = innerWidth;
    }, { signal });
    window.visualViewport?.addEventListener("resize", fit, { signal });
    window.addEventListener("focus", show, { signal });
    window.addEventListener("pageshow", show, { signal });
    void document.fonts?.ready.then(() => {
      if (!signal.aborted) fit();
    });
    const unsubscribe = subscribeLayout((data) => {
      if (embedded) {
        document.body.classList.toggle("compact-terminal", data.compact);
        document.body.classList.toggle("mobile-terminal", data.mobile);
        setMobile(data.mobile);
        chrome();
        show();
      }
    });
    document.addEventListener(
      "visibilitychange",
      () => {
        if (!document.hidden) show();
      },
      { signal },
    );
    window.addEventListener(
      "pagehide",
      (event) => {
        if (!event.persisted)
          engines.current.forEach((engine) => engine.dispose());
      },
      { signal },
    );
    document.addEventListener(
      "keydown",
      (event) => {
        if (event.key === "Escape" && tools.current?.open) {
          event.preventDefault();
          event.stopImmediatePropagation();
          tools.current.open = false;
          current()?.term.focus();
          return;
        }
        if (!((event.ctrlKey || event.metaKey) && event.code === "KeyC"))
          return;
        const selection = window.getSelection(),
          text = selection?.toString();
        if (!text || !selection?.anchorNode) return;
        const pane = [...engines.current.values()].find((engine) =>
          engine.options.frozen.contains(selection.anchorNode),
        );
        if (!pane) return;
        event.preventDefault();
        event.stopImmediatePropagation();
        void copyClipboard(text, () => pane.term.focus()).catch((error) =>
          setNotice(errorMessage(error)),
        );
      },
      { capture: true, signal },
    );
    document.addEventListener("pointerdown", (event) => {
      if (tools.current?.open && event.target instanceof Node && !tools.current.contains(event.target))
        tools.current.open = false;
    }, { signal });
    return () => {
      viewportCleanup();
      applyVisibleHeight(null);
      unsubscribe();
      abort.abort();
      document.body.classList.remove(
        "embedded",
        "mobile-terminal",
        "compact-terminal",
        "compact-chrome",
        "dragging",
      );
    };
  }, [embedded]);
  const upload = useCallback(
    async (files: File[], pane = engines.current.get(activeRef.current)) => {
      const data = infoRef.current;
      if (!data?.files_available)
        throw new Error(t("terminalPage.notice.uploadsUnavailable"));
      if (!pane?.connected)
        throw new Error(t("terminalPage.notice.connectBeforeUpload"));
      setUploading((old) => old + 1);
      try {
        for (const file of files) {
          if (file.size > 25 * 1024 * 1024)
            throw new Error(t("terminalPage.notice.tooLarge", { name: file.name }));
          setNotice(t("terminalPage.notice.uploading", { name: file.name }));
          const form = new FormData();
          form.append(
            "file",
            file,
            file.name || `screenshot-${Date.now()}.png`,
          );
          const attachment = await json<Attachment>(base + "/attachments", {
            method: "POST",
            body: form,
          });
          try {
            pane.paste(quote(attachment.path) + " ");
            setNotice(
              t("terminalPage.notice.uploaded", { name: attachment.name }),
            );
          } catch (error) {
            setNotice(t("terminalPage.notice.uploadedTo", { path: attachment.path, error: errorMessage(error) }));
            return;
          }
        }
      } finally {
        setUploading((old) => old - 1);
      }
    },
    [base],
  );
  useEffect(() => {
    const abort = new AbortController(),
      signal = abort.signal;
    let depth = 0;
    const run = (files: File[], pane?: Engine) => {
      void upload(files, pane).catch((error) => setNotice(errorMessage(error)));
    };
    document.addEventListener(
      "dragenter",
      (event) => {
        if (Array.from(event.dataTransfer?.types || []).includes("Files")) {
          event.preventDefault();
          depth++;
          document.body.classList.add("dragging");
        }
      },
      { signal },
    );
    document.addEventListener(
      "dragover",
      (event) => {
        if (Array.from(event.dataTransfer?.types || []).includes("Files"))
          event.preventDefault();
      },
      { signal },
    );
    document.addEventListener(
      "dragleave",
      () => {
        if (--depth <= 0) document.body.classList.remove("dragging");
      },
      { signal },
    );
    document.addEventListener(
      "drop",
      (event) => {
        const files = Array.from(event.dataTransfer?.files || []);
        if (!files.length) return;
        event.preventDefault();
        depth = 0;
        document.body.classList.remove("dragging");
        const pane = [...engines.current.values()].find(
          (engine) =>
            event.target instanceof Node &&
            engine.options.pane.contains(event.target),
        );
        run(files, pane);
      },
      { signal },
    );
    document.addEventListener(
      "paste",
      (event) => {
        const files = Array.from(event.clipboardData?.items || [])
          .filter((item) => item.kind === "file")
          .map((item) => item.getAsFile())
          .filter((file): file is File => file !== null);
        if (!files.length) return;
        event.preventDefault();
        event.stopImmediatePropagation();
        run(files);
      },
      { capture: true, signal },
    );
    return () => abort.abort();
  }, [upload]);
  // Find in scrollback: plain text by default, with case, whole-word and
  // regular-expression switches that are remembered as this person's defaults.
  const [findError, setFindError] = useState("");
  function find(back = false, value = query, flags = person) {
    const addon = current()?.search;
    setFindError("");
    if (!value) {
      addon?.clearDecorations();
      setMatches("");
      return;
    }
    if (flags.findRegex) {
      try {
        new RegExp(value);
      } catch {
        addon?.clearDecorations();
        setFindError(t("terminal.findBadPattern"));
        setMatches("");
        return;
      }
    }
    const options = {
      regex: flags.findRegex,
      caseSensitive: flags.findCase,
      wholeWord: flags.findWord,
      decorations: {
        matchBackground: "#66512c",
        activeMatchBackground: "#b7a0ff",
        matchOverviewRuler: "#66512c",
        activeMatchColorOverviewRuler: "#b7a0ff",
      },
    };
    const found = back ? addon?.findPrevious(value, options) : addon?.findNext(value, options);
    if (found === false) setMatches(t("terminal.noMatches"));
  }
  const openFind = () => {
    setSearch(true);
    requestAnimationFrame(() => {
      searchInput.current?.focus();
      searchInput.current?.select();
    });
  };
  const closeFind = () => {
    setSearch(false);
    current()?.search.clearDecorations();
    current()?.term.focus();
  };
  const toggleFind = (key: "findCase" | "findWord" | "findRegex") => {
    const next = { ...person, [key]: !person[key] };
    saveTerminalPrefs({ [key]: next[key] });
    find(false, query, next);
    searchInput.current?.focus();
  };
  function close() {
    setDialog(null);
  }
  const chordLabel = (action: string) => {
    const chord = chordsFor(action)[0];
    return chord ? displayChord(chord) : t("terminal.noChord");
  };
  // Keys: this frame's own actions run here; app actions pressed while the
  // terminal has focus are posted to the app when the terminal can spare the
  // chord (shortcuts/chords.ts terminalSafe).
  useEffect(() => installShortcutListener({ contexts: ["terminal"], forward: embedded ? ["global", "workspace"] : undefined }), [embedded]);
  // Tell the workspace this pane has focus: a click inside a frame does not
  // reach the page around it.
  useEffect(() => {
    if (!embedded) return;
    const tell = () => parent.postMessage({ type: "lec-terminal-focus" }, location.origin);
    addEventListener("focusin", tell);
    addEventListener("pointerdown", tell, { capture: true });
    return () => {
      removeEventListener("focusin", tell);
      removeEventListener("pointerdown", tell, { capture: true });
    };
  }, [embedded]);
  const scroll = (fn: (engine: Engine) => void) => () => {
    const engine = current();
    if (!engine) return false;
    fn(engine);
  };
  const sendQuick = (n: number) => () => {
    const engine = current();
    const row = commandsFor(projectId)[n - 1];
    if (!engine || !row || !engine.connected) return false;
    engine.input(commandBytes(row.command));
    engine.term.scrollToBottom();
  };
  const themeIds = () => allTerminalThemes(customThemes()).map((row) => row.id);
  useShortcuts({
    "terminal.find": openFind,
    "terminal.findNext": () => { if (!search) openFind(); else find(); },
    "terminal.findPrev": () => { if (!search) openFind(); else find(true); },
    "terminal.history": () => showHistory(activeRef.current),
    "terminal.copy": () => {
      const engine = current();
      if (!engine) return false;
      void engine.copySelection().catch((error) => setNotice(errorMessage(error)));
    },
    "terminal.paste": (event) => {
      // The browser's own paste chords already deliver a paste event, which
      // the terminal takes synchronously; only a remapped chord reads the
      // clipboard itself.
      if (event && (event.ctrlKey || event.metaKey) && event.code === "KeyV") return false;
      const engine = current();
      if (!engine || !navigator.clipboard?.readText) return false;
      void navigator.clipboard.readText().then((text) => engine.paste(text)).catch((error) => setNotice(errorMessage(error)));
    },
    "terminal.selectAll": scroll((engine) => engine.term.selectAll()),
    "terminal.clear": scroll((engine) => engine.term.clear()),
    "terminal.fontBigger": () => setShownFont(shown.fontSize + 1),
    "terminal.fontSmaller": () => setShownFont(shown.fontSize - 1),
    "terminal.fontReset": () => setShownFont(mobile ? 11 : 15),
    "terminal.scrollTop": scroll((engine) => engine.term.scrollToTop()),
    "terminal.scrollBottom": scroll((engine) => { engine.freeze(false); engine.term.scrollToBottom(); }),
    "terminal.pageUp": scroll((engine) => engine.term.scrollPages(-1)),
    "terminal.pageDown": scroll((engine) => engine.term.scrollPages(1)),
    "terminal.pause": scroll((engine) => engine.freeze(!engine.paused)),
    "terminal.reconnect": scroll((engine) => { if (engine.paused) engine.freeze(false); void engine.connect(); }),
    "terminal.splitShell": () => { if (!info?.shell_url) return false; setSplit((old) => !old); },
    "terminal.files": () => { if (!info?.files_available) return false; setFilesOpen((old) => !old); },
    "terminal.attach": () => { if (!info?.files_available) return false; file.current?.click(); },
    "terminal.compose": () => setDialog("compose"),
    "terminal.quickCommands": () => setDialog("snippets"),
    "terminal.appearance": () => setDialog("appearance"),
    "terminal.themeNext": () => {
      const ids = themeIds(), at = ids.indexOf(shown.theme);
      saveTerminalPrefs({ theme: ids[(at + 1) % ids.length]! });
    },
    "terminal.review": () => { if (!info) return false; onShared("review", info); },
    "terminal.saved": () => { if (!info || kind !== "session") return false; onShared("saved", info); },
    "terminal.tools": openControls,
    "terminal.native": () => { if (!info?.desktop_uri) return false; location.href = info.desktop_uri; },
    ...Object.fromEntries([1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => [`terminal.quickCommand${n}`, sendQuick(n)])),
  });
  const specs: PaneSpec[] = info
    ? [
        {
          id: "agent",
          url: info.terminal_url,
          label: kind === "project" ? t("terminalPage.pane.projectShell") : t("terminalPage.pane.agent"),
        },
        ...(split && info.shell_url
          ? [{ id: "shell", url: info.shell_url, label: t("terminalPage.pane.companionShell") }]
          : []),
      ]
    : [];
  return (
    <>
      <header>
        <a href="/" title={t("terminalPage.header.back")}>
          ◈ lectern
        </a>
        <span id="identity">
          {info ? info.tmux_session + " · " + info.target : t("terminalPage.status.connecting")}
        </span>
        <span id="connection" role="status">
          {state?.connected
            ? t("terminalPage.status.connected")
            : state?.offline
              ? t("terminalPage.status.offline")
              : state
                ? t("terminalPage.status.reconnecting")
                : t("terminalPage.status.connectingPlain")}
        </span>
      </header>
      <nav aria-label={t("terminalPage.tools.navLabel")}>
        <span
          id="compact-status"
          className={
            state?.connected ? "connected" : state?.offline ? "offline" : ""
          }
          role="status"
          aria-label={
            state?.connected
              ? t("terminalPage.status.terminalConnected")
              : state?.offline
                ? t("terminalPage.status.terminalOffline")
                : t("terminalPage.status.terminalReconnecting")
          }
          title={state?.status || t("terminalPage.status.connectingPlain")}
        />
        <button
          id="upload"
          disabled={!info?.files_available || uploading > 0}
          onClick={() => file.current?.click()}
        >
          {t("terminalPage.tools.attach")}
        </button>
        <input
          ref={file}
          id="file-input"
          type="file"
          multiple
          hidden
          onChange={(event) => {
            const files = Array.from(event.target.files || []);
            event.target.value = "";
            void upload(files).catch((error) => setNotice(errorMessage(error)));
          }}
        />
        <button
          id="files"
          disabled={!info?.files_available}
          aria-pressed={filesOpen}
          onClick={() => setFilesOpen((old) => !old)}
        >
          {t("terminalPage.tools.files")}
        </button>
        <a id="desktop" className="button" href={info?.desktop_uri}>
          {t("terminalPage.tools.openInTerminal")}
        </a>
        <details
          id="terminal-tools"
          className="action-menu"
          ref={tools}
          onToggle={(event) => setToolsOpen(event.currentTarget.open)}
        >
          <summary id="terminal-tools-summary" onClick={(event) => {
            // Native details opens before its deferred toggle event. Position
            // in this activation turn so the first visible frame isn't clipped.
            event.preventDefault();
            const details = tools.current;
            if (!details) return;
            details.open = !details.open;
            placeTools();
            setToolsOpen(details.open);
          }}>
            {state?.paused ? t("terminalPage.tools.pausedToggle") : t("terminalPage.tools.toggle")}
            <span className="terminal-controls-hint">{t("terminalPage.tools.hint")}</span>
          </summary>
          <div
            className="action-menu-panel"
            onClick={(event) => {
              if (
                event.target instanceof Element &&
                event.target.closest("button,a") &&
                tools.current
              )
                tools.current.open = false;
            }}
          >
            {/* A phone shows the menu as a titled sheet in groups; on a desk
                these are hidden and the list reads as it always has. */}
            <div className="menu-sheet-head">
              <h2>{t("terminalPage.tools.toggle")}</h2>
              <button type="button" className="menu-sheet-close" data-close aria-label={t("terminalPage.menuSheet.close")}>✕</button>
            </div>
            <div className="menu-sheet-group" data-group="files" role="presentation">{t("terminalPage.tools.group.files")}</div>
            <div className="menu-sheet-group" data-group="text" role="presentation">{t("terminalPage.tools.group.text")}</div>
            <div className="menu-sheet-group" data-group="conversations" role="presentation">{t("terminalPage.tools.group.conversations")}</div>
            <div className="menu-sheet-group" data-group="session" role="presentation">{t("terminalPage.tools.group.session")}</div>
            <div className="menu-sheet-group" data-group="settings" role="presentation">{t("terminalPage.tools.group.settings")}</div>
            <p className="terminal-controls-help">
              {t("terminalPage.tools.helpControls")} <kbd>Ctrl+]</kbd> {t("terminalPage.tools.helpThen")} <kbd>m</kbd><br />
              {t("terminalPage.tools.helpEsc")}
            </p>
            <a id="compact-desktop" className="button" href={info?.desktop_uri}>
              {t("terminalPage.tools.openInTerminal")}
            </a>
            <button
              id="compact-upload"
              disabled={!info?.files_available || uploading > 0}
              onClick={() => file.current?.click()}
            >
              {t("terminalPage.tools.attach")}
            </button>
            <button
              id="compact-files"
              disabled={!info?.files_available}
              onClick={() => setFilesOpen(true)}
            >
              {t("terminalPage.tools.files")}
            </button>
            <button
              id="go-to-file"
              disabled={!info?.files_available}
              onClick={() => setFileCommand({ name: "quick", nonce: Date.now() })}
            >
              Go to file
            </button>
            <button
              id="search-files"
              disabled={!info?.files_available}
              onClick={() => setFileCommand({ name: "search", nonce: Date.now() })}
            >
              Search in files
            </button>
            <span id="compact-workspace">{info?.workdir}</span>
            <button id="compose" disabled={!state?.connected} onClick={() => setDialog("compose")}>
              {t("terminalPage.tools.compose")}
            </button>
            <button id="mobile-snippets" disabled={!state?.connected} onClick={() => setDialog("snippets")}>
              {t("terminalPage.tools.savedReplies")}
            </button>
            <button id="customize-keys" onClick={openKeybar}>
              {t("keybar.customizeRow")}
            </button>
            <button
              id="search-conversations"
              disabled={!info}
              onClick={() => info && onShared("search", info)}
            >
              {t("terminalPage.tools.searchSaved")}
            </button>
            {kind === "session" && (
              <button
                id="saved-conversations"
                disabled={!info}
                onClick={() => info && onShared("saved", info)}
              >
                {t("terminalPage.tools.saved")}
              </button>
            )}
            <button
              id="review"
              disabled={!info}
              onClick={() => info && onShared("review", info)}
            >
              {t("terminalPage.review.title")}
            </button>
            <button id="find" onClick={openFind}>
              {t("terminal.findInTerminal")}
            </button>
            <button
              id="history"
              disabled={!state}
              onClick={() => showHistory(active)}
            >
              {t("terminalPage.history.title")}
            </button>
            <button
              id="desktop-setup"
              disabled={!info}
              onClick={() => setDialog("desktop")}
            >
              {t("terminalPage.tools.desktopSetup")}
            </button>
            <a
              id="mcp-settings"
              className="button"
              href="/#projects"
              aria-label={t("terminalPage.tools.mcpSettingsLabel")}
            >
              {t("terminalPage.tools.mcpSettings")}
            </a>
            {mobile && (
              <button id="report-keyboard" className="tools-diagnostic" onClick={reportKeyboard}>
                {t("terminalPage.tools.reportKeyboard")}
              </button>
            )}
            <button
              id="shell"
              disabled={!info?.shell_url}
              onClick={() => {
                setSplit((old) => !old);
                if (split) setActive("agent");
              }}
            >
              {split ? t("terminalPage.tools.hideShell") : t("terminalPage.tools.splitShell")}
            </button>
            <button id="preferences" onClick={() => setDialog("appearance")}>
              {t("terminalPage.tools.appearance")}
            </button>
            <button
              id="pause"
              aria-pressed={state?.paused || false}
              onClick={() => current()?.freeze(!state?.paused)}
            >
              {state?.paused ? t("terminalPage.tools.resumeView") : t("terminalPage.tools.pauseView")}
            </button>
            <button
              id="bottom"
              onClick={() => {
                current()?.freeze(false);
                current()?.term.scrollToBottom();
              }}
            >
              {t("terminalPage.tools.jumpToLive")}
            </button>
            <button
              id="reconnect"
              title={t("terminalPage.tools.reconnectHint")}
              onClick={() => {
                const engine = current();
                if (engine?.paused) engine.freeze(false);
                void engine?.connect();
              }}
            >
              {t("terminalPage.tools.reconnect")}
            </button>
          </div>
        </details>
      </nav>
      <div id="notice" role="status" hidden={!notice}>
        {notice}
      </div>
      {search && (
        <div id="searchbar" role="search" aria-label={t("terminal.find")}>
          <input
            id="search-input"
            ref={searchInput}
            placeholder={t("terminal.find")}
            aria-label={t("terminal.find")}
            aria-invalid={!!findError}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              find(false, event.target.value);
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                find(event.shiftKey);
              }
              if (event.key === "Escape") closeFind();
            }}
          />
          <div className="find-toggles" role="group" aria-label={t("terminal.findOptions")}>
            <button id="find-case" aria-pressed={person.findCase} title={t("settings.workspace.findCase")} aria-label={t("settings.workspace.findCase")} onClick={() => toggleFind("findCase")}>Aa</button>
            <button id="find-word" aria-pressed={person.findWord} title={t("settings.workspace.findWord")} aria-label={t("settings.workspace.findWord")} onClick={() => toggleFind("findWord")}><u>ab</u></button>
            <button id="find-regex" aria-pressed={person.findRegex} title={t("settings.workspace.findRegex")} aria-label={t("settings.workspace.findRegex")} onClick={() => toggleFind("findRegex")}>.*</button>
          </div>
          <span id="matches" role="status">{findError || matches}</span>
          <button id="previous" aria-label={t("terminal.findPrevious")} title={t("terminal.findPrevious")} onClick={() => find(true)}>
            ↑
          </button>
          <button id="next" aria-label={t("terminal.findNext")} title={t("terminal.findNext")} onClick={() => find()}>
            ↓
          </button>
          <button id="search-close" onClick={closeFind}>
            {t("terminal.close")}
          </button>
        </div>
      )}
      {kind === "session" && /^[1-9]\d*$/.test(id) && (
        <ApprovalStrip
          sessionId={Number(id)}
          onNotice={setNotice}
          onDecided={() => setTimeout(() => current()?.term.focus(), 0)}
        />
      )}
      <div id="workspace-row">
      <main id="workspace">
        {info &&
          specs.map((spec) => (
            <Pane
              key={spec.id}
              spec={spec}
              prefs={shown}
              active={active === spec.id}
              info={info}
              callbacks={callbacks}
            />
          ))}
      </main>
      {info?.files_available && (
        <Workbench
          base={base}
          info={info}
          open={filesOpen}
          request={fileRequest}
          command={fileCommand}
          onOpenChange={setFilesOpen}
          onNotice={setNotice}
          onInsert={(text) => {
            const engine = current();
            if (!engine)
              throw new Error(
                "Terminal disconnected. Reconnect before inserting a path.",
              );
            engine.paste(text);
          }}
        />
      )}
      </div>
      {mobile && state?.paused && (
        <div id="select-bar" role="toolbar" aria-label={t("terminalPage.select.label")}>
          <span>{t("terminalPage.select.hint")}</span>
          <button
            id="select-copy-all"
            onClick={() => {
              const engine = current();
              if (!engine) return;
              void copyClipboard(engine.options.frozen.textContent || "", () => {})
                .then(() => setNotice(t("terminalPage.notice.copiedScreen")))
                .catch((error) => setNotice(errorMessage(error)));
            }}
          >
            {t("terminalPage.select.copyAll")}
          </button>
          <button
            id="select-done"
            className="primary"
            // Keep focus off this button: it is about to be removed, and focus
            // left on a removed element lands on the page, not the terminal.
            onPointerDown={(event) => event.preventDefault()}
            onClick={() => {
              const engine = current();
              engine?.freeze(false);
              setTimeout(() => engine?.term.focus(), 60);
            }}
          >
            {t("terminalPage.select.done")}
          </button>
        </div>
      )}
      {mobile && !state?.paused && state?.selection && (
        <LiveSelectionBar engine={current} onNotice={setNotice} onLink={link} />
      )}
      {linkMenu && <LinkMenu link={linkMenu.link} point={linkMenu.point} actions={linkActions} onClose={closeLinkMenu} />}
      {fontHint && (
        <div id="font-hint" role="status">
          {t("terminalPage.fontHint", { size: fontHint, columns: current()?.term.cols ?? 0 })}
        </div>
      )}
      {state?.unresponsive && !unresponsiveDismissed && (
        <div id="unresponsive" role="alert">
          <p>
            <b>{t("terminalPage.unresponsive.title")}</b> {kind === "session" ? t("terminalPage.unresponsive.bodySession") : t("terminalPage.unresponsive.body")}
          </p>
          <div className="unresponsive-actions">
            {kind === "session" && (
              <button className="primary" disabled={reviving} onClick={() => void revive()}>
                {reviving ? t("terminalPage.unresponsive.restarting") : t("terminalPage.unresponsive.restart")}
              </button>
            )}
            <button onClick={() => setUnresponsiveDismissed(true)}>{t("terminalPage.banner.dismiss")}</button>
          </div>
        </div>
      )}
      {agentExited && !exitDismissed && !(state?.unresponsive && !unresponsiveDismissed) && (
        <div id="agent-exited" role="alert">
          <p>
            <b>{t("terminalPage.exited.title")}</b> {t("terminalPage.exited.body")}
          </p>
          <div className="unresponsive-actions">
            <button className="primary" disabled={reviving} onClick={() => void revive()}>
              {reviving ? t("terminalPage.exited.reviving") : t("terminalPage.exited.revive")}
            </button>
            <button onClick={() => setExitDismissed(true)}>{t("terminalPage.banner.dismiss")}</button>
          </div>
        </div>
      )}
      {mobile && state?.retained && !state.paused && (
        <button id="return-live" onPointerDown={event => event.preventDefault()}
          onClick={() => { current()?.leaveRetainedHistory(); current()?.term.scrollToBottom(); }}>
          {t("terminalPage.returnLive")}
        </button>
      )}
      {mobile && <button id="terminal-keyboard" aria-label={keyboardFocused ? t("terminalPage.keybar.hideKeyboard") : t("terminalPage.keybar.showKeyboard")}
        title={keyboardFocused ? t("terminalPage.keybar.hideKeyboard") : t("terminalPage.keybar.showKeyboard")} aria-pressed={keyboardFocused}
        disabled={!state?.connected || state.paused} onPointerDown={event => event.preventDefault()}
        onClick={() => {
          const engine = current();
          if (keyboardFocused) engine?.term.blur(); else engine?.term.focus();
        }}>⌨</button>}
      <Keybar
        row={keybar}
        quick={quick}
        disabled={!state?.connected || !!state.paused}
        mods={mods}
        appCursor={() => !!current()?.term.modes.applicationCursorKeysMode}
        encode={(text, keyMods) => {
          const engine = current();
          return engine?.encodeExtended(text, keyMods);
        }}
        onMod={(key) => setMods((old) => ({ ...old, [key]: !old[key] }))}
        onSend={(bytes) => {
          const engine = current();
          if (!engine) return;
          // input() applies and releases any armed modifier itself.
          engine.input(bytes);
          engine.term.scrollToBottom();
          // Keep a hidden keyboard hidden. preventDefault on pointerdown
          // already preserves focus when the keyboard is being used.
        }}
        onSnippets={() => setDialog("snippets")}
        onFind={openFind}
        onEdit={openKeybar}
      />
      <footer>
        <span id="workspace-path">{info?.workdir}</span>
        <span>
          {t("terminal.footer", { find: chordLabel("terminal.find"), history: chordLabel("terminal.history"), paste: chordLabel("terminal.paste") })}
        </span>
      </footer>
      {dialog === "compose" && (
        <Dialog id="compose-dialog" title={t("terminalPage.tools.compose")} onClose={close}>
          <p>{t("terminalPage.compose.help")}</p>
          <textarea id="terminal-draft" aria-label={t("terminalPage.compose.textLabel")} autoFocus
            value={draft} onChange={event => setDraft(event.target.value)} rows={5} />
          <div className="compose-actions">
            <button disabled={!draft || !state?.connected} onClick={() => {
              current()?.paste(draft); setDraft(""); close();
            }}>{t("terminalPage.compose.insert")}</button>
            <button className="primary" disabled={!draft || !state?.connected} onClick={() => {
              const engine = current();
              if (!engine) return;
              engine.paste(draft); engine.input("\r"); setDraft(""); close();
            }}>{t("terminalPage.compose.send")}</button>
          </div>
        </Dialog>
      )}
      {dialog === "appearance" && (
        <Appearance
          prefs={shown}
          minFont={mobile ? FONT_MIN : 10}
          onPrefs={(next) =>
            savePrefs(
              mobile
                ? { ...next, fontSize: prefs.fontSize, mobileFontSize: next.fontSize }
                : { ...next, mobileFontSize: prefs.mobileFontSize },
            )
          }
          onClose={close}
        />
      )}
      {dialog === "history" && engines.current.get(historyPane) && (
        <HistoryDialog
          url={engines.current.get(historyPane)!.options.url}
          shell={historyPane !== "agent"}
          onClose={close}
        />
      )}
      {dialog === "snippets" && (
        <Snippets
          projectId={projectId}
          onSend={(command) => {
            const engine = current();
            if (!engine) return;
            engine.input(commandBytes(command));
            engine.term.scrollToBottom();
          }}
          onClose={close}
        />
      )}
      {dialog === "keybar" && (
        <KeybarEditor
          row={keybar}
          quick={quick}
          onChange={(next) => {
            saveKeybar(next);
            setKeybar(next ?? defaultKeybar);
          }}
          onClose={close}
        />
      )}
      {dialog === "desktop" && info && (
        <Desktop info={info} onClose={close} onNotice={setNotice} />
      )}
      {dialog === "keyboard-report" && (
        <Dialog
          id="keyboard-report-dialog"
          title={t("terminalPage.keyboardReport.title")}
          onClose={close}
          actions={
            <button
              onClick={() =>
                void copyClipboard(keyboardReport, () => {}).then(() =>
                  setNotice(t("terminalPage.notice.copiedReport")),
                )
              }
            >
              {t("terminalPage.keyboardReport.copy")}
            </button>
          }
        >
          <p className="dialog-help">
            {t("terminalPage.keyboardReport.help")}
          </p>
          <textarea id="keyboard-report-text" readOnly rows={16} value={keyboardReport} />
        </Dialog>
      )}
    </>
  );
}
