/// <reference lib="webworker" />
const worker = self;
const CACHE = "agentdeck-react-34c70da55a67";
worker.addEventListener('install', event => event.waitUntil((async () => { await (await caches.open(CACHE)).addAll(["/","/icon.svg","/manifest.webmanifest","/fonts.css","/fonts/inter-latin.woff2","/fonts/inter-latin-ext.woff2","/react/assets/Review-DR5PCwew.css","/react/assets/Review-DvaE5qQo.js","/react/assets/app-Bx1_pLQO.js","/react/assets/app-D4IMCFRY.css","/react/assets/terminal-D1lajMty.css","/react/assets/terminal-XsjSY7Vj.js"]); await worker.skipWaiting(); })()));
worker.addEventListener('activate', event => event.waitUntil((async () => { await Promise.all((await caches.keys()).filter(key => key.startsWith('agentdeck-') && key !== CACHE).map(key => caches.delete(key))); await worker.clients.claim(); })()));
worker.addEventListener('fetch', event => {
    const request = event.request, url = new URL(request.url);
    if (request.method !== 'GET' || url.origin !== worker.location.origin || /^\/(api|term|terminal)\//.test(url.pathname))
        return;
    const immutable = url.pathname.startsWith('/react/assets/') || url.pathname.startsWith('/fonts/') || url.pathname === '/icon.svg';
    event.respondWith((async () => {
        const cache = await caches.open(CACHE);
        if (immutable) {
            const cached = await cache.match(request);
            if (cached)
                return cached;
        }
        try {
            const response = await fetch(request);
            if (response.ok)
                await cache.put(request, response.clone());
            return response;
        }
        catch {
            return await cache.match(request) || (request.mode === 'navigate' ? await cache.match('/') : undefined) || new Response('AgentDeck is offline', { status: 503 });
        }
    })());
});
worker.addEventListener('push', event => {
    let data = {};
    try {
        data = event.data?.json() || {};
    }
    catch { }
    const options = { body: data.body || '', icon: '/icon.svg', badge: '/icon.svg', data: { url: data.url || '/' }, actions: data.kind === 'approval' ? [{ action: 'open', title: 'Review' }] : [] };
    event.waitUntil(worker.registration.showNotification(data.title || 'agentdeck', options));
});
worker.addEventListener('notificationclick', event => {
    event.notification.close();
    const data = event.notification.data;
    let url = new URL(data?.url || '/', worker.location.origin);
    if (url.origin !== worker.location.origin)
        url = new URL('/', worker.location.origin);
    event.waitUntil((async () => { const windows = await worker.clients.matchAll({ type: 'window', includeUncontrolled: true }); const app = windows[0]; if (app) {
        await app.navigate(url.href);
        await app.focus();
    }
    else
        await worker.clients.openWindow(url.href); })());
});
