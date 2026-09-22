// Terminal frames live outside the board's replaceable DOM. Selecting a tab
// changes visibility only; it never reparents/reloads an existing frame.
const STORAGE = 'lec-terminal-tabs-v1';
function terminalPath(url) {
  const parsed = new URL(url, location.origin);
  const path = parsed.pathname.replace(/^\/term\//, '/terminal/').replace(/\/$/, '');
  if (parsed.origin !== location.origin || !/^\/terminal\/(session|attempt|project)\/[1-9]\d*$/.test(path))
    throw Error('This terminal does not have a valid Lectern address.');
  return path;
}

export class TerminalTabs {
  constructor(root, {activate, browse, search}) {
    this.root = root;
    this.root.inert = true;
    this.activate = activate;
    this.tabs = new Map();
    this.active = null;
    this.mobile = matchMedia('(max-width:1023px)');
    this.compact = true;
    try { this.compact = localStorage.getItem('lec-terminal-compact') !== '0'; } catch {}
    root.innerHTML = `<div class="terminal-tabbar">
      <div class="terminal-tablist" role="tablist" aria-label="Open terminals"></div>
      <a class="b terminal-popout" target="_blank" rel="noopener" title="Open this terminal in a separate browser tab" hidden>Pop out ↗</a>
      <button class="terminal-search" aria-label="Search sessions and actions" title="Search sessions and actions">⌕</button>
      <details class="terminal-actions"><summary aria-label="Terminal actions" title="Terminal actions">⋯</summary>
        <div class="terminal-actions-panel" role="menu"><a class="terminal-menu-popout" target="_blank" rel="noopener" role="menuitem">Open in new tab</a>
          <button class="terminal-menu-close" type="button" role="menuitem">Close this view</button></div></details>
      <button class="terminal-focus" aria-label="Show navigation" title="Show navigation">☰</button>
    </div><div class="terminal-panels"></div>
    <div class="terminal-empty"><h2>No open terminals</h2>
      <p>Attach a session or open a project shell. Its terminal stays here while you switch around Lectern.</p>
      <button class="b ok">Open sessions</button></div>`;
    root.querySelector('.terminal-empty button').onclick = browse;
    this.list = root.querySelector('.terminal-tablist');
    this.bindTabStripSwipe();
    this.panels = root.querySelector('.terminal-panels');
    this.popout = root.querySelector('.terminal-popout');
    this.actions = root.querySelector('.terminal-actions');
    this.menuPopout = root.querySelector('.terminal-menu-popout');
    this.menuClose = root.querySelector('.terminal-menu-close');
    this.empty = root.querySelector('.terminal-empty');
    this.focusButton = root.querySelector('.terminal-focus');
    this.focusButton.onclick = () => {
      this.compact = !this.compact;
      try { localStorage.setItem('lec-terminal-compact', this.compact ? '1' : '0'); } catch {}
      this.applyLayout();
    };
    root.querySelector('.terminal-search').onclick = () => search?.();
    this.menuClose.onclick = () => {
      const tab = this.tabs.get(this.active);
      if (tab) this.close(tab.path);
      this.actions.open = false;
    };
    this.mobile.addEventListener('change', () => this.applyLayout());
    try {
      const saved = JSON.parse(sessionStorage.getItem(STORAGE) || '{}');
      for (const tab of (Array.isArray(saved.tabs) ? saved.tabs : []).slice(0, 30)) {
        try { this.add(terminalPath(tab.path), tab.label); } catch {}
      }
      this.active = this.tabs.has(saved.active) ? saved.active : this.tabs.keys().next().value || null;
    } catch {}
    this.render();
  }
  add(path, label) {
    if (this.tabs.has(path)) return this.tabs.get(path);
    const tab = {path, label: String(label || 'Terminal').slice(0, 160)};
    this.tabs.set(path, tab);
    return tab;
  }
  open(url, label) {
    const path = terminalPath(url);
    const tab = this.add(path, label);
    if (label) tab.label = String(label).slice(0, 160);
    this.select(path);
  }
  select(path) {
    if (!this.tabs.has(path)) return;
    this.active = path;
    this.save();
    this.activate();
  }
  show() {
    this.root.hidden = false;
    this.root.inert = false;
    this.render();
    this.applyLayout();
    const tab = this.tabs.get(this.active);
    if (!tab) return;
    if (!tab.frame) {
      const panel = document.createElement('div');
      panel.className = 'terminal-tabpanel';
      panel.id = this.panelID(tab.path);
      panel.setAttribute('role', 'tabpanel');
      panel.setAttribute('aria-labelledby', this.buttonID(tab.path));
      const frame = document.createElement('iframe');
      frame.title = tab.label + ' terminal';
      frame.src = tab.path + '?embed=1';
      frame.allow = 'clipboard-read; clipboard-write';
      panel.appendChild(frame);
      this.panels.appendChild(panel);
      tab.panel = panel; tab.frame = frame;
      frame.onload = () => { tab.swipeBound = false; this.bindSwipe(tab); this.notifyVisible(tab); };
    }
    for (const entry of this.tabs.values()) {
      if (entry.panel) {
        entry.panel.hidden = entry !== tab;
        entry.panel.inert = entry !== tab;
      }
    }
    this.notifyVisible(tab);
    this.list.querySelector('[aria-selected="true"]')?.scrollIntoView({block:'nearest', inline:'nearest'});
  }
  hide() { this.root.hidden = true; this.root.inert = true; this.applyLayout(); }
  applyLayout() {
    const compact = this.mobile.matches && this.compact && !this.root.hidden && !!this.active;
    document.body.classList.toggle('terminal-compact', compact);
    this.focusButton.textContent = compact ? '☰' : '⤢';
    this.focusButton.setAttribute('aria-label', compact ? 'Show navigation' : 'Focus terminal');
    this.focusButton.title = compact ? 'Show navigation' : 'Focus terminal';
    this.focusButton.setAttribute('aria-pressed', String(compact));
    this.focusButton.hidden = !this.active;
    for (const tab of this.tabs.values()) this.notifyVisible(tab);
  }
  notifyVisible(tab) {
    if (!this.root.hidden && tab.path === this.active)
      tab.frame?.contentWindow?.postMessage({type:'lec-terminal-visible', mobile:this.mobile.matches, compact:this.mobile.matches && this.compact}, location.origin);
  }
  bindSwipe(tab) {
    if (tab.swipeBound || !tab.frame?.contentWindow) return;
    const win = tab.frame.contentWindow;
    let start = null;
    win.addEventListener('touchstart', (event) => {
      if (!this.mobile.matches || this.root.hidden || tab.path !== this.active) { start = null; return; }
      if (event.touches.length !== 1) { start = null; return; }
      const touch = event.touches[0];
      const target = event.target;
      // Inputs, links, and xterm's modifier/key controls own their gestures.
      if (target?.closest?.('textarea,input,button,a,select,[contenteditable="true"]')) { start = null; return; }
      start = {x: touch.clientX, y: touch.clientY, at: performance.now(), maxVertical: 0};
    }, {passive: true});
    win.addEventListener('touchmove', (event) => {
      if (!start || event.touches.length !== 1) { start = null; return; }
      const touch = event.touches[0];
      start.maxVertical = Math.max(start.maxVertical, Math.abs(touch.clientY - start.y));
    }, {passive: true});
    win.addEventListener('touchcancel', () => { start = null; }, {passive: true});
    win.addEventListener('touchend', (event) => {
      if (!start || !this.mobile.matches || this.root.hidden || tab.path !== this.active || event.changedTouches.length !== 1) { start = null; return; }
      const touch = event.changedTouches[0];
      const dx = touch.clientX - start.x, dy = touch.clientY - start.y;
      const elapsed = performance.now() - start.at;
      const selection = win.getSelection?.();
      const xterm = win.__lecTerminalState?.() || {};
      const selected = (selection && selection.toString()) || xterm.hasSelection;
      const threshold = Math.max(72, Math.min(140, win.innerWidth * .22));
      const flick = elapsed <= 550 && start.maxVertical < Math.max(48, threshold * .55) && Math.abs(dx) >= threshold && Math.abs(dx) >= Math.abs(dy) * 1.6;
      start = null;
      // A horizontal text selection or a vertical terminal scroll remains an
      // xterm gesture. Only an unambiguous one-finger flick changes tabs.
      if (!flick || selected || (xterm.mouseTrackingMode && xterm.mouseTrackingMode !== 'none')) return;
      const moved = this.selectRelative(dx < 0 ? 1 : -1);
      if (moved) event.preventDefault();
    }, {passive: false});
    tab.swipeBound = true;
  }
  bindTabStripSwipe() {
    // Terminal applications may claim the iframe's pointer stream for mouse
    // reporting. Keep the same flick available from the tab strip, whose
    // controls belong to Lectern and are always safe to own.
    const strip = this.list;
    let start = null;
    strip.addEventListener('touchstart', (event) => {
      if (!this.mobile.matches || this.root.hidden || !this.active || event.touches.length !== 1) {
        start = null; return;
      }
      const target = event.target;
      if (target?.closest?.('.terminal-tab-close,.terminal-actions,.terminal-search,.terminal-focus,a,input,select,textarea,[contenteditable="true"]')) {
        start = null; return;
      }
      const touch = event.touches[0];
      start = {x: touch.clientX, y: touch.clientY, at: performance.now(), maxVertical: 0};
    }, {passive: true});
    strip.addEventListener('touchmove', (event) => {
      if (!start || event.touches.length !== 1) { start = null; return; }
      const touch = event.touches[0];
      start.maxVertical = Math.max(start.maxVertical, Math.abs(touch.clientY - start.y));
    }, {passive: true});
    strip.addEventListener('touchcancel', () => { start = null; }, {passive: true});
    strip.addEventListener('touchend', (event) => {
      if (!start || !this.mobile.matches || this.root.hidden || !this.active || event.changedTouches.length !== 1) {
        start = null; return;
      }
      const touch = event.changedTouches[0];
      const dx = touch.clientX - start.x, dy = touch.clientY - start.y;
      const elapsed = performance.now() - start.at;
      const threshold = Math.max(72, Math.min(140, strip.clientWidth * .22 || innerWidth * .22));
      const flick = elapsed <= 550 && start.maxVertical < Math.max(48, threshold * .55) &&
        Math.abs(dx) >= threshold && Math.abs(dx) >= Math.abs(dy) * 1.6;
      start = null;
      if (!flick) return;
      const moved = this.selectRelative(dx < 0 ? 1 : -1);
      if (moved) event.preventDefault();
    }, {passive: false});
  }
  selectRelative(delta) {
    const keys = [...this.tabs.keys()], index = keys.indexOf(this.active);
    const next = keys[index + delta];
    if (!next) return false;
    this.select(next);
    return true;
  }
  close(path) {
    const tab = this.tabs.get(path);
    if (!tab) return;
    const keys = [...this.tabs.keys()];
    const index = keys.indexOf(path);
    // Removing an iframe disconnects only its ttyd client. No kill/end API call.
    tab.panel?.remove();
    this.tabs.delete(path);
    if (this.active === path) this.active = keys[index + 1] || keys[index - 1] || null;
    this.save();
    if (!this.root.hidden) this.activate();
    else this.render();
    this.list.querySelector('[aria-selected="true"]')?.focus({preventScroll:true});
  }
  buttonID(path) { return 'terminal-tab-' + path.split('/').slice(-2).join('-'); }
  panelID(path) { return this.buttonID(path) + '-panel'; }
  hash() { return this.active ? '#terminals/' + this.active.split('/').slice(-2).join('/') : '#terminals'; }
  save() {
    try {
      sessionStorage.setItem(STORAGE, JSON.stringify({active:this.active,
        tabs:[...this.tabs.values()].map(({path,label}) => ({path,label}))}));
    } catch {}
  }
  render() {
    this.list.replaceChildren();
    for (const tab of this.tabs.values()) {
      const duplicates = [...this.tabs.values()].filter((entry) => entry.label === tab.label).length > 1;
      const label = tab.label + (duplicates ? ' #' + tab.path.split('/').at(-1) : '');
      const wrap = document.createElement('div');
      wrap.className = 'terminal-tab-item';
      wrap.setAttribute('role', 'presentation');
      const button = document.createElement('button');
      button.className = 'terminal-tab'; button.textContent = label;
      button.title = label;
      button.id = this.buttonID(tab.path);
      button.setAttribute('role', 'tab');
      button.setAttribute('aria-selected', String(tab.path === this.active));
      button.setAttribute('aria-controls', this.panelID(tab.path));
      button.tabIndex = tab.path === this.active ? 0 : -1;
      button.onclick = () => this.select(tab.path);
      button.onkeydown = (e) => {
        const keys = [...this.tabs.keys()]; let next;
        if (e.key === 'ArrowRight') next = keys[(keys.indexOf(tab.path)+1)%keys.length];
        if (e.key === 'ArrowLeft') next = keys[(keys.indexOf(tab.path)+keys.length-1)%keys.length];
        if (e.key === 'Home') next = keys[0];
        if (e.key === 'End') next = keys.at(-1);
        if (e.key === 'Delete') { e.preventDefault(); this.close(tab.path); return; }
        if (next) {
          e.preventDefault(); this.select(next);
          document.getElementById(this.buttonID(next))?.focus({preventScroll:true});
        }
      };
      const close = document.createElement('button');
      close.className = 'terminal-tab-close'; close.textContent = '×';
      close.setAttribute('aria-label', 'Close terminal view: ' + label);
      close.title = 'Close this view; the agent keeps running';
      close.onclick = () => this.close(tab.path);
      wrap.append(button, close); this.list.appendChild(wrap);
    }
    const tab = this.tabs.get(this.active);
    this.popout.hidden = !tab;
    this.actions.hidden = !tab;
    if (tab) { this.popout.href = tab.path; this.menuPopout.href = tab.path; }
    this.empty.hidden = this.tabs.size > 0;
    this.panels.hidden = this.tabs.size === 0;
    const badge = document.getElementById('terminal-badge');
    badge.hidden = this.tabs.size === 0; badge.textContent = this.tabs.size;
  }
}
