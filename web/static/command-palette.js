// The palette only selects existing application actions. It does not interpret
// text as commands or take ownership of the embedded terminal's keyboard.
export function rankCommands(items, query) {
  const terms = query.toLocaleLowerCase().trim().split(/\s+/).filter(Boolean);
  return items.map((item, order) => {
    const title = item.title.toLocaleLowerCase();
    const haystack = `${title} ${item.category} ${item.detail || ''} ${item.keywords || ''}`.toLocaleLowerCase();
    if (!terms.every(term => haystack.includes(term))) return null;
    const score = !terms.length ? 0 : title === query.toLocaleLowerCase().trim() ? 3 : title.startsWith(terms[0]) ? 2 : terms.every(term => title.includes(term)) ? 1 : 0;
    return {item, score, order};
  }).filter(Boolean).sort((a,b) => b.score-a.score || a.order-b.order).map(result => result.item);
}

export class CommandPalette {
  constructor({button, items, refresh, error}) {
    this.items = items; this.refresh = refresh; this.error = error; this.generation = 0;
    this.dialog = document.createElement('dialog');
    this.dialog.className = 'command-palette';
    this.dialog.setAttribute('aria-label', 'Search Lectern');
    this.dialog.innerHTML = `<div class="command-search"><label class="sr-only" for="command-query">Search sessions, tasks, and actions</label><input id="command-query" type="search" autocomplete="off" placeholder="Search sessions, tasks, actions…" role="combobox" aria-autocomplete="list" aria-expanded="true" aria-controls="command-results"><button type="button" aria-label="Close search"><span class="command-close-text">Esc</span><span class="command-close-icon" aria-hidden="true">×</span></button></div><p class="command-status" role="status" aria-live="polite"></p><div id="command-results" role="listbox" aria-label="Search results"></div><p class="command-help">↑ ↓ to choose · Enter to open · Esc to close</p>`;
    document.body.append(this.dialog);
    const fit = () => this.dialog.style.setProperty('--command-height', `${window.visualViewport?.height || innerHeight}px`);
    window.visualViewport?.addEventListener('resize', fit);
    window.addEventListener('resize', fit);
    fit();
    this.input = this.dialog.querySelector('input');
    this.list = this.dialog.querySelector('[role=listbox]');
    this.status = this.dialog.querySelector('[role=status]');
    button.addEventListener('click', () => this.open());
    this.dialog.querySelector('button').onclick = () => this.close();
    this.dialog.addEventListener('cancel', e => { e.preventDefault(); this.close(); });
    this.dialog.addEventListener('click', e => { if (e.target === this.dialog) this.close(); });
    this.input.addEventListener('input', () => { this.selected = null; this.render(); });
    this.dialog.addEventListener('keydown', e => {
      if (e.isComposing) return;
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); this.close(); }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const index = this.results.findIndex(item => item.id === this.selected);
        const next = Math.max(0, Math.min(this.results.length-1, index + (e.key === 'ArrowDown' ? 1 : -1)));
        this.selected = this.results[next]?.id; this.paintSelection();
      }
      if (e.key === 'Enter' && e.target === this.input) { e.preventDefault(); this.choose(); }
      if (e.key === 'Tab') {
        // Two focus stops; keep focus in the dialog even in browsers with
        // incomplete native modal focus wrapping.
        const close = this.dialog.querySelector('button');
        e.preventDefault(); (document.activeElement === this.input ? close : this.input).focus();
      }
    });
    window.addEventListener('keydown', e => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k' && !e.isComposing) {
        e.preventDefault(); e.stopPropagation();
        if (this.dialog.open) this.close(); else this.open();
      }
    });
  }
  async open() {
    if (this.dialog.open) { this.input.focus(); return; }
    this.returnFocus = document.activeElement;
    this.input.value = ''; this.selected = null; this.loadError = ''; this.loading = true;
    this.dialog.showModal(); this.input.focus(); this.render();
    const generation = ++this.generation;
    this.status.textContent = 'Updating results…';
    try { await this.refresh(); }
    catch { if (this.dialog.open && generation === this.generation) this.loadError = 'Could not refresh. Showing available results.'; }
    if (this.dialog.open && generation === this.generation) { this.loading = false; this.render(); }
  }
  close() {
    this.generation++; this.dialog.close();
    if (this.returnFocus?.isConnected) this.returnFocus.focus();
  }
  render() {
    const all = rankCommands(this.items(), this.input.value);
    this.results = all.slice(0, 80);
    if (!this.results.some(item => item.id === this.selected)) this.selected = this.results[0]?.id;
    this.list.replaceChildren();
    this.results.forEach((item, index) => {
      const option = document.createElement('div');
      option.id = `command-option-${index}`; option.role = 'option';
      option.className = 'command-result'; option.dataset.commandId = item.id;
      const title = document.createElement('strong'); title.textContent = item.title;
      const detail = document.createElement('span'); detail.textContent = [item.category, item.detail].filter(Boolean).join(' · ');
      option.append(title, detail);
      option.onpointerdown = e => e.preventDefault();
      option.onclick = () => { this.selected = item.id; this.choose(); };
      this.list.append(option);
    });
    this.status.textContent = this.loadError || (all.length ? `${all.length} ${all.length === 1 ? 'result' : 'results'}${all.length > 80 ? ' · showing the first 80; type to narrow' : ''}${this.loading ? ' · updating…' : ''}` : this.loading ? 'Searching…' : 'No matches. Try a session name, project, or action.');
    this.paintSelection();
  }
  paintSelection() {
    [...this.list.children].forEach((node, index) => {
      const selected = this.results[index].id === this.selected;
      node.setAttribute('aria-selected', String(selected));
      if (selected) { this.input.setAttribute('aria-activedescendant', node.id); node.scrollIntoView({block:'nearest'}); }
    });
    if (!this.results.length) this.input.removeAttribute('aria-activedescendant');
  }
  async choose() {
    const item = this.results.find(item => item.id === this.selected);
    if (!item) return;
    this.close();
    try { await item.run(); } catch (e) { this.error(e.message); }
  }
}
