/// <reference lib="webworker" />
import {verifyManifest,sha256Hex,manifestKey} from './relay/shell';
const worker=self as unknown as ServiceWorkerGlobalScope;
declare const __CACHE_NAME__:string;
declare const __STATIC_ASSETS__:string[];
const CACHE=__CACHE_NAME__;
const API=/^\/(api|term|a2a)(\/|$)/;
// ---- encrypted relay (docs/relay.md "Trusting the app code") ----
// Once this device is paired over the relay, IndexedDB holds the host's shell
// signing key. From then on this worker serves the app shell from its cache
// only, and a new shell is accepted only if Lectern's signed manifest verifies
// against that key and every file matches it. Requests to Lectern's own API
// that the page did not send through its tunnel (images, downloads) are
// handed to an open page, which fetches them over the tunnel.
function idbGet(key:string):Promise<unknown>{return new Promise(resolve=>{
 const open=indexedDB.open('lectern-relay',1);
 open.onupgradeneeded=()=>open.result.createObjectStore('kv');
 open.onerror=()=>resolve(undefined);
 open.onsuccess=()=>{const db=open.result;try{const req=db.transaction('kv','readonly').objectStore('kv').get(key);req.onsuccess=()=>{resolve(req.result);db.close();};req.onerror=()=>{resolve(undefined);db.close();};}catch{resolve(undefined);db.close();}};
});}
interface RelayState{pinnedKey?:string;paired:boolean}
let relayState:RelayState|undefined;
async function loadRelayState():Promise<RelayState>{
 const key=await idbGet('shell-key');
 relayState={pinnedKey:typeof key==='string'?key:undefined,paired:(await idbGet('pairing'))!=null};
 return relayState;
}
const relayReady=loadRelayState();
// pinShell fills cache with every file the host's signed manifest lists,
// checking each one. Any mismatch throws: nothing unverified is served.
async function pinShell(cache:Cache,key:string){
 const signed=await (await fetch('/shell-manifest.json',{cache:'no-store'})).json();
 const files=verifyManifest(signed,key);
 if(!files)throw new Error('the shell manifest is not signed by this Lectern');
 for(const path of Object.keys(files)){
  const response=await fetch(path,{cache:'no-store'});
  if(!response.ok)throw new Error('could not fetch '+path);
  const body=await response.arrayBuffer();
  if(await sha256Hex(body)!==files[manifestKey(path)])throw new Error('shell file does not match the signed manifest: '+path);
  await cache.put(path,new Response(body,{headers:response.headers}));
 }
}
worker.addEventListener('install',event=>event.waitUntil((async()=>{
 const cache=await caches.open(CACHE);
 const state=await relayReady;
 // A paired device installs a new worker only if its shell verifies; a
 // throw here discards the new worker and keeps the current one.
 if(state.pinnedKey)await pinShell(cache,state.pinnedKey);
 else await cache.addAll(__STATIC_ASSETS__);
 await worker.skipWaiting();
})()));
worker.addEventListener('activate',event=>event.waitUntil((async()=>{await Promise.all((await caches.keys()).filter(key=>key.startsWith('lectern-')&&key!==CACHE).map(key=>caches.delete(key)));await worker.clients.claim();})()));
async function windowClient(clientId:string):Promise<Client|undefined>{
 const own=clientId?await worker.clients.get(clientId):undefined;
 if(own)return own;
 return (await worker.clients.matchAll({type:'window'}))[0];
}
// relayFetch asks an open page to make a request over its tunnel.
async function relayFetch(clientId:string,url:string,method:string,headers:[string,string][],body?:string):Promise<Response>{
 const client=await windowClient(clientId);
 if(!client)return new Response('Lectern is reachable only through the encrypted relay; open the app first.',{status:503});
 const channel=new MessageChannel();
 return new Promise<Response>(resolve=>{
  let controller:ReadableStreamDefaultController<Uint8Array>|undefined;
  const stream=new ReadableStream<Uint8Array>({start:c=>{controller=c;}});
  // A page that never answers (closed mid-request) must not hang the load.
  const timer=setTimeout(()=>resolve(new Response('Lectern relay: no answer from the app',{status:504})),30000);
  channel.port1.onmessage=event=>{
   const d=event.data as {type:string;status?:number;headers?:[string,string][];data?:ArrayBuffer;message?:string};
   if(d.type!=='chunk'&&d.type!=='end')clearTimeout(timer);
   if(d.type==='direct'){resolve(fetch(url,{method,headers,body}));channel.port1.close();}
   else if(d.type==='head'){const empty=[101,204,205,304].includes(d.status||0)||method==='HEAD';resolve(new Response(empty?null:stream,{status:d.status,headers:d.headers}));}
   else if(d.type==='chunk'&&d.data)controller?.enqueue(new Uint8Array(d.data));
   else if(d.type==='end'){controller?.close();channel.port1.close();}
   else if(d.type==='error'){resolve(new Response('Lectern relay: '+(d.message||'request failed'),{status:502}));try{controller?.error(new Error(d.message));}catch{}channel.port1.close();}
  };
  client.postMessage({type:'lec-relay-fetch',url,method,headers,body},[channel.port2]);
 });
}
worker.addEventListener('fetch',event=>{
 const request=event.request,url=new URL(request.url);
 if(url.origin!==worker.location.origin)return;
 if(API.test(url.pathname)){
  // Only a relay device needs these; everyone else goes to the network as
  // before, untouched by this worker.
  if(request.method!=='GET'&&request.method!=='HEAD')return;
  if(relayState&&!relayState.paired)return;
  event.respondWith((async()=>{
   const state=await relayReady;
   if(!state.paired)return fetch(request);
   const headers:[string,string][]=[];request.headers.forEach((v,k)=>headers.push([k,v]));
   return relayFetch(event.clientId||event.resultingClientId,request.url,request.method,headers);
  })());
  return;
 }
 if(request.method!=='GET')return;
 const immutable=url.pathname.startsWith('/react/assets/')||url.pathname.startsWith('/fonts/')||url.pathname==='/icon.svg';
 event.respondWith((async()=>{
  const cache=await caches.open(CACHE);
  const state=relayState??await relayReady;
  if(state.pinnedKey){
   // Pinned: the network is never asked for code.
   if(request.mode==='navigate'){
    const shell=url.pathname.startsWith('/terminal/')?'/terminal.html':'/';
    return await cache.match(shell)||new Response('The Lectern app is not installed on this device.',{status:503});
   }
   return await cache.match(request,{ignoreSearch:true})||new Response('',{status:404});
  }
  if(immutable){const cached=await cache.match(request);if(cached)return cached;}
  try{const response=await fetch(request);if(response.ok)await cache.put(request,response.clone());return response;}
  catch{return await cache.match(request)||(request.mode==='navigate'?await cache.match('/'):undefined)||new Response('Lectern is offline',{status:503});}
 })());
});
import {actionURL,buildNotificationPlan,confirmationNotification,decisionForAction,decisionRequestInit,decisionURL,type PushData} from './sw-actions';
import {applyBadge,needsBadge,type BadgeNavigator} from './badge';
// The open page tells this worker its last-known badge count on every SSE
// refresh (postMessage — a worker has no other way to read live app state).
// A push the worker itself decides is actionable bumps that remembered count
// by one; the page's own next refresh corrects it exactly either way, so this
// only has to be approximately right between refreshes, never authoritative.
let badgeCount=0;
worker.addEventListener('message',event=>{
 const data=event.data as {type?:string;count?:number}|undefined;
 if(data?.type==='lec-badge-count'&&typeof data.count==='number')badgeCount=Math.max(0,data.count);
 // Sent right after pairing: pin the shell now, while its install origin is
 // still reachable, and report whether it verified.
 if(data?.type==='lec-relay-pinned'){
  const port=event.ports[0];
  event.waitUntil((async()=>{
   try{const state=await loadRelayState();if(state.pinnedKey)await pinShell(await caches.open(CACHE),state.pinnedKey);port?.postMessage({ok:true});}
   catch(err){port?.postMessage({ok:false,error:String(err instanceof Error?err.message:err)});}
  })());
 }
});
worker.addEventListener('push',event=>{
 let data:PushData={};try{data=event.data?.json() as PushData||{};}catch{}
 const plan=buildNotificationPlan(data);
 if(needsBadge(data.kind)){badgeCount+=1;applyBadge(navigator as unknown as BadgeNavigator,badgeCount);}
 event.waitUntil(worker.registration.showNotification(plan.title,plan.options));
});
function resolveAppURL(raw:string|undefined):URL{
 const url=new URL(raw||'/',worker.location.origin);
 return url.origin===worker.location.origin?url:new URL('/',worker.location.origin);
}
async function openApp(url:URL){
 const windows=await worker.clients.matchAll({type:'window',includeUncontrolled:true});
 const app=windows[0];
 if(app){await app.navigate(url.href);await app.focus();}
 else await worker.clients.openWindow(url.href);
}
// An Approve/Deny action decides straight from the tray — no window is
// opened for it — then shows a confirmation notification (or a failure one,
// docs/agent-events.md section 3: "Handle failure (e.g. 403/expired) with a
// follow-up notification"). Tapping the notification body (event.action==='')
// keeps the pre-existing behavior of opening the app at the deep link.
// "terminal"/"reply" open the app at a hash the router understands, focused
// on that one session, rather than the raw push URL (see actionURL).
worker.addEventListener('notificationclick',event=>{
 const data=event.notification.data as {url?:string;approvalId?:number;sessionId?:number}|undefined;
 const decision=decisionForAction(event.action);
 event.notification.close();
 if(decision&&data?.approvalId!=null){
  const approvalId=data.approvalId;
  event.waitUntil((async()=>{
   let ok=false;
   const state=await relayReady;
   try{
    // A relay device has no direct route to Lectern: an open page sends the
    // decision over its tunnel.
    const init=decisionRequestInit(decision);
    const resp=state.paired?await relayFetch('',new URL(decisionURL(approvalId),worker.location.origin).href,'POST',[['Content-Type','application/json']],init.body as string):await fetch(decisionURL(approvalId),init);
    ok=resp.ok;
   }catch{ok=false;}
   const confirmation=confirmationNotification(decision,ok);
   await worker.registration.showNotification(confirmation.title,{body:confirmation.body,icon:'/icon.svg',badge:'/icon.svg',data:{url:resolveAppURL(data?.url).href}});
  })());
  return;
 }
 if((event.action==='terminal'||event.action==='reply')&&data?.sessionId!=null){
  event.waitUntil(openApp(resolveAppURL(actionURL(event.action,data.sessionId))));
  return;
 }
 event.waitUntil(openApp(resolveAppURL(data?.url)));
});
export {};
