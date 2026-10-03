// Run on a disposable capture host with playwright-core + Chromium + ffmpeg.
// node capture-control-plane.cjs /path/to/lectern /empty/capture-directory
// Uses the application's mock executor: no real model or cluster claims.
const { chromium } = require('playwright-core');
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const binary = path.resolve(process.argv[2]);
const root = path.resolve(process.argv[3]);
fs.mkdirSync(root, {recursive:true});
if(fs.existsSync(path.join(root,'demo.db'))) throw Error('Use a fresh capture directory');
const base='http://127.0.0.1:9502';
const delay = ms=>new Promise(r=>setTimeout(r,ms));
async function api(p, body, method=body===undefined?'GET':'POST') {
 const res=await fetch(base+'/api'+p,{method,headers:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
 if(!res.ok)throw Error(p+' '+res.status+' '+await res.text());
 const s=await res.text(); return s?JSON.parse(s):null;
}
async function until(fn, ms=25000){const end=Date.now()+ms;while(Date.now()<end){try {if(await fn())return;}catch{}await delay(150);}throw Error('State wait timed out');}
const env={PATH:process.env.PATH,HOME:root,LANG:'C.UTF-8',LC_ALL:'C.UTF-8',XDG_STATE_HOME:path.join(root,'state'),
 LECTERN_MOCK:'1',LECTERN_HOST:'127.0.0.1',LECTERN_PORT:'9502',LECTERN_DB:path.join(root,'demo.db'),
 LECTERN_TICK:'0.15',LECTERN_MOCK_DELAY:'0.4',LECTERN_BASE_URL:base,LECTERN_AUTH:'none',LECTERN_GRIMOIRE_URL:'',
 LECTERN_HOST_CLAUDE_CONFIG:path.join(root,'no-config'),LECTERN_CREDS:path.join(root,'no-creds'),LECTERN_CODEX_CREDS:path.join(root,'no-codex')};
const log=fs.openSync(path.join(root,'server.log'),'w');
let proc;
let browser;
(async()=>{
 // Refuse to touch an existing listener: all API mutations belong to this fixture.
 await new Promise((resolve,reject)=>{const server=require('node:net').createServer();server.once('error',reject);server.listen(9502,'127.0.0.1',()=>server.close(resolve));});
 proc=spawn(binary,['serve'],{env,cwd:root,stdio:['ignore',log,log]});
 proc.on('error',error=>{console.error(error);process.exitCode=1;});

 await until(()=>api('/health'));
 for(const p of await api('/projects'))await api('/projects/'+p.id,undefined,'DELETE');
 for(const t of await api('/targets'))await api('/targets/'+t.id,undefined,'DELETE');
 const targets=[];
 for(const [name,kind] of [['Workstation','local'],['Build server','ssh'],['Disposable workers','sandbox']]) {
  const t=await api('/targets',{name,kind,host:kind==='ssh'?'build.example.test':'',max_concurrent:kind==='ssh'?1:3,workroot:'/workspace'});
  await api(`/targets/${t.id}/check`,{});targets.push(t);
 }
 const projects=[];
 for(const [name,i,agent] of [['web-console',0,'claude'],['api-service',1,'codex'],['release-tools',2,'gemini']])
  projects.push(await api('/projects',{name,target_id:targets[i].id,repo_path:'/workspace/'+name,default_agent:agent}));
 async function task(title,i=0,dispatch=false,extra='') {
  const t=await api('/tasks',{title,project_id:projects[i].id,prompt:'Implement the change, run checks and summarize the result. '+extra,agent:i===1?'codex':'claude',permission_mode:'default'});
  if(dispatch)await api(`/tasks/${t.id}/dispatch`,{});return t;
 }
 const review=await task('Add an API health endpoint',1,true);
 await until(async()=>(await api('/tasks/'+review.id)).status==='review');
 await task('Document sandbox cleanup',2);
 await task('Improve keyboard navigation',0);
 const gated=await task('Review deployment cleanup',0,true,'[mock:approval]');
 await until(async()=>(await api('/approvals')).length>0);
 for(const [name,i,agent] of [['Refine the web console',0,'claude'],['Review API changes',1,'codex'],['Plan the next release',2,'gemini']])
  await api('/sessions',{name,project_id:projects[i].id,agent});
 browser=await chromium.launch({headless:true,executablePath:'/usr/bin/chromium'});
 const context=await browser.newContext({colorScheme:'dark',viewport:{width:1440,height:900},recordVideo:{dir:path.join(root,'raw'),size:{width:1440,height:900}}});
 const page=await context.newPage();page.setDefaultTimeout(15000); const errors=[];page.on('pageerror',e=>errors.push(e.message));
 async function shot(name){await page.waitForTimeout(500);await page.screenshot({path:path.join(root,name+'.png')});}
 async function click(loc){await loc.scrollIntoViewIfNeeded();const b=await loc.boundingBox();await page.mouse.move(b.x+b.width/2,b.y+b.height/2,{steps:15});await delay(200);await loc.click();}
 await page.goto(base+'/#settings/machines');await page.locator('#conn-label').filter({hasText:'LIVE'}).waitFor();
 await page.getByRole('heading',{name:'Build server',exact:true}).waitFor();
 const start=Date.now();await shot('machines');await delay(1800);
 await click(page.locator('#tabbar [data-nav-target="tasks"]'));await page.locator('#board').waitFor();await shot('tasks');await delay(1200);
 await click(page.getByRole('button',{name:'New task',exact:false}).first());
 await page.locator('#f-project').selectOption(String(projects[1].id));
 await page.locator('#f-title').fill('Expose service health');
 await page.locator('#f-prompt').fill('Expose a health function returning an OK status. Run the checks and summarize the diff.');
 await shot('dispatch');await delay(1400);await click(page.locator('#f-go'));
 await until(async()=>(await api('/tasks')).some(t=>t.title==='Expose service health'&&t.status==='review'));
 await page.locator('.col.s-review .card').filter({hasText:'Expose service health'}).waitFor();await delay(1000);
 await click(page.locator('.col.s-review .card').filter({hasText:'Expose service health'}));
 await page.locator('#actions button').filter({hasText:'Diff'}).click();
 await page.locator('.dfile').first().waitFor();await shot('review');await delay(2200);
 const end=Date.now();await context.close();
 const source=await page.video().path();
 fs.writeFileSync(path.join(root,'video-timing.json'),JSON.stringify({start,end,duration:(end-start)/1000,source}));
 const phone=await browser.newContext({colorScheme:'dark',viewport:{width:390,height:844},isMobile:true,hasTouch:true,deviceScaleFactor:2,
 recordVideo:{dir:path.join(root,'phone-raw'),size:{width:390,height:844}}});
 const pp=await phone.newPage();pp.setDefaultTimeout(15000);await pp.goto(base+'/#approvals');await pp.getByRole('button',{name:'Allow once',exact:false}).first().waitFor();
 await pp.screenshot({path:path.join(root,'phone-approval.png')});await delay(1600);
 await pp.getByRole('button',{name:'Allow once',exact:false}).first().click();
 await until(async()=>(await api('/tasks/'+gated.id)).status==='review');await delay(1500);
 await pp.goto(base+'/#sessions');await pp.locator('.scard').first().waitFor();await delay(800);
 await pp.screenshot({path:path.join(root,'phone-sessions.png')});await delay(1000);await phone.close();
 const desk=await browser.newPage({colorScheme:'dark',viewport:{width:1440,height:900}});await desk.goto(base+'/#sessions');await desk.locator('.scard').first().waitFor();
 await desk.screenshot({path:path.join(root,'sessions.png')});await desk.close();
 assert.equal(errors.length,0,errors.join('\n'));
 const streams=[['control-plane',source,1440],['phone-approval',await pp.video().path(),390]];
 for(const [name,src,width] of streams){
  execFileSync('ffmpeg',['-y','-i',src,'-vf',`scale=${width}:-2,pad=iw:ih+52:0:52:color=0x0b1018,drawtext=text='${name==='control-plane'?'LECTERN | Dispatch across machines - review from anywhere':'Phone approval'}':x=16:y=10:fontsize=${width===390?14:22}:fontcolor=white,drawtext=text='Demo data - scripted agents':x=16:y=32:fontsize=${width===390?11:13}:fontcolor=0xaab4c5`,'-c:v','libx264','-crf','23','-pix_fmt','yuv420p','-movflags','+faststart',path.join(root,name+'.mp4')],{stdio:'ignore'});
 }
 // Compact hero excerpt; full video retains the complete observed workflow.
 execFileSync('ffmpeg',['-y','-i',source,'-vf','setpts=0.55*PTS,fps=10,scale=1000:-1:flags=lanczos,pad=iw:ih+38:0:38:color=0x0b1018,drawtext=text=Demo data - scripted agents:x=12:y=10:fontsize=16:fontcolor=white,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer','-loop','0',path.join(root,'dispatch-review.gif')],{stdio:'ignore'});
 fs.writeFileSync(path.join(root,'capture-report.json'),JSON.stringify({build:await api('/health'),fixture:'Scripted mock executors; simulated targets; real app/API and approval state transitions',checks:['machine probes','UI task dispatch to project-selected target','task reaches review','diff rendered','phone approval unblocks task','current desktop navigation','no page errors'],targets:targets.map(t=>({name:t.name,kind:t.kind})),errors},null,2));
 console.log('PASS: captured machines, dispatch, review, sessions and phone approval');
})().catch(e=>{console.error(e);process.exitCode=1}).finally(async()=>{if(browser)await browser.close();proc?.kill('SIGTERM');});
