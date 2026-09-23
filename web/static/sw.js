/// <reference lib="webworker" />
const worker = self;
const CACHE = "lectern-react-86fef17c0228";
worker.addEventListener('install', event => event.waitUntil((async () => { await (await caches.open(CACHE)).addAll(["/","/icon.svg","/manifest.webmanifest","/fonts.css","/fonts/inter-latin.woff2","/fonts/inter-latin-ext.woff2","/react/assets/app-C9yddX93.css","/react/assets/app-CFjYtNUz.js","/react/assets/terminal-CR3RBNz3.js","/react/assets/terminal-qACrtkZ6.css","/react/assets/viewport-D5nCZzjZ.js","/react/assets/viewport-DR5PCwew.css"]); await worker.skipWaiting(); })()));
worker.addEventListener('activate', event => event.waitUntil((async () => { await Promise.all((await caches.keys()).filter(key => key.startsWith('lectern-') && key !== CACHE).map(key => caches.delete(key))); await worker.clients.claim(); })()));
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
            return await cache.match(request) || (request.mode === 'navigate' ? await cache.match('/') : undefined) || new Response('Lectern is offline', { status: 503 });
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
    event.waitUntil(worker.registration.showNotification(data.title || 'lectern', options));
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
