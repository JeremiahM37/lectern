// Supporting real file workflow on a disposable local target. No model calls.
// node capture-file-workflow.cjs /path/to/lectern /fresh/output
const {chromium}=require('playwright-core');
const {spawn,execFileSync}=require('node:child_process');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const binary=path.resolve(process.argv[2]),root=path.resolve(process.argv[3]);
fs.mkdirSync(root,{recursive:true});if(fs.existsSync(path.join(root,'demo.db')))throw Error('Use a fresh directory');
const work=path.join(root,'workspace');fs.mkdirSync(work);
const base='http://127.0.0.1:9503';
const env={PATH:process.env.PATH,HOME:root,LANG:'C.UTF-8',LC_ALL:'C.UTF-8',XDG_STATE_HOME:path.join(root,'state'),
 TMUX_TMPDIR:root,TMUX:'',LECTERN_MOCK:'0',LECTERN_HOST:'127.0.0.1',LECTERN_PORT:'9503',LECTERN_DB:path.join(root,'demo.db'),
 LECTERN_AUTH:'none',LECTERN_GRIMOIRE_URL:'',LECTERN_SESSION_POLL:'3600',LECTERN_TICK:'1',LECTERN_SCRATCH_ROOT:path.join(root,'scratch')};
const command=(cmd,args)=>execFileSync(cmd,args,{env,stdio:'pipe'}).toString();
const delay=ms=>new Promise(r=>setTimeout(r,ms));
async function until(fn){for(let i=0;i<160;i++){try{if(await fn())return;}catch{}await delay(150);}throw Error('Timed out');}
async function api(p,b){const r=await fetch(base+'/api'+p,{method:b?'POST':'GET',headers:{'Content-Type':'application/json'},body:b?JSON.stringify(b):undefined});if(!r.ok)throw Error(p+': '+r.status+' '+await r.text());return r.json();}
command('git',['init','-q',work]);
fs.writeFileSync(path.join(work,'README.md'),'# Release workspace\n\nReview the plan, then upload supporting files.\n');
// One-page PDF with harmless public demo text.
const stream='BT /F1 26 Tf 40 330 Td (Release plan) Tj 0 -48 Td /F1 14 Tf (1. Dispatch work to the chosen machine) Tj 0 -30 Td (2. Review changes and checks) Tj 0 -30 Td (3. Approve from your desk or phone) Tj ET';
const objects=['<< /Type /Catalog /Pages 2 0 R >>','<< /Type /Pages /Kids [3 0 R] /Count 1 >>','<< /Type /Page /Parent 2 0 R /MediaBox [0 0 500 400] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>','<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',`<< /Length ${stream.length} >>\nstream\n${stream}\nendstream`];
let pdf='%PDF-1.4\n',offsets=[];objects.forEach((o,i)=>{offsets.push(Buffer.byteLength(pdf));pdf+=`${i+1} 0 obj\n${o}\nendobj\n`;});const xref=Buffer.byteLength(pdf);pdf+='xref\n0 6\n0000000000 65535 f \n'+offsets.map(o=>String(o).padStart(10,'0')+' 00000 n \n').join('')+`trailer\n<< /Root 1 0 R /Size 6 >>\nstartxref\n${xref}\n%%EOF\n`;fs.writeFileSync(path.join(work,'release-plan.pdf'),pdf);
command('git',['-C',work,'add','.']);command('git',['-C',work,'-c','user.name=Demo','-c','user.email=demo@example.test','commit','-qm','Sample release workspace']);
command('tmux',['-f','/dev/null','new-session','-d','-s','capture-files','-c',work,'bash','--norc']);
let proc;let browser;const gui=[];
(async()=>{
 // Refuse to touch an existing listener: all API mutations belong to this fixture.
 await new Promise((resolve,reject)=>{const server=require('node:net').createServer();server.once('error',reject);server.listen(9503,'127.0.0.1',()=>server.close(resolve));});
 proc=spawn(binary,['serve'],{env,cwd:work,stdio:['ignore',fs.openSync(path.join(root,'server.log'),'w'),'ignore']});
 proc.on('error',error=>{console.error(error);process.exitCode=1;});

 await until(()=>api('/health'));const t=await api('/targets',{name:'Capture workstation',kind:'local'});
 const s=await api('/sessions/adopt',{target_id:t.id,tmux_session:'capture-files',workdir:work,name:'Review the release workspace',agent:'shell'});
 command('tmux',['send-keys','-t','=capture-files:',"clear; printf '\\nRelease workspace\\n\\nOpen the plan: release-plan.pdf\\n\\nDrop supporting files into this workspace.\\n'",'Enter']);
 browser=await chromium.launch({headless:true,executablePath:'/usr/bin/chromium'});
 const ctx=await browser.newContext({colorScheme:'dark',viewport:{width:1440,height:900},recordVideo:{dir:path.join(root,'raw'),size:{width:1440,height:900}}});
 const p=await ctx.newPage();p.setDefaultTimeout(15000);const errors=[];p.on('pageerror',e=>errors.push(e.message));
 await p.goto(base+`/terminal/session/${s.id}`);await p.locator('#connection').filter({hasText:'Connected'}).waitFor();
 await p.locator('#agent-terminal .xterm-screen').filter({hasText:'release-plan.pdf'}).waitFor();await delay(700);
 await p.screenshot({path:path.join(root,'web-terminal.png')});
 // Click the actual terminal path link, using the same hover/click route a user does.
 const point=await p.evaluate(()=>{
 const row=[...document.querySelectorAll('#agent-terminal .xterm-rows > div')].find(r=>r.textContent.includes('release-plan.pdf'));
 const walker=document.createTreeWalker(row,NodeFilter.SHOW_TEXT);let node,at=row.textContent.indexOf('release-plan.pdf')+5,seen=0;
 while((node=walker.nextNode())){if(seen+node.length>at){const r=document.createRange();r.setStart(node,at-seen);r.setEnd(node,at-seen+1);const b=r.getBoundingClientRect();return {x:b.left+b.width/2,y:b.top+b.height/2};}seen+=node.length;}
 });
 await p.mouse.move(point.x-3,point.y);await p.mouse.move(point.x,point.y);await p.locator('#agent-terminal .xterm-cursor-pointer').waitFor();await delay(350);await p.mouse.click(point.x,point.y);
 await p.locator('.wb-pdf-stage canvas').first().waitFor();await delay(1200);await p.screenshot({path:path.join(root,'pdf-preview.png')});await delay(1700);
 // HTML5 file drop through the browser's File/DataTransfer APIs, then verify disk bytes.
 const text='# Acceptance checks\n\n- Tests pass\n- Changes reviewed\n- Rollback documented\n';
 const target=p.locator('#file-list');
 await target.waitFor();const b=await target.boundingBox();await p.mouse.move(b.x+b.width/2,b.y+b.height/2,{steps:12});
 await p.evaluate(({text})=>{const dt=new DataTransfer();dt.items.add(new File([text],'acceptance-checks.md',{type:'text/markdown'}));const el=document.querySelector('#file-list');for(const type of ['dragenter','dragover','drop'])el.dispatchEvent(new DragEvent(type,{bubbles:true,cancelable:true,dataTransfer:dt}));},{text});
 await p.evaluate(()=>document.querySelector('#file-list').dispatchEvent(new DragEvent('dragleave',{bubbles:true,cancelable:true})));
 await until(()=>fs.existsSync(path.join(work,'acceptance-checks.md')));assert.equal(fs.readFileSync(path.join(work,'acceptance-checks.md'),'utf8'),text);
 await p.locator('.wb-row[data-path="acceptance-checks.md"]').getByRole('button',{name:'acceptance-checks.md',exact:true}).click();await delay(1200);await p.screenshot({path:path.join(root,'file-upload.png')});await delay(1600);
 assert.equal(errors.length,0,errors.join('\n'));await ctx.close();const video=await p.video().path();
 execFileSync('ffmpeg',['-y','-i',video,'-c:v','libx264','-crf','23','-pix_fmt','yuv420p','-movflags','+faststart',path.join(root,'files.mp4')],{stdio:'ignore'});
 // A private X display keeps the shared logged-in desktop untouched.
 const display=':98', guiEnv={...env,DISPLAY:display,LECTERN_API:base,TERM:'xterm-256color'};
 const xvfb=spawn('Xvfb',[display,'-screen','0','1280x800x24','-nolisten','tcp'],{env:guiEnv,stdio:'ignore'});gui.push(xvfb);await delay(700);
 const wm=spawn('openbox',[],{env:guiEnv,stdio:'ignore'});gui.push(wm);await delay(400);
 const term=spawn('kitty',['--class','LecternCapture','--title','Lectern terminal client','--override','font_size=16','--override','initial_window_width=1280','--override','initial_window_height=800','--override','confirm_os_window_close=0',binary,'attach','session',String(s.id)],{env:guiEnv,stdio:'ignore'});gui.push(term);await delay(2500);
 execFileSync('ffmpeg',['-y','-f','x11grab','-video_size','1280x800','-i',display,'-frames:v','1',path.join(root,'native-cli.png')],{stdio:'ignore'});
 fs.writeFileSync(path.join(root,'file-report.json'),JSON.stringify({build:await api('/health'),mock:false,fixture:'real local target on isolated capture host; HTML5 File/DataTransfer drop',checks:['real terminal connected','terminal PDF link opens rendered PDF','file drop writes exact bytes to target workspace','no page errors'],errors},null,2));
 console.log('PASS: real PDF preview and file drop verified on disk');
})().catch(e=>{console.error(e);process.exitCode=1}).finally(async()=>{for(const p of gui.reverse())p.kill('SIGTERM');if(browser)await browser.close();proc?.kill('SIGTERM');try{command('tmux',['send-keys','-t','=capture-files:','C-c']);await delay(150);command('tmux',['send-keys','-t','=capture-files:','-l','exit']);command('tmux',['send-keys','-t','=capture-files:','Enter']);}catch{}});
