/// <reference lib="webworker" />
const worker=self as unknown as ServiceWorkerGlobalScope;
declare const __CACHE_NAME__:string;
declare const __STATIC_ASSETS__:string[];
const CACHE=__CACHE_NAME__;
worker.addEventListener('install',event=>event.waitUntil((async()=>{await(await caches.open(CACHE)).addAll(__STATIC_ASSETS__);await worker.skipWaiting();})()));
worker.addEventListener('activate',event=>event.waitUntil((async()=>{await Promise.all((await caches.keys()).filter(key=>key.startsWith('lectern-')&&key!==CACHE).map(key=>caches.delete(key)));await worker.clients.claim();})()));
worker.addEventListener('fetch',event=>{
 const request=event.request,url=new URL(request.url);
 if(request.method!=='GET'||url.origin!==worker.location.origin||/^\/(api|term|terminal)\//.test(url.pathname))return;
 const immutable=url.pathname.startsWith('/react/assets/')||url.pathname.startsWith('/fonts/')||url.pathname==='/icon.svg';
 event.respondWith((async()=>{
  const cache=await caches.open(CACHE);
  if(immutable){const cached=await cache.match(request);if(cached)return cached;}
  try{const response=await fetch(request);if(response.ok)await cache.put(request,response.clone());return response;}
  catch{return await cache.match(request)||(request.mode==='navigate'?await cache.match('/'):undefined)||new Response('Lectern is offline',{status:503});}
 })());
});
import {buildNotificationPlan,confirmationNotification,decisionForAction,decisionRequestInit,decisionURL,type PushData} from './sw-actions';
worker.addEventListener('push',event=>{
 let data:PushData={};try{data=event.data?.json() as PushData||{};}catch{}
 const plan=buildNotificationPlan(data);
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
worker.addEventListener('notificationclick',event=>{
 const data=event.notification.data as {url?:string;approvalId?:number}|undefined;
 const decision=decisionForAction(event.action);
 event.notification.close();
 if(decision&&data?.approvalId!=null){
  const approvalId=data.approvalId;
  event.waitUntil((async()=>{
   let ok=false;
   try{const resp=await fetch(decisionURL(approvalId),decisionRequestInit(decision));ok=resp.ok;}catch{ok=false;}
   const confirmation=confirmationNotification(decision,ok);
   await worker.registration.showNotification(confirmation.title,{body:confirmation.body,icon:'/icon.svg',badge:'/icon.svg',data:{url:resolveAppURL(data?.url).href}});
  })());
  return;
 }
 event.waitUntil(openApp(resolveAppURL(data?.url)));
});
export {};
