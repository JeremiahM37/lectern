// One review surface for session cards and embedded/native browser terminals.
export function openReview({kind, id, name, api}) {
  const previousFocus = document.activeElement;
  const dialog = document.createElement('dialog');
  dialog.className = 'code-review'; dialog.setAttribute('aria-label', 'Review changes');
  dialog.innerHTML = `<header><div><h2>Review changes</h2><p class="review-name"></p></div><button class="review-close" aria-label="Close review">×</button></header>
    <div class="review-toolbar"><label class="review-repository-label" hidden>Repository <select class="review-repository" aria-label="Repository"></select></label><label>Changes <select class="review-scope"><option value="working">Working tree</option><option value="staged">Staged</option></select></label><span class="review-branch"></span><button class="review-refresh">Refresh</button><button class="review-wrap" aria-pressed="true">Wrap lines</button></div>
    <p class="review-status" role="status"></p><div class="review-layout"><aside><input class="review-filter" type="search" placeholder="Find a changed file" aria-label="Find a changed file"><div class="review-files" aria-label="Changed files"></div></aside><section class="review-detail"><div class="review-path"></div><div class="review-patch" tabindex="0" aria-label="File diff"></div></section></div>
    <footer>Read-only snapshot · Refresh to see the agent’s latest edits</footer>`;
  const $ = s => dialog.querySelector(s);
  $('.review-name').textContent = name || `${kind} ${id}`;
  let data = null, scope = 'working', selected = '', repository = '0', generation = 0, closed = false;
  let wrap = localStorage.getItem('lec-review-wrap') !== '0';
  function paintWrap() {
    dialog.classList.toggle('review-wrapped', wrap);
    $('.review-wrap').setAttribute('aria-pressed', String(wrap));
  }
  function drawFiles() {
    const list = $('.review-files'); list.replaceChildren();
    const query = $('.review-filter').value.toLocaleLowerCase();
    const files = (data?.files || []).filter(f => f[scope] && f.path.toLocaleLowerCase().includes(query));
    for (const file of files) {
      const b = document.createElement('button');
      b.className = 'review-file'; b.setAttribute('aria-pressed', String(file.path === selected));
      const status = document.createElement('span'); status.className = 'review-file-status'; status.textContent = file.status;
      const path = document.createElement('span'); path.textContent = file.path;
      b.append(status, path); b.title = file.path;
      b.onclick = () => load(file.path); list.appendChild(b);
    }
    if (!files.length) list.textContent = query ? 'No matching files.' : 'No changes in this view.';
  }
  function drawPatch() {
    $('.review-path').textContent = data.path || 'No file selected';
    const patch = $('.review-patch'); patch.replaceChildren(); patch.scrollTop = 0; patch.scrollLeft = 0;
    if (!data.path) { patch.textContent = 'Nothing to review here. Check the other changes view or refresh after editing.'; return; }
    let oldLine = 0, newLine = 0;
    for (const line of (data.patch || 'No textual changes (the file may have been renamed or its permissions changed).').split('\n')) {
      const row = document.createElement('div'); row.className = 'review-line';
      const match = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      let before = '', after = '';
      if (match) { oldLine = +match[1]; newLine = +match[2]; row.classList.add('review-hunk'); }
      else if (oldLine || newLine) {
        if (line.startsWith('+')) { after = newLine++; row.classList.add('review-add'); }
        else if (line.startsWith('-')) { before = oldLine++; row.classList.add('review-del'); }
        else if (line.startsWith(' ')) { before = oldLine++; after = newLine++; }
      }
      for (const n of [before, after]) { const cell = document.createElement('span'); cell.className = 'review-number'; cell.textContent = n; cell.setAttribute('aria-hidden','true'); row.appendChild(cell); }
      const code = document.createElement('code'); code.textContent = line || ' '; row.appendChild(code); patch.appendChild(row);
    }
  }
  async function load(path = '') {
    const revision = ++generation, requestScope = scope;
    $('.review-status').textContent = 'Loading changes…';
    $('.review-patch').setAttribute('aria-busy','true');
    try {
      const next = await api(`/term/${kind}/${id}/changes?scope=${requestScope}&path=${encodeURIComponent(path)}&repository=${encodeURIComponent(repository)}`);
      if (closed || revision !== generation) return;
      data = next; selected = next.path; drawFiles(); drawPatch();
      const repositories = next.repositories || [];
      $('.review-repository-label').hidden = repositories.length < 2;
      $('.review-repository').replaceChildren(...repositories.map(repo => new Option(repo.name, String(repo.id))));
      repository = String(next.selected_repository ?? 0);
      $('.review-repository').value = repository;
      const working = data.files.filter(f=>f.working).length, staged = data.files.filter(f=>f.staged).length;
      $('.review-scope').options[0].textContent = `Working tree (${working})`;
      $('.review-scope').options[1].textContent = `Staged (${staged})`;
      $('.review-branch').textContent = data.branch;
      $('.review-status').textContent = data.truncated ? 'Large diff: showing the first 512 KiB. Open the file for the remainder.' : `${data.files.filter(f=>f[scope]).length} changed files · Updated ${new Date().toLocaleTimeString()}`;
    } catch (error) {
      if (closed || revision !== generation) return;
      // Never label an older file's patch as the newly selected file/scope.
      data = null; selected = ''; drawFiles(); $('.review-path').textContent = '';
      $('.review-patch').textContent = 'Changes could not be loaded. Refresh to try again.';
      $('.review-status').textContent = error.message;
    } finally { if (revision === generation) $('.review-patch').removeAttribute('aria-busy'); }
  }
  $('.review-repository').onchange = e => { repository = e.target.value; selected = ''; $('.review-filter').value = ''; load(); };
  $('.review-scope').onchange = e => { scope = e.target.value; load(); };
  $('.review-filter').oninput = drawFiles;
  $('.review-refresh').onclick = () => load(selected);
  $('.review-wrap').onclick = () => { wrap = !wrap; localStorage.setItem('lec-review-wrap', wrap ? '1':'0'); paintWrap(); };
  $('.review-close').onclick = () => dialog.close();
  dialog.addEventListener('close', () => { closed = true; generation++; dialog.remove(); previousFocus?.focus(); });
  dialog.addEventListener('keydown', e => { e.stopPropagation(); });
  document.body.appendChild(dialog); paintWrap(); dialog.showModal(); load();
  return dialog;
}
