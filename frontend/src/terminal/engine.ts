import { errorMessage } from "./model";
import { Terminal, type IDisposable } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { installTerminalLinks, linkAtCell } from "../files/terminalLinks";
import type { ExistsCheck } from "../files/linkCheck";
import { installTerminalScroll } from "./scroll";
import {
  copyClipboard,
  json,
  request,
  withToken,
  type History,
  type Prefs,
} from "./model";
import { resolveTerminalTheme } from "../theme/terminal-prefs";
import { t } from "../i18n";
export interface Snapshot {
  connected: boolean;
  paused: boolean;
  status: string;
  frozen: string;
  retained: boolean;
  // The agent has taken several keystrokes and drawn nothing back. A live
  // process that ignores its terminal looks exactly like an idle one from the
  // board, so this is the only place the difference is visible.
  unresponsive: boolean;
  // A connection attempt failed while the device reports no network at all.
  offline: boolean;
  // Text is selected in the live terminal (a long press on a phone).
  selection: boolean;
}
export interface EngineOptions {
  url: string;
  host: HTMLElement;
  pane: HTMLElement;
  frozen: HTMLPreElement;
  prefs: () => Prefs;
  select: () => void;
  change: (state: Snapshot) => void;
  notice: (text: string) => void;
  history: () => void;
  controls: () => void;
  matches: (index: number, count: number) => void;
  // A deliberate horizontal flick on the terminal body, for the tab bar.
  swipe?: (direction: 1 | -1) => void;
  // A clicked or tapped link in the output (links.ts): a web address or a
  // file, in the workspace or outside it.
  openLink?: (link: TerminalLink) => void;
  // A right-click on a link, at a point in the page: the link's menu.
  linkMenu?: (link: TerminalLink, point: { x: number; y: number }) => void;
  // The workspace, for turning tapped paths into files the viewer can open.
  workdir?: () => string;
  // Whether a workspace path names a file: a bare name is a link only then.
  linkExists?: ExistsCheck;
  // Whether a program may copy to the clipboard with OSC 52, and what to do
  // with text it copies.
  osc52?: () => boolean;
  clipboard?: (text: string) => void;
  // Whether programs may turn on extended key reporting (the kitty keyboard
  // protocol and modifyOtherKeys, extended-keys.ts). Off, the terminal does
  // not answer for either and every key is sent as it always was.
  extendedKeys?: () => boolean;
}
import { installAndroidInput } from "./android-input";
import { modified, type Mods } from "./keys";
import { encodeKey, keyFromBytes, keyInput, KeyboardModes } from "./extended-keys";
import { hyperlinkTarget, type TerminalLink } from "./links";
import { haptic } from "../mobile/haptics";
export class Engine {
  private controlsPrefix = false;
  private androidInput?: ReturnType<typeof installAndroidInput>;
  readonly term: Terminal;
  readonly fit = new FitAddon();
  readonly search = new SearchAddon();
  connected = false;
  paused = false;
  stopped = false;
  readingRetainedHistory = false;
  private ws?: WebSocket;
  private controller?: AbortController;
  private generation = 0;
  private historyRevision = 0;
  private retry = 500;
  private timer?: number;
  private fitFrame?: number;
  private pending = 0;
  private flowPaused = false;
  private resizePending = false;
  private loadingHistory = false;
  private lastHistoryRead = 0;
  // The pane is a full-screen program without mouse reporting (see
  // History.app_screen); rechecked while in use, since the program can exit.
  private appScreen = false;
  private appScreenCheckedAt = 0;
  private appKeys() {
    if (this.appScreen && Date.now() - this.appScreenCheckedAt > 5000)
      void this.checkAppScreen();
    return this.appScreen;
  }
  private async checkAppScreen() {
    this.appScreenCheckedAt = Date.now();
    try {
      const out = await json<Partial<History>>(
        this.options.url.replace("/term/", "/api/term/") + "/history?flags_only=1",
      );
      this.appScreen = out.app_screen === true;
    } catch {
      /* keep the last answer; the next scroll asks again */
    }
  }
  private pageKey(direction: 1 | -1) {
    this.input(direction < 0 ? "\x1b[5~" : "\x1b[6~", false, true);
  }
  private retainedLines = 0;
  private frozenText = "";
  private status = t("terminalPage.status.connecting");
  // Input-to-output watchdog. A keystroke to a healthy TUI, shell or editor
  // produces output within a frame. Keys are counted until any output comes
  // back; a pause in typing with several keys still unanswered is a hung
  // agent. Only the operator's own keystrokes count, never a resize, and a
  // password prompt is cleared the moment Enter draws something.
  private silentTimer?: number;
  // When the page last went to the background. A phone freezes a backgrounded
  // page without warning: its socket can be gone while the tab still says
  // connected, and its close event may never arrive.
  private hiddenAt = 0;
  // Only a failed attempt while the browser reports no network sets this: a
  // server that is merely down is still "reconnecting", not "offline".
  private offline = false;
  private lifetime = new AbortController();
  private unansweredKeys = 0;
  unresponsive = false;
  static readonly silenceMs = 2000;
  static readonly silenceKeys = 3;
  private promptOffset = 0;
  private promptHeight = 0;
  private resetPromptPan() {
    this.promptOffset = 0;
    if (this.term.element) this.term.element.style.transform = "";
  }
  private panPrompt(pixels: number): boolean {
    if (!this.connected || this.paused || this.readingRetainedHistory ||
        document.activeElement !== this.term.textarea || navigator.maxTouchPoints < 1 ||
        !matchMedia("(max-width: 1023px)").matches) return false;
    const buffer = this.term.buffer.active;
    if (!this.promptOffset && (pixels <= 0 || buffer.viewportY < buffer.baseY)) return false;
    const height = this.options.host.clientHeight;
    const limit = Math.max(0, Math.min(height - 64, window.innerHeight * 0.6));
    const next = Math.max(0, Math.min(limit, this.promptOffset + pixels));
    if (next === this.promptOffset) return false;
    this.promptOffset = next;
    if (this.term.element) this.term.element.style.transform = `translateY(${-next}px)`;
    return true;
  }
  // Extended key reporting that programs have asked for (extended-keys.ts).
  readonly keyModes = new KeyboardModes();
  // A key encoded here was cancelled; its keypress must not type it again.
  private keySent = false;
  private extendedAllowed() {
    return this.options.extendedKeys?.() ?? true;
  }
  private keyState() {
    const alt = this.term.buffer.active.type === "alternate";
    return {
      flags: this.keyModes.flags(alt),
      modifyOtherKeys: this.keyModes.modifyOtherKeys,
      cursorKeys: this.term.modes.applicationCursorKeysMode,
    };
  }
  /** Whether a program has asked for extended keys and they are allowed. */
  extendedKeysActive() {
    const state = this.keyState();
    return this.extendedAllowed() && (state.flags !== 0 || state.modifyOtherKeys !== 0);
  }
  /** Applies modifiers to bytes from the key bar or a phone keyboard: as the
   * extended encoding when a program asked for it, as legacy bytes otherwise. */
  modify(text: string, mods: Mods): string {
    return this.encodeExtended(text, mods) ?? modified(text, mods);
  }
  /** The extended encoding of these bytes and modifiers, or undefined when
   * none is in force or the key has no extended form. */
  encodeExtended(text: string, mods: Mods): string | undefined {
    if (!this.extendedKeysActive()) return undefined;
    const input = keyFromBytes(text, mods);
    return (input && encodeKey(input, this.keyState())) ?? undefined;
  }
  // Set by the page: the phone key bar's armed Ctrl/Alt, which a hardware
  // key reported as an extended key must carry too.
  stickyModifiers?: { get(): Mods; release(): void };
  private installExtendedKeys() {
    const term = this.term;
    const allowed = () => this.extendedAllowed();
    const alt = () => term.buffer.active.type === "alternate";
    const first = (params: (number | number[])[], index: number, fallback: number) => {
      const value = params[index];
      const n = Array.isArray(value) ? value[0] : value;
      return n === undefined || n === 0 && fallback !== 0 ? fallback : n;
    };
    this.disposables.push(
      // kitty: CSI ? u query, CSI > flags u push, CSI < n u pop, CSI = flags ; mode u set.
      term.parser.registerCsiHandler({ prefix: "?", final: "u" }, () => {
        if (!allowed()) return false;
        this.reply(`\x1b[?${this.keyModes.flags(alt())}u`);
        return true;
      }),
      term.parser.registerCsiHandler({ prefix: ">", final: "u" }, (params) => {
        if (!allowed()) return false;
        this.keyModes.push(alt(), first(params, 0, 0));
        return true;
      }),
      term.parser.registerCsiHandler({ prefix: "<", final: "u" }, (params) => {
        if (!allowed()) return false;
        this.keyModes.pop(alt(), first(params, 0, 1));
        return true;
      }),
      term.parser.registerCsiHandler({ prefix: "=", final: "u" }, (params) => {
        if (!allowed()) return false;
        this.keyModes.set(alt(), first(params, 0, 0), first(params, 1, 1));
        return true;
      }),
      // xterm: CSI > 4 ; n m sets modifyOtherKeys (no value resets it), CSI >
      // 4 n resets it, CSI ? 4 m asks for it. Other resources are not ours.
      term.parser.registerCsiHandler({ prefix: ">", final: "m" }, (params) => {
        if (!allowed() || first(params, 0, 0) !== 4) return false;
        const level = first(params, 1, 0);
        this.keyModes.modifyOtherKeys = level >= 0 && level <= 2 ? level : 0;
        return true;
      }),
      term.parser.registerCsiHandler({ prefix: ">", final: "n" }, (params) => {
        if (!allowed() || first(params, 0, 0) !== 4) return false;
        this.keyModes.modifyOtherKeys = 0;
        return true;
      }),
      term.parser.registerCsiHandler({ prefix: "?", final: "m" }, (params) => {
        if (!allowed() || first(params, 0, 0) !== 4) return false;
        this.reply(`\x1b[>4;${this.keyModes.modifyOtherKeys}m`);
        return true;
      }),
      // A full reset (RIS) clears them; xterm.js still does its own part.
      term.parser.registerEscHandler({ final: "c" }, () => {
        this.keyModes.reset();
        return false;
      }),
      // A program entering the alternate screen starts with nothing set there.
      term.buffer.onBufferChange((buffer) => {
        if (buffer.type === "alternate") this.keyModes.clearScreen(true);
      }),
    );
  }
  // Extended encoding for one key event. Returns false when the event was
  // handled here (sent, or deliberately silent), true to leave it to xterm.js.
  private extendedKey(event: KeyboardEvent): boolean {
    if (event.type === "keypress") {
      const sent = this.keySent;
      this.keySent = false;
      return !sent;
    }
    if (event.type === "keydown") this.keySent = false;
    // Composition belongs to the IME; xterm.js and android-input.ts handle it.
    if (event.isComposing || event.keyCode === 229 || !this.extendedKeysActive()) return true;
    const input = keyInput(event);
    const sticky = this.stickyModifiers?.get();
    const armed = event.type === "keydown" && sticky && (sticky.ctrl || sticky.alt) &&
      !["Shift", "Control", "Alt", "Meta", "AltGraph", "CapsLock", "NumLock"].includes(event.key);
    if (armed) {
      input.ctrlKey ||= sticky.ctrl;
      input.altKey ||= sticky.alt;
    }
    const bytes = encodeKey(input, this.keyState());
    // A modifier key pressed on its own reports nothing, and xterm.js sends
    // nothing for it either.
    if (bytes === null || (bytes === "" && event.type === "keydown")) return true;
    if (armed) this.stickyModifiers?.release();
    if (event.type === "keydown") {
      event.preventDefault();
      this.keySent = true;
    }
    // A release is not a keystroke waiting for an answer: sent without
    // arming the unresponsive-agent watchdog.
    if (bytes && event.type === "keyup") this.reply(bytes);
    else if (bytes) {
      this.input(bytes, false, true);
      if (this.term.options.scrollOnUserInput) this.term.scrollToBottom();
    }
    // xterm.js's own key-up bookkeeping (focus, cursor style) still runs.
    return event.type === "keyup";
  }
  // A terminal's answer to a program's query (or a key release): sent as-is,
  // and not counted as a keystroke waiting for output.
  private reply(text: string) {
    if (this.connected && !this.paused) this.send("0" + text);
  }
  private disposeScroll: () => void;
  private observer: ResizeObserver;
  private disposables: IDisposable[] = [];
  constructor(readonly options: EngineOptions) {
    const prefs = options.prefs();
    this.term = new Terminal({
      fontSize: prefs.fontSize,
      lineHeight: prefs.lineHeight,
      theme: resolveTerminalTheme(prefs.theme),
      fontFamily: "Cascadia Mono, Consolas, Liberation Mono, monospace",
      scrollback: 100000,
      convertEol: false,
      allowProposedApi: true,
      scrollOnUserInput: true,
      disableStdin: true,
    });
    this.term.loadAddon(this.fit);
    this.term.loadAddon(this.search);
    // Paths and web addresses in the output, whole even when wrapped across
    // rows (files/terminalLinks.ts). Taps go through tapAt below.
    this.disposables.push(
      installTerminalLinks(this.term, {
        workdir: () => options.workdir?.() || "",
        exists: (path) => options.linkExists?.(path) ?? false,
        activate: (link) => this.open(link),
      }),
    );
    // OSC 8 hyperlinks (ls --hyperlink, compilers, test runners): web
    // addresses open, file:// links into the workspace open in the viewer.
    this.term.options.linkHandler = {
      allowNonHttpProtocols: true,
      activate: (_event, uri) => {
        const link = hyperlinkTarget(uri, options.workdir?.() || "");
        if (link) this.open(link);
      },
    };
    this.term.open(options.host);
    this.installExtendedKeys();
    // OSC 52: a program (tmux, vim, a remote shell) sets the clipboard. Writes
    // are honoured when allowed; a request to read the clipboard ("?") never
    // is, because it would hand the local clipboard to whatever is running.
    this.disposables.push(this.term.parser.registerOscHandler(52, (data) => {
      const at = data.indexOf(";");
      const payload = at < 0 ? "" : data.slice(at + 1);
      if (!payload || payload === "?" || !(options.osc52?.() ?? true)) return true;
      try {
        const bytes = Uint8Array.from(atob(payload), (ch) => ch.charCodeAt(0));
        const text = new TextDecoder().decode(bytes);
        if (text.length <= 1_000_000) options.clipboard?.(text);
      } catch {}
      return true;
    }));
    if (/Android/i.test(navigator.userAgent) && this.term.textarea) {
      this.androidInput = installAndroidInput(options.host, this.term.textarea,
        text => this.input(text, true));
      this.disposables.push({ dispose: () => this.androidInput?.dispose() });
    }
    this.fit.fit();
    this.disposables.push(
      this.term.onData((data) => this.input(data)),
      this.term.onBinary((data) => this.sendBinary(data)),
      this.term.onResize(({ cols, rows }) => {
        this.resizePending = true;
        this.send("1" + JSON.stringify({ columns: cols, rows }));
        this.term.scrollToBottom();
      }),
      this.search.onDidChangeResults((event) =>
        options.matches(event.resultIndex, event.resultCount),
      ),
    );
    // Clicking or tapping into this terminal makes it the one tmux sizes for.
    const textarea = this.term.textarea;
    if (textarea) {
      const claim = () => this.scheduleFit(true);
      textarea.addEventListener("focus", claim);
      this.disposables.push({
        dispose: () => textarea.removeEventListener("focus", claim),
      });
    }
    this.disposables.push(
      this.term.onSelectionChange(() => {
        const has = this.term.hasSelection();
        if (has !== this.selection) {
          this.selection = has;
          this.changed();
        }
      }),
    );
    // A right-click on a link opens its menu, and is not also sent to the
    // program as a mouse press. Elsewhere the browser's menu is untouched.
    let menuLink: TerminalLink | undefined;
    options.host.addEventListener("mousedown", (event) => {
      menuLink = undefined;
      if (event.button !== 2 || !options.linkMenu || event.shiftKey) return;
      const link = this.linkAtPoint({ x: event.clientX, y: event.clientY });
      if (!link) return;
      menuLink = link;
      event.preventDefault();
      event.stopPropagation();
    }, { signal: this.lifetime.signal, capture: true });
    // A program that tracks the mouse (a full-screen agent, tmux with mouse
    // on) would otherwise get a click on a link and xterm would never open
    // it. A click on a detected link is Lectern's; everywhere else, and with
    // Shift held, clicks go to the program as before.
    let pressed: TerminalLink | undefined;
    const same = (a: TerminalLink, b: TerminalLink | undefined) =>
      !!b && a.kind === b.kind && (a.kind === "url" ? a.url === (b as typeof a).url : a.path === (b as typeof a).path);
    options.host.addEventListener("mousedown", (event) => {
      pressed = undefined;
      // A tap is tapAt's; this is for a mouse.
      const fromTouch = (event as MouseEvent & { sourceCapabilities?: { firesTouchEvents?: boolean } }).sourceCapabilities?.firesTouchEvents;
      if (event.button !== 0 || event.shiftKey || fromTouch || this.term.modes.mouseTrackingMode === "none") return;
      const link = this.linkAtPoint({ x: event.clientX, y: event.clientY });
      if (!link) return;
      pressed = link;
      event.preventDefault();
      event.stopPropagation();
      this.term.focus();
    }, { signal: this.lifetime.signal, capture: true });
    options.host.addEventListener("mouseup", (event) => {
      if (!pressed || event.button !== 0) return;
      const link = pressed;
      pressed = undefined;
      event.preventDefault();
      event.stopPropagation();
      if (same(link, this.linkAtPoint({ x: event.clientX, y: event.clientY }))) this.open(link);
    }, { signal: this.lifetime.signal, capture: true });
    // A long press is a selection here, never the browser's context menu.
    options.host.addEventListener("contextmenu", (event) => {
      const link = menuLink ?? (event.button === 2 && options.linkMenu ? this.linkAtPoint({ x: event.clientX, y: event.clientY }) : undefined);
      menuLink = undefined;
      if (link && options.linkMenu && !(event instanceof PointerEvent && event.pointerType === "touch")) {
        event.preventDefault();
        options.linkMenu(link, { x: event.clientX, y: event.clientY });
        return;
      }
      if (navigator.maxTouchPoints > 0) event.preventDefault();
    }, { signal: this.lifetime.signal });
    this.observer = new ResizeObserver(() => this.scheduleFit());
    this.observer.observe(options.host);
    this.term.textarea?.addEventListener("blur", () => {
      this.controlsPrefix = false;
      this.resetPromptPan();
    }, { signal: this.lifetime.signal });
    this.term.attachCustomKeyEventHandler((event) => {
      if (event.type !== "keydown") return this.extendedKey(event);
      const prefix = event.ctrlKey && !event.altKey && !event.metaKey &&
        (event.key === "]" || event.code === "BracketRight");
      if (this.controlsPrefix || prefix) {
        // Reserve a client-side prefix exactly as the native attachment does.
        // Neither the prefix nor menu keystrokes are sent to the agent.
        event.preventDefault();
        if (event.repeat || ["Control", "Shift", "Alt", "Meta"].includes(event.key)) return false;
        if (this.controlsPrefix) {
          this.controlsPrefix = false;
          if (prefix) this.input("\x1d");
          else if (event.key === "m" && !event.ctrlKey && !event.altKey && !event.metaKey)
            options.controls();
        } else this.controlsPrefix = true;
        return false;
      }
      // Ctrl+C with text selected copies it; with nothing selected it is the
      // interrupt the program expects. Chorded copy, history and the rest are
      // registry shortcuts (shortcuts/registry.ts), handled by the page.
      if (
        (event.ctrlKey || event.metaKey) &&
        !event.shiftKey &&
        event.code === "KeyC" &&
        this.selectedText()
      ) {
        event.preventDefault();
        void copyClipboard(this.selectedText(), () => this.term.focus()).catch(
          (error) => options.notice(errorMessage(error)),
        );
        return false;
      }
      return this.extendedKey(event);
    });
    this.disposeScroll = installTerminalScroll({
      host: options.host,
      term: this.term,
      enabled: () => this.connected && !this.paused && !this.stopped,
      // Held still, the terminal becomes text: the buffer as the phone's own
      // selectable type, which is the only kind a phone can select and copy.
      // Held still, the word under the finger is selected in the live
      // buffer, and the same finger drags the selection. Output keeps
      // arriving; Tools → Pause view still gives the whole buffer as
      // native text.
      longPress: (point) => this.selectAt(point),
      selectMove: (point) => this.extendTo(point),
      selectEnd: () => this.changed(),
      tap: (point) => this.tapAt(point),
      promptPan: pixels => this.panPrompt(pixels),
      // A live selection owns a horizontal drag: it is how text is extended,
      // not how the next terminal is chosen.
      selection: () => this.term.hasSelection(),
      swipe: (direction) => this.options.swipe?.(direction),
      autoscrollHost: options.pane,
      historyViewport: () =>
        this.readingRetainedHistory ? options.frozen : null,
      appKeys: () => this.appKeys(),
      pageKey: (direction) => this.pageKey(direction),
      retainedHistory: (lines) => {
        void this.readRetainedHistory(lines);
      },
      liveIntent: () => {
        ++this.historyRevision;
      },
    });
    // Coming back from a phone's background or a lost network reconnects at
    // once instead of waiting out an exponential backoff that was never told
    // what happened.
    const signal = this.lifetime.signal;
    // A phone that has lost its network cannot use the socket it still holds:
    // show that at once, drop the dead stream, and stop hammering a network
    // that is not there. The explicit event is the signal, not the
    // `navigator.onLine` flag, which a hermetic browser may report wrongly.
    window.addEventListener("offline", () => {
      this.offline = true;
      this.connected = false;
      this.status = t("terminalPage.status.offline");
      this.term.options.disableStdin = true;
      clearTimeout(this.timer);
      this.timer = undefined;
      this.ws?.close();
      this.changed();
    }, { signal });
    // The network is back: reconnect at once, and softly, so the screen keeps
    // what the session already printed and nothing the finger typed is sent
    // a second time.
    window.addEventListener("online", () => {
      this.offline = false;
      this.retry = 500;
      clearTimeout(this.timer);
      this.timer = undefined;
      if (this.connected) this.changed();
      else void this.connect(true);
    }, { signal });
    window.addEventListener("pageshow", () => this.resume(), { signal });
    document.addEventListener("visibilitychange", () => {
      if (document.hidden) {
        this.hiddenAt = Date.now();
        return;
      }
      this.resume();
    }, { signal });
    void this.connect();
  }
  // A socket that outlived a long stay in the background may be a zombie: it
  // still reports open, but nothing has been read or written through it.
  private resume() {
    if (this.stopped || this.paused) return;
    if (!this.connected) {
      this.retry = 500;
      clearTimeout(this.timer);
      this.timer = undefined;
      // A resume reattaches to the same session: the screen keeps what it
      // already printed instead of being wiped by a hard reconnect.
      void this.connect(true);
      return;
    }
    if (
      this.hiddenAt &&
      Date.now() - this.hiddenAt > 60000 &&
      this.ws?.readyState === WebSocket.OPEN
    ) {
      this.hiddenAt = 0;
      this.retry = 500;
      void this.connect(true);
    }
  }
  private changed() {
    if (!this.stopped)
      this.options.change({
        connected: this.connected,
        paused: this.paused,
        status: this.status,
        frozen: this.frozenText,
        retained: this.readingRetainedHistory,
        unresponsive: this.unresponsive,
        offline: this.offline,
        selection: this.selection,
      });
  }
  selection = false;
  private anchor?: { start: [number, number]; end: [number, number] };
  // The buffer cell under a point on screen: column, and absolute row.
  private cellAt(point: { x: number; y: number }) {
    const screen = this.term.element?.querySelector<HTMLElement>(".xterm-screen");
    if (!screen || !this.term.cols || !this.term.rows) return undefined;
    const box = screen.getBoundingClientRect();
    const clamp = (v: number, max: number) => Math.max(0, Math.min(max, v));
    const col = clamp(Math.floor(((point.x - box.left) * this.term.cols) / box.width), this.term.cols - 1);
    const row = clamp(Math.floor(((point.y - box.top) * this.term.rows) / box.height), this.term.rows - 1);
    return { col, row: this.term.buffer.active.viewportY + row, box };
  }
  // A run of non-blank cells: a word, a path, a URL, a number.
  private wordAt(col: number, row: number): [number, number] {
    const line = this.term.buffer.active.getLine(row);
    if (!line) return [col, col];
    const word = (x: number) => {
      const cell = line.getCell(x);
      if (!cell) return false;
      return cell.getWidth() === 0 || (cell.getChars() !== "" && !/\s/.test(cell.getChars()));
    };
    if (!word(col)) return [col, col];
    let start = col,
      end = col;
    while (start > 0 && word(start - 1)) start--;
    while (end < this.term.cols - 1 && word(end + 1)) end++;
    return [start, end];
  }
  private selectRange(a: [number, number], b: [number, number]) {
    const [first, last] = a[1] < b[1] || (a[1] === b[1] && a[0] <= b[0]) ? [a, b] : [b, a];
    this.term.select(first[0], first[1], (last[1] - first[1]) * this.term.cols + last[0] - first[0] + 1);
  }
  selectAt(point: { x: number; y: number }): boolean {
    const cell = this.cellAt(point);
    if (!cell) return false;
    const [start, end] = this.wordAt(cell.col, cell.row);
    this.anchor = { start: [start, cell.row], end: [end, cell.row] };
    this.selectRange(this.anchor.start, this.anchor.end);
    haptic("tick");
    return true;
  }
  extendTo(point: { x: number; y: number }) {
    const cell = this.cellAt(point);
    if (!cell || !this.anchor) return;
    // Near the top or bottom edge the view scrolls, so a selection can grow
    // past what is on screen.
    if (point.y < cell.box.top + 18) this.term.scrollLines(-1);
    else if (point.y > cell.box.bottom - 18) this.term.scrollLines(1);
    const here: [number, number] = [cell.col, cell.row];
    const before = cell.row < this.anchor.start[1] || (cell.row === this.anchor.start[1] && cell.col < this.anchor.start[0]);
    this.selectRange(before ? here : this.anchor.start, before ? this.anchor.end : here);
  }
  /** Grows the selection to whole lines, for copying a command or a stack
   * trace without aiming at its first and last character. */
  selectLines() {
    const at = this.term.getSelectionPosition();
    if (!at) return;
    this.term.selectLines(at.start.y, at.end.x === 0 && at.end.y > at.start.y ? at.end.y - 1 : at.end.y);
  }
  /** The link a selection or tap is on, if it is one. */
  selectedLink(): TerminalLink | undefined {
    const at = this.term.getSelectionPosition();
    if (!at || at.start.y !== at.end.y) return undefined;
    return this.hyperlinkAt(at.start.x, at.start.y) ?? this.textLinkAt(at.start.x, at.start.y);
  }
  // A path or address in the text under a cell. A bare workspace name known
  // not to exist is not a link; one not yet checked is, and is checked on use.
  private textLinkAt(col: number, row: number): TerminalLink | undefined {
    const link = linkAtCell(this.term, col, row, this.options.workdir?.() || "")?.link;
    if (!link || link.kind === "url") return link;
    if (!this.options.workdir?.()) return undefined;
    if (link.verify && this.options.linkExists?.(link.path) === false) return undefined;
    return link;
  }
  private linkAtPoint(point: { x: number; y: number }): TerminalLink | undefined {
    const cell = this.cellAt(point);
    return cell && (this.hyperlinkAt(cell.col, cell.row) ?? this.textLinkAt(cell.col, cell.row));
  }
  private tapAt(point: { x: number; y: number }): boolean {
    if (this.term.hasSelection()) {
      this.term.clearSelection();
      return true;
    }
    const cell = this.cellAt(point);
    if (!cell) return false;
    const link = this.hyperlinkAt(cell.col, cell.row) ?? this.textLinkAt(cell.col, cell.row);
    if (!link || (link.kind === "file" && !this.options.workdir?.())) return false;
    this.open(link);
    return true;
  }
  // xterm has no public way to read an OSC 8 link under a cell; its own
  // hover uses these internals. Guarded, so a future xterm simply falls back
  // to the text-based detection above.
  private hyperlinkAt(col: number, row: number): TerminalLink | undefined {
    try {
      const cell = this.term.buffer.active.getLine(row)?.getCell(col) as unknown as
        { hasExtendedAttrs?(): number; extended?: { urlId?: number } } | undefined;
      const id = cell?.hasExtendedAttrs?.() ? cell.extended?.urlId : 0;
      const service = (this.term as unknown as { _core?: { _oscLinkService?: { getLinkData(id: number): { uri: string } | undefined } } })._core?._oscLinkService;
      const uri = id && service ? service.getLinkData(id)?.uri : undefined;
      return uri ? hyperlinkTarget(uri, this.options.workdir?.() || "") : undefined;
    } catch {
      return undefined;
    }
  }
  private open(link: TerminalLink) {
    haptic("tick");
    this.options.openLink?.(link);
  }
  private armSilence() {
    ++this.unansweredKeys;
    clearTimeout(this.silentTimer);
    this.silentTimer = window.setTimeout(() => {
      this.silentTimer = undefined;
      if (this.stopped || !this.connected) return;
      if (this.unansweredKeys >= Engine.silenceKeys && !this.unresponsive) {
        this.unresponsive = true;
        this.changed();
      }
    }, Engine.silenceMs);
  }
  private sawOutput() {
    clearTimeout(this.silentTimer);
    this.silentTimer = undefined;
    this.unansweredKeys = 0;
    if (this.unresponsive) {
      this.unresponsive = false;
      this.changed();
    }
  }
  private send(text: string) {
    if (this.ws?.readyState === WebSocket.OPEN)
      this.ws.send(new TextEncoder().encode(text));
  }
  // Set by the page for the on-screen modifier keys: a phone keyboard has no
  // Ctrl, so an armed Ctrl has to rewrite whatever is typed next.
  inputFilter?: (text: string) => string;
  input(text: string, fromIME = false, encoded = false) {
    if (!this.connected || this.paused) return;
    if (!fromIME) this.androidInput?.reset();
    if (this.inputFilter && !encoded) {
      const filtered = this.inputFilter(text);
      if (filtered !== text) this.androidInput?.reset();
      text = filtered;
    }
    ++this.historyRevision;
    this.leaveRetainedHistory();
    this.send("0" + text);
    if (text) this.armSilence();
  }
  private sendBinary(text: string) {
    if (!this.connected || this.paused || !this.ws) return;
    ++this.historyRevision;
    this.leaveRetainedHistory();
    const bytes = new Uint8Array(text.length + 1);
    bytes[0] = 48;
    for (let i = 0; i < text.length; i++)
      bytes[i + 1] = text.charCodeAt(i) & 255;
    this.ws.send(bytes);
  }
  // Claims survive a cancelled frame: a plain fit scheduled right after a
  // claiming one must not drop the claim.
  private claimQueued = false;
  scheduleFit(claim = false) {
    if (claim) this.claimQueued = true;
    if (this.fitFrame !== undefined) cancelAnimationFrame(this.fitFrame);
    this.fitFrame = requestAnimationFrame(() => {
      if (
        this.stopped ||
        this.paused ||
        !this.options.host.getBoundingClientRect().width
      )
        return;
      const height = this.options.host.clientHeight;
      if (height > this.promptHeight + 20) this.resetPromptPan();
      this.promptHeight = height;
      this.fit.fit();
      this.options.frozen.style.top = this.options.host.offsetTop + "px";
      this.term.refresh(0, this.term.rows - 1);
      if (this.claimQueued) {
        this.claimQueued = false;
        this.claim();
      }
    });
  }
  // tmux sizes a shared window to the client used last (window-size latest).
  // Re-announcing this view's size makes it that client, so a terminal the
  // operator has just brought into view is drawn at its own size rather than
  // at the size of a phone, hidden tab or native terminal also attached. The
  // kernel only signals a real size change, so step one row down and back;
  // tmux counts both as a resize from this client even though the final size
  // is the one it already had.
  private claim() {
    if (!this.connected || document.hidden || this.term.rows < 2) return;
    const { cols, rows } = this.term;
    this.send("1" + JSON.stringify({ columns: cols, rows: rows - 1 }));
    this.send("1" + JSON.stringify({ columns: cols, rows }));
  }
  async connect(soft = false) {
    if (this.stopped) return;
    // A reconnect to the same session keeps what is on screen: the session is
    // the same, and the new attachment repaints over it. Only a first connect,
    // or a stream that may be a different session, starts from a clean buffer.
    this.androidInput?.reset();
    this.hiddenAt = 0;
    ++this.historyRevision;
    this.leaveRetainedHistory();
    const generation = ++this.generation;
    const current = () => !this.stopped && generation === this.generation;
    clearTimeout(this.timer);
    this.controller?.abort();
    this.controller = new AbortController();
    this.ws?.close();
    this.ws = undefined;
    this.connected = false;
    this.status = t("terminalPage.status.connecting");
    this.term.options.disableStdin = true;
    this.changed();
    try {
      await new Promise<void>((resolve) => this.term.write("", resolve));
      if (!current()) return;
      if (!soft) this.term.reset();
      // Whatever asked for extended keys asks again: tmux on attach, a
      // program when it starts. A mode left from a dead stream would garble
      // keys for whatever is there now.
      this.keyModes.reset();
      if (!this.paused) this.fit.fit();
      const data = await json<{ token: string }>(
        withToken(this.options.url + "/token"),
        { signal: this.controller.signal },
      );
      if (!current()) return;
      const url = new URL(this.options.url + "/ws", location.href);
      url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(withToken(url.toString()), ["tty"]);
      this.ws = ws;
      ws.binaryType = "arraybuffer";
      ws.onopen = () => {
        if (!current()) {
          ws.close();
          return;
        }
        this.pending = 0;
        this.flowPaused = false;
        this.connected = true;
        this.offline = false;
        this.retry = 500;
        this.term.options.disableStdin = this.paused;
        this.send(
          JSON.stringify({
            AuthToken: data.token,
            columns: this.term.cols,
            rows: this.term.rows,
          }),
        );
        this.status = t("terminalPage.status.connected");
        this.changed();
        this.scheduleFit();
      };
      ws.onmessage = (event) => {
        if (!current()) return;
        const bytes = new Uint8Array(event.data as ArrayBuffer);
        if (bytes[0] !== 48) return;
        const content = bytes.subarray(1);
        if (content.length) this.sawOutput();
        this.pending += content.length;
        if (this.pending > 1000000 && !this.flowPaused) {
          this.flowPaused = true;
          this.send("2");
        }
        this.term.write(content, () => {
          if (!current()) return;
          this.pending = Math.max(0, this.pending - content.length);
          if (this.resizePending && this.pending === 0 && !this.paused) {
            this.term.scrollToBottom();
            this.resizePending = false;
          }
          if (this.pending < 100000 && this.flowPaused) {
            this.flowPaused = false;
            this.send("3");
          }
        });
      };
      ws.onclose = () => {
        if (current()) this.reconnect();
      };
      ws.onerror = () => ws.close();
    } catch (error) {
      if (current()) {
        // A failed request plus the browser's offline flag is useful evidence
        // even if its offline event was missed. Never reject a working stream
        // solely because of this flag (private test networks can report false).
        this.offline = this.offline || navigator.onLine === false;
        this.reconnect(
          error instanceof Error ? error.message : errorMessage(error),
        );
      }
    }
  }
  private reconnect(message = t("terminalPage.status.reconnecting")) {
    ++this.generation;
    this.connected = false;
    this.status = this.offline ? t("terminalPage.status.offline") : message;
    this.term.options.disableStdin = true;
    this.changed();
    if (this.stopped) return;
    clearTimeout(this.timer);
    this.timer = undefined;
    // Online events reconnect immediately. A slow fallback also covers a
    // missed event or a browser with an inaccurate network flag.
    this.timer = window.setTimeout(() => {
      this.timer = undefined;
      if (!this.connected) void this.connect(true);
    }, this.offline ? 10000 : this.retry);
    this.retry = Math.min(this.retry * 2, 10000);
  }
  paste(text: string) {
    if (!this.connected)
      throw new Error(t("terminalPage.errors.disconnected"));
    if (this.paused) this.freeze(false);
    this.term.paste(text);
    this.term.focus();
  }
  copySelection() {
    const text = this.selectedText();
    if (!text) return Promise.resolve(false);
    return copyClipboard(text, () => this.term.focus()).then(() => true);
  }
  selectedText() {
    const selection = window.getSelection();
    return selection?.toString() &&
      this.options.frozen.contains(selection.anchorNode)
      ? selection.toString()
      : this.term.getSelection();
  }
  async readRetainedHistory(lines: number) {
    if (
      this.loadingHistory ||
      this.paused ||
      this.readingRetainedHistory ||
      this.stopped ||
      Date.now() - this.lastHistoryRead < 1000
    )
      return;
    const revision = this.historyRevision;
    this.lastHistoryRead = Date.now();
    this.loadingHistory = true;
    try {
      const out = await json<History>(
        this.options.url.replace("/term/", "/api/term/") + "/history",
      );
      if (this.stopped || this.paused || revision !== this.historyRevision)
        return;
      if (out.app_screen) {
        // The program keeps its own transcript: scroll it with its keys,
        // now and for the wheel from here on.
        this.appScreen = true;
        this.appScreenCheckedAt = Date.now();
        this.pageKey(-1);
        return;
      }
      if (out.text.trimEnd().split("\n").length <= this.term.rows) {
        this.options.notice(
          t("terminalPage.notice.noOlderOutput"),
        );
        return;
      }
      this.frozenText = out.text;
      this.retainedLines = lines;
      this.readingRetainedHistory = true;
      this.changed();
    } catch (error) {
      if (!this.stopped)
        this.options.notice(
          error instanceof Error ? error.message : errorMessage(error),
        );
    } finally {
      this.loadingHistory = false;
    }
  }
  syncFrozenLayout() {
    if (this.stopped || !this.readingRetainedHistory) return;
    const frozen = this.options.frozen;
    frozen.style.top = this.options.host.offsetTop + "px";
    frozen.scrollTop =
      frozen.scrollHeight -
      frozen.clientHeight +
      this.retainedLines * this.options.prefs().fontSize;
  }
  leaveRetainedHistory() {
    if (!this.readingRetainedHistory) return;
    this.readingRetainedHistory = false;
    this.term.scrollToBottom();
    this.changed();
  }
  freeze(on: boolean, snapshot?: string) {
    ++this.historyRevision;
    if (on && this.readingRetainedHistory) snapshot ??= this.frozenText;
    this.leaveRetainedHistory();
    this.paused = on;
    if (on) {
      const lines: string[] = [],
        buffer = this.term.buffer.active;
      for (let i = 0; i < buffer.length; i++)
        lines.push(buffer.getLine(i)?.translateToString(true) || "");
      // The rows under the cursor are blank; left in, the frozen view would
      // open scrolled to an empty screen instead of to what was just showing.
      while (lines.length && !lines[lines.length - 1]) lines.pop();
      this.frozenText = snapshot ?? lines.join("\n");
    }
    this.term.options.disableStdin = on || !this.connected;
    this.changed();
    if (!on)
      requestAnimationFrame(() => {
        if (this.stopped) return;
        this.fit.fit();
        this.term.scrollToBottom();
        this.term.focus();
      });
  }
  applyPrefs() {
    const prefs = this.options.prefs();
    this.term.options.fontSize = prefs.fontSize;
    this.term.options.lineHeight = prefs.lineHeight;
    this.term.options.theme = resolveTerminalTheme(prefs.theme);
    if (!this.paused) this.scheduleFit();
  }
  dispose() {
    this.stopped = true;
    this.lifetime.abort();
    ++this.generation;
    this.controller?.abort();
    if (this.fitFrame !== undefined) cancelAnimationFrame(this.fitFrame);
    clearTimeout(this.timer);
    clearTimeout(this.silentTimer);
    this.ws?.close();
    this.disposeScroll();
    this.observer.disconnect();
    this.disposables.forEach((disposable) => disposable.dispose());
    this.term.dispose();
  }
}
