import { openNativeSearch } from "/native-search.js";
import { openNativeHistory } from "/native-history.js";
import { openReview } from "/review.js";
import { installTerminalScroll } from "/terminal-scroll.js";
import '/ui-menu.js';
const $ = (s) => document.querySelector(s);
const [, , kind, id] = location.pathname.split("/");
const base = `/api/term/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`;
const encoder = new TextEncoder();
const embedded = new URLSearchParams(location.search).get("embed") === "1";
document.body.classList.toggle("embedded", embedded);
const mobileTerminal = matchMedia('(max-width:1023px)');
const shortTerminal = matchMedia('(max-height:380px)');
const applyTerminalChrome = () => document.body.classList.toggle('compact-chrome',
  document.body.classList.contains('compact-terminal') ||
  (document.body.classList.contains('mobile-terminal') && shortTerminal.matches));
shortTerminal.addEventListener('change', applyTerminalChrome);
const applyStandaloneLayout = () => {
  if (!embedded) document.body.classList.toggle('mobile-terminal', mobileTerminal.matches);
  applyTerminalChrome();
};
mobileTerminal.addEventListener('change', applyStandaloneLayout);
applyStandaloneLayout();
let disposePDF = () => {};
let panes = [],
  active,
  info,
  uploads = 0,
  dir = ".",
  previewURL = "",
  historyText = "",
  historyMarks = [],
  historyIndex = -1;
let prefs = { fontSize: 15, lineHeight: 1.15, theme: "slate" };
try {
  Object.assign(
    prefs,
    JSON.parse(localStorage.getItem("lec-terminal-prefs") || "{}"),
  );
} catch {}
prefs.fontSize = Math.max(10, Math.min(30, Number(prefs.fontSize) || 15));
const themes = {
  slate: {
    background: "#10121c",
    foreground: "#e0e4f0",
    cursor: "#b6a4ff",
    selectionBackground: "#66578a",
  },
  black: { background: "#000000", foreground: "#e9e9e9", cursor: "#ffffff" },
  light: {
    background: "#f6f4ee",
    foreground: "#202334",
    cursor: "#453575",
    selectionBackground: "#c7b9ee",
  },
};
function token() {
  return localStorage.getItem("lec-token") || "";
}
function authURL(url) {
  return (
    url +
    (url.includes("?") ? "&" : "?") +
    "token=" +
    encodeURIComponent(token())
  );
}
async function request(url, options = {}) {
  const headers = { ...options.headers };
  if (token()) headers.Authorization = "Bearer " + token();
  const r = await fetch(url, { ...options, headers });
  if (!r.ok) {
    let message = r.statusText;
    try {
      message = (await r.json()).detail || message;
    } catch {}
    const error = Error(message); error.status = r.status; throw error;
  }
  return r;
}
async function api(suffix, options) {
  return (await request(base + suffix, options)).json();
}
function notice(text, error = false) {
  $("#notice").textContent = text;
  $("#notice").classList.toggle("error", error);
  $("#notice").hidden = !text;
}
function act(fn) {
  return (...args) =>
    Promise.resolve()
      .then(() => fn(...args))
      .catch((e) => notice(e.message, true));
}
function copyWithExecCommand(text, restoreFocus) {
  const previous = document.activeElement;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  let copied = false;
  try {
    copied = document.execCommand("copy");
  } finally {
    textarea.remove();
    if (restoreFocus) restoreFocus();
    else if (previous instanceof HTMLElement) previous.focus({ preventScroll: true });
  }
  if (!copied) throw Error("Clipboard access is unavailable. Use your browser's Copy command.");
}
async function copyClipboard(text, restoreFocus) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {}
  }
  copyWithExecCommand(text, restoreFocus);
}
function quote(path) {
  return "'" + path.replaceAll("'", "'\\''") + "'";
}
function select(p) {
  active = p;
  panes.forEach((x) => x.el.classList.toggle("active", x === p));
  $("#pause").textContent = p.paused ? "Resume view" : "Pause view";
  $("#pause").setAttribute("aria-pressed", String(p.paused));
  $('#terminal-tools-summary').textContent = p.paused ? 'Paused · Tools' : 'Tools';
  $("#connection").textContent = p.connected ? "Connected" : "Reconnecting…";
  p.el.classList.toggle('connected', p.connected);
  $('#compact-status').classList.toggle('connected', p.connected);
  $('#compact-status').title = p.connected ? 'Connected' : 'Reconnecting…';
  $('#compact-status').setAttribute('aria-label', p.connected ? 'Terminal connected' : 'Terminal reconnecting');
  document.querySelectorAll('[data-terminal-key]').forEach(button => button.disabled = !p.connected || p.paused);
}
// The containing tab strip uses this small read-only bridge to respect xterm's
// own canvas selection and negotiated application mouse mode. DOM selection is
// not reliable with xterm's renderer.
window.__lecTerminalState = () => ({
  hasSelection: !!active?.term?.hasSelection(),
  mouseTrackingMode: active?.term?.modes?.mouseTrackingMode || 'none',
});
class Pane {
  constructor(el, url) {
    this.el = el;
    this.url = url;
    this.connected = false;
    this.stopped = false;
    this.paused = false;
    this.historyRevision = 0;
    this.retry = 500;
    this.generation = 0;
    this.term = new Terminal({
      fontSize: prefs.fontSize,
      lineHeight: Number(prefs.lineHeight),
      theme: themes[prefs.theme] || themes.slate,
      fontFamily: "Cascadia Mono, Consolas, Liberation Mono, monospace",
      scrollback: 100000,
      convertEol: false,
      allowProposedApi: true,
      scrollOnUserInput: true,
      disableStdin: true,
    });
    this.fit = new FitAddon.FitAddon();
    this.search = new SearchAddon.SearchAddon();
    this.term.loadAddon(this.fit);
    this.term.loadAddon(this.search);
    this.term.loadAddon(
      new WebLinksAddon.WebLinksAddon((e, url) => {
        if (/^https?:\/\//i.test(url)) window.open(url, "_blank", "noopener");
      }),
    );
    el.querySelector(".terminal-host").style.background = (
      themes[prefs.theme] || themes.slate
    ).background;
    this.term.open(el.querySelector(".terminal-host"));
    this.fit.fit();
    this.term.onData((data) => this.input(data));
    this.term.onBinary((data) => this.sendBinary(data));
    this.term.onResize(({ cols, rows }) => {
      this.resizePending = true;
      this.send("1" + JSON.stringify({ columns: cols, rows }));
      // Reflow can leave the viewport showing old redraws in scrollback.
      // A live terminal must stay on the actual screen after its grid changes.
      this.term.scrollToBottom();
    });
    this.search.onDidChangeResults((e) => {
      if (active === this)
        $("#matches").textContent = e.resultCount
          ? `${e.resultIndex + 1} / ${e.resultCount}`
          : "No matches";
    });
    this.observer = new ResizeObserver(() => this.scheduleFit());
    this.observer.observe(el.querySelector(".terminal-host"));
    el.addEventListener("pointerdown", () => select(this));
    el.addEventListener("focusin", () => select(this));
    const frozen = el.querySelector(".frozen");
    this.frozen = frozen;
    frozen.addEventListener("scroll", () => {
      if (this.readingRetainedHistory && frozen.scrollHeight - frozen.clientHeight - frozen.scrollTop <= 2)
        this.leaveRetainedHistory();
    });
    frozen.addEventListener("click", () => {
      if (this.readingRetainedHistory && !window.getSelection()?.toString()) this.term.focus();
    });
    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.code === "KeyF") {
        e.preventDefault();
        act(showHistory)();
        return false;
      }
      const copyShortcut =
        (e.ctrlKey || e.metaKey) && e.shiftKey && e.code === "KeyC";
      const selectedCopy =
        (e.ctrlKey || e.metaKey) && !e.shiftKey && e.code === "KeyC" && this.selectedText();
      if (copyShortcut || selectedCopy) {
        e.preventDefault();
        act(() => copyClipboard(this.selectedText(), () => this.term.focus()))();
        return false;
      }
      return true;
    });
    this.disposeScroll = installTerminalScroll({
      host: el.querySelector(".terminal-host"), term: this.term,
      enabled: () => this.connected && !this.paused && !this.stopped,
      autoscrollHost: el,
      historyViewport: () => this.readingRetainedHistory ? frozen : null,
      retainedHistory: (lines) => this.readRetainedHistory(lines),
      liveIntent: () => { ++this.historyRevision; },
    });
    this.connect();
  }
  send(s) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(encoder.encode(s));
  }
  input(s) {
    if (!this.connected || this.paused) return;
    ++this.historyRevision;
    this.leaveRetainedHistory();
    this.send("0" + s);
  }
  sendBinary(s) {
    if (!this.connected || this.paused) return;
    ++this.historyRevision;
    this.leaveRetainedHistory();
    const b = new Uint8Array(s.length + 1);
    b[0] = 48;
    for (let i = 0; i < s.length; i++) b[i + 1] = s.charCodeAt(i) & 255;
    this.ws.send(b);
  }
  scheduleFit() {
    cancelAnimationFrame(this.fitFrame);
    this.fitFrame = requestAnimationFrame(() => {
      if (this.stopped || this.paused || !this.el.getBoundingClientRect().width) return;
      this.fit.fit();
      this.el.querySelector(".frozen").style.top = this.el.querySelector(".terminal-host").offsetTop + "px";
      this.term.refresh(0, this.term.rows - 1);
    });
  }
  async connect() {
    if (this.stopped) return;
    ++this.historyRevision;
    this.leaveRetainedHistory();
    const generation = ++this.generation;
    const current = () => !this.stopped && generation === this.generation;
    clearTimeout(this.timer);
    this.controller?.abort();
    this.controller = new AbortController();
    this.ws?.close();
    this.ws = null;
    this.connected = false;
    this.el.classList.remove('connected');
    const status = this.el.querySelector('.pane-status');
    if (!status.textContent || status.textContent === 'Connected') status.textContent = 'Connecting…';
    this.term.options.disableStdin = true;
    if (active === this) select(this);
    try {
      // xterm parses writes asynchronously. Drain the old stream before reset;
      // otherwise its queued escape sequences can corrupt the new redraw.
      await new Promise((resolve) => this.term.write("", resolve));
      if (!current()) return;
      this.term.reset();
      if (!this.paused) this.fit.fit();
      const r = await request(authURL(this.url + "/token"), {signal: this.controller.signal});
      const t = await r.json();
      if (!current()) return;
      const ws = new WebSocket(
        authURL((location.protocol === "https:" ? "wss://" : "ws://") + location.host + this.url + "/ws"),
        ["tty"],
      );
      this.ws = ws;
      ws.binaryType = "arraybuffer";
      ws.onopen = () => {
        if (!current()) { ws.close(); return; }
        this.pending = 0;
        this.flowPaused = false;
        this.connected = true;
        this.el.classList.add('connected');
        this.retry = 500;
        this.term.options.disableStdin = this.paused;
        this.send(JSON.stringify({AuthToken: t.token, columns: this.term.cols, rows: this.term.rows}));
        this.el.querySelector(".pane-status").textContent = "Connected";
        if (active === this) select(this);
        this.scheduleFit();
      };
      ws.onmessage = (e) => {
        if (!current()) return;
        const b = new Uint8Array(e.data);
        if (b[0] !== 48) return;
        const data = b.subarray(1);
        this.pending += data.length;
        if (this.pending > 1000000 && !this.flowPaused) {
          this.flowPaused = true; this.send("2");
        }
        this.term.write(data, () => {
          if (!current()) return;
          this.pending = Math.max(0, this.pending - data.length);
          if (this.resizePending && this.pending === 0 && !this.paused) {
            this.term.scrollToBottom();
            this.resizePending = false;
          }
          if (this.pending < 100000 && this.flowPaused) {
            this.flowPaused = false; this.send("3");
          }
        });
      };
      ws.onclose = () => { if (current()) this.reconnect(); };
      ws.onerror = () => ws.close();
    } catch (e) {
      if (!current()) return;
      this.reconnect(e.message);
    }
  }
  reconnect(error = '') {
    // Invalidate all callbacks immediately, including xterm write completions.
    ++this.generation;
    this.connected = false;
    this.el.classList.remove('connected');
    this.el.querySelector('.pane-status').textContent = error || 'Reconnecting…';
    this.term.options.disableStdin = true;
    if (active === this) select(this);
    if (this.stopped) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.connect(), this.retry);
    this.retry = Math.min(this.retry * 2, 10000);
  }
  paste(s) {
    if (!this.connected)
      throw Error("Terminal disconnected. Reconnect before inserting a path.");
    if (this.paused) this.freeze(false);
    this.term.paste(s);
    this.term.focus();
  }
  selectedText() {
    const selection = window.getSelection();
    if (selection?.toString() && this.frozen.contains(selection.anchorNode))
      return selection.toString();
    return this.term.getSelection();
  }
  async readRetainedHistory(lines) {
    if (this.loadingHistory || this.paused || this.readingRetainedHistory || this.stopped || Date.now() - (this.lastHistoryRead || 0) < 1000) return;
    const revision = this.historyRevision;
    this.lastHistoryRead = Date.now();
    this.loadingHistory = true;
    try {
      const url = this.url.replace("/term/", "/api/term/") + "/history";
      const out = await (await request(url)).json();
      if (this.stopped || this.paused || revision !== this.historyRevision) return;
      if (out.text.trimEnd().split("\n").length <= this.term.rows) {
        notice("No older output is retained in tmux. Full-screen chats may keep their history inside the app.");
        return;
      }
      const frozen = this.el.querySelector(".frozen");
      frozen.textContent = out.text;
      frozen.style.fontSize = prefs.fontSize + "px";
      frozen.style.top = this.el.querySelector(".terminal-host").offsetTop + "px";
      frozen.classList.add("retained-history");
      frozen.hidden = false;
      frozen.scrollTop = frozen.scrollHeight - frozen.clientHeight + lines * prefs.fontSize;
      this.readingRetainedHistory = true;
    } catch (e) { notice(e.message, true); }
    finally { this.loadingHistory = false; }
  }
  leaveRetainedHistory() {
    if (!this.readingRetainedHistory) return;
    this.readingRetainedHistory = false;
    const frozen = this.el.querySelector(".frozen");
    frozen.hidden = true;
    frozen.classList.remove("retained-history");
    this.term.scrollToBottom();
    // Keep keyboard focus where it was: returning by touch must not open the keyboard.
  }
  freeze(on, snapshot) {
    ++this.historyRevision;
    if (on && this.readingRetainedHistory) snapshot ??= this.el.querySelector(".frozen").textContent;
    this.leaveRetainedHistory();
    this.paused = on;
    const frozen = this.el.querySelector(".frozen");
    if (on) {
      let lines = [];
      const b = this.term.buffer.active;
      for (let i = 0; i < b.length; i++)
        lines.push(b.getLine(i)?.translateToString(true) || "");
      frozen.textContent = snapshot ?? lines.join("\n");
      frozen.style.fontSize = prefs.fontSize + "px";
    }
    frozen.hidden = !on;
    this.el.querySelector(".terminal-host").hidden = on;
    this.term.options.disableStdin = on || !this.connected;
    if (!on) {
      this.fit.fit();
      this.term.scrollToBottom();
      this.term.focus();
    }
    select(this);
  }
  dispose() {
    this.stopped = true;
    ++this.generation;
    this.controller?.abort();
    cancelAnimationFrame(this.fitFrame);
    clearTimeout(this.timer);
    this.ws?.close();
    this.disposeScroll?.();
    this.observer.disconnect();
    this.term.dispose();
  }
}
document.addEventListener("keydown", (e) => {
  const copyShortcut =
    (e.ctrlKey || e.metaKey) && e.shiftKey && e.code === "KeyC";
  const selectedCopy =
    (e.ctrlKey || e.metaKey) && !e.shiftKey && e.code === "KeyC";
  if (!(copyShortcut || selectedCopy)) return;
  const selection = window.getSelection();
  const text = selection?.toString();
  const pane = selection?.anchorNode && panes.find((p) => p.frozen.contains(selection.anchorNode));
  if (!pane || !text) return;
  e.preventDefault();
  e.stopImmediatePropagation();
  act(() => copyClipboard(text, () => pane.term.focus()))();
}, true);
// Mobile keyboards, font loading and returning from a background tab can all
// change cell geometry without a normal window resize.
const fitPanes = () => panes.forEach((p) => p.scheduleFit());
window.visualViewport?.addEventListener("resize", fitPanes);
document.fonts?.ready.then(fitPanes);
window.addEventListener("focus", fitPanes);
window.addEventListener("pageshow", fitPanes);
window.addEventListener("message", (e) => {
  if (embedded && e.source === parent && e.origin === location.origin && e.data?.type === "lec-terminal-visible") {
    document.body.classList.toggle('compact-terminal', e.data.compact === true);
    document.body.classList.toggle('mobile-terminal', e.data.mobile === true || (e.data.mobile === undefined && e.data.compact === true));
    applyTerminalChrome();
    fitPanes();
  }
});
const terminalKeys = {escape:'\x1b',tab:'\t',left:'\x1b[D',up:'\x1b[A',down:'\x1b[B',right:'\x1b[C',interrupt:'\x03'};
document.querySelectorAll('[data-terminal-key]').forEach(button => {
  button.addEventListener('pointerdown', event => event.preventDefault());
  button.onclick = () => {
    if (!active?.connected || active.paused) return;
    let key = terminalKeys[button.dataset.terminalKey];
    if (active.term.modes.applicationCursorKeysMode && /^\x1b\[[ABCD]$/.test(key)) key = key.replace('[', 'O');
    active.input(key);
    active.term.scrollToBottom();
    active.term.focus();
  };
});
$('#compact-upload').onclick = () => $('#upload').click();
$('#compact-files').onclick = () => $('#files').click();
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) fitPanes();
});
$("#reconnect").onclick = () => {
  if (active?.paused) active.freeze(false);
  active?.connect();
};
async function uploadFiles(files, pane = active) {
  if (!info.files_available)
    throw Error("Uploads unavailable for this workspace.");
  if (!pane?.connected)
    throw Error("Connect to the terminal before uploading.");
  uploads++;
  $("#upload").disabled = true;
  try {
    for (const f of files) {
      if (f.size > 25 * 1024 * 1024) throw Error(`${f.name}: exceeds 25 MiB`);
      notice(`Uploading ${f.name}…`);
      const data = new FormData();
      data.append("file", f, f.name || `screenshot-${Date.now()}.png`);
      const a = await api("/attachments", { method: "POST", body: data });
      try {
        pane.paste(quote(a.path) + " ");
        notice(`${a.name} uploaded. Path inserted; press Enter when ready.`);
      } catch (e) {
        notice(`Uploaded to ${a.path}. ${e.message}`, true);
        return;
      }
    }
  } finally {
    uploads--;
    $("#upload").disabled = uploads > 0 || !info.files_available;
  }
}
$("#upload").onclick = () => $("#file-input").click();
$("#file-input").onchange = act(async (e) => {
  await uploadFiles([...e.target.files]);
  e.target.value = "";
});
let dragDepth = 0;
document.addEventListener("dragenter", (e) => {
  if ([...e.dataTransfer.types].includes("Files")) {
    e.preventDefault();
    dragDepth++;
    document.body.classList.add("dragging");
  }
});
document.addEventListener("dragover", (e) => {
  if ([...e.dataTransfer.types].includes("Files")) e.preventDefault();
});
document.addEventListener("dragleave", () => {
  if (--dragDepth <= 0) document.body.classList.remove("dragging");
});
document.addEventListener("drop", (e) => {
  if (!e.dataTransfer.files.length) return;
  e.preventDefault();
  dragDepth = 0;
  document.body.classList.remove("dragging");
  const pane = panes.find((p) => p.el.contains(e.target)) || active;
  act(() => uploadFiles([...e.dataTransfer.files], pane))();
});
document.addEventListener(
  "paste",
  (e) => {
    const files = [...(e.clipboardData?.items || [])]
      .filter((x) => x.kind === "file")
      .map((x) => x.getAsFile())
      .filter(Boolean);
    if (!files.length) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    act(() => uploadFiles(files))();
  },
  true,
);
$("#find").onclick = () => {
  $("#searchbar").hidden = false;
  $("#search-input").focus();
};
function find(back = false) {
  const text = $("#search-input").value;
  if (text)
    active.search[back ? "findPrevious" : "findNext"](text, {
      decorations: {
        matchBackground: "#66512c",
        activeMatchBackground: "#b7a0ff",
        matchOverviewRuler: "#66512c",
        activeMatchColorOverviewRuler: "#b7a0ff",
      },
    });
  else active.search.clearDecorations();
}
$("#search-input").oninput = () => find();
$("#search-input").onkeydown = (e) => {
  if (e.key === "Enter") find(e.shiftKey);
  if (e.key === "Escape") $("#search-close").click();
};
$("#previous").onclick = () => find(true);
$("#next").onclick = () => find();
$("#search-close").onclick = () => {
  $("#searchbar").hidden = true;
  active.search.clearDecorations();
  active.term.focus();
};
$("#pause").onclick = () => active.freeze(!active.paused);
$("#bottom").onclick = () => {
  active.freeze(false);
  active.term.scrollToBottom();
};
$("#shell").onclick = act(async () => {
  const existing = panes.find((p) => p !== panes[0]);
  if (existing) {
    existing.dispose();
    existing.el.remove();
    panes = panes.filter((p) => p !== existing);
    select(panes[0]);
    $("#shell").textContent = "Split shell";
    return;
  }
  if (!info.shell_url) return;
  const el = document.createElement("section");
  el.className = "pane";
  el.innerHTML =
    '<div class="pane-title"><span>Companion shell</span><span class="pane-status">Connecting…</span></div><div class="terminal-host"></div><pre class="frozen" hidden></pre>';
  $("#workspace").append(el);
  const p = new Pane(el, info.shell_url);
  panes.push(p);
  select(p);
  $("#shell").textContent = "Hide shell";
});
function savePrefs() {
  prefs = {
    fontSize: Math.max(10, Math.min(30, Number($("#font-size").value) || 15)),
    lineHeight: Number($("#line-height").value),
    theme: $("#theme").value,
  };
  localStorage.setItem("lec-terminal-prefs", JSON.stringify(prefs));
  for (const p of panes) {
    p.term.options.fontSize = prefs.fontSize;
    p.term.options.lineHeight = prefs.lineHeight;
    p.term.options.theme = themes[prefs.theme];
    p.el.querySelector(".terminal-host").style.background =
      themes[prefs.theme].background;
    if (!p.paused) p.fit.fit();
  }
}
$("#preferences").onclick = () => {
  $("#font-size").value = prefs.fontSize;
  $("#line-height").value = prefs.lineHeight;
  $("#theme").value = prefs.theme;
  $("#settings-dialog").showModal();
};
for (const s of ["#font-size", "#line-height", "#theme"])
  $(s).onchange = savePrefs;
document
  .querySelectorAll("[data-close]")
  .forEach((b) => (b.onclick = () => b.closest("dialog").close()));
function blobDownload(blob, name) {
  const a = document.createElement("a"),
    u = URL.createObjectURL(blob);
  a.href = u;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(u), 30000);
}
let historyPane;
async function loadHistory() {
  const historyBase = historyPane.url.replace("/term/", "/api/term/");
  const out = await (await request(historyBase + "/history")).json();
  historyText = out.text;
  $("#history-note").textContent =
    `Snapshot of ${historyPane === panes[0] ? "agent" : "companion shell"} tmux history · up to ${out.limit_lines.toLocaleString()} retained lines${out.truncated ? " · limited to final 8 MiB" : ""}`;
  renderHistory();
}
async function showHistory() {
  historyPane = active;
  $("#history-dialog").showModal();
  $("#history-query").focus();
  await loadHistory();
}
function renderHistory() {
  const container = $("#history-text");
  container.replaceChildren();
  historyMarks = [];
  historyIndex = -1;
  const q = $("#history-query").value;
  if (!q) {
    container.textContent = historyText;
    $("#history-count").textContent = "";
    return;
  }
  const hay = historyText.toLowerCase(),
    needle = q.toLowerCase();
  let from = 0,
    at;
  const frag = document.createDocumentFragment();
  while (
    (at = hay.indexOf(needle, from)) !== -1 &&
    historyMarks.length < 2000
  ) {
    frag.append(document.createTextNode(historyText.slice(from, at)));
    const m = document.createElement("mark");
    m.textContent = historyText.slice(at, at + q.length);
    frag.append(m);
    historyMarks.push(m);
    from = at + q.length;
  }
  frag.append(document.createTextNode(historyText.slice(from)));
  container.append(frag);
  stepHistory(1);
}
function stepHistory(delta) {
  if (!historyMarks.length) {
    $("#history-count").textContent = "No matches";
    return;
  }
  historyMarks[historyIndex]?.classList.remove("current");
  historyIndex =
    (historyIndex + delta + historyMarks.length) % historyMarks.length;
  const m = historyMarks[historyIndex];
  m.classList.add("current");
  m.scrollIntoView({ block: "center" });
  $("#history-count").textContent =
    `${historyIndex + 1} / ${historyMarks.length}${historyMarks.length === 2000 ? "+" : ""}`;
}
$("#history").onclick = act(showHistory);
$("#history-refresh").onclick = act(loadHistory);
$("#history-query").oninput = renderHistory;
$("#history-query").onkeydown = (e) => {
  if (e.key === "Enter") stepHistory(e.shiftKey ? -1 : 1);
};
$("#history-prev").onclick = () => stepHistory(-1);
$("#history-next").onclick = () => stepHistory(1);
$("#history-save").onclick = () =>
  blobDownload(
    new Blob([historyText], { type: "text/plain" }),
    "terminal-history.txt",
  );
async function listFiles(p = dir) {
  const out = await api("/files?path=" + encodeURIComponent(p));
  dir = out.path;
  $("#files-path").textContent = dir;
  $("#files-up").disabled = dir === ".";
  $("#file-list").replaceChildren();
  for (const f of out.entries) {
    const row = document.createElement("div");
    row.className = "file-row";
    const b = document.createElement("button");
    b.textContent = (f.directory ? "▸ " : "") + f.name;
    b.onclick = act(() => (f.directory ? listFiles(f.path) : preview(f.path)));
    const size = document.createElement("span");
    size.textContent = f.directory
      ? "folder"
      : `${Math.ceil(f.size / 1024)} KiB`;
    row.append(b, size);
    if (!f.directory) {
      const insert = document.createElement("button");
      insert.textContent = "Insert path";
      insert.onclick = act(() => {
        active.paste(quote(info.workdir + "/" + f.path) + " ");
        $("#files-dialog").close();
      });
      row.append(insert);
    }
    $("#file-list").append(row);
  }
  if (!out.entries.length)
    $("#file-list").textContent = "This folder is empty.";
}
$("#files").onclick = act(async () => {
  $("#files-dialog").showModal();
  await listFiles();
});
$("#files-refresh").onclick = act(() => listFiles());
$("#files-up").onclick = act(() =>
  listFiles(dir.split("/").slice(0, -1).join("/") || "."),
);
async function preview(p) {
  disposePDF();
  disposePDF = () => {};
  const response = await request(base + "/file?path=" + encodeURIComponent(p)),
    blob = await response.blob();
  if (previewURL) URL.revokeObjectURL(previewURL);
  const ext = p.split(".").pop().toLowerCase();
  const images = {
    png: "image/png",
    jpg: "image/jpeg",
    jpeg: "image/jpeg",
    gif: "image/gif",
    webp: "image/webp",
    avif: "image/avif",
  };
  const mime =
    images[ext] ||
    (ext === "pdf" ? "application/pdf" : "application/octet-stream");
  previewURL = URL.createObjectURL(new Blob([blob], { type: mime }));
  $("#preview-title").textContent = p.split("/").pop();
  $("#preview-download").href = previewURL;
  $("#preview-download").download = p.split("/").pop();
  $("#preview-body").replaceChildren();
  if (images[ext]) {
    const img = document.createElement("img");
    img.src = previewURL;
    img.alt = p;
    $("#preview-body").append(img);
  } else if (ext === "pdf") {
    $("#preview-body").textContent = "Loading PDF…";
    $("#preview-dialog").showModal();
    const { renderPDF } = await import("/terminal-pdf.js");
    disposePDF = await renderPDF(blob, $("#preview-body"));
  } else {
    const sample = new Uint8Array(await blob.slice(0, 8192).arrayBuffer());
    if (!sample.includes(0)) {
      const pre = document.createElement("pre");
      pre.textContent = await blob.slice(0, 1024 * 1024).text();
      $("#preview-body").append(pre);
      if (blob.size > 1024 * 1024)
        pre.prepend(
          "Preview limited to first 1 MiB. Download for the full file.\n\n",
        );
    } else
      $("#preview-body").textContent =
        "Binary file. Use Download to open it on your device.";
  }
  $("#preview-insert").onclick = act(() => {
    active.paste(quote(info.workdir + "/" + p) + " ");
    $("#preview-dialog").close();
    $("#files-dialog").close();
  });
  if (!$("#preview-dialog").open) $("#preview-dialog").showModal();
}
$("#desktop-setup").onclick = () => {
  $("#desktop-open").href = info.desktop_uri;
  $("#desktop-command").value = info.desktop_command;
  $("#desktop-dialog").showModal();
};
$('#cli-install-command').textContent = 'bash install-lectern-cli.sh --server lectern --api ' + quote(location.origin);
if (/Win/i.test(navigator.userAgentData?.platform || navigator.platform)) {
  $('#cli-install-command').textContent = 'powershell -ExecutionPolicy Bypass -File .\\install-lectern-cli.ps1 -Server lectern';
}
$('#terminal-tools .action-menu-panel').addEventListener('click', e => {
  if (e.target.closest('button,a')) $('#terminal-tools').open = false;
});
$("#desktop-copy").onclick = act(async () => {
  await navigator.clipboard.writeText(info.desktop_command);
  notice("SSH command copied.");
});
// File references emitted by the agent can be opened without leaving the terminal.
function fileLinks(p) {
  p.term.registerLinkProvider({
    provideLinks(y, callback) {
      const text =
        p.term.buffer.active.getLine(y - 1)?.translateToString() || "";
      const links = [];
      const re =
        /(?:\/|\.\/)?[\w@.+~-]+(?:\/[\w@.+~-]+)*\.[a-zA-Z0-9]{1,12}(?::\d+(?::\d+)?)?/g;
      for (const match of text.matchAll(re)) {
        let value = match[0].replace(/:\d+(?::\d+)?$/, "");
        if (value.startsWith("/") && !value.startsWith(info.workdir + "/"))
          continue;
        if (value.startsWith(info.workdir + "/"))
          value = value.slice(info.workdir.length + 1);
        links.push({
          text: match[0],
          range: {
            start: { x: match.index + 1, y },
            end: { x: match.index + match[0].length, y },
          },
          activate: act(() => preview(value)),
        });
      }
      callback(links);
    },
  });
}
// External protocol links (and cancelled navigation) fire beforeunload without
// leaving this document. Dispose only after an actual departure, and retain
// panes when the browser caches this page for back/forward restoration.
window.addEventListener("pagehide", (event) => {
  if (!event.persisted) panes.forEach((p) => p.dispose());
});
act(async () => {
  info = await api("/info");
  $("#desktop").href = info.desktop_uri;
  $('#compact-desktop').href = info.desktop_uri;
  $('#compact-workspace').textContent = info.workdir;
  $("#identity").textContent = info.tmux_session + " · " + info.target;
  document.title = info.tmux_session + " · Lectern";
  $("#workspace-path").textContent = info.workdir;
  $("#upload").disabled = !info.files_available;
  $("#files").disabled = !info.files_available;
  $("#shell").disabled = !info.shell_url;
  $("#agent-label").textContent =
    kind === "project" ? "Project shell" : "Agent";
  const p = new Pane($("#agent-pane"), info.terminal_url);
  panes.push(p);
  select(p);
  if (info.files_available) fileLinks(p);
})();

$("#review").onclick = () => openReview({kind,id,name:info?.workdir,api: async path => (await request("/api"+path)).json()});

$("#saved-conversations").hidden=kind!=="session";
$("#saved-conversations").onclick=()=>openNativeHistory({id,name:info?.workdir,api:async (path,options={})=>{const opts={...options};if(opts.body){opts.body=JSON.stringify(opts.body);opts.headers={"Content-Type":"application/json"};}return (await request("/api"+path,opts)).json();},onFork:()=>notice("Fork started. Find it in Sessions; this terminal stays attached.")});

$("#search-conversations").onclick=async()=>{
  try {
    const targets=await (await request("/api/targets")).json();
    openNativeSearch({targets,onFork:()=>notice("Fork started. Find it in Sessions; this terminal stays attached."),api:async(path,options={})=>{
      const opts={...options};if(opts.body){opts.body=JSON.stringify(opts.body);opts.headers={"Content-Type":"application/json"};}
      return (await request("/api"+path,opts)).json();
    }});
  } catch(error){notice(error.message);}
};
