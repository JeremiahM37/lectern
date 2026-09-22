// Agent runner registry editor. A runner is the executable that owns a
// session; a model/provider URL is only configuration passed to that runner.
// Keep this distinction visible because an API endpoint alone cannot launch a
// coding session.
export function renderAgentSettings(container, {api, onChange = () => {}}) {
  container.innerHTML = `<div class="agent-settings">
    <div class="agent-settings-head"><div><h3>Agents</h3>
      <p class="sub">Choose a command and its model connection. The target is selected when you start a session or task.</p></div>
      <button class="b ok" data-agent-add type="button">Add agent</button></div>
    <div class="agent-settings-status sub" role="status"></div>
    <div class="agent-settings-list"></div>
  </div>`;
  const list = container.querySelector('.agent-settings-list');
  const status = container.querySelector('.agent-settings-status');
  let rows = [];

  const displayEnv = spec => {
    const env = spec?.env && typeof spec.env === 'object' ? spec.env : {};
    return Object.keys(env).sort().map(k => {
      const value = env[k];
      return `${k}=${value && typeof value === 'object' ? '••••' : value}`;
    }).join('\n');
  };
  const endpointKeys = ['OPENAI_API_BASE', 'OPENAI_BASE_URL', 'ANTHROPIC_BASE_URL', 'OLLAMA_HOST', 'BASE_URL'];
  const providerEnv = spec => spec?.provider_env || endpointKeys.find(k => spec?.env?.[k]) || 'OPENAI_BASE_URL';
  const providerURL = spec => {
    const key = providerEnv(spec);
    const value = spec?.env?.[key];
    return typeof value === 'string' && !['••••', '***', '__KEEP__'].includes(value) ? value : '';
  };
  const parseArgs = (value, label) => {
    if (!value.trim()) return [];
    let out;
    try { out = JSON.parse(value); } catch { out = value.split(/\r?\n/).map(v => v.trim()).filter(Boolean); }
    if (!Array.isArray(out) || out.some(v => typeof v !== 'string')) throw Error(`${label} must be a JSON array of strings.`);
    return out;
  };
  const parseEnv = value => {
    if (!value.trim()) return {};
    // JSON remains available for complex values. KEY=value is easier to edit
    // on a phone and lets us mask existing values without exposing secrets.
    if (value.trim().startsWith('{')) {
      let parsed;
      try { parsed = JSON.parse(value); } catch { throw Error('Environment must be JSON or KEY=value lines.'); }
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object' || Object.values(parsed).some(v => typeof v !== 'string')) throw Error('Environment values must be strings.');
      return parsed;
    }
    const out = {};
    for (const raw of value.split(/\r?\n/)) {
      const line = raw.trim(); if (!line) continue;
      const i = line.indexOf('='); if (i <= 0) throw Error('Environment lines must use KEY=value.');
      const key = line.slice(0, i).trim(), val = line.slice(i + 1);
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw Error(`Invalid environment name ${key}.`);
      if (val === '••••' || val === '***') continue;
      // A blank KEY= is the explicit removal path. Leaving a masked line
      // untouched retains its typed marker from GET /agents.
      out[key] = val === '' ? '__DELETE__' : val;
    }
    return out;
  };
  const clone = value => JSON.parse(JSON.stringify(value || {}));
  const normalized = spec => {
    const out = {...spec};
    delete out.id; delete out.builtin;
    return out;
  };
  const saveRows = async next => {
    // The API replaces custom definitions in one atomic PUT. Built-in defaults
    // are omitted by callers unless one was explicitly edited.
    const saved = await api('/agents', {method:'PUT', body: next.map(normalized)});
    rows = saved;
    render();
    onChange(saved);
  };
  const openEditor = (source = null) => {
    const dialog = document.createElement('dialog');
    dialog.className = 'agent-settings-dialog';
    dialog.setAttribute('aria-label', source ? `Edit agent ${source.name}` : 'Add agent');
    dialog.innerHTML = `<form method="dialog">
      <header><div><h2>${source ? 'Edit agent' : 'Add agent'}</h2>
        <p class="sub">Choose a command and its model connection.</p></div>
        <button type="button" class="agent-dialog-close" aria-label="Close">Close</button></header>
      ${source ? '' : `<label>Starter template<select class="agent-preset"><option value="custom">Custom runner</option><option value="opencode">OpenCode</option><option value="aider">Aider</option></select></label>`}
      <label>Name<input class="agent-name" required maxlength="120" autocomplete="off" placeholder="opencode"></label>
      <label>Command<input class="agent-command" required maxlength="4096" autocomplete="off" placeholder="/usr/local/bin/opencode"></label>
      <label>Fixed arguments (one per line; JSON array accepted)<textarea class="agent-args" rows="2" spellcheck="false" placeholder="--some-flag"></textarea></label>
      <div class="agent-two"><label>Model flag<input class="agent-model-flag" maxlength="80" placeholder="--model"></label>
        <label>Provider endpoint URL (optional)<input class="agent-provider-url" type="url" maxlength="2048" placeholder="http://127.0.0.1:11434/v1"></label></div>
      <div class="agent-provider-fields"><label>Provider URL environment variable<input class="agent-provider-env" maxlength="80" placeholder="OPENAI_API_BASE"></label>
      <p class="sub agent-provider-help">Lectern passes this endpoint to the runner through the named environment variable.</p></div>
      <label class="check"><input type="checkbox" class="agent-prompt-arg"> Opening prompt is a positional argument</label>
      <p class="sub agent-prompt-help">Leave off when the CLI prompts inside its terminal; Lectern will type the opening message after the pane is ready.</p>
      <div class="agent-two"><label>Resume arguments (one per line; JSON accepted)<textarea class="agent-resume" rows="2" spellcheck="false" placeholder="--continue"></textarea></label>
        <label>Yolo arguments (one per line; JSON accepted)<textarea class="agent-yolo" rows="2" spellcheck="false" placeholder="--yolo"></textarea></label></div>
      <label>Environment (KEY=value lines; existing values are masked and retained)<textarea class="agent-env" rows="4" spellcheck="false" placeholder="OPENAI_BASE_URL=http://127.0.0.1:11434/v1\nOPENAI_API_KEY=…"></textarea></label>
      <p class="sub agent-env-help">Use environment variables for provider credentials. Masked values are never sent back as replacements when unchanged.</p>
      <label class="check"><input type="checkbox" class="agent-task-enabled"> Enable background tasks for this agent</label>
      <p class="sub agent-task-help">A task runner uses its own one-shot command. Leave this off for interactive-only CLIs.</p>
      <div class="agent-task-fields" hidden>
        <label>Task command<input class="agent-task-command" maxlength="4096" placeholder="same command by default"></label>
        <label>Task arguments (one per line; JSON array accepted)<textarea class="agent-task-args" rows="2" spellcheck="false" placeholder="--message"></textarea></label>
        <label>Prompt template<input class="agent-task-prompt" maxlength="4096" placeholder="run {prompt}"></label>
        <label>Task output<select class="agent-task-output"><option value="plain">Plain text</option><option value="jsonl">JSONL events</option></select></label>
        <label>Permission arguments (JSON object, optional)<textarea class="agent-task-permissions" rows="3" spellcheck="false" placeholder='{"plan":["--dry-run"]}'></textarea></label>
      </div>
      <div class="agent-dialog-status" role="status"></div><div class="agent-dialog-buttons">
        <button type="submit" class="b ok agent-save">Save runner</button><button type="button" class="b agent-cancel">Cancel</button></div>
    </form>`;
    const $ = sel => dialog.querySelector(sel);
    const set = (sel, value) => { const el = $(sel); if (el) el.value = value ?? ''; };
    set('.agent-name', source?.name); set('.agent-command', source?.command);
    set('.agent-args', JSON.stringify(source?.args || [], null, 2)); set('.agent-model-flag', source?.model_flag);
    set('.agent-provider-url', providerURL(source)); set('.agent-provider-env', providerEnv(source));
    set('.agent-resume', JSON.stringify(source?.resume_args || [], null, 2)); set('.agent-yolo', JSON.stringify(source?.yolo_args || [], null, 2));
    set('.agent-env', displayEnv(source));
    $('.agent-prompt-arg').checked = !!source?.prompt_arg;
    const applyPreset = value => {
      if (value === 'opencode') { set('.agent-name', 'opencode'); set('.agent-command', 'opencode'); set('.agent-args', '[]'); set('.agent-model-flag', '--model'); set('.agent-provider-url', ''); set('.agent-provider-env', ''); $('.agent-provider-help').textContent = 'OpenCode uses its configured provider settings. Add OPENCODE_CONFIG_CONTENT in Environment for a custom provider.'; $('.agent-prompt-arg').checked = false; enableTask(true); set('.agent-task-command', 'opencode'); set('.agent-task-args', '[]'); set('.agent-task-prompt', 'run {prompt}'); }
      if (value === 'aider') { set('.agent-name', 'aider'); set('.agent-command', 'aider'); set('.agent-args', '[]'); set('.agent-model-flag', '--model'); set('.agent-provider-env', 'OPENAI_API_BASE'); $('.agent-provider-help').textContent = 'Lectern passes this endpoint to Aider as OPENAI_API_BASE.'; $('.agent-prompt-arg').checked = false; enableTask(true); set('.agent-task-command', 'aider'); set('.agent-task-args', '[]'); set('.agent-task-prompt', '--message {prompt}'); }
    };
    const enableTask = value => { $('.agent-task-enabled').checked = value; $('.agent-task-fields').hidden = !value; };
    const syncProviderFields = preset => {
      const openCode = preset === 'opencode';
      $('.agent-provider-fields').hidden = openCode;
      $('.agent-provider-url').closest('label').hidden = openCode;
      if (openCode) $('.agent-provider-help').textContent = 'OpenCode uses its configured provider settings. Add OPENCODE_CONFIG_CONTENT in Environment for a custom provider.';
    };
    syncProviderFields(source?.command === 'opencode' ? 'opencode' : 'custom');
    if (source?.task) { enableTask(true); set('.agent-task-command', source.task.command); set('.agent-task-args', JSON.stringify(source.task.args || [], null, 2)); set('.agent-task-prompt', source.task.prompt_template); set('.agent-task-output', source.task.output_mode || 'plain'); set('.agent-task-permissions', JSON.stringify(source.task.permission_args || {}, null, 2)); }
    $('.agent-task-enabled').onchange = e => enableTask(e.target.checked);
    $('.agent-preset')?.addEventListener('change', e => { syncProviderFields(e.target.value); applyPreset(e.target.value); });
    const close = () => { dialog.close(); dialog.remove(); };
    $('.agent-dialog-close').onclick = close; $('.agent-cancel').onclick = close;
    dialog.addEventListener('cancel', e => {e.preventDefault(); close();});
    dialog.querySelector('form').onsubmit = async e => {
      e.preventDefault(); const message = $('.agent-dialog-status');
      try {
        const name = $('.agent-name').value.trim(), command = $('.agent-command').value.trim();
        if (!name || !command) throw Error('Name and command are required.');
        const envText = $('.agent-env').value;
        const enteredEnv = parseEnv(envText);
        const priorEnv = source?.env && typeof source.env === 'object' ? source.env : {};
        // If the user left a masked line in place, merge in the server value.
        // This also preserves keys returned by a redacting backend.
        const env = Object.keys(enteredEnv).length ? {...priorEnv, ...enteredEnv} : clone(priorEnv);
        const presetValue = $('.agent-preset')?.value || (command === 'opencode' ? 'opencode' : 'custom');
        const endpointKey = $('.agent-provider-env').value.trim() || 'OPENAI_API_BASE';
        if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(endpointKey)) throw Error('Provider environment name is invalid.');
        const endpoint = $('.agent-provider-url').value.trim();
        if (presetValue === 'opencode' && endpoint) throw Error('OpenCode uses its configured provider settings; add OPENCODE_CONFIG_CONTENT in Environment instead.');
        if (endpoint) env[endpointKey] = endpoint;
        else if (source && providerURL(source)) {
          const oldEndpointKey = providerEnv(source);
          if (source.env?.[oldEndpointKey] !== undefined) delete env[oldEndpointKey];
        }
        if (source && providerURL(source) && endpointKey !== providerEnv(source)) delete env[providerEnv(source)];
        for (const [key, value] of Object.entries(env)) if (value === '__DELETE__') delete env[key];
        // A redacting API returns typed retention markers. They remain objects
        // in the replacement PUT, so the backend can verify and restore them.
        for (const [key, value] of Object.entries(env)) {
          if (value === '••••' || value === '***') delete env[key];
        }
        const spec = {...(source ? normalized(source) : {}), name, command,
          args: parseArgs($('.agent-args').value, 'Fixed arguments'), model_flag: $('.agent-model-flag').value.trim(),
          prompt_arg: $('.agent-prompt-arg').checked,
          resume_args: parseArgs($('.agent-resume').value, 'Resume arguments'), yolo_args: parseArgs($('.agent-yolo').value, 'Yolo arguments'), env};
        if ($('.agent-task-enabled').checked) {
          let permissions = {};
          try { permissions = JSON.parse($('.agent-task-permissions').value || '{}'); } catch { throw Error('Permission arguments must be JSON.'); }
          if (!permissions || Array.isArray(permissions) || typeof permissions !== 'object') throw Error('Permission arguments must be a JSON object.');
          spec.task = {command: $('.agent-task-command').value.trim() || command,
            args: parseArgs($('.agent-task-args').value, 'Task arguments'), prompt_template: $('.agent-task-prompt').value.trim(),
            output_mode: $('.agent-task-output').value, permission_args: permissions};
        } else delete spec.task;
        if (source?.builtin && !spec.task && !confirm('This override changes the built-in runner. Background tasks use the built-in adapter today; continue and make this an interactive-only custom override?')) {
          throw Error('Keep the built-in task adapter or enable a separate task definition.');
        }
        delete spec.provider_url;
        delete spec.provider_env;
        const custom = rows.filter(row => !row.builtin).map(normalized);
        // Built-in defaults stay implicit until the operator explicitly edits
        // one, so a save cannot turn every built-in into a custom override.
        const next = source
          ? (source.builtin ? [...custom, spec] : custom.map(row => row.name === source.name ? spec : row))
          : [...custom, spec];
        $('.agent-save').disabled = true; message.textContent = 'Saving…';
        await saveRows(next); close(); status.textContent = 'Runner saved.';
      } catch (error) { message.textContent = `${error.message} Your draft is retained.`; $('.agent-save').disabled = false; }
    };
    document.body.append(dialog); dialog.showModal(); $('.agent-name').focus();
  };
  const render = () => {
    list.replaceChildren();
    for (const spec of rows) {
      const card = document.createElement('div'); card.className = 'rowcard agent-card';
      const details = [spec.command, spec.model_flag ? `model ${spec.model_flag}` : 'no model flag', spec.env?.[providerEnv(spec)] ? `provider via ${providerEnv(spec)}` : 'provider via env/default'].join(' · ');
      card.innerHTML = `<div class="agent-card-main"><h3></h3><div class="sub"></div></div><div class="agent-card-actions"><button class="b agent-edit" type="button">Edit</button><button class="b no agent-delete" type="button">Delete</button></div>`;
      card.querySelector('h3').textContent = spec.name + (spec.builtin ? ' · built in' : ' · custom'); card.querySelector('.sub').textContent = details + (spec.builtin || spec.task ? ' · background tasks enabled' : ' · interactive sessions');
      card.querySelector('.agent-delete').hidden = !!spec.builtin;
      card.querySelector('.agent-edit').onclick = () => openEditor(spec);
      card.querySelector('.agent-delete').onclick = async () => {
        if (!confirm(`Delete the runner “${spec.name}”? Existing sessions keep their captured settings.`)) return;
        try { await saveRows(rows.filter(row => !row.builtin && row.name !== spec.name)); status.textContent = 'Agent removed.'; }
        catch (error) { status.textContent = error.message; }
      };
      list.append(card);
    }
    if (!rows.length) list.innerHTML = '<div class="hint">No runner definitions are available.</div>';
  };
  container.querySelector('[data-agent-add]').onclick = () => openEditor();
  const load = async () => {
    status.textContent = 'Loading runners…'; container.querySelector('[data-agent-add]').disabled = true;
    try { rows = await api('/agents'); render(); status.textContent = rows.length ? '' : 'Add a runner to make it available in sessions.'; }
    catch (error) { status.textContent = `Could not load runners: ${error.message}`; }
    finally { container.querySelector('[data-agent-add]').disabled = false; }
  };
  load();
}
