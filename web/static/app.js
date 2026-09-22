import { openWorkspaceExtension } from "/workspace-extension.js";
import { openAgentCommands } from "/agent-commands.js";
import { SheetFocus } from "/sheet-focus.js";
import { workspaceRepositories } from "/workspace-repositories.js";
import { renderSessionGroups } from "/session-groups.js";
import { CommandPalette } from "/command-palette.js";
import { openArchiveHistory } from "/archive-history.js";
import { openNativeHistory } from "/native-history.js";
import { openNativeSearch } from "/native-search.js";
import { openLaunchProfiles } from "/launch-profiles.js";
import { renderAgentSettings } from "/agent-settings.js";
import { openReview } from "/review.js";
import { TerminalTabs } from "/terminal-tabs.js";
import { actionMenu } from "/ui-menu.js";
import { openConversation } from "/conversation.js";
/* lectern PWA — vanilla ES module, no build step. */
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const sheetFocus = new SheetFocus($("#sheet"));
const COLUMNS = ["backlog", "queued", "running", "review", "done", "failed"];
// the columns where a card is history rather than work in progress
const FINISHED_COLUMNS = ["done", "failed"];

const state = {
  tab: "board", tasks: [], projects: [], targets: [], approvals: [], sessions: [],
  sheet: null,            // {kind:'task', id} | {kind:'new'} | null
  taskES: null, taskEvents: [], taskDiff: null, diffOpen: false,
  deckPanes: new Map(),   // taskId -> {el, es}: persisted deck panes/streams
  mobileCol: null,        // phone: which status column is showing (null = auto)
  mobileColPinned: false, // ...and whether the user chose it themselves
  showAllDone: false,     // phone: finished lists are capped until asked
  diffWrap: localStorage.getItem("lec-diffwrap") === "1",
  sessionFilter: "", sessionGrouping: sessionStorage.getItem("lec-session-grouping") || "none", archivedSessions: [], showArchivedSessions: false, showEndedSessions: false, endedSessions: [], settingsSection: sessionStorage.getItem('lec-settings-section') || "machines", agents: [],
};

// Rotating a phone or dragging a desktop window across the breakpoint has to
// rebuild the board — the two layouts are different DOM, not just different CSS.
let _wasPhone = null;
addEventListener("resize", () => {
  const now = !window.matchMedia("(min-width: 1024px)").matches;
  if (now !== _wasPhone) { _wasPhone = now; if (state.tab === "board") renderColumns(); }
});

/* ---------- api ---------- */
function authToken() { return localStorage.getItem("lec-token") || ""; }
// EventSource can't set headers and fetch needs the bearer too — thread the
// token (when the server runs in token-auth mode) through both surfaces.
function withToken(url) {
  const t = authToken();
  return t ? url + (url.includes("?") ? "&" : "?") + "token=" + encodeURIComponent(t) : url;
}
async function api(path, opts = {}) {
  const multipart = opts.body instanceof FormData;
  const headers = multipart ? {} : { "Content-Type": "application/json" };
  if (authToken()) headers.Authorization = "Bearer " + authToken();
  const r = await fetch(`/api${path}`, {
    headers, ...opts, body: multipart ? opts.body : opts.body ? JSON.stringify(opts.body) : undefined,
  });
  if (r.status === 401) {
    const t = prompt("This lectern requires an access token:", authToken());
    if (t !== null) { localStorage.setItem("lec-token", t); location.reload(); }
    throw new Error("unauthorized");
  }
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).detail || msg; } catch {}
    const error = new Error(msg); error.status = r.status; throw error;
  }
  return r.status === 204 ? null : r.json();
}

function toast(msg, err = false) {
  const t = document.createElement("div");
  t.className = "toast" + (err ? " err" : "");
  t.textContent = msg;
  $("#toasts").appendChild(t);
  setTimeout(() => t.remove(), 4200);
}

/* ---------- live board stream ---------- */
let es, sseConnectedOnce = false;
function connectSSE() {
  es = new EventSource(withToken("/api/stream"));
  es.onopen = () => {
    setConn(true);
    // EventSource auto-reconnects but never replays what it missed while down —
    // resync board + approvals on every (re)connect after the first.
    if (sseConnectedOnce) { refreshTasks(); refreshApprovals(); refreshSessions(); }
    sseConnectedOnce = true;
  };
  es.onerror = () => setConn(false);
  es.addEventListener("task", (e) => {
    const task = JSON.parse(e.data);
    refreshTasks();               // authoritative refetch (cheap at homelab scale)
    if (state.sheet?.kind === "task" && state.sheet.id === task.id) loadTaskSheet(task.id, true);
  });
  es.addEventListener("approval", () => { refreshApprovals(); });
  es.addEventListener("session", () => refreshSessions());
  es.addEventListener("session_dismissed", () => refreshSessions());
  es.addEventListener("session_handoff", (e) => {
    const d = JSON.parse(e.data);
    toast(d.ok
      ? "Handoff written" + (d.successor ? " — successor session started" : "") +
        (d.remembered ? " · remembered" : "")
      : "Handoff failed: " + (d.error || "unknown"), !d.ok);
    refreshSessions();
  });
  es.addEventListener("task_deleted", (e) => {
    const { id } = JSON.parse(e.data);
    if (state.sheet?.kind === "task" && state.sheet.id === id) closeSheet();
    refreshTasks();
  });
}
function setConn(on) {
  $("#conn-led").className = "led " + (on ? "led-on" : "led-err");
  $("#conn-label").textContent = on ? "LIVE" : "RECONNECTING";
}

/* ---------- data ---------- */
async function refreshTasks() {
  state.tasks = await api("/tasks");
  if (state.tab === "board") ($("#board") ? renderColumns() : renderBoard());
  if (state.tab === "deck") renderDeck();
}
async function refreshApprovals() {
  state.approvals = await api("/approvals?status=pending");
  const b = $("#appr-badge");
  b.hidden = state.approvals.length === 0;
  b.textContent = state.approvals.length;
  $('#more-badge').hidden = state.approvals.length === 0;
  $('#more-badge').textContent = state.approvals.length;
  if (state.tab === "approvals") renderApprovals();
  if (state.sheet?.kind === "task") renderSheet();
}
const collapsedSessionGroups = new Set((()=>{try{const value=JSON.parse(sessionStorage.getItem('lec-collapsed-session-groups')||'[]');return Array.isArray(value)?value:[];}catch{return [];}})());
let sessionRefreshVersion=0;
async function refreshSessions() {
  const generation=++sessionRefreshVersion;
  const [rows, archived] = await Promise.all([api(state.showEndedSessions ? "/sessions?all=true" : "/sessions?include_setup_failures=true"),state.showArchivedSessions ? api("/sessions?archived=true") : Promise.resolve([])]);
  if(generation!==sessionRefreshVersion)return;
  state.sessions = rows.filter(s => s.ended_at == null || s.setup_state === "failed");
  state.endedSessions = rows.filter(s => s.ended_at != null && s.setup_state !== "failed");
  state.archivedSessions = archived;
  const live = state.sessions.filter((s) => s.status !== "dead").length;
  const b = $("#sess-badge");
  b.hidden = live === 0;
  b.textContent = live;
  if (state.tab === "sessions") renderSessions();
}
let setupPollBusy=false;
async function pollWorkspaceSetups() {
  if(setupPollBusy || state.tab!=="sessions" || !state.sessions.some(s=>s.setup_state==='creating'))return;
  setupPollBusy=true;
  try {
    await refreshSessions();
    const pending=state.sessions.filter(s=>s.setup_state==='creating' && s.workspace?.repositories?.length);
    await Promise.all(pending.map(async session=>{
      let progress,error;
      try { progress=await api(`/sessions/${session.id}/worktree`); } catch(e) { error=e.message; }
      const current=state.sessions.find(s=>s.id===session.id);
      if(current?.setup_state==='creating' && current.workspace?.path===session.workspace.path) {
        if(progress)current.workspace=progress;
        current.setup_progress_error=error;
      }
    }));
    if(state.tab==='sessions')renderSessions();
  } finally { setupPollBusy=false; }
}
async function refreshMeta() {
  [state.projects, state.targets, state.models, state.agents] = await Promise.all([
    api("/projects"), api("/targets"), api("/models").catch(() => ({})), api("/agents").catch(() => [])]);
  if (state.tab === "targets") renderTargets();
}

/* ---------- board ---------- */
function fmtCost(c) { return c == null ? "" : `$${(+c).toFixed(3)}`; }
function attachMic(btn, input) {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) { btn.style.display = "none"; return; }
  btn.onclick = () => {
    const rec = new SR();
    rec.lang = "en-US"; rec.interimResults = false;
    btn.classList.add("rec");
    rec.onresult = (e) => { input.value = (input.value + " " + e.results[0][0].transcript).trim(); };
    rec.onend = () => btn.classList.remove("rec");
    rec.onerror = () => { btn.classList.remove("rec"); toast("voice input failed", true); };
    rec.start();
  };
}

function card(t) {
  const el = document.createElement("div");
  el.className = `card s-${t.status}`;
  el.draggable = true;
  el.ondragstart = (e) => e.dataTransfer.setData("text/lec-task", JSON.stringify(
    { id: t.id, status: t.status }));
  // diff_stat is {} server-side until a diff is captured; a running card that
  // assumed an array once threw and aborted the whole column render
  const ds = Array.isArray(t.attempt?.diff_stat) ? t.attempt.diff_stat : [];
  const adds = ds.reduce((a, f) => a + (f.additions || 0), 0);
  const dels = ds.reduce((a, f) => a + (f.deletions || 0), 0);
  const v = t.attempt?.verify;
  const review = t.attempt?.result?.review;
  el.innerHTML = `
    <button class="card-x" title="delete this card">✕</button>
    <div class="t"></div>
    <button class="b task-chat" aria-label="Chat with this task">Chat</button>
    <div class="meta">
      <span class="chip">${esc(t.project_name)}</span>
      <span class="chip tgt">${esc(t.target_name)}</span>
      ${t.status === "running" ? '<span class="working"><i></i><i></i><i></i></span>' : ""}
      ${ds.length ? `<span class="chip ds">+${adds} <b>−${dels}</b></span>` : ""}
      ${t.attempt?.result?.cost_usd != null
        ? `<span class="chip cost">${fmtCost(t.attempt.result.cost_usd)}</span>` : ""}
      ${v?.cmd ? (v.rc === 0
        ? '<span class="chip ds" title="auto-verify passed">✓ verified</span>'
        : '<span class="chip bad" title="auto-verify FAILED">✗ verify</span>') : ""}
      ${t.priority >= 3 ? '<span class="chip warn">▲ high</span>' : ""}
      ${t.agent && t.agent !== "claude" ? `<span class="chip tgt">${esc(t.agent)}</span>` : ""}
      ${t.created_by === "agent"
        ? `<span class="chip info" title="filed by an agent (parent #${t.parent_task_id})">by agent</span>` : ""}
      ${t.created_by === "reviewer-gate" ? '<span class="chip tgt">reviewer</span>' : ""}
      ${review ? (review.verdict === "APPROVE"
        ? '<span class="chip ds" title="reviewer approved">⚖ approved</span>'
        : '<span class="chip warn" title="reviewer requests changes">⚖ changes</span>') : ""}
      ${(t.attempts || []).length > 1
        ? `<span class="chip info" title="${t.attempts.length} attempts (retries / follow-ups / A/B)">⑂ ×${t.attempts.length}</span>` : ""}
    </div>`;
  $(".t", el).textContent = t.title;
  $(".task-chat", el).onclick = ev => { ev.stopPropagation(); openTaskChat(t); };
  el.onclick = () => openTaskSheet(t.id);
  // stopPropagation, or deleting a card would also open the sheet for the card
  // that is on its way out
  $(".card-x", el).onclick = (ev) => { ev.stopPropagation(); deleteCard(t); };
  return el;
}

function openTaskChat(t) {
  if (t.takeover?.status === "ready") return openTakenOverSession(t);
  openConversation({kind:"task",id:t.id,name:t.title,api,attachMic,onClose:()=>openTaskSheet(t.id)}); }

function renderBoard() {
  const main = $("#view");
  main.innerHTML = `
    <div class="page-heading"><div><h2>Task board</h2><p>Dispatch work and review what needs you.</p></div>
      <button class="b" id="qb-routines" title="Routines — saved jobs you can run with one button">Routines</button>
    </div>
    <div id="quickbar">
      <select id="qb-project" title="project"></select>
      <input id="qb-input" placeholder="Describe it, hit ⏎ — instant dispatch" autocomplete="off">
      <button id="qb-mic" title="voice">🎤</button>
      <input id="qb-filter" placeholder="Filter…" autocomplete="off">
    </div>
    <div id="board"></div>`;
  $("#qb-routines").onclick = () => { state.sheet = { kind: "routines" }; renderSheet(); };
  $("#qb-filter").value = state.filter || "";
  $("#qb-filter").oninput = (e) => { state.filter = e.target.value; renderColumns(); };
  const sel = $("#qb-project");
  sel.innerHTML = state.projects.map((p) => `<option value="${p.id}">${esc(p.name)}</option>`).join("");
  sel.value = localStorage.getItem("lec-quickproj") || (state.projects[0]?.id ?? "");
  sel.onchange = () => localStorage.setItem("lec-quickproj", sel.value);
  $("#qb-input").onkeydown = async (e) => {
    if (e.key !== "Enter" || !e.target.value.trim()) return;
    const text = e.target.value.trim();
    e.target.value = "";
    try {
      const t = await api("/tasks", { method: "POST", body: {
        project_id: +sel.value, title: text.slice(0, 70), prompt: text } });
      await api(`/tasks/${t.id}/dispatch`, { method: "POST", body: {} });
      toast("Dispatched — " + text.slice(0, 40));
      refreshTasks();
    } catch (err) { toast(err.message, true); }
  };
  attachMic($("#qb-mic"), $("#qb-input"));
  renderColumns();
}

/* ---------- board columns ----------
   Desktop keeps the kanban. A phone cannot use one: six columns at 86vw is
   ~2100px of sideways scrolling, so the app opened on an empty BACKLOG with the
   only actionable card three swipes away — and the scroll-to-the-busy-column
   nudge latched after its first run, while every re-render reset scrollLeft to 0.
   Narrow screens now get ONE column plus a status strip, ordered by what wants a
   decision first. Same data, same actions, no horizontal scrolling. */
const MOBILE_ORDER = ["review", "running", "queued", "backlog", "failed", "done"];
const COL_LABEL = { review: "needs you", running: "running", queued: "queued",
                    backlog: "backlog", failed: "failed", done: "done" };
const DONE_PAGE = 15;   // 40+ finished nightly-smoke cards is a DOM full of noise

function isPhone() { return !window.matchMedia("(min-width: 1024px)").matches; }

function visibleTasks() {
  const f = (state.filter || "").trim().toLowerCase();
  return f ? state.tasks.filter((t) =>
    `${t.title} ${t.project_name} ${t.target_name}`.toLowerCase().includes(f))
    : state.tasks;
}

function attachDrop(c, col) {
  c.ondragover = (e) => { e.preventDefault(); c.classList.add("dropok"); };
  c.ondragleave = () => c.classList.remove("dropok");
  c.ondrop = async (e) => {
    e.preventDefault(); c.classList.remove("dropok");
    const data = e.dataTransfer.getData("text/lec-task");
    if (!data) return;
    const { id, status } = JSON.parse(data);
    try {
      if (col === "queued" && ["backlog", "failed", "cancelled"].includes(status))
        await api(`/tasks/${id}/dispatch`, { method: "POST", body: {} });
      else if (col === "done" && status === "review")
        await api(`/tasks/${id}/complete`, { method: "POST" });
      else if (col === "cancelled" || (col === "backlog" && status === "backlog")) return;
      else return toast(`${status} → ${col}: not a thing. Drag to queued (dispatch) or done (complete).`, true);
      refreshTasks();
    } catch (err) { toast(err.message, true); }
  };
}

/** Fill a .col-body with cards, capping long finished lists behind a reveal. */
function fillColumn(body, col, items) {
  if (!items.length) { body.innerHTML = '<div class="col-empty">Nothing here</div>'; return; }
  const cap = col === "done" && !state.showAllDone ? DONE_PAGE : items.length;
  items.slice(0, cap).forEach((t) => {
    try { body.appendChild(card(t)); }   // one bad card must never blank the board
    catch (err) { console.error("card render failed", t?.id, err); }
  });
  if (items.length > cap) {
    const more = document.createElement("button");
    more.className = "col-more";
    more.textContent = `show ${items.length - cap} more`;
    more.onclick = () => { state.showAllDone = true; renderColumns(); };
    body.appendChild(more);
  }
}

function renderColumns() {
  const board = $("#board");
  if (!board) return;
  board.innerHTML = "";
  const visible = visibleTasks();
  const byCol = Object.fromEntries(
    COLUMNS.map((c) => [c, visible.filter((t) => t.status === c)]));

  if (isPhone()) return renderPhoneBoard(board, byCol);

  board.classList.remove("phone");
  for (const col of COLUMNS) {
    const c = document.createElement("div");
    c.className = `col s-${col}`;
    const clearable = FINISHED_COLUMNS.includes(col) && byCol[col].length;
    c.innerHTML = `
      <div class="col-head"><span class="dot"></span>${col}<span class="cnt">${byCol[col].length}</span>${
        clearable ? `<button class="col-clear" title="clear every ${col} card">clear</button>` : ""}</div>
      <div class="col-body"></div>`;
    if (clearable) $(".col-clear", c).onclick = () => clearColumn(col, byCol[col].length);
    attachDrop(c, col);
    fillColumn($(".col-body", c), col, byCol[col]);
    board.appendChild(c);
  }
}

/** One column at a time, chosen by a status strip. */
function renderPhoneBoard(board, byCol) {
  board.classList.add("phone");
  // auto-focus the most urgent non-empty status until the user picks one, and
  // fall back to auto if their pick empties out — never strand them on nothing
  let col = state.mobileCol;
  if (!col || (!byCol[col].length && !state.mobileColPinned))
    col = MOBILE_ORDER.find((c) => byCol[c].length) || "backlog";
  state.mobileCol = col;

  const strip = document.createElement("div");
  strip.className = "colstrip";
  for (const c of MOBILE_ORDER) {
    const b = document.createElement("button");
    b.className = `colchip s-${c}${c === col ? " on" : ""}`;
    b.innerHTML = `<span class="dot"></span>${COL_LABEL[c]}<b>${byCol[c].length}</b>`;
    b.onclick = () => {
      state.mobileCol = c; state.mobileColPinned = true; state.showAllDone = false;
      renderColumns();
    };
    strip.appendChild(b);
  }
  board.appendChild(strip);

  const wrap = document.createElement("div");
  wrap.className = `col s-${col} solo`;
  const clearable = FINISHED_COLUMNS.includes(col) && byCol[col].length;
  wrap.innerHTML = `${clearable
    ? `<div class="col-head solo-head">${col}<span class="cnt">${byCol[col].length}</span>` +
      `<button class="col-clear" title="clear every ${col} card">clear</button></div>`
    : ""}<div class="col-body"></div>`;
  if (clearable) $(".col-clear", wrap).onclick = () => clearColumn(col, byCol[col].length);
  attachDrop(wrap, col);
  fillColumn($(".col-body", wrap), col, byCol[col]);
  board.appendChild(wrap);
  strip.querySelector(".colchip.on")?.scrollIntoView(
    { inline: "center", block: "nearest", behavior: "instant" });
}

/* ---------- deck view (desktop multi-pane cockpit) ---------- */
// Panes are reconciled incrementally: streams persist across task updates so a
// status change elsewhere never tears down and reconnects every pane's SSE
// (that thrash risked dropping mid-stream events under concurrent load).
function closeDeckStreams() {
  for (const p of state.deckPanes.values()) p.es.close();
  state.deckPanes.clear();
}
function makePaneLogger(log) {
  return (e) => {
    const p = e.payload || {};
    const line = document.createElement("div");
    line.className = "pane-line";
    line.textContent =
      e.type === "text" ? p.text :
      e.type === "tool_use" ? `▸ ${p.name} ${snippet(p.input)}` :
      e.type === "tool_result" ? `↳ ${(p.content || "").slice(0, 80)}` :
      e.type === "verify" ? `verify ${p.rc === 0 ? "PASS" : "FAIL"}` :
      e.type === "result" ? `✔ ${p.result || ""}` : e.type;
    log.appendChild(line);
    while (log.children.length > 40) log.firstChild.remove();
    log.scrollTop = log.scrollHeight;
  };
}
function renderDeck() {
  const main = $("#view");
  const active = state.tasks
    .filter((t) => ["running", "review", "queued"].includes(t.status))
    .slice(0, 16);
  let deck = $("#deck");
  if (!active.length) {
    closeDeckStreams();
    main.innerHTML = '<div class="hint">Nothing live right now.<br>Dispatch tasks and watch them run here, side by side.</div>';
    return;
  }
  if (!deck) { main.innerHTML = '<div id="deck"></div>'; deck = $("#deck"); }
  const activeIds = new Set(active.map((t) => t.id));
  // remove panes whose task left the active set (close their stream)
  for (const [id, p] of [...state.deckPanes]) {
    if (!activeIds.has(id)) { p.es.close(); p.el.remove(); state.deckPanes.delete(id); }
  }
  for (const t of active) {
    let p = state.deckPanes.get(t.id);
    if (!p) {
      const pane = document.createElement("div");
      pane.innerHTML = `
        <div class="pane-head">
          <span class="statpill"></span>
          <span class="pane-title"></span>
          <span class="pane-sub">${esc(t.target_name)}</span>
        </div>
        <div class="pane-log"></div>`;
      $(".pane-title", pane).textContent = t.title;
      $(".pane-head", pane).onclick = () => openTaskSheet(t.id);
      deck.appendChild(pane);
      const log = $(".pane-log", pane);
      const add = makePaneLogger(log);
      const es = new EventSource(withToken(`/api/tasks/${t.id}/stream`));
      es.addEventListener("agent_event", (e) => add(JSON.parse(e.data)));
      p = { el: pane, es };
      state.deckPanes.set(t.id, p);
      api(`/tasks/${t.id}/events`).then((evs) => evs.slice(-15).forEach(add));
    }
    // update header status in place (no stream churn)
    p.el.className = `pane s-${t.status}`;
    $(".statpill", p.el).textContent = t.status;
  }
}

/* ---------- sessions tab ----------
   The other half of the board. A task is work you hand off; a session is an
   agent you work WITH, for days. Its card leads with the two things a task card
   never needs — how long it has been quiet, and what is on its screen — and its
   primary action is Attach, because the point is to get you into the actual
   terminal in one tap. */
const SESSION_ORDER = { waiting: 0, running: 1, starting: 2, idle: 3, dead: 4 };

function fmtDuration(seconds) {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}
const SESSION_LABEL = { waiting: "wants you", running: "working",
                        starting: "starting", idle: "idle", dead: "ended" };

function sessionCard(s) {
  const settingUp=s.setup_state === "creating";
  const el = document.createElement("div");
  el.className = `scard s-${s.setup_state === "failed" ? "failed" : s.status}`;
  const live = s.status === "running";
  const ctx = s.context_pct;
  const ctxClass = ctx == null ? "" : ctx <= 10 ? " crit" : ctx <= 25 ? " low" : "";
  el.innerHTML = `
    <div class="scard-project">${esc(s.project_name || "Unassigned")}</div>
    <div class="scard-top">
      <span class="dot${live ? " live" : ""}"></span>
      <span class="nm"></span>
      <span class="sstate">${settingUp ? (s.setup_cancel_requested?"cancelling":"setting up") : s.setup_state === "failed" ? "setup failed" : s.archived_at != null ? "archived" : s.ended_at != null ? (s.status === "dead" ? "ended" : "untracked") : (SESSION_LABEL[s.status] || esc(s.status))}</span>
      <span class="sidle">${s.status === "dead" || settingUp ? "" : "quiet " + fmtDuration(s.idle_seconds)}</span>
    </div>
    <div class="smeta">
      <span class="chip">${esc(s.agent)}${s.model ? " · " + esc(s.model) : ""}</span>
      <span class="chip tgt">${esc(s.target_name || "")}</span>
      <span class="chip">${settingUp?"setup":"up"} ${fmtDuration(s.uptime_seconds)}</span>
      ${s.launch_profile ? `<span class="chip" title="Captured launch profile">${esc(s.launch_profile)}</span>` : ""}
      ${s.origin === "discovered" ? '<span class="chip info" title="started outside lectern and adopted">adopted</span>' : ""}
      ${s.wraps ? `<span class="chip info" title="handoffs written from this session">⇥ ${s.wraps}</span>` : ""}
      ${s.handoff_in_flight ? '<span class="chip warn">writing handoff…</span>' : ""}
      ${ctx != null ? `<span class="ctxbar${ctxClass}">ctx <i><b style="width:${ctx}%"></b></i> ${ctx}%</span>` : ""}
    </div>
    <div class="spane"></div>
    <div class="btnrow"></div>`;
  $(".nm", el).textContent = s.name;
  if(s.group_path){const tag=document.createElement("span");tag.className="chip";tag.textContent=s.group_path;$(".smeta",el).appendChild(tag);}
  // an agent's working directory is often a scratch dir, so which project it
  // belongs to is a judgement only you can make — and one you can change later
  const projSel = document.createElement("select");
  projSel.className = "f sess-proj";
  projSel.style.cssText = "width:auto;min-width:130px;padding:5px 8px;font-size:11.5px";
  projSel.innerHTML = `<option value="">— unassigned —</option>` +
    state.projects.map((p) =>
      `<option value="${p.id}">${esc(p.name)}</option>`).join("");
  projSel.value = s.project_id ? String(s.project_id) : "";
  projSel.onchange = async () => {
    try {
      await api(`/sessions/${s.id}`, { method: "PATCH",
        body: { project_id: projSel.value ? +projSel.value : null } });
      refreshSessions();
    } catch (e) { toast(e.message, true); }
  };
  $(".spane", el).textContent = settingUp ? (s.setup_cancel_requested?"Cancellation requested. Waiting for checkout to stop; files will be retained.":"Setting up workspace… Attach becomes available when setup finishes.")+(s.workspace?.repositories||[]).map(repo=>`\n${repo.name}: ${repo.worktree.state}`).join('')+(s.setup_error?'\n'+s.setup_error:'')+(s.setup_progress_error?'\nProgress unavailable: '+s.setup_progress_error:'') : s.setup_error ? 'Setup failed: '+s.setup_error : s.pane_tail || "";

  const row = $(".btnrow", el);
  const {menu, panel} = actionMenu('More ···', `More actions for ${s.name}`);
  let actionRow = row;
  const act = (label, cls, fn) => {
    const b = document.createElement("button");
    b.className = `b ${cls}`; b.textContent = label; b.onclick = fn;
    actionRow.appendChild(b);
    return b;
  };
  if(settingUp) act("Setting up", "", ()=>{}).disabled=true;
  const cancelSetup=async()=>{
    try {await api(`/sessions/${s.id}/setup/cancel`,{method:"POST",body:{}});toast("Cancellation requested. Files already created will be retained.");}
    catch(e){toast(e.message,true);}
    finally{await refreshSessions();}
  };
  if(settingUp) act(s.setup_cancel_requested?"Retry cancellation":"Cancel setup","no",cancelSetup);

  if (s.ended_at == null && s.status !== "dead" && !settingUp) {
    // the whole point: one tap into the real terminal, same tmux, same chat
    act("⌨ Attach", "attach", () => attachSession(s));
    act("Chat", "grow", () => openConversation({kind:"session",id:s.id,name:s.name,api,attachMic,onClose:refreshSessions}));
    actionRow = panel;
    act("Review changes", "", () => openReview({kind:"session", id:s.id, name:s.name, api}));
    const native = document.createElement('a');
    native.className = 'b';
    native.href = `lectern://attach/session/${s.id}`;
    native.textContent = 'Open in terminal';
    panel.appendChild(native);
    if (s.status === "running") act("⎋ Interrupt", "warn", () => sendKey(s, "escape"));
    act("⇥ Handoff", "", () => handoffSession(s));
    // work that started in a blank room: name it once you know what it is
    if (!s.project_id) act("⇑ Make a project", "ok", () => promoteSession(s));
  }
  // Adoption is non-destructive, so letting go has to be too. An agent you
  // started yourself is released — lectern stops watching, the terminal keeps
  // running — and killing it is a separate, explicit choice.
  const adopted = s.origin === "discovered";
  actionRow = panel;
  if(s.setup_state === "failed" && s.workspace?.state !== "removed") act("Cancel remaining checkout","",cancelSetup);
  act("Move to group", "", ()=>{state.sheet={kind:"session-group",session:s};renderSheet();});
  if (["claude", "codex"].includes(s.agent)) {
    act("Saved conversations", "", () => openNativeHistory({id:s.id,name:s.name,api,onResume:session=>{refreshSessions();attachSession(session);toast("Resumed the selected conversation.");},onFork:session=>handleNativeFork(session,false)}));
  }
  if (s.workspace) {
    if(s.workspace.repositories?.length && !settingUp && s.workspace.state !== "removed") act("Workspace repositories", "", () => openWorkspaceExtension({api,session:s,onChange:refreshSessions}));
    if((s.setup_state === "failed" || s.workspace.state === "failed") && s.workspace.state !== "removed") act("Recover allocation","",async()=>{
      try{await api(`/sessions/${s.id}/worktree/recover`,{method:"POST",body:{}});toast("Allocation validated; files retained. Setup did not restart.");}
      catch(e){toast(e.message,true);}
      finally{await refreshSessions();}
    });
    const info=document.createElement('details');info.className='session-worktree';const heading=document.createElement('summary');heading.textContent=`${s.workspace.repositories?.length?'Workspace':'Worktree'} · ${s.workspace.branch} · ${s.workspace.state}`;const location=document.createElement('code');location.textContent=s.workspace.path;const base=document.createElement('small');base.textContent=s.workspace.repositories?.length?`${s.workspace.repositories.length} repositories`:`Base: ${s.workspace.base} · ${s.workspace.commit?.slice(0,12)||'not created'}`;info.append(heading,location,base);if(s.workspace.error){const failure=document.createElement('p');failure.textContent='Setup error: '+s.workspace.error;failure.style.whiteSpace='pre-wrap';info.append(failure);}el.insertBefore(info,row);
    for (const entry of s.workspace.repositories || [{name:s.project_name || 'Repository',worktree:s.workspace}]) {
      if (!entry.worktree.setup_command) continue;
      const setup=document.createElement('pre');setup.className='workspace-setup-output';setup.style.whiteSpace='pre-wrap';
      setup.textContent=`${entry.name} setup: ${entry.worktree.setup_state || 'not completed'}\n${entry.worktree.setup_output || ''}`;
      info.append(setup);
    }
    if(s.workspace.repositories?.length) {
      const progress=document.createElement('pre');progress.style.whiteSpace='pre-wrap';progress.setAttribute('aria-live','polite');
      const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Refresh setup progress';
      refresh.onclick=async()=>{
        refresh.disabled=true;
        try {
          const current=await api(`/sessions/${s.id}/worktree`);
          progress.textContent=`Recorded workspace state: ${current.state}\n`+current.repositories.map(repo=>`${repo.name}: ${repo.worktree.state}${repo.worktree.error?' — '+repo.worktree.error:''}`).join('\n')+(current.error?'\n'+current.error:'');
        } catch(error) { progress.textContent=error.message; }
        finally { refresh.disabled=false; }
      };
      info.append(refresh,progress);
    }
    if(s.workspace.state!=='removed') act("Remove worktree", "", async()=>{
      if(!confirm(`Remove ${s.workspace.path}? End its sessions first. Changed, untracked or ignored files prevent removal. The Git branch is kept.`))return;
      try{await api(`/sessions/${s.id}/worktree`,{method:'DELETE'});await refreshSessions();toast('Worktree removed; branch kept.');}catch(e){toast(e.message,true);}
    });
  }
  if (s.archived_at != null) {
    act("Archived terminal output", "", ()=>openArchiveHistory({id:s.id,name:s.name,api}));
    act("Unarchive record", "", async()=>{try{await api(`/sessions/${s.id}/archive`,{method:"DELETE"});await refreshSessions();toast("Record unarchived. Its terminal stays stopped; find it under Include ended and untracked.");}catch(e){toast(e.message,true);}});
  } else if (s.ended_at != null) {
    act("Archive stopped record", "", async()=>{try{await api(`/sessions/${s.id}/archive`,{method:"POST",body:{stop:false}});await refreshSessions();}catch(e){toast(e.message,true);}});
    if (s.can_restore) {
      actionRow = row;
      act("Track again", "ok", async () => {
        try { await api(`/sessions/${s.id}/restore`, {method:"POST",body:{}}); await refreshSessions(); toast("Tracking restored. Your session keeps running."); }
        catch (e) { toast(e.message, true); }
      });
      actionRow = panel;
    } else {
      act("Find running sessions", "", () => { state.sheet={kind:"discover"}; renderSheet(); });
    }
  } else if (s.status === "dead") {
    act("Dismiss", "no", () => endSession(s, false));
  } else if (settingUp) {
    // Setup owns this reservation until its worker finishes.
  } else if (adopted) {
    act("Stop tracking", "", () => endSession(s, false));
    act("Kill", "no", async () => {
      if (!confirm(`Kill "${s.name}"?\n\nThis ends the tmux session and the ` +
        `conversation running in it. You started this one yourself — ` +
        `"Stop tracking" removes it from the board and leaves it running.`)) return;
      endSession(s, true);
    });
  } else {
    act("End", "no", async () => {
      if (!confirm(`End "${s.name}"? The tmux session is killed; the record and its handoffs stay.`))
        return;
      endSession(s, true);
    });
  }
  if(s.archived_at == null && s.ended_at == null) act("Stop and archive", "no", async()=>{
    if(!confirm(`Stop "${s.name}" and move its record to Archive? This ends its terminal process. Captured output, saved conversations and worktree files are retained. Unarchiving does not restart it.`))return;
    try{await api(`/sessions/${s.id}/archive`,{method:"POST",body:{stop:true}});await refreshSessions();toast("Session stopped and archived.");}catch(e){toast(e.message,true);}
  });
  const projectLabel = document.createElement('label');
  projectLabel.className = 'menu-field';
  projectLabel.textContent = 'Project';
  projectLabel.appendChild(projSel);
  panel.appendChild(projectLabel);
  row.appendChild(menu);
  return el;
}

async function endSession(s, kill) {
  try {
    await api(`/sessions/${s.id}${kill ? "?kill=true" : ""}`, { method: "DELETE" });
    refreshSessions();
  } catch (e) { toast(e.message, true); }
}

function handleNativeFork(session, attach=true) {
  switchTab("sessions");
  refreshSessions();
  if(session.setup_state === "creating") toast("Fork workspace setup started. You can follow progress in Sessions.");
  else if(attach) attachSession(session);
  else toast("Fork started. The original session keeps running.");
}

async function attachSession(s) {
  if(s.setup_state === "failed") { toast(s.setup_error || "Workspace setup failed. Inspect its retained files before launching again.",true);return; }
  if(s.setup_state === "creating") { toast("Workspace is setting up. Attach becomes available when setup finishes.");return; }
  try {
    const r = await api(`/sessions/${s.id}/terminal`, { method: "POST" });
    openTerminal(r.url, s.name);
  } catch (e) {
    // ttyd may not be installed; the manual command is still useful
    toast(e.message + " — attach manually", true);
    prompt("Attach with:", `tmux attach -t ${s.tmux_session}`);
  }
}
async function sendToSession(s) {
  const text = prompt(`Send to ${s.name}:`);
  if (!text) return;
  try {
    await api(`/sessions/${s.id}/send`, { method: "POST", body: { text } });
    toast("Sent");
    setTimeout(refreshSessions, 800);
  } catch (e) { toast(e.message, true); }
}
async function sendKey(s, key) {
  try { await api(`/sessions/${s.id}/send`, { method: "POST", body: { key } }); }
  catch (e) { toast(e.message, true); }
}
// promoteSession turns a blank room into a project. Nothing moves: the directory
// the agent has been working in becomes the project's repository, so the
// conversation carries straight on in the same tmux session.
async function promoteSession(s) {
  const dir = (s.workdir || "").split("/").filter(Boolean).pop() || "";
  const suggested = dir.replace(/-\d{8}-[A-Za-z0-9]{6}$/, "");
  const name = prompt(
    `Make this a project.\n\n${s.workdir}\n\n` +
    "It stays exactly where it is — the directory becomes the project's repo and " +
    "this session keeps running. Name it:", suggested || s.name);
  if (name === null) return;
  try {
    const out = await api(`/sessions/${s.id}/promote`, { method: "POST",
      body: { name: name.trim(), wrap: true } });
    toast(`"${out.project.name}" is a project now — you can dispatch tasks to it`);
    // the projects list has changed, and the session card's project picker and
    // the task board both read from it
    await Promise.all([refreshMeta(), refreshSessions()]);
  } catch (e) { toast(e.message, true); }
}

function handoffSession(sess) {
  state.sheet = { kind: "handoff", session: sess };
  renderSheet();
}

// A handoff is a session writing down where it got to, so the next one starts
// there instead of from nothing.
//
// Two different things want that. One is a context window running out, where the
// successor is the same agent with a clean slate. The other — the more useful
// one — is moving the work to a different agent entirely: claude hands the
// inference project to codex, and codex starts knowing what happened. The old
// UI only ever did the first, because it never asked which agent should pick it
// up.
function renderHandoff(sheet) {
  const sess = state.sheet.session;
  sheet.innerHTML = `
    <div class="sheet-grip"><i></i></div>
    <div class="sheet-head"><h2>Hand off</h2><button class="x">✕</button></div>
    <div class="sub" style="color:var(--ink-dim);font-size:12.5px">
      <b class="hs-name"></b> writes down where it got to — what it did, what it
      learned, what it was about to do — and the next session starts primed with it.
    </div>
    <label class="f">Then</label>
    <select class="f" id="ho-mode">
      <option value="successor">Start a new session with it</option>
      <option value="note">Just write it down, keep this session running</option>
    </select>
    <div id="ho-successor">
      <label class="f">Hand it to</label>
      <select class="f" id="ho-agent"></select>
      <div class="subhint" id="ho-agent-hint"></div>
      <label class="f">Model</label>
      <input class="f" id="ho-model" list="lec-models" placeholder="default" autocomplete="off">
      <label class="f check" style="display:flex;align-items:center;gap:9px;cursor:pointer;margin-top:12px">
        <input type="checkbox" id="ho-kill" checked style="width:auto;margin:0">
        <span>Retire <span class="hs-name2"></span> once the handoff is written</span>
      </label>
      <div class="subhint" id="ho-kill-hint"></div>
    </div>
    <div class="btnrow" style="margin-top:18px">
      <button class="b ok grow" id="ho-go">⇥ Write the handoff</button>
    </div>`;
  $(".x", sheet).onclick = closeSheet;
  $(".hs-name", sheet).textContent = sess.name;
  $(".hs-name2", sheet).textContent = sess.name;

  const agentBox = $("#ho-agent", sheet);
  api("/agents").then((specs) => {
    agentBox.innerHTML = specs.map((a) =>
      `<option value="${esc(a.name)}">${esc(a.name)}${
        a.name === sess.agent ? " — same agent, clean context" : ""}</option>`).join("");
    agentBox.value = sess.agent;
    syncAgent(specs);
    agentBox.onchange = () => syncAgent(specs);
  }).catch(() => {
    agentBox.innerHTML = `<option value="${esc(sess.agent)}">${esc(sess.agent)}</option>`;
  });

  function syncAgent(specs) {
    const a = specs.find((x) => x.name === agentBox.value);
    const moving = agentBox.value !== sess.agent;
    $("#ho-agent-hint", sheet).textContent = moving
      ? `The work moves to ${agentBox.value}. It starts fresh, knowing only what the handoff says.`
      : "Same agent, clean context — for when the window is full.";
    if (a && !a.model_flag) {
      $("#ho-model", sheet).disabled = true;
      $("#ho-model", sheet).placeholder = `${a.name} has no model switch`;
    } else {
      $("#ho-model", sheet).disabled = false;
      $("#ho-model", sheet).placeholder = "default";
    }
  }

  const syncMode = () => {
    const successor = $("#ho-mode", sheet).value === "successor";
    $("#ho-successor", sheet).style.display = successor ? "" : "none";
    $("#ho-go", sheet).textContent = successor
      ? "⇥ Write it and hand over" : "⇥ Write the handoff";
  };
  $("#ho-mode", sheet).onchange = syncMode;
  syncMode();

  const syncKill = () => {
    $("#ho-kill-hint", sheet).textContent = $("#ho-kill", sheet).checked
      ? "Its tmux session ends. The handoff and its history stay."
      : "Both sessions keep running — useful if you want to compare them.";
  };
  $("#ho-kill", sheet).onchange = syncKill;
  syncKill();

  $("#ho-go", sheet).onclick = async () => {
    const successor = $("#ho-mode", sheet).value === "successor";
    try {
      await api(`/sessions/${sess.id}/handoff`, { method: "POST", body: {
        successor,
        kill_old: successor && $("#ho-kill", sheet).checked,
        agent: successor ? agentBox.value : "",
        model: successor && !$("#ho-model", sheet).disabled
          ? $("#ho-model", sheet).value.trim() : "",
      } });
      closeSheet();
      toast("Asked for a handoff — it lands when the agent finishes its turn");
      refreshSessions();
    } catch (e) { toast(e.message, true); }
  };
}

function renderSessions() {
  const main = $("#view");
  if (main.querySelector('#sesslist') && (main.querySelector('.action-menu[open]') ||
      main.contains(document.activeElement) && document.activeElement.matches('input,select:not(#sess-scope)'))) {
    // Keep the focused controls, but apply arriving results to their current query.
    if (!main.querySelector('.action-menu[open]')) renderSessionList();
    return;
  }
  const live = state.sessions.filter((s) => s.status !== "dead");
  main.innerHTML = `
    <div class="list wide">
      <div class="sesshead">
        <div><h2>Sessions</h2><p>${live.length} active · Pick up where you left off.</p></div>
        <button class="b" id="sess-saved-search">Search saved conversations</button>
        <button class="b" id="sess-discover">⌕ Find running agents</button>
        <button class="b ok" id="sess-new">+ New session</button>
      </div>
      <input id="sess-search" class="f" type="search" placeholder="Search sessions, groups, branches or folders" aria-label="Find a session or project">
      <label class="session-grouping">Group by <select class="f" id="sess-grouping" aria-label="Group sessions by"><option value="none">None</option><option value="group">Named group</option><option value="project">Project</option><option value="target">Target</option></select></label>
      <label class="session-grouping session-scope">Show <select class="f" id="sess-scope"><option value="active">Active sessions</option><option value="all">Include ended and untracked</option><option value="archived">Archived sessions</option></select></label>
      <div id="sesslist"></div>
    </div>`;
  $("#sess-saved-search").onclick = () => openNativeSearch({api,targets:state.targets,onFork:handleNativeFork});
  $("#sess-new").onclick = () => { state.sheet = { kind: "new-session" }; renderSheet(); };
  $("#sess-discover").onclick = () => { state.sheet = { kind: "discover" }; renderSheet(); };

  $('#sess-scope').value=state.showArchivedSessions?'archived':state.showEndedSessions?'all':'active';
  $('#sess-scope').onchange=async e=>{state.showArchivedSessions=e.target.value==='archived';state.showEndedSessions=e.target.value!=='active';try{await refreshSessions();}catch(err){toast(err.message,true);}};
  $('#sess-grouping').value=state.sessionGrouping;
  $('#sess-grouping').onchange=e=>{state.sessionGrouping=e.target.value;sessionStorage.setItem('lec-session-grouping',state.sessionGrouping);renderSessionList();};
  $('#sess-search').value = state.sessionFilter;
  $('#sess-search').oninput = (e) => { state.sessionFilter = e.target.value; renderSessionList(); };
  renderSessionList();
}

function renderSessionList() {
  const live = state.showArchivedSessions ? state.archivedSessions : state.showEndedSessions ? [...state.sessions, ...state.endedSessions] : state.sessions.filter((s) => s.status !== 'dead' || s.setup_state === 'failed');
  const list = $('#sesslist');
  list.replaceChildren();
  if (!live.length) {
    list.innerHTML = state.showArchivedSessions ? `<div class="hint">No archived sessions. Use “Stop and archive” in a session’s actions to keep it here for later.</div>` : `<div class="hint">No sessions yet.<br><br>
      Start one here, or hit <b>Find running agents</b> to adopt the Claude and Codex
      sessions already running in tmux — lectern will watch them from then on.</div>`;
    return;
  }
  const query = state.sessionFilter.toLowerCase().trim();
  const items = live.filter(s => {const text=[s.name,s.project_name,s.target_name,s.agent,s.group_path,s.workdir,s.workspace?.branch].join(' ').toLowerCase();return query.split(/\s+/).every(word=>text.includes(word));});
  items.sort((a,b) => (SESSION_ORDER[a.status] ?? 9) - (SESSION_ORDER[b.status] ?? 9) || a.idle_seconds-b.idle_seconds);
  renderSessionGroups(list,items,{mode:state.sessionGrouping,query,collapsed:collapsedSessionGroups,renderCard:sessionCard,onToggle:()=>sessionStorage.setItem('lec-collapsed-session-groups',JSON.stringify([...collapsedSessionGroups]))});
  if (!items.length) list.innerHTML = '<div class="hint">No sessions match your search.</div>';
}

/* ---------- new session sheet ---------- */
function renderNewSession(sheet) {
  sheet.innerHTML = `
    <div class="sheet-grip"><i></i></div>
    <div class="sheet-head"><h2>New session</h2><button class="x" aria-label="Close new session">✕</button></div>
    <div class="sub" style="color:var(--ink-dim);font-size:12.5px">
      An interactive agent you attach to and work with — not a dispatched task.
    </div>
    <label class="f" for="ns-project">Project</label>
    <select class="f" id="ns-project" aria-describedby="ns-proj-hint">
      <option value="">▢ Blank room — no project yet</option>
      ${state.projects.map((p) =>
      `<option value="${p.id}">${esc(p.name)} — ${esc(p.target_name)}</option>`).join("")}</select>
    <div class="subhint" id="ns-proj-hint"></div>
    <label class="f" for="ns-name">Name</label>
    <input class="f" id="ns-name" placeholder="what you're working on">
    <label class="f" for="ns-group">Group (optional)</label><input class="f" id="ns-group" placeholder="Work/Client">
    <label class="f" for="ns-profile">Launch profile</label>
    <select class="f" id="ns-profile" aria-describedby="ns-profile-hint"><option value="">Agent and project defaults</option></select>
    <button class="b" id="ns-manage-profiles" type="button">Manage launch profiles</button>
    <div class="subhint" id="ns-profile-hint" role="status"></div>
    <label class="f" for="ns-agent">Agent</label>
    <select class="f" id="ns-agent" aria-describedby="ns-agent-hint"></select>
    <div class="subhint" id="ns-agent-hint"></div>
    <label class="f" for="ns-model">Model</label>
    <input class="f" id="ns-model" list="lec-models" placeholder="default" autocomplete="off">
    <datalist id="lec-models"></datalist>
    <label class="f check"><input type="checkbox" id="ns-worktree"> Isolate in a new Git worktree</label>
    <div id="ns-worktree-options" hidden>
      <p class="subhint">A fresh session with separate files on a new branch. Starts from a committed revision; uncommitted edits stay in the original directory.</p>
      <label class="f" for="ns-worktree-base">Base branch, tag or commit</label><input class="f" id="ns-worktree-base" placeholder="HEAD — current committed revision">
      <label class="f" for="ns-worktree-branch">New branch name</label><input class="f" id="ns-worktree-branch" placeholder="Automatic unique branch">
      <div id="ns-repositories"></div>
    </div>
    <label class="f" for="ns-start">Start from</label>
    <select class="f" id="ns-start" aria-describedby="ns-hint ns-memory-status">
      <option value="fresh">Fresh context</option>
      <option value="brief">Fresh, primed with what this project knows</option>
      <option value="resume">Resume the agent's own last conversation</option>
    </select>
    <div class="subhint" id="ns-hint"></div>
    <div class="subhint" id="ns-memory-status" role="status"></div>
    <label class="f check" style="display:flex;align-items:center;gap:9px;cursor:pointer">
      <input type="checkbox" id="ns-yolo" aria-describedby="ns-yolo-hint" checked style="width:auto;margin:0">
      <span>Yolo — no approval prompts</span>
    </label>
    <div class="subhint" id="ns-yolo-hint"></div>
    <label class="f" for="ns-prime">First message (optional)</label>
    <textarea class="f" id="ns-prime" placeholder="Typed in once the agent is up."></textarea>
    <div class="btnrow session-launch-actions">
      <button class="b ok grow" id="ns-go">▶ Start session</button>
    </div>`;
  $(".x", sheet).onclick = closeSheet;
  const agentBox = $("#ns-agent");
  const profileBox = $("#ns-profile", sheet);
  let profiles = [], agentSpecs = [], profileRequest = 0;
  const chosenProfile = () => profiles.find(p => String(p.id) === profileBox.value);
  function syncProfile() {
    if (!sheet.isConnected) return;
    const profile = chosenProfile();
    agentBox.disabled = !!profile;
    if (profile && agentSpecs.length) agentBox.value = profile.agent;
    $("#ns-profile-hint", sheet).textContent = profile
      ? `${profile.name} · ${profile.agent}${profile.model ? ' · default model: '+profile.model : ''}. Settings are captured when the session starts.` : '';
    if (agentSpecs.length) syncAgentHint(agentSpecs);
  }
  async function loadProfiles(saved) {
    const request = ++profileRequest;
    try {
      const result = await api('/launch-profiles');
      if (!sheet.isConnected || request !== profileRequest) return;
      const selected = saved ? String(saved.id) : profileBox.value;
      profiles = result;
      profileBox.replaceChildren(new Option('Agent and project defaults', ''));
      for (const p of profiles) profileBox.append(new Option(`${p.name} · ${p.agent}`, String(p.id)));
      profileBox.value = profiles.some(p => String(p.id) === selected) ? selected : '';
      syncProfile();
    } catch(e) { if(sheet.isConnected) $('#ns-profile-hint', sheet).textContent = 'Could not load profiles: '+e.message; }
  }
  profileBox.onchange = syncProfile;
  $('#ns-manage-profiles', sheet).onclick = () => openLaunchProfiles({api,onChange:loadProfiles});
  loadProfiles();
  // the agent set is yours: the three built-ins plus anything you defined, so
  // any CLI that starts in a terminal starts from here
  api("/agents").then((specs) => {
    agentBox.innerHTML = specs.map((a) =>
      `<option value="${esc(a.name)}">${esc(a.name)}${a.builtin ? "" : " (custom)"}</option>`).join("");
    agentSpecs = specs;
    agentBox.value = chosenProfile()?.agent || "claude";
    syncProfile();
    syncAgentHint(specs);
    agentBox.onchange = () => syncAgentHint(specs);
  }).catch(() => {
    agentBox.innerHTML = '<option value="claude">claude</option>';
  });
  // model names are per-agent and the sets move, so the list is what this agent
  // actually accepts plus what you have already run with it — never Claude's
  // shorthands under codex. The field itself stays free text either way.
  api("/models").then((m) => { state.models = m; syncModelList(); }).catch(() => {});
  function syncModelList() {
    const box = $("#ns-model");
    const list = (state.models || {})[agentBox.value];
    const spec = agentSpecs.find((a) => a.name === agentBox.value);
    $("#lec-models").innerHTML = (list || [])
      .map((m) => `<option>${esc(m)}</option>`).join("");
    box.placeholder = list === undefined
      ? "this agent has no model switch"
      : list.length ? "default — or type any model name"
      : "type the model name";
    if(chosenProfile()?.model) box.placeholder = chosenProfile().model + " — or override";
    // A custom runner may accept models even when it has no catalog command.
    // Keep the field editable whenever its definition declares a model flag.
    box.disabled = list === undefined && !spec?.model_flag;
  }
  function syncAgentHint(specs) {
    const a = specs.find((x) => x.name === agentBox.value);
    const bits = [];
    if (a && !a.model_flag) bits.push("no model switch — the Model field is ignored");
    if (a && !a.resume_args) bits.push("cannot resume its own history");
    $("#ns-agent-hint").textContent = bits.join(" · ");
    syncModelList();
    syncYolo(specs);
  }
  // On by default: you are sitting in the terminal watching it, and confirming
  // every edit in a session you opened on purpose is friction rather than
  // safety. Untick it when the agent is loose in something you care about.
  function syncYolo(specs) {
    const a = specs.find((x) => x.name === agentBox.value);
    const box = $("#ns-yolo");
    const supported = !a || (a.yolo_args && a.yolo_args.length);
    box.disabled = !supported;
    if (!supported) box.checked = false;
    $("#ns-yolo-hint").textContent = !supported
      ? `${agentBox.value} has no way to skip its prompts — it will ask.`
      : box.checked
      ? "The agent acts without stopping to ask. You are the supervision."
      : "The agent stops and asks before it edits or runs anything.";
  }
  // a blank room is for work that has no name yet; what it becomes is decided
  // afterwards, from the session card
  const projBox = $("#ns-project");
  const repositories = workspaceRepositories($("#ns-repositories"), state.projects);
  // the blank room is offered first because it is the option people do not know
  // exists — but starting a session usually means starting it on a project, so
  // that stays the selected default whenever there is one
  if (state.projects.length) projBox.value = String(state.projects[0].id);
  const syncProjHint = () => {
    const blank = !projBox.value;
    repositories.sync(projBox.value);
    $('#ns-worktree').disabled=blank;
    if(blank)$('#ns-worktree').checked=false;
    $('#ns-worktree-options').hidden=!$('#ns-worktree').checked;
    const resume=$('#ns-start option[value="resume"]');resume.disabled=$('#ns-worktree').checked;
    if(resume.disabled && $('#ns-start').value==='resume')$('#ns-start').value='fresh';
    $("#ns-proj-hint").textContent = blank
      ? "Starts the agent in a fresh throwaway directory. Turn it into a project later."
      : state.projects.find(p => String(p.id) === projBox.value)?.setup_cmd && $("#ns-worktree").checked ? "This project’s setup command runs in the new checkout before the agent starts." : "";
    const start = $("#ns-start");
    for (const opt of start.options) {
      if (opt.value === "brief") opt.disabled = blank; // nothing known about it yet
    }
    if (blank && start.value === "brief") start.value = "fresh";
    syncHint();
  };
  projBox.onchange = syncProjHint;
  $("#ns-worktree").onchange=syncProjHint;

  const hint = $("#ns-hint");
  let briefRequest = 0;
  const syncHint = () => {
    const request = ++briefRequest;
    const mode = $("#ns-start").value;
    hint.textContent = mode === "brief"
      ? "Pulls the project's durable memory and its last handoff into the first message."
      : mode === "resume"
      ? "Reopens the agent's own previous conversation in this directory."
      : "";
    const status = $("#ns-memory-status");
    status.textContent = "";
    if (mode === "brief" && projBox.value) {
      status.textContent = "Checking project memory…";
      api(`/projects/${projBox.value}/brief`).then((r) => {
        if (request !== briefRequest) return;
        status.textContent = r.memory?.message || "Memory status unavailable";
        status.dataset.status = r.memory?.status || "unavailable";
      }).catch(() => {
        if (request === briefRequest) status.textContent = "Could not preview project context. You can still start the session.";
      });
    }
  };
  $("#ns-start").onchange = syncHint;
  $("#ns-yolo").onchange = () => api("/agents").then(syncYolo).catch(() => {});
  syncProjHint();
  $("#ns-go").onclick = async () => {
    const mode = $("#ns-start").value;
    $("#ns-go").disabled=true;
    try {
      const projectID = projBox.value ? +projBox.value : null;
      const launched=await api("/sessions", { method: "POST", body: {
        background: $("#ns-worktree").checked,
        profile_id: profileBox.value ? Number(profileBox.value) : 0,
        project_id: projectID,
        scratch: projectID === null,
        worktree: $("#ns-worktree").checked ? {base:$("#ns-worktree-base").value.trim(),branch:$("#ns-worktree-branch").value.trim(),extra_repositories:repositories.value()} : null,
        name: $("#ns-name").value.trim(),
        group_path: $("#ns-group").value.trim(),
        agent: agentBox.value,
        model: $("#ns-model").value.trim(),
        resume: mode === "resume",
        brief: mode === "brief",
        yolo: $("#ns-yolo").checked,
        prime: $("#ns-prime").value.trim(),
      } });
      closeSheet();
      switchTab("sessions");
      await refreshSessions();
      toast(launched.setup_state === "creating" ? "Workspace setup started. You can keep using Lectern." : "Session started");
    } catch (e) { toast(e.message, true); } finally {const button=$("#ns-go");if(button)button.disabled=false;}
  };
}

/* ---------- discover sheet ----------
   The sessions worth tracking are usually the ones you started yourself, by
   hand, weeks ago. Adopting one is non-destructive: the tmux session is left
   exactly as it is and lectern simply starts watching it. */
function renderDiscover(sheet) {
  sheet.innerHTML = `
    <div class="sheet-grip"><i></i></div>
    <div class="sheet-head"><h2>Running agents</h2><button class="x">✕</button></div>
    <div class="sub" style="color:var(--ink-dim);font-size:12.5px">
      Every Claude/Codex process found in tmux on your targets. Adopting one does
      not restart or disturb it.
    </div>
    <div id="cands" style="margin-top:14px"><div class="hint">Scanning…</div></div>`;
  $(".x", sheet).onclick = closeSheet;
  const box = $("#cands", sheet);
  api("/sessions/discover").then((list) => {
    if (!list.length) {
      box.innerHTML = '<div class="hint">No agents found running on any target.</div>';
      return;
    }
    box.innerHTML = "";
    for (const c of list) {
      const row = document.createElement("div");
      row.className = "cand";
      row.innerHTML = `
        <div class="grow">
          <div class="who"></div>
          <div class="where"></div>
        </div>
        <select class="f cand-proj" style="width:auto;min-width:150px;padding:8px 10px">
          <option value="">— no project —</option>
          ${state.projects.map((p) =>
            `<option value="${p.id}">${esc(p.name)}</option>`).join("")}
        </select>
        <div class="btnrow" style="margin:0"></div>`;
      $(".who", row).textContent =
        `${c.tmux_session} · ${c.agent}${c.model ? " " + c.model : ""}`;
      $(".where", row).textContent = `${c.target_name} · ${c.workdir}`;
      const proj = $(".cand-proj", row);
      // an agent's working directory is often a scratch dir, not the project it
      // is actually working on — so the match is a suggestion, not a decision
      if (c.project_id) proj.value = String(c.project_id);
      const btns = $(".btnrow", row);
      if (c.adopted) {
        proj.remove();
        const tag = document.createElement("span");
        tag.className = "chip ds";
        tag.textContent = "tracked";
        btns.appendChild(tag);
      } else {
        const b = document.createElement("button");
        b.className = "b ok";
        b.textContent = "Adopt";
        b.onclick = async () => {
          b.textContent = "Adopting…";
          try {
            await api("/sessions/adopt", { method: "POST", body: {
              target_id: c.target_id, tmux_session: c.tmux_session,
              project_id: proj.value ? +proj.value : null,
              agent: c.agent, model: c.model,
              workdir: c.workdir, name: c.tmux_session } });
            closeSheet(); switchTab("sessions"); refreshSessions();
            toast(`Adopted ${c.tmux_session}`);
          } catch (e) { toast(e.message, true); b.textContent = "Adopt"; }
        };
        btns.appendChild(b);
      }
      box.appendChild(row);
    }
  }).catch((e) => { box.innerHTML = `<div class="hint">${esc(e.message)}</div>`; });
}

/* ---------- approvals tab ---------- */
function renderApprovals() {
  const main = $("#view");
  if (!state.approvals.length) {
    main.innerHTML = '<div class="hint">No pending approvals.<br>When an agent needs permission it shows up here — and pings your phone.</div>';
    return;
  }
  main.innerHTML = '<div class="list"></div>';
  const list = $(".list", main);
  for (const a of state.approvals) list.appendChild(approvalCard(a));
}
function approvalCard(a) {
  const el = document.createElement("div");
  el.className = "rowcard";
  el.innerHTML = `
    <h3>${esc(a.tool_name)} <span style="color:var(--ink-faint);font-weight:400">wants to run</span></h3>
    <div class="sub">task #${a.task_id} · ${esc(a.task_title || "")}</div>
    <pre></pre>
    <div class="btnrow">
      <button class="b ok grow">Approve</button>
      <button class="b ok" title="approve and never ask again for this pattern in this project">∞ Always</button>
      <button class="b no grow">Deny</button>
    </div>`;
  $("pre", el).textContent = JSON.stringify(a.input, null, 2).slice(0, 1200);
  $(".ok", el).onclick = () => decide(a.id, "approved");
  el.querySelectorAll(".ok")[1].onclick = () => decide(a.id, "approved", "", true);
  $(".no", el).onclick = async () => {
    const note = prompt("Reason (sent back to the agent):", "not safe, find another way") || "";
    decide(a.id, "denied", note);
  };
  return el;
}
async function decide(id, decision, note = "", always = false) {
  try {
    await api(`/approvals/${id}/decision`, { method: "POST",
      body: { decision, note, always_allow: always } });
    toast(decision === "approved" ? "Approved — agent continuing" : "Denied — agent notified");
    refreshApprovals();
  } catch (e) { toast(e.message, true); }
}

/* ---------- targets tab ---------- */
async function renderTargets() {
  const main = $("#view");
  main.innerHTML = `<div class="settings-page"><div class="page-heading"><div><h2>Settings</h2><p>Machines, projects and preferences in one place.</p></div></div>
    <div class="settings-nav" role="tablist" aria-label="Settings sections">
      <button data-settings="machines" role="tab">Targets</button><button data-settings="projects" role="tab">Projects</button>
      <button data-settings="notifications" role="tab">Notifications</button><button data-settings="about" role="tab">Usage &amp; about</button>
      <button data-settings="agents" role="tab">Agents</button>
    </div>
    <section data-settings-panel="machines" class="settings-grid" role="tabpanel"></section>
    <section data-settings-panel="projects" role="tabpanel"></section>
    <section data-settings-panel="notifications" role="tabpanel"></section>
    <section data-settings-panel="about" class="settings-grid" role="tabpanel"></section>
    <section data-settings-panel="agents" role="tabpanel"></section></div>`;
  const page = $('.settings-page', main);
  const list = $('[data-settings-panel="machines"]', page);
  const projectPanel = $('[data-settings-panel="projects"]', page);
  const aboutPanel = $('[data-settings-panel="about"]', page);
  const agentsPanel = $('[data-settings-panel="agents"]', page);
  const chooseSection = (section) => {
    state.settingsSection = section;
    sessionStorage.setItem('lec-settings-section', section);
    $$('[data-settings-panel]',page).forEach(p => p.hidden = p.dataset.settingsPanel !== section);
    $$('[data-settings]',page).forEach(b => { const selected=b.dataset.settings === section; b.setAttribute('aria-selected', String(selected)); b.tabIndex=selected?0:-1; });
  };
  const sectionButtons = $$('[data-settings]', page);
  sectionButtons.forEach((b,index) => {
    b.id = 'settings-tab-' + b.dataset.settings;
    b.setAttribute('aria-controls', 'settings-panel-' + b.dataset.settings);
    const panel = $('[data-settings-panel="' + b.dataset.settings + '"]',page);
    panel.id = 'settings-panel-' + b.dataset.settings; panel.setAttribute('aria-labelledby', b.id);
    b.onclick = () => chooseSection(b.dataset.settings);
    b.onkeydown = e => {
      let next; if(e.key==='ArrowRight') next=(index+1)%sectionButtons.length;
      if(e.key==='ArrowLeft') next=(index+sectionButtons.length-1)%sectionButtons.length;
      if(e.key==='Home') next=0; if(e.key==='End') next=sectionButtons.length-1;
      if(next!==undefined) {e.preventDefault();sectionButtons[next].click();sectionButtons[next].focus();}
    };
  });
  chooseSection(state.settingsSection);
  // fetch BEFORE building the form — a late response must never clobber typed input
  let settings = {};
  try { settings = await api("/settings"); } catch {}
  if (!page.isConnected) return;
  const buildCard = document.createElement("div");
  buildCard.className = "rowcard";
  buildCard.id = "running-build";
  buildCard.innerHTML = '<h3>Running build</h3><div class="sub">Loading…</div>';
  aboutPanel.appendChild(buildCard);
  api("/health").then((h) => {
    const b = h.build || {};
    const status = b.modified === true ? "local changes" : b.modified === false ? "clean" : "build status unknown";
    buildCard.querySelector(".sub").textContent = `${h.version} · ${b.revision ? b.revision.slice(0, 12) : "revision unknown"} · ${status}`;
    buildCard.title = b.revision || "VCS metadata was not recorded in this binary.";
  }).catch(() => { buildCard.querySelector(".sub").textContent = "Build information unavailable"; });
  for (const t of state.targets) {
    const el = document.createElement("div");
    el.className = "rowcard";
    const led = t.status === "online" ? "led-on" : t.status === "offline" ? "led-err" : "led-warn";
    const info = safeParse(t.info_json);
    el.innerHTML = `
      <h3><span class="led ${led}"></span> ${esc(t.name)}</h3>
      <div class="sub">${esc(t.kind)}${t.host ? " · " + esc(t.user + "@" + t.host) : ""} · ${t.max_concurrent} slots${t.sandbox ? " · sandbox" : ""}</div>
      ${info.claude ? `<div class="sub" style="margin-top:5px">claude ${esc(info.claude)} · ${esc(info.git || "")}</div>` : ""}
      <div class="btnrow"><button class="b">Probe</button><button class="b target-agents">Agent commands</button></div>`;
    $("button", el).onclick = async (ev) => {
      ev.target.textContent = "Probing…";
      try { await api(`/targets/${t.id}/check`, { method: "POST" }); await refreshMeta(); }
      catch (e) { toast(e.message, true); }
    };
    $(".target-agents", el).onclick = () => openAgentCommands({api, target: t});
    list.appendChild(el);
  }
  projectPanel.appendChild(projectsCard());

  projectPanel.appendChild(importCard());

  const statsCard = document.createElement("div");
  statsCard.className = "rowcard";
  statsCard.innerHTML = '<h3>Spend</h3><div class="sub">loading…</div>';
  api("/stats").then((s) => {
    statsCard.innerHTML = `<h3>Spend</h3>
      <div class="sub">$${s.total_cost_usd.toFixed(2)} all-time · $${s.last_7d_usd.toFixed(2)} last 7d · ${s.tasks_done} tasks done</div>
      ${s.by_project.slice(0, 5).map((p) =>
        `<div class="sub" style="margin-top:4px">${esc(p.name)} <span style="color:var(--amber)">$${p.cost_usd.toFixed(2)}</span></div>`).join("")}`;
  }).catch(() => { statsCard.querySelector(".sub").textContent = "unavailable"; });
  aboutPanel.appendChild(statsCard);

  const foot = document.createElement("div");
  foot.className = "rowcard";
  foot.innerHTML = `<h3>Notifications</h3>
    <div class="sub">Get pinged for approvals and finished tasks.</div>
    <div class="btnrow"><button class="b warn grow" id="push-btn">Enable push on this device</button></div>
    <label class="f">Discord webhook URL</label>
    <input class="f" id="s-discord" placeholder="https://discord.com/api/webhooks/…">
    <label class="f">ntfy server / topic <span style="text-transform:none;letter-spacing:0">(gets approve/deny buttons)</span></label>
    <div style="display:flex;gap:8px">
      <input class="f" id="s-ntfy-server" placeholder="https://ntfy.sh" style="flex:2">
      <input class="f" id="s-ntfy-topic" placeholder="topic" style="flex:1">
    </div>
    <div class="btnrow">
      <button class="b grow" id="s-save">Save sinks</button>
      <button class="b" id="s-test">Send test</button>
    </div>`;
  $("#push-btn", foot).onclick = enablePush;
  $("#s-discord", foot).value = settings.discord_webhook || "";
  $("#s-ntfy-server", foot).value = settings.ntfy_server || "";
  $("#s-ntfy-topic", foot).value = settings.ntfy_topic || "";
  $("#s-save", foot).onclick = async () => {
    try {
      await api("/settings", { method: "PUT", body: {
        discord_webhook: $("#s-discord", foot).value.trim(),
        ntfy_server: $("#s-ntfy-server", foot).value.trim(),
        ntfy_topic: $("#s-ntfy-topic", foot).value.trim() } });
      toast("Sinks saved");
    } catch (e) { toast(e.message, true); }
  };
  $("#s-test", foot).onclick = async () => {
    try { await api("/settings/test-notification", { method: "POST" }); toast("Test sent"); }
    catch (e) { toast(e.message, true); }
  };
  $('[data-settings-panel="notifications"]',page).appendChild(foot);
  renderAgentSettings(agentsPanel, {api, onChange: async (next) => {
    state.agents = next || [];
    // The next task/session/routine sheet should use the just-saved registry.
  }});
}

/** One project's capability, stated from the server's RESOLVED view.

    A dispatched agent is headless: it cannot be asked for permission, so a tool
    that is not granted is denied with no prompt and no error the operator sees.
    'parity' grants what a terminal session has — Bash with pipes, the MCP servers
    the target can actually reach, and the shared memory store. */
function projectCard(p) {
  const el = document.createElement("div");
  el.className = "rowcard";
  el.innerHTML = `
    <h3>${esc(p.name)}</h3>
    <div class="sub">${esc(p.target_name)} · ${esc(p.target_kind)} · ${esc(p.repo_path)}</div>
    <label class="f">Agent capability</label>
    <select class="f cap-sel">
      <option value="restricted">restricted — only rules you set</option>
      <option value="parity">parity — same tools as your terminal</option>
    </select>
    <div class="sub cap-info">checking…</div>
    <label class="f">Default permission mode</label>
    <select class="f perm-sel">
      <option value="">— task default (accept edits) —</option>
      <option value="default">Gated — approve every tool call</option>
      <option value="acceptEdits">Accept edits</option>
      <option value="plan">Plan only</option>
      <option value="bypassPermissions">Bypass — sandboxed targets only</option>
    </select>
    <label class="f">New worktree setup command<textarea class="f project-setup" rows="3" maxlength="16384" spellcheck="false"></textarea></label>
    <p class="sub">Runs in each new isolated checkout before its agent starts, using this project’s environment. A failure keeps the files for inspection. Existing directories and attachments do not rerun it.</p>
    <button class="b project-setup-save">Save setup command</button><div class="sub project-setup-status" role="status"></div>`;
  const setup = $(".project-setup", el), saveSetup = $(".project-setup-save", el), setupStatus = $(".project-setup-status", el);
  setup.value = p.setup_cmd || '';
  saveSetup.onclick = async () => {
    const command = setup.value; saveSetup.disabled = true; setupStatus.textContent = 'Saving…';
    try {
      await api(`/projects/${p.id}`, {method:'PATCH', body:{setup_cmd:command}});
      p.setup_cmd = command; setupStatus.textContent = 'Saved for new isolated workspaces.';
    } catch(e) { setupStatus.textContent = e.message; }
    finally { saveSetup.disabled = false; }
  };

  const mcpSection = document.createElement("section");
  mcpSection.className = "project-mcp";
  mcpSection.innerHTML = `<h4>Project MCP servers</h4>
    <p class="sub">Applied to new, resumed, and forked sessions. Running agent processes do not hot-reload. Codex uses additive settings; strict replacement is Claude-only.</p>
    <label class="f">Claude strict MCP replacement
      <select class="f project-mcp-strict"><option value="false">Off — add to the target settings</option><option value="true">On — replace inherited MCP settings</option></select>
    </label>
    <div class="project-mcp-list"></div>
    <div class="btnrow"><button class="b project-mcp-add">Add server</button><button class="b project-mcp-reload">Reload</button><button class="b ok project-mcp-save">Save MCP settings</button></div>
    <div class="sub project-mcp-status" role="status"></div>`;
  el.appendChild(mcpSection);
  const mcpList = $(".project-mcp-list", mcpSection), mcpStatus = $(".project-mcp-status", mcpSection);
  let sourceMCP = {};
  let mcpRevision = "";
  const draft = {};
  const drawMCP = () => {
    mcpList.innerHTML = "";
    for (const [name, cfg] of Object.entries(draft)) {
      const row = document.createElement("div"); row.className = "mcp-row";
      row.innerHTML = `<input class="f mcp-name" aria-label="MCP server name" value="${esc(name)}">
        <select class="f mcp-type" aria-label="MCP transport"><option value="stdio">stdio</option><option value="http">HTTP</option></select>
        <input class="f mcp-command" aria-label="MCP command or URL">
        <textarea class="f mcp-extra" rows="2" aria-label="MCP arguments, headers, or environment"></textarea>
        <button class="b no mcp-remove" type="button">Remove</button>`;
      const type = cfg.url ? "http" : "stdio"; $(".mcp-type", row).value = type;
      const command = $(".mcp-command", row); command.value = cfg.url || cfg.command || "";
      const extra = $(".mcp-extra", row); extra.placeholder = type === "http" ? '{"headers":{}}' : '{"args":[],"env":{}}';
      const extraObj = {...cfg}; delete extraObj.command; delete extraObj.url;
      // The endpoint already returned opaque retention tokens. Keep those
      // tokens visible in the draft so a save can restore the server value;
      // never replace them with a second client-side placeholder.
      extra.value = JSON.stringify(extraObj, null, 2);
      const remove = $(".mcp-remove", row); remove.onclick = () => { delete draft[name]; drawMCP(); };
      row.onchange = () => { mcpStatus.textContent = "Unsaved MCP changes"; };
      $(".mcp-type", row).onchange = () => {
        extra.placeholder = $(".mcp-type", row).value === "http" ? '{"headers":{}}' : '{"args":[],"env":{}}';
        mcpStatus.textContent = "Unsaved MCP changes";
      };
      mcpList.appendChild(row);
    }
  };
  $(".project-mcp-add", mcpSection).onclick = () => { let n = "server"; let i = 1; while (draft[n]) n = `server-${i++}`; draft[n] = {command:""}; drawMCP(); mcpList.lastElementChild?.querySelector(".mcp-name")?.focus(); };
  $(".project-mcp-save", mcpSection).onclick = async () => {
    const next = {}; let invalid = "";
    for (const row of $$(".mcp-row", mcpSection)) {
      const name = $(".mcp-name", row).value.trim(), type = $(".mcp-type", row).value, command = $(".mcp-command", row).value.trim();
      if (!name || !/^[A-Za-z0-9_-]+$/.test(name) || next[name]) { invalid = "Names must be unique and use letters, digits, _ or -."; break; }
      if (!command) { invalid = `${name}: command or URL is required.`; break; }
      let extra = {}; try { extra = JSON.parse($(".mcp-extra", row).value || "{}"); } catch { invalid = `${name}: extra settings must be valid JSON.`; break; }
      next[name] = type === "http" ? {url: command, ...extra} : {command, ...extra};
    }
    if (invalid) { mcpStatus.textContent = invalid; return; }
    // Capture the form values before the request so a conflict can redraw the
    // same draft after refreshing only its retention references.
    Object.keys(draft).forEach(k => delete draft[k]); Object.assign(draft, JSON.parse(JSON.stringify(next)));
    const btn = $(".project-mcp-save", mcpSection); btn.disabled = true; mcpStatus.textContent = "Saving…";
    try {
      const saved = await api(`/projects/${p.id}/mcp`, {method:"PUT", body:{mcp: next, revision:mcpRevision, strict_mcp: $(".project-mcp-strict", mcpSection).value === "true"}});
      sourceMCP = saved.mcp || {}; mcpRevision = saved.revision || "";
      Object.keys(draft).forEach(k => delete draft[k]); Object.assign(draft, JSON.parse(JSON.stringify(sourceMCP)));
      $(".project-mcp-strict", mcpSection).value = saved.strict_mcp ? "true" : "false";
      drawMCP();
      mcpStatus.textContent = "Saved. New and resumed/forked sessions will use this configuration.";
    }
    catch (e) {
      if (e.status === 409) {
        // Refresh only the conditional revision. Keep the draft on screen so
        // the next explicit Save is the user's conflict resolution, rather
        // than silently replacing their edits with the other writer's copy.
        try {
          const latest = await api(`/projects/${p.id}/mcp`);
          mcpRevision = latest.revision || mcpRevision;
          sourceMCP = latest.mcp || sourceMCP;
          for (const key of Object.keys(draft)) draft[key] = syncRetained(draft[key], sourceMCP[key]);
          drawMCP();
          mcpStatus.textContent = "MCP settings changed elsewhere; your draft is preserved. Review it, then Save again to replace the newer copy.";
        } catch (refreshError) {
          mcpStatus.textContent = "MCP settings changed elsewhere; your draft is preserved, but the new revision could not be loaded: " + refreshError.message;
        }
      } else {
        mcpStatus.textContent = e.message;
      }
    }
    finally { btn.disabled = false; }
  };
  const loadMCP = async (discardDraft = false) => {
    if (!discardDraft && mcpRevision && mcpStatus.textContent.startsWith("Unsaved")) return;
    try {
      const loaded = await api(`/projects/${p.id}/mcp`);
      sourceMCP = loaded.mcp || {}; mcpRevision = loaded.revision || "";
      Object.keys(draft).forEach(k => delete draft[k]); Object.assign(draft, JSON.parse(JSON.stringify(sourceMCP)));
      $(".project-mcp-strict", mcpSection).value = loaded.strict_mcp ? "true" : "false";
      drawMCP(); mcpStatus.textContent = "MCP settings loaded.";
    } catch (e) { mcpStatus.textContent = "Could not load MCP settings: " + e.message; }
  };
  const syncRetained = (draftValue, latestValue) => {
    if (latestValue && typeof latestValue === "object" && !Array.isArray(latestValue) &&
        Object.keys(latestValue).length === 1 && Object.prototype.hasOwnProperty.call(latestValue, "__lectern_retained")) {
      return JSON.parse(JSON.stringify(latestValue));
    }
    if (Array.isArray(draftValue) && Array.isArray(latestValue)) {
      return draftValue.map((value, i) => syncRetained(value, latestValue[i]));
    }
    if (draftValue && typeof draftValue === "object" && !Array.isArray(draftValue) &&
        latestValue && typeof latestValue === "object" && !Array.isArray(latestValue)) {
      return Object.fromEntries(Object.entries(draftValue).map(([key, value]) => [key, syncRetained(value, latestValue[key])]));
    }
    return draftValue;
  };
  $(".project-mcp-reload", mcpSection).onclick = () => {
    if (mcpStatus.textContent.startsWith("Unsaved") || mcpStatus.textContent.includes("draft")) {
      if (!confirm("Discard the unsaved MCP draft and reload the server settings?")) return;
    }
    loadMCP(true);
  };
  drawMCP();
  loadMCP();

  // Project skills are target-local. The catalog is queried from the selected
  // target for the selected provider, while attachments are the project's
  // durable intent. Keep these as separate loads so a discovery failure never
  // hides the attachments the operator may need to remove.
  const skillsSection = document.createElement("section");
  skillsSection.className = "project-skills";
  skillsSection.innerHTML = `<h4>Project skills</h4>
    <p class="sub">Choose a provider, search the target's named skill catalog, then attach or detach skills for this project. New launches use the saved attachments; running processes are not restarted automatically.</p>
    <div class="skills-controls">
      <label class="f">Provider<select class="f skills-agent" aria-label="Skills provider"><option value="claude">Claude Code</option><option value="codex">Codex</option></select></label>
      <button class="b skills-reload" type="button">Reload</button>
    </div>
    <label class="f">Search catalog<input class="f skills-search" placeholder="name, source, description" autocomplete="off"></label>
    <div class="sub skills-status" role="status" aria-live="polite">Loading skills…</div>
    <div class="skills-attached"><h5>Attached</h5><div class="skills-attached-list"></div></div>
    <div class="skills-catalog"><h5>Available on target</h5><div class="skills-catalog-list"></div></div>
    <details class="skills-sources"><summary>Extra target skill directories</summary>
      <p class="sub">One absolute directory per line. These paths are read on the project target during discovery.</p>
      <textarea class="f skills-source-input" rows="3" spellcheck="false" aria-label="Extra target skill directories"></textarea>
      <div class="btnrow"><button class="b skills-source-save" type="button">Save directories</button><button class="b no skills-source-clear" type="button">Clear directories</button></div>
      <div class="sub skills-source-status" role="status"></div>
    </details>`;
  el.appendChild(skillsSection);
  const skillAgent = $(".skills-agent", skillsSection);
  const skillSearch = $(".skills-search", skillsSection);
  const skillStatus = $(".skills-status", skillsSection);
  const attachedList = $(".skills-attached-list", skillsSection);
  const catalogList = $(".skills-catalog-list", skillsSection);
  const sourceInput = $(".skills-source-input", skillsSection);
  const sourceStatus = $(".skills-source-status", skillsSection);
  let skillCatalog = [], skillAttachments = [], skillGeneration = 0, skillsLoading = false;
  const configuredSources = () => {
    try { return JSON.parse(p.skill_sources_json || "[]"); } catch { return []; }
  };
  sourceInput.value = configuredSources().join("\n");
  skillAgent.value = p.default_agent === "codex" ? "codex" : "claude";
  const drawSkills = () => {
    const attachedIDs = new Set(skillAttachments.map((a) => a.skill_id));
    attachedList.innerHTML = "";
    if (!skillAttachments.length) {
      attachedList.innerHTML = '<div class="sub skills-empty">No skills attached for this provider.</div>';
    } else for (const a of skillAttachments) {
      const row = document.createElement("div"); row.className = "skill-row attached-skill";
      row.innerHTML = `<span class="skill-main"><b></b><small></small></span><button class="b no skill-detach" type="button">Detach</button>`;
      $("b", row).textContent = a.entry_name || a.skill_id;
      $("small", row).textContent = [a.source_id || "target", a.skill_id].filter(Boolean).join(" · ");
      $(".skill-detach", row).onclick = async () => {
      const rowGeneration = skillGeneration, rowAgent = skillAgent.value;
      const button = $(".skill-detach", row); button.disabled = skillsLoading || true; skillStatus.textContent = "Detaching…";
      if (rowGeneration !== skillGeneration || rowAgent !== skillAgent.value) { skillStatus.textContent = "Provider changed; reload the selected provider before detaching."; return; }
        try { const result = await api(`/projects/${p.id}/skills/${a.id}`, {method:"DELETE"});
          skillAttachments = skillAttachments.filter((x) => x.id !== a.id); drawSkills();
          skillStatus.textContent = result?.preserved?.length ? "Detached; target content was preserved." : "Skill detached.";
        } catch (e) { button.disabled = false; skillStatus.textContent = "Could not detach: " + e.message; }
      };
      attachedList.appendChild(row);
    }
    const q = (skillSearch.value || "").trim().toLowerCase();
    catalogList.innerHTML = "";
    const rows = skillCatalog.filter((s) => !q || [s.name, s.entry_name, s.source, s.description, s.id].some((v) => String(v || "").toLowerCase().includes(q)));
    if (!rows.length) { catalogList.innerHTML = `<div class="sub skills-empty">${skillCatalog.length ? "No catalog entries match this search." : "No skills discovered on this target."}</div>`; return; }
    for (const s of rows) {
      const row = document.createElement("div"); row.className = "skill-row catalog-skill";
      row.innerHTML = `<span class="skill-main"><b></b><small></small></span><button class="b ok skill-attach" type="button"></button>`;
      $("b", row).textContent = s.name || s.entry_name || s.id;
      $("small", row).textContent = [s.source || "target", s.description].filter(Boolean).join(" · ");
      const button = $(".skill-attach", row);
      const attached = attachedIDs.has(s.id);
      button.textContent = attached ? "Attached" : "Attach"; button.disabled = attached || skillsLoading;
      const rowGeneration = skillGeneration, rowAgent = skillAgent.value;
      button.onclick = async () => {
        if (rowGeneration !== skillGeneration || rowAgent !== skillAgent.value || skillsLoading) { skillStatus.textContent = "Provider changed; reload the selected provider before attaching."; return; }
        button.disabled = true; skillStatus.textContent = `Attaching ${s.name || s.entry_name || s.id}…`;
        try { const result = await api(`/projects/${p.id}/skills`, {method:"POST", body:{agent:skillAgent.value, skill_id:s.id}});
          if (result?.attachment) skillAttachments = [...skillAttachments, result.attachment];
          drawSkills(); skillStatus.textContent = "Skill attached.";
        } catch (e) { button.disabled = false; skillStatus.textContent = e.status === 409 ? "Skill is already attached or its target destination is occupied; reload to review." : "Could not attach: " + e.message; }
      };
      catalogList.appendChild(row);
    }
  };
  const loadSkills = async () => {
    const agent = skillAgent.value, generation = ++skillGeneration;
    skillCatalog = []; skillAttachments = [];
    skillsLoading = true; skillStatus.textContent = `Loading ${agent} skills…`; drawSkills();
    $(".skills-reload", skillsSection).disabled = true;
    const [available, attached] = await Promise.allSettled([
      api(`/skills?project_id=${p.id}&agent=${encodeURIComponent(agent)}`),
      api(`/projects/${p.id}/skills?agent=${encodeURIComponent(agent)}`)]);
    if (generation !== skillGeneration || agent !== skillAgent.value) return;
    const errors = [];
    if (available.status === "fulfilled") skillCatalog = available.value.skills || [];
    else errors.push("catalog: " + (available.reason?.message || String(available.reason)));
    if (attached.status === "fulfilled") skillAttachments = attached.value.attachments || [];
    else errors.push("attachments: " + (attached.reason?.message || String(attached.reason)));
    skillsLoading = false; drawSkills();
    skillStatus.textContent = errors.length ? "Could not load " + errors.join("; ") + ". Use Reload to retry." : `${skillCatalog.length} available · ${skillAttachments.length} attached.`;
    $(".skills-reload", skillsSection).disabled = false;
  };
  skillSearch.oninput = drawSkills;
  skillAgent.onchange = loadSkills;
  $(".skills-reload", skillsSection).onclick = loadSkills;
  $(".skills-source-save", skillsSection).onclick = async () => {
    const values = sourceInput.value.split("\n").map((x) => x.trim()).filter(Boolean);
    const button = $(".skills-source-save", skillsSection); button.disabled = true; sourceStatus.textContent = "Saving…";
    try { const saved = await api(`/projects/${p.id}`, {method:"PATCH", body:{skill_sources:values}}); p.skill_sources_json = saved.skill_sources_json || JSON.stringify(values); sourceInput.value = values.join("\n"); sourceStatus.textContent = "Directories saved. Reload the provider to discover them."; }
    catch (e) { sourceStatus.textContent = "Could not save directories: " + e.message; }
    finally { button.disabled = false; }
  };
  $(".skills-source-clear", skillsSection).onclick = () => { sourceInput.value = ""; sourceStatus.textContent = "Unsaved directory changes — use Save directories to clear the target configuration."; sourceInput.focus(); };
  loadSkills();
  const sel = $(".cap-sel", el);
  const info = $(".cap-info", el);
  sel.value = p.capability_profile || "restricted";
  const permSel = $(".perm-sel", el);
  permSel.value = p.default_permission_mode || "";
  permSel.onchange = async () => {
    try {
      await api(`/projects/${p.id}`, { method: "PATCH",
        body: { default_permission_mode: permSel.value } });
      p.default_permission_mode = permSel.value;
      toast(`${p.name}: ${permSel.value || "task default"}`);
    } catch (e) {
      toast(e.message, true);
      permSel.value = p.default_permission_mode || "";
    }
  };

  const paint = (c) => {
    const bits = [];
    bits.push(c.mcp_servers.length ? `MCP: ${c.mcp_servers.join(", ")}` : "MCP: none reachable");
    bits.push(c.memory_dir ? "memory: shared" : "memory: none");
    if (c.allow.includes("Bash")) bits.push("bash: unrestricted");
    info.innerHTML = esc(bits.join(" · ")) +
      (c.notes.length ? c.notes.map((n) => `<div class="cap-note">⚠ ${esc(n)}</div>`).join("") : "");
  };
  const load = () => api(`/projects/${p.id}/capability`).then(paint)
    .catch(() => { info.textContent = "capability unavailable"; });
  load();

  sel.onchange = async () => {
    try {
      await api(`/projects/${p.id}`, { method: "PATCH", body: { capability_profile: sel.value } });
      p.capability_profile = sel.value;
      toast(`${p.name}: ${sel.value}`);
      load();
    } catch (e) { toast(e.message, true); sel.value = p.capability_profile || "restricted"; }
  };
  return el;
}

/** Bulk-register the projects you already have.

    An empty board is the reason a tool like this gets abandoned in week one.
    Point it at where your code lives; the scan looks for a git repo, a build
    manifest, or a project-shaped document, and you pick from the list. */
function importCard() {
  const el = document.createElement("div");
  el.className = "rowcard";
  el.innerHTML = `
    <h3>Import projects</h3>
    <div class="sub">Scan a directory on a target and register what looks like a project.</div>
    <label class="f">Target</label>
    <select class="f" id="imp-target">${state.targets.map((t) =>
      `<option value="${t.id}">${esc(t.name)} — ${esc(t.kind)}</option>`).join("")}</select>
    <label class="f">Directory to scan</label>
    <div style="display:flex;gap:8px">
      <input class="f" id="imp-root" placeholder="/home/you/projects" style="flex:1">
      <button class="b" id="imp-scan">Scan</button>
    </div>
    <div id="imp-out"></div>`;
  const out = $("#imp-out", el);
  $("#imp-scan", el).onclick = async () => {
    const root = $("#imp-root", el).value.trim();
    if (!root) return toast("Give a directory to scan", true);
    out.innerHTML = '<div class="sub" style="margin-top:10px">Scanning…</div>';
    try {
      const found = await api(`/projects/import/scan?root=${encodeURIComponent(root)}` +
        `&target_id=${$("#imp-target", el).value}`);
      if (!found.length) {
        out.innerHTML = '<div class="sub" style="margin-top:10px">Nothing project-shaped in there.</div>';
        return;
      }
      out.innerHTML = `<div class="sub" style="margin-top:12px">${found.length} found —
        untick anything you don't want.</div><div id="imp-list"></div>
        <label class="f"><input type="checkbox" id="imp-verify"> also set the suggested test command</label>
        <div class="btnrow"><button class="b ok grow" id="imp-go">Import selected</button></div>`;
      const box = $("#imp-list", out);
      for (const c of found) {
        const row = document.createElement("label");
        row.className = "cand";
        row.innerHTML = `
          <input type="checkbox" ${c.registered ? "disabled" : "checked"} value="${esc(c.path)}">
          <span class="grow">
            <span class="who"></span>
            <span class="where"></span>
          </span>`;
        $(".who", row).textContent = c.name + (c.registered ? "  (already on the board)" : "");
        $(".where", row).textContent =
          [c.git ? "git " + (c.branch || "?") : "no git", c.marker, c.last_commit]
            .filter(Boolean).join(" · ");
        box.appendChild(row);
      }
      $("#imp-go", out).onclick = async () => {
        const paths = [...box.querySelectorAll("input:checked")].map((i) => i.value);
        if (!paths.length) return toast("Nothing selected", true);
        try {
          const r = await api("/projects/import", { method: "POST", body: {
            target_id: +$("#imp-target", el).value, paths,
            verify: $("#imp-verify", out).checked } });
          toast(`Imported ${r.imported.length} project(s)`);
          await refreshMeta();
          renderTargets();
        } catch (e) { toast(e.message, true); }
      };
    } catch (e) { out.innerHTML = `<div class="sub" style="margin-top:10px">${esc(e.message)}</div>`; }
  };
  return el;
}

/* ---------- task sheet ---------- */
async function openTaskSheet(id) {
  state.sheet = { kind: "task", id };
  state.taskEvents = [];
  state.taskDiff = null; state.diffOpen = false; state.attemptView = null;
  await loadTaskSheet(id);
  if (state.taskES) state.taskES.close();
  let taskSseOnce = false;
  state.taskES = new EventSource(withToken(`/api/tasks/${id}/stream`));
  state.taskES.onopen = () => {
    // resync the timeline on reconnect — SSE doesn't replay missed events
    if (taskSseOnce && state.sheet?.kind === "task" && state.sheet.id === id)
      loadTaskSheet(id);
    taskSseOnce = true;
  };
  state.taskES.addEventListener("agent_event", (e) => {
    state.taskEvents.push(JSON.parse(e.data));
    renderSheet();
  });
  state.taskES.addEventListener("approval", () => refreshApprovals());
}
async function loadTaskSheet(id, soft = false) {
  state.sheetTask = await api(`/tasks/${id}`);
  if (!soft || !state.taskEvents.length) {
    const q = state.attemptView ? `?attempt_n=${state.attemptView}` : "";
    state.taskEvents = (await api(`/tasks/${id}/events${q}`)).map((e) => ({
      type: e.type, payload: e.payload, seq: e.seq, attempt_n: e.attempt_n }));
  }
  renderSheet();
}
function closeSheet() {
  state.sheet = null;
  if (state.taskES) { state.taskES.close(); state.taskES = null; }
  $("#sheet").hidden = true; $("#sheet-backdrop").hidden = true;
  sheetFocus.close();
}

function evRow(e) {
  const el = document.createElement("div");
  el.className = `ev e-${e.type}`;
  const p = e.payload || {};
  if (e.type === "init")
    el.innerHTML = `<div class="k">session start</div><div class="body dim">model <code>${esc(p.model || "?")}</code> · session <code>${esc((p.session_id || "").slice(0, 18))}</code></div>`;
  else if (e.type === "text") {
    el.innerHTML = `<div class="k">agent</div><div class="body"></div>`;
    $(".body", el).textContent = p.text;
  } else if (e.type === "tool_use")
    el.innerHTML = `<div class="k">tool</div><div class="body"><code>${esc(p.name)}</code> <span style="color:var(--ink-dim)">${esc(snippet(p.input))}</span></div>`;
  else if (e.type === "tool_result") {
    el.innerHTML = `<div class="k">↳ result${p.is_error ? " · ERROR" : ""}</div><div class="body dim"></div>`;
    $(".body", el).textContent = (p.content || "").slice(0, 400);
  } else if (e.type === "verify")
    el.innerHTML = `<div class="k">auto-verify · ${p.rc === 0 ? "PASS ✓" : "FAIL ✗"}</div>
      <div class="body" style="color:${p.rc === 0 ? "var(--green)" : "var(--red)"}"><code>${esc(p.cmd)}</code>\n${esc((p.output || "").slice(-500))}</div>`;
  else if (e.type === "review_verdict")
    el.innerHTML = `<div class="k">reviewer verdict · ${esc(p.verdict || "")}</div><div class="body dim">${esc((p.notes || "").slice(-400))}</div>`;
  else if (e.type === "result")
    el.innerHTML = `<div class="k">finished · ${esc(p.subtype)}</div><div class="body">${esc(p.result || "")}\n<span style="color:var(--ink-dim)">${p.num_turns ?? "?"} turns · ${fmtCost(p.cost_usd)} · ${p.duration_ms ? (p.duration_ms / 1000).toFixed(1) + "s" : ""}</span></div>`;
  else {
    el.innerHTML = `<div class="k">${esc(e.type)}</div><div class="body dim"></div>`;
    $(".body", el).textContent = JSON.stringify(p).slice(0, 300);
  }
  return el;
}

/* ---------- clearing finished cards ---------- */

// A board that has run for a month is mostly history. Clearing lives on the
// column it clears, so which cards go is the thing you clicked rather than a
// choice made in a dialog.
async function clearColumn(status, count) {
  if (!confirm(`Clear ${count} ${status} card${count === 1 ? "" : "s"}?\n\n` +
    "The work itself is untouched — this removes the record from the board.")) return;
  try {
    const r = await api("/tasks/clear", { method: "POST", body: { statuses: [status] } });
    toast(`Cleared ${r.cleared} ${status} card${r.cleared === 1 ? "" : "s"}`);
    await refreshTasks();
  } catch (e) { toast(e.message, true); }
}

// deleteCard is the ✕ on a card. Finished work goes without ceremony — it is a
// record, not a thing in flight. Anything still live asks first, because
// deleting it stops a running agent.
async function deleteCard(t) {
  const live = !FINISHED_COLUMNS.includes(t.status) && t.status !== "cancelled";
  if (live && !confirm(`Delete "${t.title}"?\n\n` +
    `It is ${t.status} — deleting it stops the agent and discards the attempt.`)) return;
  try {
    await api(`/tasks/${t.id}`, { method: "DELETE" });
    await refreshTasks();
  } catch (e) { toast(e.message, true); }
}

/* ---------- routines: the job you keep asking for ---------- */

function renderRoutines(sheet) {
  sheet.innerHTML = `
    <div class="sheet-grip"><i></i></div>
    <div class="sheet-head"><h2>Routines</h2><button class="x">✕</button></div>
    <div class="sub" style="color:var(--ink-dim);font-size:12.5px">
      A job you keep asking for, saved. One button runs it across every project
      you picked; give it a schedule and it runs itself.
    </div>
    <div id="rt-active"></div><div id="rt-list"></div>
    <details id="rt-form" style="margin-top:14px">
      <summary id="rt-legend" style="cursor:pointer;padding:8px 0">+ New routine</summary>
      <label class="f">Name</label>
      <input class="f" id="rt-name" placeholder="PR sweep">
      <label class="f">Projects</label>
      <select class="f" id="rt-projects" multiple size="6">${state.projects.map((p) =>
        `<option value="${p.id}">${esc(p.name)}</option>`).join("")}</select>
      <div class="subhint">One task per project, every time it runs.</div>
      <label class="f">What should the agent do?</label>
      <textarea class="f" id="rt-prompt" rows="5" placeholder="Go through every open pull request…"></textarea>
      <label class="f">Schedule</label>
      <input class="f" id="rt-schedule" placeholder="leave empty to run only when you press it">
      <div class="subhint">every 6h &middot; hourly &middot; daily at 09:00 &middot; weekly on mon at 08:30</div>
      <label class="f">Agent</label>
      <select class="f" id="rt-agent"></select>
      <label class="f">Model</label>
      <input class="f" id="rt-model" list="lec-models" placeholder="the project's default" autocomplete="off">
      <datalist id="lec-models"></datalist>
      <label class="f">Permission mode</label>
      <select class="f" id="rt-perm">
        <option value="acceptEdits">acceptEdits</option>
        <option value="bypassPermissions">bypassPermissions</option>
        <option value="plan">plan</option>
        <option value="default">default</option>
      </select>
      <div class="btnrow" style="margin-top:14px">
        <button class="b ok grow" id="rt-save">Save routine</button>
        <button class="b" id="rt-cancel" hidden>Cancel</button>
      </div>
    </details>`;
  $(".x", sheet).onclick = closeSheet;

  // the agent set is the operator's, and the model list is whatever that agent
  // actually reports — same source as the session sheet
  const agentBox = $("#rt-agent", sheet);
  let routineAgentSpecs = [];
  const syncModels = () => {
    const list = (state.models || {})[agentBox.value] || [];
    const spec = routineAgentSpecs.find((a) => a.name === agentBox.value);
    $("#lec-models", sheet).innerHTML = list.map((m) => `<option>${esc(m)}</option>`).join("");
    $("#rt-model", sheet).disabled = !(state.models || {})[agentBox.value] && !spec?.model_flag;
  };
  api("/agents").then((specs) => {
    // Scheduled runs need an explicit one-shot adapter. Keep interactive-only
    // custom CLIs available to sessions while excluding them here.
    routineAgentSpecs = specs.filter((a) => a.builtin || a.task);
    agentBox.innerHTML = `<option value="">the project's default</option>` +
      routineAgentSpecs.map((a) => `<option value="${esc(a.name)}">${esc(a.name)}${a.builtin ? "" : " (custom)"}</option>`).join("");
    agentBox.onchange = syncModels;
    syncModels();
  }).catch(() => { agentBox.innerHTML = '<option value="">default</option>'; });

  const draw = async () => {
    const activeBox = $("#rt-active", sheet);
    try {
      const tasks = await api("/tasks");
      const runs = tasks.filter((t) => t.created_by?.startsWith("routine:") && (["queued","running","review"].includes(t.status) || t.takeover));
      activeBox.innerHTML = runs.length ? '<h3>Started routine runs</h3><p class="subhint">Open a run to take it over as an interactive session.</p>' : '';
      for (const t of runs) {
        const b = document.createElement("button"); b.className = "b";
        b.textContent = `${t.title} · ${t.project_name} · ${t.takeover?.status === "ready" ? "interactive" : t.status}`;
        b.onclick = () => openTaskSheet(t.id); activeBox.appendChild(b);
      }
    } catch (e) { activeBox.textContent = e.message; }
    const box = $("#rt-list", sheet);
    let rows = [];
    try { rows = await api("/routines"); } catch { }
    if (!rows.length) {
      box.innerHTML = `<div class="hint">No routines yet.<br><br>
        If you have typed the same request at an agent twice, it belongs here.</div>`;
      return;
    }
    box.innerHTML = "";
    for (const r of rows) {
      const el = document.createElement("div");
      el.className = "rowcard";
      const names = r.project_ids
        .map((id) => (state.projects.find((p) => p.id === id) || {}).name)
        .filter(Boolean);
      const how = [r.agent || "project default", r.model].filter(Boolean).join(" · ");
      const when = r.schedule
        ? `${esc(r.schedule)}${r.next_run_at ? " · next " + fmtWhen(r.next_run_at) : ""}`
        : "manual only";
      el.innerHTML = `
        <h3></h3>
        <div class="sub">${esc(names.join(", ") || "no projects")}</div>
        <div class="sub" style="margin-top:4px">${esc(how)}</div>
        <div class="sub" style="margin-top:4px">${when}${
          r.last_run_at ? " · last ran " + fmtDuration(Date.now() / 1000 - r.last_run_at) + " ago" : ""}</div>
        <div class="btnrow"></div>`;
      $("h3", el).textContent = r.name + (r.enabled ? "" : " (off)");
      const row = $(".btnrow", el);
      const act = (label, cls, fn) => {
        const b = document.createElement("button");
        b.className = `b ${cls}`; b.textContent = label; b.onclick = fn;
        row.appendChild(b);
      };
      act("▶ Run now", "ok grow", async () => {
        try {
          const out = await api(`/routines/${r.id}/run`, { method: "POST" });
          toast(`Started ${out.tasks.length} task${out.tasks.length === 1 ? "" : "s"}` +
            (out.failed.length ? ` · ${out.failed.length} could not run` : ""));
          if (out.failed.length) console.warn("routine failures", out.failed);
          await refreshTasks();
          await draw();
        } catch (e) { toast(e.message, true); }
      });
      if (r.schedule) {
        act(r.enabled ? "Pause" : "Resume", "", async () => {
          try {
            await api(`/routines/${r.id}`, { method: "PATCH", body: { enabled: !r.enabled } });
            draw();
          } catch (e) { toast(e.message, true); }
        });
      }
      act("Edit", "", () => loadForEdit(r));
      act("Delete", "no", async () => {
        if (!confirm(`Delete the routine "${r.name}"?\n\n` +
          "The tasks it already created stay on the board.")) return;
        try { await api(`/routines/${r.id}`, { method: "DELETE" }); draw(); }
        catch (e) { toast(e.message, true); }
      });
      box.appendChild(el);
    }
  };
  draw();

  // editing reuses the form rather than a second one: a routine is small enough
  // that "the fields it has" is the whole editor, and two forms drift apart
  let editing = null;
  function loadForEdit(r) {
    editing = r.id;
    $("#rt-form", sheet).open = true;
    $("#rt-name", sheet).value = r.name;
    $("#rt-prompt", sheet).value = r.prompt;
    $("#rt-schedule", sheet).value = r.schedule;
    $("#rt-perm", sheet).value = r.permission_mode || "acceptEdits";
    agentBox.value = r.agent || "";
    syncModels();
    $("#rt-model", sheet).value = r.model || "";
    for (const opt of $("#rt-projects", sheet).options) {
      opt.selected = r.project_ids.includes(+opt.value);
    }
    $("#rt-legend", sheet).textContent = `Editing "${r.name}"`;
    $("#rt-save", sheet).textContent = "Save changes";
    $("#rt-cancel", sheet).hidden = false;
    $("#rt-form", sheet).scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
  function resetForm() {
    editing = null;
    for (const id of ["rt-name", "rt-prompt", "rt-schedule", "rt-model"]) $("#" + id, sheet).value = "";
    agentBox.value = "";
    syncModels();
    for (const opt of $("#rt-projects", sheet).options) opt.selected = false;
    $("#rt-legend", sheet).textContent = "+ New routine";
    $("#rt-save", sheet).textContent = "Save routine";
    $("#rt-cancel", sheet).hidden = true;
  }
  $("#rt-cancel", sheet).onclick = resetForm;

  $("#rt-save", sheet).onclick = async () => {
    const picked = [...$("#rt-projects", sheet).selectedOptions].map((o) => +o.value);
    if (!picked.length) return toast("Pick at least one project", true);
    const body = {
      name: $("#rt-name", sheet).value.trim(),
      prompt: $("#rt-prompt", sheet).value.trim(),
      project_ids: picked,
      schedule: $("#rt-schedule", sheet).value.trim(),
      permission_mode: $("#rt-perm", sheet).value,
      agent: agentBox.value,
      model: $("#rt-model", sheet).disabled ? "" : $("#rt-model", sheet).value.trim(),
    };
    try {
      if (editing) await api(`/routines/${editing}`, { method: "PATCH", body });
      else await api("/routines", { method: "POST", body });
      toast(editing ? "Routine updated" : "Routine saved");
      resetForm();
      draw();
    } catch (e) { toast(e.message, true); }
  };
}

// fmtWhen renders an epoch as a short local time, for "next run".
function fmtWhen(epoch) {
  const d = new Date(epoch * 1000);
  const today = new Date().toDateString() === d.toDateString();
  return today ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
}

/* ---------- projects: finding and removing the dead ones ---------- */

// Eighty-one projects is a wall of cards. What is actually needed is finding
// which are no longer worked on, so this leads with staleness and attachment
// counts and keeps deletion behind an explicit, itemised confirmation.
function projectsCard() {
  const el = document.createElement("div");
  el.className = "rowcard";
  el.innerHTML = `
    <h3>Projects <span class="chip" id="pj-count">${state.projects.length}</span></h3>
    <div class="sub">Sorted by how long since anything happened. Tap one to edit it.</div>
    <input class="f" id="pj-search" placeholder="filter by name or path" autocomplete="off">
    <div class="btnrow" style="margin:8px 0">
      <button class="b" id="pj-select">Select…</button>
      <button class="b no" id="pj-del" hidden></button>
      <button class="b" id="pj-cancel" hidden>Cancel</button>
    </div>
    <div id="pj-list" class="pjlist"></div>`;

  let selecting = false;
  const chosen = new Set();
  const usage = {};

  const draw = () => {
    const q = ($("#pj-search", el).value || "").toLowerCase();
    const rows = state.projects
      .filter((p) => !q || p.name.toLowerCase().includes(q) ||
        (p.repo_path || "").toLowerCase().includes(q))
      .map((p) => ({ p, u: usage[p.id] || {} }))
      .sort((a, b) => (a.u.last_active_at || 0) - (b.u.last_active_at || 0));

    const box = $("#pj-list", el);
    box.innerHTML = "";
    for (const { p, u } of rows) {
      const row = document.createElement("div");
      row.className = "pjrow";
      const bits = [];
      if (u.tasks) bits.push(`${u.tasks} task${u.tasks === 1 ? "" : "s"}` +
        (u.open_tasks ? ` (${u.open_tasks} open)` : ""));
      if (u.sessions) bits.push(`${u.sessions} session${u.sessions === 1 ? "" : "s"}`);
      const quiet = u.last_active_at
        ? "quiet " + fmtDuration(Date.now() / 1000 - u.last_active_at) : "";
      row.innerHTML = `
        ${selecting ? `<input type="checkbox" class="pjbox">` : ""}
        <div class="pjmain">
          <div class="pjname"></div>
          <div class="pjmeta">${esc(quiet)}${bits.length ? " · " + esc(bits.join(" · ")) : ""}</div>
          <div class="pjpath"></div>
        </div>
        ${u.open_tasks ? '<span class="chip warn">active</span>' : ""}`;
      $(".pjname", row).textContent = p.name;
      $(".pjpath", row).textContent = p.repo_path || "";
      if (!selecting) {
        const sh = document.createElement("button");
        sh.className = "b";
        sh.textContent = "⌨";
        sh.title = `Open a shell in ${p.repo_path || "this project"}`;
        sh.style.cssText = "flex:0 0 auto;padding:5px 9px";
        sh.onclick = (ev) => { ev.stopPropagation(); openProjectShell(p); };
        row.appendChild(sh);
      }
      if (selecting) {
        const cb = $(".pjbox", row);
        cb.checked = chosen.has(p.id);
        cb.onchange = () => { cb.checked ? chosen.add(p.id) : chosen.delete(p.id); syncBar(); };
        row.onclick = (ev) => { if (ev.target !== cb) { cb.checked = !cb.checked; cb.onchange(); } };
      } else {
        row.onclick = () => openProjectEditor(p);
      }
      box.appendChild(row);
    }
    $("#pj-count", el).textContent = rows.length === state.projects.length
      ? state.projects.length : `${rows.length}/${state.projects.length}`;
  };

  const syncBar = () => {
    const del = $("#pj-del", el);
    del.hidden = !selecting || chosen.size === 0;
    del.textContent = `Delete ${chosen.size}`;
  };

  $("#pj-search", el).oninput = draw;
  $("#pj-select", el).onclick = () => {
    selecting = !selecting;
    chosen.clear();
    $("#pj-select", el).textContent = selecting ? "Selecting" : "Select…";
    $("#pj-cancel", el).hidden = !selecting;
    syncBar(); draw();
  };
  $("#pj-cancel", el).onclick = () => {
    selecting = false; chosen.clear();
    $("#pj-select", el).textContent = "Select…";
    $("#pj-cancel", el).hidden = true;
    syncBar(); draw();
  };
  $("#pj-del", el).onclick = () => deleteProjects([...chosen], usage, () => {
    selecting = false; chosen.clear();
    $("#pj-select", el).textContent = "Select…";
    $("#pj-cancel", el).hidden = true;
    syncBar();
  });

  api("/projects/usage").then((rows) => {
    for (const u of rows) usage[u.project_id] = u;
    draw();
  }).catch(draw);
  draw();
  return el;
}

// Deletion is itemised before it happens: the confirmation names what is being
// removed and what history goes with it, because "delete 14 projects" is not
// something anyone can check after the fact.
async function deleteProjects(ids, usage, done) {
  if (!ids.length) return;
  const named = ids.map((id) => state.projects.find((p) => p.id === id)).filter(Boolean);
  const withHistory = named.filter((p) => (usage[p.id] || {}).tasks);
  const active = named.filter((p) => (usage[p.id] || {}).open_tasks);

  let msg = `Delete ${named.length} project${named.length === 1 ? "" : "s"}?\n\n` +
    named.map((p) => "  • " + p.name).join("\n") +
    "\n\nThe project record goes. Your code on disk is untouched.";
  if (withHistory.length) {
    const total = withHistory.reduce((n, p) => n + usage[p.id].tasks, 0);
    msg += `\n\n${withHistory.length} of them carry ${total} task${total === 1 ? "" : "s"}` +
      " — that history is deleted too and cannot be recovered.";
  }
  if (active.length) {
    msg += `\n\n⚠ ${active.length} still ${active.length === 1 ? "has" : "have"}` +
      " work in flight: " + active.map((p) => p.name).join(", ");
  }
  if (!confirm(msg)) return;
  if (active.length && !confirm(
      `Really delete ${active.length} project${active.length === 1 ? "" : "s"} with ` +
      "work still running? Those tasks are lost.")) return;

  let ok = 0;
  const failed = [];
  for (const p of named) {
    try {
      await api(`/projects/${p.id}?cascade=true`, { method: "DELETE" });
      ok++;
    } catch (e) { failed.push(`${p.name}: ${e.message}`); }
  }
  done?.();
  await refreshMeta();
  await refreshTasks().catch(() => {});
  renderTargets();
  if (failed.length) toast(`Deleted ${ok}. Failed: ${failed.join("; ")}`, true);
  else toast(`Deleted ${ok} project${ok === 1 ? "" : "s"}`);
}

// A way into the machine where the code actually lives — read a file, fix one
// line, check what a command prints — without asking an agent to do it.
async function openProjectShell(p) {
  try {
    const r = await api(`/projects/${p.id}/terminal`, { method: "POST" });
    openTerminal(r.url, p.name + " · Shell");
  } catch (e) { toast(e.message, true); }
}

// the single-project editor, reached by tapping a row
function openProjectEditor(p) {
  const card = projectCard(p);
  const shell = document.createElement("button");
  shell.className = "b";
  shell.textContent = "⌨ Shell here";
  shell.onclick = () => openProjectShell(p);
  ($(".btnrow", card) || card).appendChild(shell);

  const del = document.createElement("button");
  del.className = "b no";
  del.textContent = "Delete project";
  del.onclick = () => api("/projects/usage")
    .then((rows) => {
      const usage = {};
      for (const u of rows) usage[u.project_id] = u;
      return deleteProjects([p.id], usage);
    })
    .catch(() => deleteProjects([p.id], {}));
  ($(".btnrow", card) || card).appendChild(del);
  state.sheet = { kind: "project", node: card };
  renderSheet();
}

async function openTakenOverSession(t) {
  try {
    const session = await api(`/sessions/${t.takeover.session_id}`);
    closeSheet(); switchTab("sessions"); await refreshSessions();
    openConversation({kind:"session",id:session.id,name:session.name,api,attachMic,onClose:refreshSessions});
  } catch (e) { toast(e.message, true); }
}
async function takeOverTask(t) {
  try {
    await api(`/tasks/${t.id}/takeover`, {method:"POST"});
    toast("Taking over this run in its existing worktree.");
    await openTaskSheet(t.id);
  } catch (e) { toast(e.message, true); }
}

function renderSheet() {
  if (!state.sheet) return;
  const previous = document.activeElement;
  renderSheetContent();
  sheetFocus.open(previous);
}
function renderSheetContent() {
  if (!state.sheet) return;
  const sheet = $("#sheet");
  sheet.hidden = false; $("#sheet-backdrop").hidden = false;
  if (state.sheet.kind === "new") return renderNewTask(sheet);
  if (state.sheet.kind === "session-group") return renderSessionGroup(sheet);
  if (state.sheet.kind === "new-session") return renderNewSession(sheet);
  if (state.sheet.kind === "discover") return renderDiscover(sheet);
  if (state.sheet.kind === "routines") return renderRoutines(sheet);
  if (state.sheet.kind === "handoff") return renderHandoff(sheet);
  if (state.sheet.kind === "project") {
    sheet.innerHTML = `<div class="sheet-grip"><i></i></div>
      <div class="sheet-head"><h2>Project</h2><button class="x">✕</button></div>`;
    sheet.appendChild(state.sheet.node);
    $(".x", sheet).onclick = closeSheet;
    return;
  }
  const t = state.sheetTask;
  if (!t) return;
  sheet.innerHTML = `
    <div class="sheet-grip"><i></i></div>
    <div class="sheet-head"><h2></h2><button class="x">✕</button></div>
    <div class="statline s-${t.status}">
      <span class="statpill">${t.status}</span>
      <span>${esc(t.project_name)} → ${esc(t.target_name)}</span>
      ${t.attempt ? `<span>attempt #${t.attempt.n}${t.attempt.branch ? " · <code>" + esc(t.attempt.branch) + "</code>" : ""}</span>` : ""}
    </div>
    <div class="btnrow" id="attempt-chips"></div>
    <div class="btnrow" id="actions"></div>
    <div id="sheet-approvals"></div>
    <div id="sheet-body"></div>`;
  if ((t.attempts || []).length > 1) {
    const chips = $("#attempt-chips", sheet);
    for (const a of t.attempts) {
      const b = document.createElement("button");
      b.className = "b" + ((state.attemptView ?? t.attempt.n) === a.n ? " ok" : "");
      b.textContent = `⑂ A${a.n}${a.model ? " · " + a.model : ""} · ${a.status}` +
        (a.cost_usd != null ? ` · ${fmtCost(a.cost_usd)}` : "");
      b.onclick = async () => {
        state.attemptView = a.n; state.taskEvents = []; state.taskDiff = null;
        await loadTaskSheet(t.id);
      };
      chips.appendChild(b);
    }
  }
  $("h2", sheet).textContent = t.title;
  $(".x", sheet).onclick = closeSheet;

  const actions = $("#actions", sheet);
  const act = (label, cls, fn) => {
    const b = document.createElement("button");
    b.className = `b ${cls}`; b.textContent = label; b.onclick = fn;
    actions.appendChild(b);
  };
  act(t.takeover?.status === "ready" ? "Open session" : "Chat", "ok grow", () => openTaskChat(t));
  if (t.takeover) {
    const hint = document.createElement("p"); hint.className = "subhint";
    hint.textContent = t.takeover.status === "ready" ? "Continued in an interactive session. Use Open session to chat or attach to its terminal." :
      t.takeover.status === "failed" ? t.takeover.error : "Taking over this run… Its worktree is preserved.";
    actions.appendChild(hint);
    if (t.takeover.status === "failed") act("Retry takeover", "warn", () => takeOverTask(t));
  } else if (t.attempt?.worktree_path && ["running","review","failed","cancelled","done"].includes(t.status) && t.target_kind !== "sandbox") {
    act("Take over as session", "ok", () => takeOverTask(t));
  }
  if (!t.takeover && ["backlog", "failed", "cancelled"].includes(t.status))
    act(t.status === "backlog" ? "▶ Dispatch" : "↻ Retry", "ok grow", () => doAction(`/tasks/${t.id}/dispatch`));
  if (!t.takeover && ["queued", "running"].includes(t.status))
    act("■ Cancel", "no", () => doAction(`/tasks/${t.id}/cancel`));
  if (!t.takeover && t.status === "review") {
    act("✓ Mark done", "ok grow", () => doAction(`/tasks/${t.id}/complete`));
    act("↺ Request changes", "warn grow", async () => {
      const fb = prompt("What should change?");
      if (fb) doAction(`/tasks/${t.id}/followup`, { feedback: fb });
    });
  }
  if (["review", "done"].includes(t.status)) {
    act(state.diffOpen ? "Timeline" : "± Diff", "", toggleDiff);
    act("▶ Replay", "", () => {
      if (state.diffOpen) return toast("switch to timeline first", true);
      const rows = [...document.querySelectorAll("#sheet .tl .ev")];
      rows.forEach((r) => (r.style.display = "none"));
      let i = 0;
      const iv = setInterval(() => {
        if (i >= rows.length) return clearInterval(iv);
        rows[i].style.display = "";
        rows[i].scrollIntoView({ block: "nearest", behavior: "smooth" });
        i++;
      }, 260);
    });
    act("⎇ Commit", "", async () => {
      const message = prompt("Commit message:", t.title);
      if (message == null) return;
      const push = confirm("Also push the branch to origin?");
      const pr = push && confirm("…and open a PR (needs gh on the target)?");
      try {
        const r = await api(`/tasks/${t.id}/commit`, { method: "POST",
          body: { message, push, pr } });
        const prStep = r.steps.find((s) => s.step === "pr");
        toast("Committed" + (push ? " + pushed" : "") +
              (prStep?.url ? ` · PR: ${prStep.url}` : ""));
      } catch (e) { toast(e.message, true); }
    });
  }
  if (!t.takeover && ["done", "failed", "cancelled"].includes(t.status) && t.attempt?.worktree_path)
    act("Clean worktree", "", async () => {
      if (!confirm("Remove the worktree(s)? Uncommitted changes are lost.")) return;
      try { await api(`/tasks/${t.id}/cleanup`, { method: "POST" });
            toast("Worktrees removed"); loadTaskSheet(t.id); }
      catch (e) { toast(e.message, true); }
    });
  if (t.status === "running" && t.attempt?.tmux_session) {
    act("⌨ Terminal", "", async () => {
      try {
        const r = await api(`/tasks/${t.id}/terminal`, { method: "POST" });
        openTerminal(r.url, t.title);
      } catch (e) {
        const sshPrefix = t.target_kind === "ssh"
          ? `ssh -t ${t.target_user}@${t.target_host} ` : "";
        const cmd = `${sshPrefix}tmux attach -t ${t.attempt.tmux_session}`;
        toast(e.message + " — attach manually", true);
        prompt("Attach with:", cmd);
      }
    });
  }
  act("Delete", "no", async () => {
    if (!confirm(`Delete "${t.title}"? Removes its attempts, events, diffs and worktrees. Cannot be undone.`)) return;
    try { await api(`/tasks/${t.id}`, { method: "DELETE" }); toast("Task deleted"); closeSheet(); refreshTasks(); }
    catch (e) { toast(e.message, true); }
  });

  const apDiv = $("#sheet-approvals", sheet);
  state.approvals.filter((a) => a.task_id === t.id).forEach((a) => apDiv.appendChild(approvalCard(a)));

  const body = $("#sheet-body", sheet);
  if (state.diffOpen && state.taskDiff) renderDiff(body);
  else {
    const tl = document.createElement("div");
    tl.className = "tl";
    if (!state.taskEvents.length && t.prompt) {
      const pr = document.createElement("div");
      pr.className = "ev";
      pr.innerHTML = '<div class="k">prompt</div><div class="body dim"></div>';
      $(".body", pr).textContent = t.prompt;
      tl.appendChild(pr);
    }
    state.taskEvents.forEach((e) => tl.appendChild(evRow(e)));
    body.appendChild(tl);
  }
}

async function toggleDiff() {
  if (!state.diffOpen) {
    const q = state.attemptView ? `?attempt_n=${state.attemptView}` : "";
    try { state.taskDiff = await api(`/tasks/${state.sheet.id}/diff${q}`); }
    catch (e) { return toast(e.message, true); }
  }
  state.diffOpen = !state.diffOpen;
  renderSheet();
}
function renderDiff(body) {
  const d = state.taskDiff;
  const stats = d.stats || [];
  const head = document.createElement("div");
  head.className = "diffhead";
  head.innerHTML = `<span>attempt #${d.attempt_n} · ${stats.length} file(s) changed</span>`;
  // Reviewing on a phone means prose and long lines run off the right edge with
  // no way back; wrapping is the difference between readable and unreadable.
  const wrapBtn = document.createElement("button");
  wrapBtn.className = "wrapbtn";
  const paintWrap = () => {
    wrapBtn.textContent = state.diffWrap ? "⏎ wrap: on" : "⏎ wrap: off";
    wrapBtn.classList.toggle("on", !!state.diffWrap);
    body.classList.toggle("wrapped", !!state.diffWrap);
  };
  wrapBtn.onclick = () => {
    state.diffWrap = !state.diffWrap;
    localStorage.setItem("lec-diffwrap", state.diffWrap ? "1" : "");
    paintWrap();
  };
  head.appendChild(wrapBtn);
  body.appendChild(head);
  paintWrap();
  for (const f of d.files || []) {
    const st = stats.find((s) => s.path === f.path) || {};
    const det = document.createElement("details");
    det.className = "dfile"; det.open = (d.files.length <= 3);
    det.innerHTML = `<summary><span>${esc(f.path)}</span>
      <span class="pm"><b class="a">+${st.additions ?? "?"}</b> <b class="d">−${st.deletions ?? "?"}</b></span></summary>
      <div class="dcode"></div>`;
    const code = $(".dcode", det);
    for (const line of f.patch.split("\n")) {
      const div = document.createElement("div");
      div.textContent = line || " ";
      if (line.startsWith("+") && !line.startsWith("+++")) div.className = "dl-add";
      else if (line.startsWith("-") && !line.startsWith("---")) div.className = "dl-del";
      else if (line.startsWith("@@")) div.className = "dl-hunk";
      else if (line.startsWith("diff ") || line.startsWith("index ")) div.className = "dl-meta";
      code.appendChild(div);
    }
    body.appendChild(det);
  }
}

/* ---------- new task sheet ---------- */
function renderNewTask(sheet) {
  sheet.innerHTML = `
    <div class="sheet-grip"><i></i></div>
    <div class="sheet-head"><h2>New task</h2><button class="x">✕</button></div>
    <label class="f">Template</label>
    <select class="f" id="f-template"><option value="">— none —</option></select>
    <label class="f">Project</label>
    <select class="f" id="f-project">${state.projects.map((p) =>
      `<option value="${p.id}">${esc(p.name)} — ${esc(p.target_name)}</option>`).join("")}</select>
    <div class="subhint" id="f-cap-hint"></div>
    <label class="f">Title</label>
    <input class="f" id="f-title" placeholder="Add /health endpoint">
    <label class="f">Prompt — what should the agent do? <button id="f-mic" style="float:right;background:none;border:1px solid var(--line-2);border-radius:7px;cursor:pointer;color:var(--ink-dim)">🎤</button></label>
    <textarea class="f" id="f-prompt" placeholder="Describe intent. Be specific about files, behavior, and how to verify."></textarea>
    <label class="f">Permissions</label>
    <select class="f" id="f-perm">
      <option value="default">Gated — ask me before running anything (push)</option>
      <option value="acceptEdits" selected>Accept edits — file changes auto-approved</option>
      <option value="plan">Plan only — no changes</option>
      <option value="bypassPermissions">Bypass — sandboxed targets only</option>
    </select>
    <label class="f">Agent</label>
    <div class="seg f" id="f-agent" data-value="claude" aria-label="Task runner"></div>
    <div class="subhint" id="f-agent-hint"></div>
    <label class="f">Model</label>
    <input class="f" id="f-model" list="lec-models" placeholder="default" autocomplete="off">
    <datalist id="lec-models">
      <option>fable</option><option>opus</option><option>sonnet</option><option>haiku</option>
    </datalist>
    <div id="f-ab-row">
      <label class="f">A/B second attempt (parallel, compare diffs)</label>
      <select class="f" id="f-modelb">
        <option value="">off</option><option>fable</option><option>opus</option><option>sonnet</option><option>haiku</option>
      </select>
    </div>
    <label class="f">Priority</label>
    <select class="f" id="f-prio">
      <option value="1">low</option><option value="2" selected>normal</option><option value="3">high</option>
    </select>
    <div class="btnrow" style="margin-top:20px">
      <button class="b grow" id="f-save">Save to backlog</button>
      <button class="b grow" id="f-go">Dispatch to board</button>
      <button class="b ok grow" id="f-chat">Dispatch &amp; chat</button>
    </div>`;
  $(".x", sheet).onclick = closeSheet;
  attachMic($("#f-mic"), $("#f-prompt"));

  // Agent toggle. Only claude supports gated approvals and the claude model
  // aliases, so switching agents has to reshape the rest of the form — and say
  // so, rather than letting a dispatch fail later for reasons that look random.
  const agentBox = $("#f-agent");
  const allAgentSpecs = state.agents.length ? state.agents : [
    {name:"claude", builtin:true}, {name:"codex", builtin:true}, {name:"gemini", builtin:true}];
  // Built-ins retain their existing task adapters. A custom runner enters this
  // selector only after it declares a separate one-shot `task` definition.
  const taskAgentSpecs = allAgentSpecs.filter(a => a.builtin || a.task);
  const agentLabel = a => a.builtin
    ? ({claude: "Claude Code", codex: "Codex", gemini: "Gemini"}[a.name] || a.name)
    : `${a.name} (custom)`;
  const paintTaskAgents = specs => {
    agentBox.replaceChildren(...specs.map(a => {
      const button = document.createElement("button"); button.type = "button";
      button.dataset.agent = a.name; button.textContent = agentLabel(a);
      button.onclick = () => { agentBox.dataset.value = a.name; syncAgent(); };
      return button;
    }));
  };
  paintTaskAgents(taskAgentSpecs);
  const syncAgent = () => {
    const agent = agentBox.dataset.value;
    $$("button", agentBox).forEach((b) => b.classList.toggle("on", b.dataset.agent === agent));
    const claude = agent === "claude";
    const gated = $("#f-perm").querySelector('option[value="default"]');
    gated.disabled = !claude;
    gated.textContent = claude
      ? "Gated — ask me before running anything (push)"
      : "Gated — Claude Code only";
    if (!claude && $("#f-perm").value === "default") $("#f-perm").value = "acceptEdits";
    $("#f-ab-row").style.display = claude ? "" : "none";
    if (!claude) $("#f-modelb").value = "";
    $("#lec-models").innerHTML = ((state.models || {})[agentBox.dataset.value] || [])
      .map((m) => `<option>${esc(m)}</option>`).join("");
    // probe truth beats optimism: say when the target has no such binary
    const proj = state.projects.find((p) => p.id === +$("#f-project").value);
    const tgt = state.targets.find((t) => t.id === proj?.target_id);
    let info = {};
    try { info = JSON.parse(tgt?.info_json || "{}"); } catch {}
    const missing = tgt && info[agent] === null;
    $("#f-agent-hint").textContent = missing
      ? `⚠ ${agent} not detected on ${tgt.name} — probe the target, or set `
        + `LECTERN_${agent.toUpperCase()}_BIN if it lives outside the service PATH`
      : "";
  };
  if (!taskAgentSpecs.some(a => a.name === agentBox.dataset.value)) agentBox.dataset.value = taskAgentSpecs[0]?.name || "";
  // what the agent will actually be able to do, before you spend a dispatch on it
  const syncCapability = () => {
    const el = $("#f-cap-hint");
    const id = +$("#f-project").value;
    if (!el || !id) return;
    el.textContent = "checking capability…";
    api(`/projects/${id}/capability`).then((c) => {
      if (+$("#f-project").value !== id) return;   // a later pick already won
      const have = [c.mcp_servers.length ? `MCP ${c.mcp_servers.join(", ")}` : "no MCP",
                    c.memory_dir ? "shared memory" : "no memory"];
      el.innerHTML = `<b>${esc(c.profile)}</b> · ${esc(have.join(" · "))}` +
        (c.profile === "restricted"
          ? '<div class="cap-note">⚠ restricted: tools it was not granted are '
            + 'denied with no prompt. Set parity on the Targets tab.</div>' : "");
    }).catch(() => { el.textContent = ""; });
  };
  $("#f-project").addEventListener("change", () => {
    const proj = state.projects.find((p) => p.id === +$("#f-project").value);
    agentBox.dataset.value = proj?.default_agent || "claude";
    if (proj?.default_permission_mode) $("#f-perm").value = proj.default_permission_mode;
    syncAgent();
    syncCapability();
  });
  syncCapability();
  const initialProj = state.projects.find((p) => p.id === +$("#f-project").value);
  agentBox.dataset.value = initialProj?.default_agent || "claude";
  if (initialProj?.default_permission_mode) $("#f-perm").value = initialProj.default_permission_mode;
  syncAgent();
  // State normally has the registry from boot. This retry keeps a task sheet
  // useful when it was opened before metadata finished loading.
  if (!state.agents.length) api("/agents").then(specs => { if (specs?.length) { state.agents = specs; const capable = specs.filter(a => a.builtin || a.task); paintTaskAgents(capable); if (!capable.some(a => a.name === agentBox.dataset.value)) agentBox.dataset.value = capable[0]?.name || ""; syncAgent(); } }).catch(() => {});
  api("/templates").then((tpls) => {
    const sel = $("#f-template");
    tpls.forEach((t, i) => {
      const o = document.createElement("option");
      o.value = i; o.textContent = t.name;
      sel.appendChild(o);
    });
    sel.onchange = () => {
      const t = tpls[+sel.value];
      if (!t) return;
      if (t.title) $("#f-title").value = t.title;
      if (t.prompt) $("#f-prompt").value = t.prompt;
      if (t.permission_mode) $("#f-perm").value = t.permission_mode;
      if (t.model !== undefined) $("#f-model").value = t.model;
    };
  }).catch(() => {});
  const collect = () => ({
    project_id: +$("#f-project").value,
    title: $("#f-title").value.trim(),
    prompt: $("#f-prompt").value.trim(),
    agent: $("#f-agent").dataset.value,
    permission_mode: $("#f-perm").value,
    model: $("#f-model").value,
    priority: +$("#f-prio").value,
  });
  const create = async (dispatch, chat = false) => {
    const data = collect();
    if (!data.title) return toast("Title required", true);
    const modelB = $("#f-modelb").value;
    // Fable 5 is the most capable — and highest-usage — model; confirm before
    // dispatching an agent on it so it is never an accidental quota burn.
    if (dispatch && (data.model === "fable" || modelB === "fable") &&
        !confirm("Dispatch on Fable 5? It's the most capable model and uses the "
                 + "most of your Claude Code plan. Continue?")) return;
    try {
      const t = await api("/tasks", { method: "POST", body: data });
      if (dispatch) await api(`/tasks/${t.id}/dispatch`, { method: "POST",
        body: modelB ? { model_b: modelB } : {} });
      closeSheet(); refreshTasks();
      toast(dispatch ? "Dispatched" : "Saved to backlog");
      if (chat) openTaskChat(t);
    } catch (e) { toast(e.message, true); }
  };
  $("#f-save").onclick = () => create(false);
  $("#f-go").onclick = () => create(true);
  $("#f-chat").onclick = () => create(true, true);
}

async function doAction(path, body = {}) {
  try {
    await api(path, { method: "POST", body });
    await loadTaskSheet(state.sheet.id);
    refreshTasks();
  } catch (e) { toast(e.message, true); }
}

/* ---------- push ---------- */
async function enablePush() {
  try {
    if (!("serviceWorker" in navigator)) throw new Error("no service worker support");
    const reg = await navigator.serviceWorker.ready;
    const perm = await Notification.requestPermission();
    if (perm !== "granted") throw new Error("notifications not granted");
    const keyResp = await fetch("/api/push/vapid").catch(() => null);
    let appServerKey;
    if (keyResp && keyResp.ok) appServerKey = (await keyResp.json()).key;
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      ...(appServerKey ? { applicationServerKey: urlB64(appServerKey) } : {}),
    });
    await api("/push/subscribe", { method: "POST", body: sub.toJSON() });
    toast("Push enabled on this device");
  } catch (e) { toast("Push: " + e.message, true); }
}
function urlB64(s) {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from([...raw].map((c) => c.charCodeAt(0)));
}

/* ---------- helpers / boot ---------- */
function esc(s) { const d = document.createElement("i"); d.textContent = s ?? ""; return d.innerHTML; }
function snippet(input) {
  if (!input) return "";
  const s = input.command || input.file_path || JSON.stringify(input);
  return String(s).slice(0, 120);
}
function safeParse(s) { try { return JSON.parse(s || "{}"); } catch { return {}; } }

/* ---------- hash routing ----------
   The address bar is an interface. Notification sinks send "/#task/12" and the
   phone opens the embedded board at "/#sessions"; before this, both landed on
   whatever tab happened to be default and the link may as well not have existed. */
const TABS = ["board", "sessions", "terminals", "deck", "approvals", "targets"];

function applyHash() {
  let raw;
  try { raw = decodeURIComponent(location.hash.replace(/^#/, "")); } catch { return false; }
  if (!raw) return false;
  const [kind, id, terminalID] = raw.split("/");
  if (kind === "terminals" && /^(session|attempt|project)$/.test(id) && /^[1-9]\d*$/.test(terminalID)) {
    terminalTabs.open(`/terminal/${id}/${terminalID}`);
    return true;
  }
  if (TABS.includes(kind)) { switchTab(kind, { fromHash: true }); return true; }
  if (kind === "task" && id) {
    switchTab("board", { fromHash: true });
    openTaskSheet(+id);
    return true;
  }
  if (kind === "session" && id) {
    switchTab("sessions", { fromHash: true });
    return true;
  }
  return false;
}
addEventListener("hashchange", applyHash);

function switchTab(tab, opts = {}) {
  state.tab = tab;
  if (!opts.fromHash) {
    try { localStorage.setItem('lec-last-view', tab); } catch {}
  }
  const terminal = tab === "terminals";
  $("#view").hidden = terminal;
  $("#fab").hidden = tab !== 'board';
  document.body.classList.toggle("terminals-open", terminal);
  if (terminal) terminalTabs.show(); else terminalTabs.hide();
  // replaceState, not a new entry: flipping tabs should not fill the back stack
  if (!opts.fromHash) history.replaceState(null, "", tab === "terminals" ? terminalTabs.hash() : "#" + tab);
  if (tab !== "deck") closeDeckStreams();
  $$(".tab").forEach((b) => b.classList.toggle("on", b.dataset.tab === tab));
  $('#nav-overflow').classList.toggle('on', ['deck','approvals','targets'].includes(tab));
  $('#nav-overflow').open = false;
  if (tab === "board") renderBoard();
  if (tab === "sessions") { renderSessions(); refreshSessions(); }
  if (tab === "deck") renderDeck();
  if (tab === "approvals") renderApprovals();
  if (tab === "targets") renderTargets();
}
$$(".tab").forEach((b) => (b.onclick = () => switchTab(b.dataset.tab)));
$$('[data-nav-target]').forEach(b => b.onclick = () => switchTab(b.dataset.navTarget));
const terminalTabs = new TerminalTabs($("#terminal-workspace"), {
  activate: () => switchTab("terminals"),
  browse: () => switchTab("sessions"),
  search: () => commandPalette.open(),
});
function openTerminal(url, label) {
  closeSheet();
  terminalTabs.open(url, label);
}
const commandPalette = new CommandPalette({
  button: $('#command-open'),
  error: message => toast(message, true),
  refresh: () => Promise.all([refreshSessions(), refreshTasks(), refreshMeta()]),
  items: () => {
    const command = (id, title, run, detail = '', keywords = '', category = 'Actions') => ({id, title, run, detail, keywords, category});
    const sheet = kind => { state.sheet = {kind}; renderSheet(); };
    const settings = section => { state.settingsSection = section; switchTab('targets'); };
    return [
      command('launch-profiles', 'Manage launch profiles', () => openLaunchProfiles({api}), 'Reusable agent, model and environment settings', 'profiles accounts configuration'),
      command('new-session', 'New session', () => sheet('new-session'), 'Start an interactive agent', 'create launch'),
      command('new-task', 'New task', () => sheet('new'), 'Plan or dispatch work', 'create'),
      command('saved-search', 'Search saved conversations', () => openNativeSearch({api, targets:state.targets,onFork:handleNativeFork}), 'Find text across local and SSH histories', 'history messages content native'),
      command('discover', 'Find running agents', () => sheet('discover'), 'Track existing tmux sessions', 'adopt restore untracked'),
      command('routines', 'Routines', () => sheet('routines'), 'Saved jobs and active runs', 'schedule takeover'),
      ...[['board','Task board'],['sessions','Sessions'],['terminals','Open terminals'],['deck','Deck'],['approvals','Approvals']].map(([tab,title]) => command(`nav-${tab}`,title,()=>switchTab(tab),'','navigate view','Navigate')),
      ...[['machines','Targets','ssh remote local machines'],['projects','Projects','repositories workspaces'],['notifications','Notifications','alerts push'],['about','Usage and about','settings version costs'],['agents','Agents','agent runners commands custom providers models']].map(([section,title,words]) => command(`settings-${section}`,title,()=>settings(section),'Settings',words,'Navigate')),
      ...state.sessions.map(s => command(`session-${s.id}`,s.name || `Session ${s.id}`,()=>attachSession(s),[s.status,s.agent,s.group_path,s.project_name,s.target_name,s.workdir].filter(Boolean).join(' · '),'attach terminal '+(s.workspace?.branch||''),'Sessions')),
      ...state.tasks.map(t => command(`task-${t.id}`,t.title || `Task ${t.id}`,()=>openTaskSheet(t.id),[t.status,t.project_name].filter(Boolean).join(' · '),`task ${t.id}`,'Tasks')),
      ...state.projects.map(p => command(`project-${p.id}`,`Edit project: ${p.name}`,()=>openProjectEditor(p),p.repo_path,'repository workspace configuration','Projects')),
    ];
  },
});
const fitTerminalWorkspace = () => {
  const top = $("#topbar").getBoundingClientRect().bottom;
  document.documentElement.style.setProperty("--terminal-top", `${top}px`);
};
new ResizeObserver(fitTerminalWorkspace).observe($("#topbar"));
window.addEventListener("resize", fitTerminalWorkspace);
fitTerminalWorkspace();
$("#fab").onclick = () => {
  state.sheet = { kind: state.tab === "sessions" ? "new-session" : "new" };
  renderSheet();
};
$("#sheet-backdrop").onclick = closeSheet;
addEventListener("keydown", (e) => { if (e.key === "Escape" && state.sheet && !document.querySelector("dialog[open]")) closeSheet(); });
// A phone that was locked or backgrounded drops SSE silently — resync on return
// to foreground (shares the reconnect resync path).
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") {
    refreshTasks(); refreshApprovals(); refreshSessions();
  }
});

if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js");

(async function boot() {
  connectSSE();
  await Promise.all([refreshMeta(), refreshTasks(), refreshApprovals(), refreshSessions()]);
  if (!applyHash()) {
    let tab = 'board';
    if (!location.hash) {
      try {
        const saved = localStorage.getItem('lec-last-view');
        if (TABS.includes(saved)) tab = saved;
      } catch {}
      // Terminal frames belong to this browser tab. A fresh browser tab should
      // open the session list rather than an empty terminal workspace.
      if (tab === 'terminals' && !terminalTabs.active) tab = 'sessions';
    }
    switchTab(tab, {fromHash: true});
  }
  setInterval(()=>pollWorkspaceSetups().catch(()=>{}),4000);
  setInterval(refreshTasks, 30000);   // safety net if SSE hiccups
  // sessions carry a live idle clock, so the list is re-rendered on a cadence
  // even when nothing changed — "quiet for 40 minutes" is the number you act on
  setInterval(() => { if (state.tab === "sessions") renderSessions(); }, 5000);
})();

function renderSessionGroup(sheet) {
 const session=state.sheet.session, editorState=state.sheet;
 sheet.innerHTML=`<div class="sheet-head"><h2>Move to group</h2><button class="x" aria-label="Close group editor">✕</button></div><p id="sg-name"></p><label class="f" for="sg-path">Group path</label><input class="f" id="sg-path" list="sg-existing" placeholder="Work/Client"><datalist id="sg-existing"></datalist><p class="subhint">Use / for nested groups. Leave blank to ungroup.</p><p id="sg-error" role="status"></p><button class="b ok" id="sg-save">Save group</button>`;
 $('#sg-name',sheet).textContent=session.name;$('#sg-path',sheet).value=session.group_path||'';
 for(const group of [...new Set([...state.sessions,...state.endedSessions].map(s=>s.group_path).filter(Boolean))].sort()){const option=document.createElement('option');option.value=group;$('#sg-existing',sheet).appendChild(option);}
 $('.x',sheet).onclick=closeSheet;
 $('#sg-save',sheet).onclick=async()=>{
  const button=$('#sg-save',sheet),error=$('#sg-error',sheet);button.disabled=true;
  try{await api(`/sessions/${session.id}`,{method:'PATCH',body:{group_path:$('#sg-path',sheet).value}});if(state.sheet===editorState)closeSheet();await refreshSessions();toast('Group saved');}
  catch(e){error.textContent=e.message;button.disabled=false;}
 };
 $('#sg-path',sheet).focus();
}
