const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const uid = () => globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(36).slice(2)}`;
let currentClose;

// The reader keeps its DOM and composer while data refreshes. A stream update
// must never erase a draft, move the caret, close the keyboard, or steal scroll.
export function openConversation({kind, id, name, api, attachMic, onClose}) {
  currentClose?.();
  const root = document.createElement('section');
  root.id = 'conversation'; root.className = 'conversation';
  root.setAttribute('role', 'dialog'); root.setAttribute('aria-modal', 'true'); root.setAttribute('aria-labelledby','conversation-title');
  const draftKey = `lec-draft-${kind}-${id}`;
  let draft = {};
  try { draft = JSON.parse(localStorage.getItem(draftKey) || '{}'); } catch {}
  root.innerHTML = `<header class="conversation-head"><div><h2 id="conversation-title">${esc(name)}</h2><p id="conversation-status" role="status">Connecting…</p></div><button class="b" id="conversation-close" aria-label="Close conversation">✕</button></header>
    <div class="reader-controls"><span>${kind === 'session' ? 'Live output · last 500 lines' : 'Task conversation'}</span><button class="b" id="reader-smaller" aria-label="Smaller text">A−</button><button class="b" id="reader-larger" aria-label="Larger text">A+</button><button class="b" id="reader-latest">↓ Latest</button></div>
    <div id="conversation-error" role="status" hidden></div>
    <div id="conversation-log" tabindex="0" aria-label="Agent output"><p class="reader-empty">Loading…</p></div>
    <div id="conversation-approvals"></div>
    <form id="conversation-compose"><label for="conversation-input">Message ${kind === 'session' ? 'this agent' : 'this task'}</label>
      <textarea id="conversation-input" rows="3" maxlength="32000" placeholder="Write a message or use your keyboard’s microphone…"></textarea>
      <input type="file" id="conversation-files" multiple hidden><div id="conversation-attachments" aria-label="Attached files"></div>
      <p id="conversation-upload-status" role="status"></p>
      <p id="conversation-hint">${kind === 'session' ? 'Sends to the same running session. Enter adds a new line.' : 'Follow-ups wait for the current run, then continue in the same worktree.'}</p>
      <div class="compose-actions"><button type="button" class="b" id="conversation-attach" title="PDFs, images, documents and other files · 25 MiB each">📎 Attach</button>${kind === 'task' ? '<label class="interrupt-option"><input id="conversation-interrupt" type="checkbox"> Interrupt and send</label>' : '<button type="button" class="b warn" id="conversation-interrupt-session">Interrupt</button>'}<button type="button" class="b" id="conversation-mic" aria-label="Dictate message">🎙</button><button type="submit" class="b ok grow" id="conversation-send">Send</button></div>
      <p id="conversation-receipt" role="status"></p>
    </form>`;
  document.body.append(root);
  const background=[...document.body.children].filter(el=>el!==root).map(el=>[el,el.inert]);
  background.forEach(([el])=>{el.inert=true;});
  const $ = selector => root.querySelector(selector);
  const input = $('#conversation-input'), log = $('#conversation-log'), status = $('#conversation-status');
  input.value = draft.text || '';
  const interrupt = $('#conversation-interrupt');
  if (interrupt) interrupt.checked = !!draft.interrupt;
  let uploading = false, unavailable = false;
  const uploadAbort = new AbortController();
  let attachments = Array.isArray(draft.attachments) ? draft.attachments : [];
  let closed = false, busy = false, sending = false, follow = true, lastSignature = '', latestTask;
  let font = Math.max(16,Math.min(24,Number(localStorage.getItem('lec-reader-font')) || 17));
  const applyFont = () => { root.style.setProperty('--reader-font', `${font}px`); localStorage.setItem('lec-reader-font', font); };
  applyFont();
  $('#reader-smaller').onclick = () => { font=Math.max(16,font-1); applyFont(); };
  $('#reader-larger').onclick = () => { font=Math.min(24,font+1); applyFont(); };
  const bottom = () => { follow = true; log.scrollTop = log.scrollHeight; };
  $('#reader-latest').onclick = bottom;
  log.onscroll = () => { follow=log.scrollHeight-log.clientHeight-log.scrollTop < 70; };
  const persist = () => {
    if (draft.text !== input.value || draft.interrupt !== !!interrupt?.checked) draft.request_id=uid();
    draft.text=input.value; draft.interrupt=!!interrupt?.checked; draft.attachments=attachments;
    localStorage.setItem(draftKey, JSON.stringify(draft));
  };
  input.addEventListener('input',persist); interrupt?.addEventListener('change',persist);
  input.onkeydown = e => { if (e.key==='Enter' && (e.ctrlKey || e.metaKey) && !e.isComposing) { e.preventDefault(); $('#conversation-compose').requestSubmit(); } };
  attachMic($('#conversation-mic'),input);
  const viewport = () => {
    root.style.height=`${window.visualViewport?.height || innerHeight}px`;
    root.style.top=`${window.visualViewport?.offsetTop || 0}px`;
  };
  viewport(); window.visualViewport?.addEventListener('resize',viewport); window.visualViewport?.addEventListener('scroll',viewport);
  const priorOverflow=document.body.style.overflow; document.body.style.overflow='hidden';
  const priorFocus=document.activeElement;
  const close=()=>{
    if (closed) return; closed=true; persist(); uploadAbort.abort(); clearInterval(timer);
    window.visualViewport?.removeEventListener('resize',viewport); window.visualViewport?.removeEventListener('scroll',viewport);
    document.body.style.overflow=priorOverflow; background.forEach(([el,was])=>{el.inert=was;}); root.remove(); priorFocus?.focus(); currentClose=null; onClose?.();
  };
  currentClose=close; $('#conversation-close').onclick=close;
  root.onkeydown = e => {
    if (e.key==='Escape') { e.stopPropagation(); close(); }
    if (e.key==='Tab') {
      const els=[...root.querySelectorAll('button:not([disabled]),textarea:not([disabled]),input:not([disabled]),[tabindex="0"]')].filter(e=>e.offsetParent!==null);
      if (e.shiftKey && document.activeElement===els[0]) { e.preventDefault(); els.at(-1).focus(); }
      else if (!e.shiftKey && document.activeElement===els.at(-1)) { e.preventDefault(); els[0].focus(); }
    }
  };
  $('#conversation-close').focus({preventScroll:true});
  const showError = text => { $('#conversation-error').hidden=!text; $('#conversation-error').textContent=text; };
  const updateCompose = () => {
    $('#conversation-send').disabled = sending || uploading || (kind==='session' && unavailable);
    $('#conversation-attach').disabled = sending || uploading || unavailable;
    $('#conversation-files').disabled = sending || uploading || unavailable;
    $('#conversation-attach').title = unavailable ? 'Attachments unavailable for this session or sandbox' : 'PDFs, images, documents and other files · 25 MiB each';
  };
  const renderAttachments = () => {
    const list=$('#conversation-attachments'); list.replaceChildren();
    for (const a of attachments) {
      const chip=document.createElement('div');chip.className='context-attachment';
      const label=document.createElement('span');label.textContent=`${a.name} · ${Math.max(1,Math.ceil(a.size/1024))} KB`;label.title=a.path;
      const remove=document.createElement('button');remove.type='button';remove.className='b';remove.textContent='×';remove.setAttribute('aria-label',`Remove ${a.name}`);remove.disabled=sending;
      remove.onclick=()=>{attachments=attachments.filter(item=>item!==a);draft.request_id=uid();persist();renderAttachments();};
      chip.append(label,remove);list.append(chip);
    }
  };
  renderAttachments();
  const uploadFiles = async files => {
    if (uploading || sending || unavailable || !files.length) return;
    uploading=true;updateCompose();
    const progress=$('#conversation-upload-status');
    try {
      for (const file of files) {
        if (closed) return;
        if (attachments.length >= 10) throw new Error('Attach up to 10 files per message.');
        if (file.size > 25*1024*1024) throw new Error(`${file.name} exceeds 25 MiB.`);
        progress.textContent=`Uploading ${file.name}…`;
        const body=new FormData();body.append('file',file);
        const attachment=await api(`/${kind==='session'?'sessions':'tasks'}/${id}/attachments`,{method:'POST',body,signal:uploadAbort.signal});
        if (closed) return;
        attachments.push(attachment);draft.request_id=uid();persist();renderAttachments();
      }
      progress.textContent='Files ready. Add a message, then Send.';
    } catch(e) { if(!closed) progress.textContent=`Upload failed: ${e.message} Previously uploaded files are kept.`; }
    finally { uploading=false;if(!closed){$('#conversation-files').value='';updateCompose();} }
  };
  $('#conversation-attach').onclick=()=>$('#conversation-files').click();
  $('#conversation-files').onchange=e=>uploadFiles([...e.target.files]);
  const composer=$('#conversation-compose');
  composer.addEventListener('dragover',e=>{if([...e.dataTransfer.types].includes('Files')){e.preventDefault();composer.classList.add('file-drag');}});
  composer.addEventListener('dragleave',()=>composer.classList.remove('file-drag'));
  composer.addEventListener('drop',e=>{if(e.dataTransfer.files.length){e.preventDefault();composer.classList.remove('file-drag');uploadFiles([...e.dataTransfer.files]);}});
  input.addEventListener('paste',e=>{if(e.clipboardData.files.length){e.preventDefault();uploadFiles([...e.clipboardData.files]);}});
  const updateLog = (signature, fill) => {
    if (signature===lastSignature) return;
    const scroll=log.scrollTop, shouldFollow=follow;
    fill(); lastSignature=signature;
    log.scrollTop=shouldFollow?log.scrollHeight:scroll; follow=shouldFollow;
  };
  async function refresh() {
    if (closed || busy || document.hidden) return;
    busy=true;
    try {
      if (kind==='session') {
        const data=await api(`/sessions/${id}/reader`); if (closed) return;
        status.textContent=`${data.session.agent} · ${data.ended?'Ended':data.session.status} · live reader`;
        unavailable=data.ended; updateCompose();
        $('#conversation-interrupt-session').disabled=data.ended;
        updateLog(data.text,()=>{ const pre=document.createElement('pre');pre.className='session-reader';pre.textContent=data.text||'Waiting for agent output…';log.replaceChildren(pre); });
      } else {
        const [task,events,messages,approvals]=await Promise.all([api(`/tasks/${id}`),api(`/tasks/${id}/events`),api(`/tasks/${id}/messages`),api('/approvals?status=pending')]);
        if (closed) return; latestTask=task; unavailable=task.target_kind==='sandbox' || Boolean(task.takeover); updateCompose();
        const lastMessage=messages.at(-1);
        if (lastMessage && !sending && !input.value && lastMessage.status!=='pending') $('#conversation-receipt').textContent=lastMessage.status==='failed' ? `Not delivered: ${lastMessage.error}` : lastMessage.attempt_id ? 'Message delivered to the agent.' : 'Instructions added to the task.';
        status.textContent=`${task.agent} · ${task.status}${task.attempt ? ` · turn ${task.attempt.n}` : ''}`;
        const running=task.status==='running';
        interrupt.disabled=!running || task.target_kind==='sandbox';
        if (interrupt.disabled) interrupt.checked=false;
        $('#conversation-hint').textContent=task.takeover ? 'This run is continuing as an interactive session. Close Chat and choose Open session.' : task.target_kind==='sandbox' && task.status!=='backlog' ? 'Send queues a new sandbox run with your instructions and the previous result.' : task.status==='backlog' ? 'Adds instructions to this task without dispatching it.' : running ? 'Send queues a follow-up. Interrupt and send stops this run first, then continues.' : task.status==='queued' ? 'Your message is added before the queued run starts.' : 'Send continues the task in its existing worktree.';
        const signature=JSON.stringify([task.prompt,events,messages]);
        updateLog(signature,()=>renderTaskLog(log,task,events,messages));
        const ap=$('#conversation-approvals'); ap.replaceChildren();
        for (const a of approvals.filter(a=>a.task_id===id)) {
          const card=document.createElement('div');card.className='reader-approval';
          card.innerHTML=`<strong>Approval needed: ${esc(a.tool_name)}</strong><pre>${esc(JSON.stringify(a.input || a.input_json || {}))}</pre>`;
          for (const [label,decision] of [['Approve','approved'],['Deny','denied']]) {
            const b=document.createElement('button');b.type='button';b.className='b';b.textContent=label;
            b.onclick=async()=>{try{await api(`/approvals/${a.id}/decision`,{method:'POST',body:{decision}});refresh();}catch(e){showError(e.message)}};card.append(b);
          } ap.append(card);
        }
      }
      showError('');
    } catch(e) { if(!closed) showError(`Could not refresh: ${e.message}. Displayed output may be stale.`); }
    finally { busy=false; }
  }
  $('#conversation-interrupt-session')?.addEventListener('click',async()=>{
    try { await api(`/sessions/${id}/send`,{method:'POST',body:{key:'escape'}});$('#conversation-receipt').textContent='Interrupt sent. Check the agent output before sending new instructions.';refresh(); }
    catch(e){showError(e.message)}
  });
  $('#conversation-compose').onsubmit=async e=>{
    e.preventDefault(); if(sending || uploading || (!input.value.trim() && !attachments.length)) return;
    persist(); const submitted={...draft,request_id:draft.request_id||uid()};draft.request_id=submitted.request_id;persist();
    const text=submitted.text + (attachments.length ? '\n\nUse these attached files as context (paths on this agent’s machine):\n' + JSON.stringify(attachments.map(a=>({name:a.name,path:a.path})),null,2) : '');
    if(new TextEncoder().encode(text).length>32000){showError('Message including attachments exceeds 32000 bytes. Shorten the message.');return;}
    sending=true;updateCompose();renderAttachments();$('#conversation-receipt').textContent='Sending…';
    try {
      const receipt=await api(kind==='session'?`/sessions/${id}/send`:`/tasks/${id}/messages`,{method:'POST',body:kind==='session'?{text}:{...submitted,text}});
      if(closed)return;
      // Do not erase text typed while the request was in flight.
      attachments=[];renderAttachments();$('#conversation-upload-status').textContent='';
      if(input.value===submitted.text){input.value='';draft={};}persist();
      $('#conversation-receipt').textContent=kind==='session'?'Sent to the session.':receipt.status==='delivered'?'Already delivered.':submitted.interrupt?'Saved. Interrupting the current run before continuing.':'Saved. Waiting for delivery to the agent.';
      refresh();
    }catch(e){if(!closed){$('#conversation-receipt').textContent=`Could not confirm delivery: ${e.message}. Your draft is kept.`;}}
    finally{sending=false;if(!closed){updateCompose();renderAttachments();}}
  };
  const timer=setInterval(refresh,2000);refresh();
}

function renderTaskLog(log,task,events,messages) {
  const opened=new Set([...log.querySelectorAll('details[open]')].map(e=>e.dataset.event));
  const rows=[{time:task.created_at||0,type:'operator',text:task.prompt||task.title,label:'Task'}];
  for(const m of messages)rows.push({time:m.created_at,type:'operator',text:m.text,label:m.status==='pending'?'You · queued for next turn':m.status==='failed'?`Not delivered · ${m.error}`:m.attempt_id?'You · delivered to agent':'You · added to task'});
  for(const e of events){const p=e.payload||{};
    if(e.type==='text')rows.push({time:e.ts,type:'agent',text:p.text,label:`Agent · turn ${e.attempt_n}`});
    else if(e.type==='result')rows.push({time:e.ts,type:'agent',text:p.result||p.text||JSON.stringify(p),label:`Result · turn ${e.attempt_n}`});
    else if(['tool_use','tool_result','verify'].includes(e.type))rows.push({time:e.ts,type:'detail',text:typeof p.content==='string'?p.content:JSON.stringify(p,null,2),label:e.type==='tool_use'?p.name||'Tool':e.type.replace('_',' '),id:String(e.id)});
  }
  rows.sort((a,b)=>(a.time||0)-(b.time||0));
  const fragment=document.createDocumentFragment();
  for(const r of rows){const el=document.createElement(r.type==='detail'?'details':'article');el.className=`reader-message ${r.type}`;
    if(r.type==='detail'){el.dataset.event=r.id;el.open=opened.has(r.id);el.innerHTML=`<summary>${esc(r.label)}</summary><pre></pre>`;el.querySelector('pre').textContent=r.text;}
    else{el.innerHTML=`<div class="reader-speaker">${esc(r.label)}</div><div class="reader-text"></div>`;el.querySelector('.reader-text').textContent=r.text;}
    fragment.append(el);
  }
  log.replaceChildren(fragment);
}
