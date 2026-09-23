import { errorMessage } from "./model";
import { Terminal, type IDisposable } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { installTerminalScroll } from "./scroll";
import {
  copyClipboard,
  json,
  request,
  themes,
  withToken,
  type History,
  type Prefs,
} from "./model";
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
}
import { installAndroidInput } from "./android-input";
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
  private retainedLines = 0;
  private frozenText = "";
  private status = "Connecting…";
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
  private disposeScroll: () => void;
  private observer: ResizeObserver;
  private disposables: IDisposable[] = [];
  constructor(readonly options: EngineOptions) {
    const prefs = options.prefs();
    this.term = new Terminal({
      fontSize: prefs.fontSize,
      lineHeight: prefs.lineHeight,
      theme: themes[prefs.theme] || themes.slate,
      fontFamily: "Cascadia Mono, Consolas, Liberation Mono, monospace",
      scrollback: 100000,
      convertEol: false,
      allowProposedApi: true,
      scrollOnUserInput: true,
      disableStdin: true,
    });
    this.term.loadAddon(this.fit);
    this.term.loadAddon(this.search);
    this.term.loadAddon(
      new WebLinksAddon((_event, url) => {
        if (/^https?:\/\//i.test(url)) window.open(url, "_blank", "noopener");
      }),
    );
    this.term.open(options.host);
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
    this.observer = new ResizeObserver(() => this.scheduleFit());
    this.observer.observe(options.host);
    this.term.textarea?.addEventListener("blur", () => {
      this.controlsPrefix = false;
    }, { signal: this.lifetime.signal });
    this.term.attachCustomKeyEventHandler((event) => {
      if (event.type !== "keydown") return true;
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
      if (
        (event.ctrlKey || event.metaKey) &&
        event.shiftKey &&
        event.code === "KeyF"
      ) {
        event.preventDefault();
        options.history();
        return false;
      }
      if (
        (event.ctrlKey || event.metaKey) &&
        event.code === "KeyC" &&
        (event.shiftKey || this.selectedText())
      ) {
        event.preventDefault();
        void copyClipboard(this.selectedText(), () => this.term.focus()).catch(
          (error) => options.notice(errorMessage(error)),
        );
        return false;
      }
      return true;
    });
    this.disposeScroll = installTerminalScroll({
      host: options.host,
      term: this.term,
      enabled: () => this.connected && !this.paused && !this.stopped,
      // Held still, the terminal becomes text: the buffer as the phone's own
      // selectable type, which is the only kind a phone can select and copy.
      longPress: () => this.freeze(true),
      // A live selection owns a horizontal drag: it is how text is extended,
      // not how the next terminal is chosen.
      selection: () => this.term.hasSelection(),
      swipe: (direction) => this.options.swipe?.(direction),
      autoscrollHost: options.pane,
      historyViewport: () =>
        this.readingRetainedHistory ? options.frozen : null,
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
      this.status = "Offline";
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
      });
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
  input(text: string, fromIME = false) {
    if (!this.connected || this.paused) return;
    if (!fromIME) this.androidInput?.reset();
    if (this.inputFilter) {
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
    this.status = "Connecting…";
    this.term.options.disableStdin = true;
    this.changed();
    try {
      await new Promise<void>((resolve) => this.term.write("", resolve));
      if (!current()) return;
      if (!soft) this.term.reset();
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
        this.status = "Connected";
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
  private reconnect(message = "Reconnecting…") {
    ++this.generation;
    this.connected = false;
    this.status = this.offline ? "Offline" : message;
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
      throw new Error(
        "Terminal disconnected. Reconnect before inserting a path.",
      );
    if (this.paused) this.freeze(false);
    this.term.paste(text);
    this.term.focus();
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
      if (out.text.trimEnd().split("\n").length <= this.term.rows) {
        this.options.notice(
          "No older output is retained in tmux. Full-screen chats may keep their history inside the app.",
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
    this.term.options.theme = themes[prefs.theme] || themes.slate;
    if (!this.paused) this.scheduleFit();
  }
  fileLinks(workdir: string, preview: (path: string) => void) {
    this.disposables.push(
      this.term.registerLinkProvider({
        provideLinks: (y, callback) => {
          const text =
              this.term.buffer.active.getLine(y - 1)?.translateToString() || "",
            links = [];
          for (const match of text.matchAll(
            /(?:\/|\.\/)?[\w@.+~-]+(?:\/[\w@.+~-]+)*\.[a-zA-Z0-9]{1,12}(?::\d+(?::\d+)?)?/g,
          )) {
            let value = match[0].replace(/:\d+(?::\d+)?$/, "");
            if (value.startsWith("/") && !value.startsWith(workdir + "/"))
              continue;
            if (value.startsWith(workdir + "/"))
              value = value.slice(workdir.length + 1);
            links.push({
              text: match[0],
              range: {
                start: { x: match.index + 1, y },
                end: { x: match.index + match[0].length, y },
              },
              activate: () => preview(value),
            });
          }
          callback(links);
        },
      }),
    );
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
